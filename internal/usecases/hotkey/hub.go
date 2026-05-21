// Package hotkey is the usecase that mediates the daemon-owned
// evdev hotkey pipeline. It owns:
//
//   - the canonical recording state (idle / recording),
//   - the active inject_token for the current cycle,
//   - the single-subscriber publication channel that streams
//     semantic HotkeyEvents (PRESS / RELEASE in HOLD mode, TOGGLE
//     in TOGGLE mode) to the UI client.
//
// Audio capture is intentionally out of scope. The UI owns the
// user session and is therefore the right place to start / stop
// microphone capture in response to the events this hub broadcasts;
// captured audio is uploaded to the daemon over a separate gRPC
// channel for transcription.
//
// The daemon serves a single UI client at a time (enforced at the
// gRPC transport layer), so the Hub keeps exactly one subscriber
// slot. Subscribe refuses a second call while the slot is occupied;
// the slot is freed when the active subscriber's context is
// cancelled (typically when the gRPC stream is torn down).
//
// Cycle entry points (Start / End) are usable from both the gRPC
// adapter (UI-initiated StartCycle) and the evdev reader goroutine
// (physical hotkey edges). They serialise on the same mutex as the
// subscriber slot so the event ordering observed by the UI is the
// same one the state machine committed.
package hotkey

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/partyzanex/a2text/internal/domain"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

// defaultSubBufferSize is the subscriber channel capacity. When the
// channel is full the publish path drops the new event to keep the
// slow consumer from blocking — the lossy ring-buffer policy
// promised on the wire.
const defaultSubBufferSize = 64

// ErrAlreadySubscribed is returned by Subscribe when the single
// subscriber slot is already occupied.
var ErrAlreadySubscribed = errors.New("hotkey hub: subscriber already attached")

// Hub is the in-memory single-subscriber publication point plus the
// daemon-side cycle state machine.
type Hub struct {
	log *slog.Logger

	mu          sync.Mutex
	state       a2textv1.HotkeyState
	mode        a2textv1.HotkeyMode
	activeToken string
	sequence    uint64
	sub         *subscriber
}

// subscriber holds the per-Subscribe channel. Allocated fresh on
// each successful Subscribe; cleared on context cancel. The pointer
// is used as identity so the cleanup goroutine never frees a slot
// that has already been replaced.
type subscriber struct {
	ch chan *a2textv1.HotkeyEvent
}

// New constructs a Hub. log may be nil; it is replaced with a
// discard handler. The initial state is IDLE and mode is taken from
// caller config.
func New(log *slog.Logger, mode a2textv1.HotkeyMode) *Hub {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Hub{
		log:   log,
		state: a2textv1.HotkeyState_HOTKEY_STATE_IDLE,
		mode:  mode,
	}
}

// Subscribe registers the single subscriber and returns the
// daemon's current state snapshot plus the channel future
// HotkeyEvents will be pushed to. The channel is closed when ctx
// is cancelled. Returns ErrAlreadySubscribed when the slot is
// already occupied.
func (h *Hub) Subscribe(ctx context.Context) (*a2textv1.InitialState, <-chan *a2textv1.HotkeyEvent, error) {
	h.mu.Lock()

	if h.sub != nil {
		h.mu.Unlock()

		return nil, nil, ErrAlreadySubscribed
	}

	sub := &subscriber{
		ch: make(chan *a2textv1.HotkeyEvent, defaultSubBufferSize),
	}
	h.sub = sub

	initial := &a2textv1.InitialState{
		State:             h.state,
		Mode:              h.mode,
		ActiveInjectToken: h.activeToken,
	}

	h.mu.Unlock()

	go h.unsubscribeOnCtxDone(ctx, sub)

	return initial, sub.ch, nil
}

// Start begins a new recording cycle bound to token. The Hub:
//
//  1. Refuses a second concurrent cycle with domain.ErrCycleInFlight.
//  2. Transitions state to RECORDING, records the token, increments
//     the publication sequence and emits a cycle-start HotkeyEvent
//     (PRESS in HOLD mode, TOGGLE in TOGGLE mode) on the subscriber
//     channel.
//
// Audio capture happens UI-side in reaction to that event; Hub
// itself never touches the microphone.
//
// Both the gRPC StartCycle adapter and the evdev reader goroutine
// use this method as the single source of truth.
func (h *Hub) Start(_ context.Context, token string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.state == a2textv1.HotkeyState_HOTKEY_STATE_RECORDING {
		return domain.ErrCycleInFlight
	}

	h.state = a2textv1.HotkeyState_HOTKEY_STATE_RECORDING
	h.activeToken = token

	h.publishLocked(h.startKindLocked(), token)

	return nil
}

// End closes the current recording cycle. The Hub:
//
//  1. No-ops when no cycle is in flight (idempotent).
//  2. Emits a cycle-end HotkeyEvent (RELEASE in HOLD mode, TOGGLE
//     in TOGGLE mode) carrying the same token the cycle was started
//     with.
//  3. Transitions state to IDLE and clears the active token.
//
// The UI uses the broadcasted end event as its signal to stop the
// local microphone capture and start uploading the audio buffer.
func (h *Hub) End() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.state != a2textv1.HotkeyState_HOTKEY_STATE_RECORDING {
		return
	}

	endingToken := h.activeToken

	h.publishLocked(h.endKindLocked(), endingToken)

	h.state = a2textv1.HotkeyState_HOTKEY_STATE_IDLE
	h.activeToken = ""
}

// startKindLocked maps the active mode to the kind a cycle-start
// event carries. Caller must hold h.mu.
func (h *Hub) startKindLocked() a2textv1.HotkeyEventKind {
	if h.mode == a2textv1.HotkeyMode_HOTKEY_MODE_HOLD {
		return a2textv1.HotkeyEventKind_HOTKEY_EVENT_KIND_PRESS
	}

	return a2textv1.HotkeyEventKind_HOTKEY_EVENT_KIND_TOGGLE
}

// endKindLocked maps the active mode to the kind a cycle-end event
// carries. Caller must hold h.mu.
func (h *Hub) endKindLocked() a2textv1.HotkeyEventKind {
	if h.mode == a2textv1.HotkeyMode_HOTKEY_MODE_HOLD {
		return a2textv1.HotkeyEventKind_HOTKEY_EVENT_KIND_RELEASE
	}

	return a2textv1.HotkeyEventKind_HOTKEY_EVENT_KIND_TOGGLE
}

// publishLocked emits an event to the active subscriber, dropping
// silently when the buffer is full (lossy ring-buffer policy) or
// when no subscriber is attached. Caller must hold h.mu so the
// sequence increment, event build and channel send observe the
// same state.
func (h *Hub) publishLocked(kind a2textv1.HotkeyEventKind, token string) {
	h.sequence++

	if h.sub == nil {
		return
	}

	ev := &a2textv1.HotkeyEvent{
		Kind:        kind,
		EmitTime:    timestamppb.New(time.Now()),
		Sequence:    h.sequence,
		InjectToken: token,
	}

	select {
	case h.sub.ch <- ev:
	default:
		// Drop. The next event will be observed by the consumer
		// as a sequence gap, which is exactly the contract.
	}
}

// unsubscribeOnCtxDone clears the subscriber slot once the caller's
// context is cancelled. Compares the pointer so a slot that has
// already been replaced by a fresh Subscribe is never wiped a
// second time.
func (h *Hub) unsubscribeOnCtxDone(ctx context.Context, sub *subscriber) {
	<-ctx.Done()

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.sub != sub {
		return
	}

	close(sub.ch)
	h.sub = nil
}
