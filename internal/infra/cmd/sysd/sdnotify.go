package sysd

import (
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/partyzanex/a2text/internal/domain"
)

// SdNotifier is a thin wrapper over coreos/go-systemd/v22/daemon that the
// state machine listener can call after every transition.
//
// Behaviour when NOTIFY_SOCKET is unset (manual run, not under systemd):
// every method becomes a no-op. Tests can probe via Active().
//
// The Ready and Stopping handshakes use an exactly-once-on-success guard
// (mutex + bool flipped only after a successful send), not sync.Once: a
// transient sender error must not consume the single attempt and leave
// systemd waiting forever for READY=1.
type SdNotifier struct {
	log          *slog.Logger
	sender       NotifySender
	mu           sync.Mutex
	readySent    bool
	stoppingSent bool
}

// NotifySender is the seam used to swap real systemd notify for a fake in
// tests. Active() lets send() distinguish "no NOTIFY_SOCKET, sent==false
// is expected" from "socket is set but the underlying lib refused to
// deliver" — the second case is a real misconfiguration that deserves a
// log line. Routing the active check through the sender keeps tests free
// of NOTIFY_SOCKET env coupling.
//
//go:generate go run go.uber.org/mock/mockgen@latest -package=cmd -destination=sdnotify_mocks_test.go -source=sdnotify.go NotifySender
type NotifySender interface {
	Notify(state string) (sent bool, err error)
	Active() bool
}

// realSender uses the actual coreos/go-systemd implementation. Active is
// derived from NOTIFY_SOCKET — the same env variable the daemon library
// itself reads, so they cannot disagree.
type realSender struct{}

func (realSender) Notify(state string) (bool, error) {
	sent, err := daemon.SdNotify(false, state)
	if err != nil {
		return sent, fmt.Errorf("sdnotify: %w", err)
	}

	return sent, nil
}

func (realSender) Active() bool {
	return os.Getenv("NOTIFY_SOCKET") != ""
}

// NewSdNotifier returns a notifier that talks to the systemd notify socket
// pointed at by NOTIFY_SOCKET. If the env var is empty, the notifier is
// a no-op — manual runs do not crash and need no special-casing in callers.
func NewSdNotifier(log *slog.Logger) *SdNotifier {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &SdNotifier{log: log, sender: realSender{}}
}

// Active reports whether the notifier has a live notify socket. Useful for
// log output and tests; callers should not gate behaviour on it (the
// wrapper already no-ops when inactive).
//
// Defensive against nil/uninitialised receivers: SdNotifier is an exported
// struct, so callers in other packages can construct one without going
// through NewSdNotifier. We return false (the "inactive" answer) rather
// than panicking.
func (n *SdNotifier) Active() bool {
	if n == nil || n.sender == nil {
		return false
	}

	return n.sender.Active()
}

// Status sends `STATUS=...` only. Idempotent: same string twice produces
// the same systemd display.
func (n *SdNotifier) Status(text string) {
	n.send("STATUS=" + text)
}

