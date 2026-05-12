package hotkey

import "context"

// Event distinguishes key press from key release. Backends that can
// observe both (XGrabKey, xdg-desktop-portal GlobalShortcuts) deliver
// both; backends that only see press (DE shortcut launching a process)
// deliver Press only and the listener never sees a release.
type Event int

const (
	// Press fires when the bound key combination transitions from
	// up to down. Toggle-mode bindings react only to this.
	Press Event = iota

	// Release fires when the bound key combination transitions from
	// down to up. Hold-mode bindings stop recording on this.
	Release
)

// Handler is the callback invoked on every hotkey transition the
// backend observes. evt tells the consumer which edge fired so
// push-to-talk hold and click-to-toggle behaviours can share the same
// listener.
//
// Handler must not block: it is called synchronously from the backend's
// event-loop goroutine, so a blocking Handler stalls all subsequent
// key events. Long-running work must be dispatched to a separate
// goroutine.
//
// Backends that cannot distinguish press from release MUST emit
// Press only. Consumers that care about release degrade to toggle
// when only Press is observed.
type Handler func(ctx context.Context, evt Event)

// Listener registers a global hotkey and invokes the Handler on each
// observed edge (press, optionally release).
//
// Stop releases the grab and cleans up resources. Stop is idempotent.
type Listener interface {
	// Listen starts listening for the hotkey. Blocks until ctx is
	// cancelled or Stop is called. Only one Listen call is allowed
	// per Listener.
	Listen(ctx context.Context) error

	// Stop releases the hotkey grab. Safe to call multiple times.
	Stop() error
}
