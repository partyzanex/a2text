package server_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/partyzanex/a2text/internal/adapters/grpc/server"
	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/infra/cycletoken"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

// KeyboardServiceInjectSuite covers the validation + token-error
// mapping + happy-path of KeyboardService.Inject. Uses a real
// cycletoken.Store so the Consume code path is exercised end to end.
type KeyboardServiceInjectSuite struct {
	suite.Suite

	ctrl     *gomock.Controller
	source   *server.MockHotkeySource
	injector *server.MockInjector
	trigger  *server.MockCycleTrigger
	tokens   *cycletoken.Store
	svc      *server.KeyboardService
}

// SetupTest builds fresh mocks + store + service per test case.
func (s *KeyboardServiceInjectSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.source = server.NewMockHotkeySource(s.ctrl)
	s.injector = server.NewMockInjector(s.ctrl)
	s.trigger = server.NewMockCycleTrigger(s.ctrl)
	s.tokens = cycletoken.New(30*time.Second, nil)
	s.svc = server.NewKeyboardService(
		slog.New(slog.DiscardHandler),
		s.tokens,
		s.source,
		s.injector,
		s.trigger,
	)
}

func (s *KeyboardServiceInjectSuite) TearDownTest() {
	s.ctrl.Finish()
}

// TestInject_EmptyTokenRejected verifies an empty inject_token
// short-circuits to INVALID_ARGUMENT before the store is touched.
func (s *KeyboardServiceInjectSuite) TestInject_EmptyTokenRejected() {
	_, err := s.svc.Inject(context.Background(), &a2textv1.InjectRequest{
		Text:        "any",
		InjectToken: "",
	})

	requireGRPCCode(s.T(), err, codes.InvalidArgument)
}

// TestInject_UnknownTokenRejected verifies a token that was never
// issued maps to PERMISSION_DENIED.
func (s *KeyboardServiceInjectSuite) TestInject_UnknownTokenRejected() {
	_, err := s.svc.Inject(context.Background(), &a2textv1.InjectRequest{
		Text:        "any",
		InjectToken: "never-issued",
	})

	requireGRPCCode(s.T(), err, codes.PermissionDenied)
}

// TestInject_ConsumedTokenRejected verifies a second Inject with the
// same token after a successful consume maps to PERMISSION_DENIED
// (idempotent replay is intentionally NOT supported).
func (s *KeyboardServiceInjectSuite) TestInject_ConsumedTokenRejected() {
	tok, _, err := s.tokens.Issue()
	s.Require().NoError(err)

	s.injector.EXPECT().
		Inject(gomock.Any(), gomock.Any()).
		Return(a2textv1.InjectMode_INJECT_MODE_PASTE, int32(4), nil)

	_, err = s.svc.Inject(context.Background(), &a2textv1.InjectRequest{
		Text:        "any",
		InjectToken: string(tok),
	})
	s.Require().NoError(err)

	_, err = s.svc.Inject(context.Background(), &a2textv1.InjectRequest{
		Text:        "any",
		InjectToken: string(tok),
	})

	requireGRPCCode(s.T(), err, codes.PermissionDenied)
}

// TestInject_HappyPath verifies a valid token + successful injector
// produces an InjectResponse carrying the injector's mode and
// events_written.
func (s *KeyboardServiceInjectSuite) TestInject_HappyPath() {
	tok, _, err := s.tokens.Issue()
	s.Require().NoError(err)

	s.injector.EXPECT().
		Inject(gomock.Any(), "transcript").
		Return(a2textv1.InjectMode_INJECT_MODE_PASTE, int32(4), nil)

	resp, err := s.svc.Inject(context.Background(), &a2textv1.InjectRequest{
		Text:        "transcript",
		InjectToken: string(tok),
	})

	s.Require().NoError(err)
	s.Equal(a2textv1.InjectMode_INJECT_MODE_PASTE, resp.GetMode())
	s.Equal(int32(4), resp.GetEventsWritten())
}

// TestInject_InjectorErrorMappedToInternal verifies an injector
// failure surfaces as INTERNAL with a generic message (no leak of
// the underlying error onto the wire).
func (s *KeyboardServiceInjectSuite) TestInject_InjectorErrorMappedToInternal() {
	tok, _, err := s.tokens.Issue()
	s.Require().NoError(err)

	s.injector.EXPECT().
		Inject(gomock.Any(), gomock.Any()).
		Return(a2textv1.InjectMode_INJECT_MODE_PASTE, int32(0), errors.New("uinput closed"))

	_, err = s.svc.Inject(context.Background(), &a2textv1.InjectRequest{
		Text:        "any",
		InjectToken: string(tok),
	})

	requireGRPCCode(s.T(), err, codes.Internal)
}

func TestKeyboardServiceInjectSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(KeyboardServiceInjectSuite))
}

// KeyboardServiceStartCycleSuite covers the token-issue +
// trigger-error mapping + happy-path of KeyboardService.StartCycle.
type KeyboardServiceStartCycleSuite struct {
	suite.Suite

	ctrl     *gomock.Controller
	source   *server.MockHotkeySource
	injector *server.MockInjector
	trigger  *server.MockCycleTrigger
	tokens   *cycletoken.Store
	svc      *server.KeyboardService
}

func (s *KeyboardServiceStartCycleSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.source = server.NewMockHotkeySource(s.ctrl)
	s.injector = server.NewMockInjector(s.ctrl)
	s.trigger = server.NewMockCycleTrigger(s.ctrl)
	s.tokens = cycletoken.New(30*time.Second, nil)
	s.svc = server.NewKeyboardService(
		slog.New(slog.DiscardHandler),
		s.tokens,
		s.source,
		s.injector,
		s.trigger,
	)
}

func (s *KeyboardServiceStartCycleSuite) TearDownTest() {
	s.ctrl.Finish()
}

// TestStartCycle_HappyPath verifies a freshly-issued token is
// returned together with an expire_time in the future.
func (s *KeyboardServiceStartCycleSuite) TestStartCycle_HappyPath() {
	s.trigger.EXPECT().
		Start(gomock.Any(), gomock.Any()).
		Return(nil)

	resp, err := s.svc.StartCycle(context.Background(), &a2textv1.StartCycleRequest{})

	s.Require().NoError(err)
	s.NotEmpty(resp.GetInjectToken())
	s.True(resp.GetExpireTime().AsTime().After(time.Now()), "expire_time must be in the future")
}

// TestStartCycle_AlreadyActiveReturnsFailedPrecondition verifies a
// second StartCycle while the cycletoken slot is still alive maps
// to FAILED_PRECONDITION.
func (s *KeyboardServiceStartCycleSuite) TestStartCycle_AlreadyActiveReturnsFailedPrecondition() {
	_, _, err := s.tokens.Issue()
	s.Require().NoError(err)

	_, err = s.svc.StartCycle(context.Background(), &a2textv1.StartCycleRequest{})

	requireGRPCCode(s.T(), err, codes.FailedPrecondition)
}

// TestStartCycle_CycleInFlightMappedToFailedPrecondition verifies a
// trigger that reports ErrCycleInFlight (an in-flight recording
// from elsewhere) maps to FAILED_PRECONDITION too.
func (s *KeyboardServiceStartCycleSuite) TestStartCycle_CycleInFlightMappedToFailedPrecondition() {
	s.trigger.EXPECT().
		Start(gomock.Any(), gomock.Any()).
		Return(domain.ErrCycleInFlight)

	_, err := s.svc.StartCycle(context.Background(), &a2textv1.StartCycleRequest{})

	requireGRPCCode(s.T(), err, codes.FailedPrecondition)
}

// TestStartCycle_UnknownTriggerErrorMappedToInternal verifies any
// non-sentinel trigger error surfaces as INTERNAL.
func (s *KeyboardServiceStartCycleSuite) TestStartCycle_UnknownTriggerErrorMappedToInternal() {
	s.trigger.EXPECT().
		Start(gomock.Any(), gomock.Any()).
		Return(errors.New("kernel exploded"))

	_, err := s.svc.StartCycle(context.Background(), &a2textv1.StartCycleRequest{})

	requireGRPCCode(s.T(), err, codes.Internal)
}

func TestKeyboardServiceStartCycleSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(KeyboardServiceStartCycleSuite))
}

// KeyboardServiceStreamSuite covers StreamHotkeyEvents: the
// initial-state framing, the event-loop pump, and the two
// termination paths (ctx cancel and source-channel close).
type KeyboardServiceStreamSuite struct {
	suite.Suite

	ctrl     *gomock.Controller
	source   *server.MockHotkeySource
	injector *server.MockInjector
	trigger  *server.MockCycleTrigger
	tokens   *cycletoken.Store
	svc      *server.KeyboardService
}

func (s *KeyboardServiceStreamSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.source = server.NewMockHotkeySource(s.ctrl)
	s.injector = server.NewMockInjector(s.ctrl)
	s.trigger = server.NewMockCycleTrigger(s.ctrl)
	s.tokens = cycletoken.New(30*time.Second, nil)
	s.svc = server.NewKeyboardService(
		slog.New(slog.DiscardHandler),
		s.tokens,
		s.source,
		s.injector,
		s.trigger,
	)
}

func (s *KeyboardServiceStreamSuite) TearDownTest() {
	s.ctrl.Finish()
}