// Ready signals systemd that the daemon finished startup and is ready to
// accept work. Sent at most once *successfully* per notifier: if the
// first attempt errors or returns sent=false on an active socket, the
// next call will retry. Once a send is acknowledged, subsequent calls
// no-op — systemd treats duplicate READY=1 as a unit state change.
//
// The check-send-set sequence runs under a single lock so concurrent
// callers (signal handler + main bootstrap converging on Ready) cannot
// both pass the "not yet sent" gate and emit two READY=1 messages.
func (n *SdNotifier) Ready(initialStatus string) {
	if n == nil || n.sender == nil {
		return
	}

	payload := "READY=1"
	if initialStatus != "" {
		payload = "READY=1\nSTATUS=" + initialStatus
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	if n.readySent {
		return
	}

	if n.sendLocked(payload) {
		n.readySent = true
	}
}

// Stopping signals systemd that we have begun graceful shutdown. systemd
// will not consider the unit failed for taking time after this. Same
// exactly-once-on-success semantics as Ready — a failed first attempt
// can be retried by a later Shutdown path. Concurrency: check-send-set
// is one critical section.
func (n *SdNotifier) Stopping(reason string) {
	if n == nil || n.sender == nil {
		return
	}

	payload := "STOPPING=1"
	if reason != "" {
		payload = "STOPPING=1\nSTATUS=" + reason
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	if n.stoppingSent {
		return
	}

	if n.sendLocked(payload) {
		n.stoppingSent = true
	}
}

// send is the lock-acquiring entry used by Status (which has no lifecycle
// flag to protect). It exists only so callers outside Ready/Stopping have
// a nil-safe path; the actual work lives in sendLocked.
func (n *SdNotifier) send(payload string) bool {
	if n == nil || n.sender == nil {
		return false
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	return n.sendLocked(payload)
}

// sendLocked delivers payload and reports whether it was acknowledged.
// The caller MUST hold n.mu — Ready/Stopping call this from inside their
// check-send-set critical section to keep the flag flip atomic with the
// delivery.
//
// Returns true on (sent=true) from any sender or on an intentionally-
// inactive sender (NOTIFY_SOCKET unset is a "successful no-op"); false
// on error or on sent=false from an active sender.
func (n *SdNotifier) sendLocked(payload string) bool {
	sent, err := n.sender.Notify(payload)

	log := n.logger() // never nil — even if n.log was never assigned

	if err != nil {
		// Log even when the sender is inactive: a non-nil error from an
		// inactive sender means the lib is misbehaving, not a benign skip.
		log.Warn("voice: sd_notify failed",
			slog.Int("payload_len", len(payload)),
			slog.Any("err", err),
		)

		return false
	}

	active := n.sender.Active()

	if !sent && active {
		// NOTIFY_SOCKET is set but the lib reported sent=false. Either the
		// socket vanished mid-run or the payload was rejected — surface it.
		log.Warn("voice: sd_notify not sent despite active notify socket",
			slog.Int("payload_len", len(payload)),
		)

		return false
	}

	// sent==true OR (sent==false && inactive): inactive is the manual-run
	// path where no socket exists; treat it as a "successful no-op" so the
	// readySent/stoppingSent flags flip and we don't retry forever.
	return true
}

// logger returns a non-nil *slog.Logger even if the field was never set.
// SdNotifier is an exported struct, so a caller may build one without
// NewSdNotifier and leave log==nil; we must not panic on the first warn.
func (n *SdNotifier) logger() *slog.Logger {
	if n.log != nil {
		return n.log
	}

	return slog.New(slog.DiscardHandler)
}

// MakeStateListener returns a domain.TransitionListener that fans every state
// transition out to: (a) the systemd notifier as a STATUS= line, and (b) the
// structured logger as a single "voice: state" record carrying state + action.
//
// The log line is the operator-facing channel in production (journalctl, log
// shippers): a single transition source means a `journalctl --user -u
// a2text-voice -g "voice: state"` gives the full SM trace for the lifetime of
// the daemon. Returning nil only when BOTH sinks are nil keeps the contract
// "no observers → nil listener" without hiding the log half on a manual run
// where sd_notify is inactive but the user still wants to see transitions.
func MakeStateListener(notifier *SdNotifier, log *slog.Logger) domain.TransitionListener {
	if notifier == nil && log == nil {
		return nil
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return func(newState domain.State, action domain.Action) {
		if notifier != nil {
			notifier.Status(stateNotifyText(newState))
		}

		log.Info("voice: state",
			slog.String("state", string(newState)),
			slog.String("action", string(action)),
		)
	}
}

func stateNotifyText(state domain.State) string {
	switch state {
	case domain.StateIdle:
		return "idle"
	case domain.StateRecording:
		return "recording"
	case domain.StateTranscribing:
		return "transcribing"
	case domain.StateDelivering:
		return "delivering"
	case domain.StateError:
		return "error"
	case domain.StateShuttingDown:
		return "shutting down"
	default:
		return fmt.Sprintf("unknown state %q", state)
	}
}
