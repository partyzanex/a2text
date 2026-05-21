// Package server hosts the gRPC adapters that implement the wire
// services declared in proto/a2text/v1. Each service has its own
// concrete type in its own file; transport-level wiring (listener,
// grpc.Server, Serve / Stop) lives in internal/infra/grpc and
// composes these adapters at bootstrap.
//
//nolint:godoclint // mockgen file shares package + its own header
package server

//go:generate go run go.uber.org/mock/mockgen@latest -source=keyboard_service.go -destination=keyboard_service_mocks_test.go -package=server

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/infra/cycletoken"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

// HotkeySource is the seam the KeyboardService uses to observe the
// daemon-owned evdev hotkey pipeline.
type HotkeySource interface {
	// Subscribe returns the daemon's current hotkey state at the
	// moment of subscribe plus a channel of subsequent transitions.
	// The channel is closed when ctx is cancelled or the source is
	// torn down. Events delivered through the channel are
	// best-effort: a slow consumer may see drops under the lossy
	// ring-buffer policy enforced by the source.
	Subscribe(ctx context.Context) (*a2textv1.InitialState, <-chan *a2textv1.HotkeyEvent, error)
}

// Injector is the seam the KeyboardService uses to deliver a
// transcript to the user's focused window. The infrastructure
// implementation owns the daemon-side config-driven output policy
// and the platform virtual keyboard; it picks the mode (CLIPBOARD /
// PASTE / TYPE) internally and reports back what it actually did.
type Injector interface {
	// Inject delivers text per the active output policy and returns
	// the mode it resolved to plus the number of low-level key
	// events it wrote to the platform virtual keyboard. CLIPBOARD
	// mode is a legal outcome and reports zero events_written.
	Inject(ctx context.Context, text string) (a2textv1.InjectMode, int32, error)
}

// CycleTrigger is the seam the KeyboardService uses to start a
// UI-initiated recording cycle on the daemon-side state machine.
// The implementation is expected to also broadcast the cycle-start
// event to active HotkeySource subscribers so observer UIs see the
// cycle begin.
type CycleTrigger interface {
	// Start begins a new recording cycle and attaches token to it so
	// every HotkeyEvent emitted for this cycle carries that value.
	// Returns ErrCycleInFlight when a cycle is already running and
	// ErrAudioUnavailable when the audio device is missing or busy.
	Start(ctx context.Context, token string) error
}

// KeyboardService implements a2textv1.KeyboardServiceServer. It
// exposes the daemon-owned evdev/uinput pipeline (read + write
// halves) plus the UI-initiated StartCycle trigger over the gRPC
// channel.
//
// No idempotency cache is kept: the loopback gRPC channel does not
// drop in-flight RPCs in practice and the UI is the only client, so
// retry with a previously consumed token is treated as a hard
// PERMISSION_DENIED rather than transparently replayed.
type KeyboardService struct {
	a2textv1.UnimplementedKeyboardServiceServer

	log      *slog.Logger
	tokens   *cycletoken.Store
	source   HotkeySource
	injector Injector
	trigger  CycleTrigger
}

// NewKeyboardService constructs a KeyboardService adapter. A nil log
// is replaced with a discard handler. tokens, source, injector, and
// trigger are required dependencies — passing nil is a programmer
// error and will surface as a panic on the first call that needs
// them.
func NewKeyboardService(
	log *slog.Logger,
	tokens *cycletoken.Store,
	source HotkeySource,
	injector Injector,
	trigger CycleTrigger,
) *KeyboardService {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &KeyboardService{
		log:      log,
		tokens:   tokens,
		source:   source,
		injector: injector,
		trigger:  trigger,
	}
}

// StreamHotkeyEvents subscribes to the daemon-owned hotkey pipeline
// and forwards transitions to the caller as HotkeyStreamFrame
// messages. The first frame is always the InitialState snapshot the
// source returned at subscribe time; every subsequent frame carries
// a single HotkeyEvent received from the source channel.
//
// The stream terminates cleanly (nil return) on either of two
// signals: the gRPC peer cancels (stream context Done) or the
// source closes its event channel (typically on daemon shutdown).
//
// Expected gRPC error codes:
//
//   - INTERNAL — subscribe failed, or stream.Send returned a
//     non-recoverable error.
func (k *KeyboardService) StreamHotkeyEvents(
	_ *a2textv1.StreamHotkeyEventsRequest,
	stream a2textv1.KeyboardService_StreamHotkeyEventsServer,
) error {
	ctx := stream.Context()

	initial, events, err := k.source.Subscribe(ctx)
	if err != nil {
		k.log.Error("hotkey subscribe failed", slog.Any("error", err))

		return status.Errorf(codes.Internal, "subscribe failed")
	}

	if err := stream.Send(&a2textv1.HotkeyStreamFrame{
		Payload: &a2textv1.HotkeyStreamFrame_InitialState{
			InitialState: initial,
		},
	}); err != nil {
		k.log.Error("hotkey initial send failed", slog.Any("error", err))

		return status.Errorf(codes.Internal, "send initial frame failed")
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				return nil
			}

			if err := stream.Send(&a2textv1.HotkeyStreamFrame{
				Payload: &a2textv1.HotkeyStreamFrame_Event{
					Event: ev,
				},
			}); err != nil {
				k.log.Error("hotkey event send failed",
					slog.Uint64("sequence", ev.GetSequence()),
					slog.Any("error", err),
				)

				return status.Errorf(codes.Internal, "send event frame failed")
			}
		}
	}
}