// TestStream_SubscribeFailureReturnsInternal verifies a source that
// fails to Subscribe surfaces as INTERNAL.
func (s *KeyboardServiceStreamSuite) TestStream_SubscribeFailureReturnsInternal() {
	s.source.EXPECT().
		Subscribe(gomock.Any()).
		Return(nil, nil, errors.New("source closed"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := newFakeStream(ctx)

	err := s.svc.StreamHotkeyEvents(&a2textv1.StreamHotkeyEventsRequest{}, stream)

	requireGRPCCode(s.T(), err, codes.Internal)
	s.Empty(stream.sent)
}

// TestStream_HappyPath verifies the first frame is InitialState,
// subsequent frames are HotkeyEvent, and the stream exits cleanly
// when the source closes its channel.
func (s *KeyboardServiceStreamSuite) TestStream_HappyPath() {
	events := make(chan *a2textv1.HotkeyEvent, 2)

	events <- &a2textv1.HotkeyEvent{
		Kind:        a2textv1.HotkeyEventKind_HOTKEY_EVENT_KIND_TOGGLE,
		Sequence:    1,
		InjectToken: "abc",
	}

	close(events)

	initial := &a2textv1.InitialState{
		State: a2textv1.HotkeyState_HOTKEY_STATE_IDLE,
		Mode:  a2textv1.HotkeyMode_HOTKEY_MODE_TOGGLE,
	}

	s.source.EXPECT().
		Subscribe(gomock.Any()).
		Return(initial, (<-chan *a2textv1.HotkeyEvent)(events), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := newFakeStream(ctx)

	err := s.svc.StreamHotkeyEvents(&a2textv1.StreamHotkeyEventsRequest{}, stream)
	s.Require().NoError(err)

	s.Require().Len(stream.sent, 2)

	s.Equal(initial, stream.sent[0].GetInitialState())
	s.Equal(uint64(1), stream.sent[1].GetEvent().GetSequence())
	s.Equal("abc", stream.sent[1].GetEvent().GetInjectToken())
}

// TestStream_ContextCancelReturnsNil verifies the stream exits with
// nil when the caller cancels its context while the source channel
// is still open.
func (s *KeyboardServiceStreamSuite) TestStream_ContextCancelReturnsNil() {
	events := make(chan *a2textv1.HotkeyEvent)

	s.source.EXPECT().
		Subscribe(gomock.Any()).
		Return(&a2textv1.InitialState{}, (<-chan *a2textv1.HotkeyEvent)(events), nil)

	ctx, cancel := context.WithCancel(context.Background())
	stream := newFakeStream(ctx)

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.svc.StreamHotkeyEvents(&a2textv1.StreamHotkeyEventsRequest{}, stream)
	}()

	// Give the handler time to send the initial frame and enter
	// the event loop.
	time.Sleep(20 * time.Millisecond)

	cancel()

	select {
	case err := <-errCh:
		s.Require().NoError(err)
	case <-time.After(time.Second):
		s.FailNow("stream did not exit after ctx cancel")
	}

	s.Require().Len(stream.sent, 1)
	s.NotNil(stream.sent[0].GetInitialState())
}

func TestKeyboardServiceStreamSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(KeyboardServiceStreamSuite))
}

// --- test helpers -----------------------------------------------------------

// fakeStream satisfies a2textv1.KeyboardService_StreamHotkeyEventsServer
// (via the embedded grpc.ServerStream surface) and records every frame
// Send is called with. Used to drive StreamHotkeyEvents without a
// real gRPC transport.
type fakeStream struct {
	googlegrpc.ServerStream

	ctx  context.Context //nolint:containedctx // matches the gRPC handler shape
	mu   sync.Mutex
	sent []*a2textv1.HotkeyStreamFrame
}

func newFakeStream(ctx context.Context) *fakeStream {
	return &fakeStream{ctx: ctx}
}

func (f *fakeStream) Send(frame *a2textv1.HotkeyStreamFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sent = append(f.sent, frame)

	return nil
}

func (f *fakeStream) Context() context.Context {
	return f.ctx
}

func (f *fakeStream) SetHeader(_ metadata.MD) error  { return nil }
func (f *fakeStream) SendHeader(_ metadata.MD) error { return nil }
func (f *fakeStream) SetTrailer(_ metadata.MD)       {}
func (f *fakeStream) SendMsg(_ any) error            { return nil }
func (f *fakeStream) RecvMsg(_ any) error            { return nil }

// requireGRPCCode asserts err is a gRPC status error with the
// expected code. Shared helper across the three suites in this file.
func requireGRPCCode(t *testing.T, err error, want codes.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected gRPC error with code %s, got nil", want)
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}

	if st.Code() != want {
		t.Fatalf("status code mismatch: got=%s want=%s msg=%q", st.Code(), want, st.Message())
	}
}
