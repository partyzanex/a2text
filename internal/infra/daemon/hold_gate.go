package daemon

import (
	"sync"
	"time"
)

// DefaultHoldMinDuration is the minimum time between Press and Release for a
// hold-mode hotkey to count as a real recording attempt. Anything shorter is
// treated as an accidental tap and dropped (no recording, no STT call, no
// state-machine churn).
//
// 150 ms was picked empirically: human deliberate "tap and hold" presses are
// >250 ms in practice, while spurious key bounces and accidental key strikes
// land under 100 ms.
const DefaultHoldMinDuration = 150 * time.Millisecond

// holdGate debounces hold-mode Press/Release pairs that are too short to be
// real recording attempts. It defers the actual Press dispatch by
// minDuration: if Release lands inside that window the pair is dropped
// entirely; otherwise the deferred dispatch fires and the subsequent Release
// flows through normally.
//
// Concurrency: HotkeyHandler is called from per-device goroutines inside the
// hotkey backend (evdev) but the daemon installs only one listener, so calls
// are serialised by the backend. The mutex still guards against the
// AfterFunc callback racing with an inbound Release on the listener
// goroutine.
type holdGate struct {
	mu      sync.Mutex
	timer   *time.Timer
	pending bool // Press received, deferred dispatch not yet fired
	started bool // deferred dispatch fired, awaiting Release
}

// OnPress arms the deferred dispatch. dispatch is invoked from the timer
// goroutine after minDur has elapsed, unless OnRelease cancels first.
// Repeated Press while pending or started is a no-op (key autorepeat or
// duplicate edge from a backend that does not deduplicate).
func (g *holdGate) OnPress(minDur time.Duration, dispatch func()) {
	g.mu.Lock()

	if g.started || g.pending {
		g.mu.Unlock()

		return
	}

	g.pending = true
	g.mu.Unlock()

	g.timer = time.AfterFunc(minDur, func() {
		g.mu.Lock()

		if !g.pending {
			// Cancelled by OnRelease between Stop() and the AfterFunc
			// goroutine acquiring the lock. Drop silently.
			g.mu.Unlock()

			return
		}

		g.pending = false
		g.started = true
		g.mu.Unlock()

		dispatch()
	})
}

// OnRelease reports whether the caller should dispatch the Stop event.
//
//   - pending (Press in flight, dispatch not fired): cancel the timer and
//     return false. The Press+Release pair is dropped entirely.
//   - started (dispatch fired): clear the flag and return true.
//   - neither (spurious Release with no prior Press, or a Press already
//     cancelled): return false.
func (g *holdGate) OnRelease() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.pending {
		if g.timer != nil {
			g.timer.Stop()
		}

		g.pending = false

		return false
	}

	if g.started {
		g.started = false

		return true
	}

	return false
}