// Inject validates the request, consumes the inject_token against
// the cycletoken store, and dispatches the transcript to the
// Injector.
//
// Validation rules:
//
//   - text is required (non-empty).
//   - inject_token is required (non-empty); the daemon rejects
//     unknown / expired / consumed tokens.
//
// Expected gRPC error codes:
//
//   - INVALID_ARGUMENT  — empty text or empty token.
//   - PERMISSION_DENIED — token unknown, expired, or consumed.
//   - INTERNAL          — token store or injector failure.
func (k *KeyboardService) Inject(
	ctx context.Context,
	req *a2textv1.InjectRequest,
) (*a2textv1.InjectResponse, error) {
	text := req.GetText()
	if text == "" {
		return nil, status.Errorf(codes.InvalidArgument, "text must not be empty")
	}

	tok := cycletoken.Token(req.GetInjectToken())
	if tok == "" {
		return nil, status.Errorf(codes.InvalidArgument, "inject_token must not be empty")
	}

	if err := k.tokens.Consume(tok); err != nil {
		return k.handleConsumeError(tok, err)
	}

	mode, events, err := k.injector.Inject(ctx, text)
	if err != nil {
		k.log.Error("inject failed",
			slog.String("token", string(tok)),
			slog.Any("error", err),
		)

		return nil, status.Errorf(codes.Internal, "inject failed")
	}

	return &a2textv1.InjectResponse{
		Mode:          mode,
		EventsWritten: events,
	}, nil
}

// StartCycle mints a fresh inject_token through the cycletoken store
// and asks the CycleTrigger to begin a UI-initiated recording cycle
// bound to that token. The token is returned to the caller — the
// same value will also be embedded in the HotkeyEvent the trigger
// broadcasts to active HotkeySource subscribers.
//
// Expected gRPC error codes:
//
//   - FAILED_PRECONDITION — a cycle is already in flight
//     (CycleTrigger.Start returned ErrCycleInFlight).
//   - RESOURCE_EXHAUSTED  — the audio device is missing or busy
//     (CycleTrigger.Start returned ErrAudioUnavailable).
//   - INTERNAL            — token store failure or unspecified
//     trigger error.
func (k *KeyboardService) StartCycle(
	ctx context.Context,
	_ *a2textv1.StartCycleRequest,
) (*a2textv1.StartCycleResponse, error) {
	tok, expire, err := k.tokens.Issue()
	if err != nil {
		return nil, k.mapIssueError(err)
	}

	if err := k.trigger.Start(ctx, string(tok)); err != nil {
		return nil, k.mapTriggerError(tok, err)
	}

	return &a2textv1.StartCycleResponse{
		InjectToken: string(tok),
		ExpireTime:  timestamppb.New(expire),
	}, nil
}

// handleConsumeError maps a non-nil error from cycletoken.Store.Consume
// to the right gRPC response. Every branch refuses the call —
// idempotent replay is intentionally not supported, the UI is
// expected to mint a fresh token when it needs a fresh delivery.
func (k *KeyboardService) handleConsumeError(tok cycletoken.Token, err error) (*a2textv1.InjectResponse, error) {
	switch {
	case errors.Is(err, cycletoken.ErrConsumed):
		return nil, status.Errorf(codes.PermissionDenied, "inject_token already consumed")

	case errors.Is(err, cycletoken.ErrExpired):
		return nil, status.Errorf(codes.PermissionDenied, "inject_token expired")

	case errors.Is(err, cycletoken.ErrNotFound):
		return nil, status.Errorf(codes.PermissionDenied, "inject_token unknown")

	default:
		k.log.Error("cycletoken consume failed",
			slog.String("token", string(tok)),
			slog.Any("error", err),
		)

		return nil, status.Errorf(codes.Internal, "token validation failed")
	}
}

// mapIssueError converts a cycletoken.Store.Issue failure into the
// appropriate gRPC status. ErrAlreadyActive is the expected business
// signal for "cycle is already in flight"; everything else (crypto
// failure, future sentinels) is treated as INTERNAL.
func (k *KeyboardService) mapIssueError(err error) error {
	if errors.Is(err, cycletoken.ErrAlreadyActive) {
		return status.Errorf(codes.FailedPrecondition, "cycle already in flight")
	}

	k.log.Error("cycletoken issue failed", slog.Any("error", err))

	return status.Errorf(codes.Internal, "token issue failed")
}

// mapTriggerError converts a CycleTrigger.Start failure into the
// appropriate gRPC status. Unknown errors are logged at error level
// and surface as INTERNAL.
func (k *KeyboardService) mapTriggerError(tok cycletoken.Token, err error) error {
	if errors.Is(err, domain.ErrCycleInFlight) {
		return status.Errorf(codes.FailedPrecondition, "cycle already in flight")
	}

	k.log.Error("cycle trigger failed",
		slog.String("token", string(tok)),
		slog.Any("error", err),
	)

	return status.Errorf(codes.Internal, "cycle start failed")
}
