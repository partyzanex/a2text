package factory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/partyzanex/a2text/internal/adapters/output"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/usecases/voice"
	"github.com/partyzanex/a2text/pkg/clipboard"
)

//go:generate go run go.uber.org/mock/mockgen@latest -package=factory -destination=output_builder_mocks_test.go -source=output_builder.go

// SessionClipboard is the minimal interface the daemon needs from a clipboard
// backend. Defined here (consumer side) — adapters/clipboard exposes concrete
// types; wiring provides structural compatibility.
//
// CopyTyped is part of the contract because the restore-clipboard path
// reuses the same backend to write non-text payloads back. Both
// WaylandClipboard and X11Clipboard implement it.
type SessionClipboard interface {
	Copy(ctx context.Context, text string) error
	CopyTyped(ctx context.Context, mime string, data []byte) error
}

// SessionClipboardReader is the minimal read-side contract used by the
// restore-clipboard path. Mirrors the producer/consumer split already in
// place for SessionClipboard.
type SessionClipboardReader interface {
	Snapshot(ctx context.Context) (clipboard.Snapshot, error)
}

// SessionAutopaster is the minimal interface the daemon needs from an autopaste
// backend. Same consumer-side pattern as SessionClipboard.
type SessionAutopaster interface {
	Paste(ctx context.Context) error
	Backend() string
}

// clipboardBuilderFn is the function type used to create a session-aware
// clipboard backend. Extracted as a type so buildOutputWith tests can inject
// a fake without depending on real Wayland/X11 session state.
type clipboardBuilderFn func(log *slog.Logger) (SessionClipboard, error)

// autopasteBuilderFn is the function type used to create a session-aware
// autopaste backend.
type autopasteBuilderFn func(cmd string, log *slog.Logger) (SessionAutopaster, error)

// clipboardReaderBuilderFn is the function type used to create a
// session-aware clipboard reader. Same DI pattern as clipboardBuilderFn.
type clipboardReaderBuilderFn func(log *slog.Logger) (SessionClipboardReader, error)

// buildSessionClipboard detects the session type and returns the appropriate
// clipboard backend. Wayland is preferred; X11 is the fallback; both are
// probed when the session type cannot be determined.
//
// factory: session detection selects Wayland or X11 concrete type at runtime;
// SessionClipboard is the only stable contract.
func buildSessionClipboard(log *slog.Logger) (SessionClipboard, error) {
	if clipboard.DetectWayland() {
		if wl, err := clipboard.NewWaylandClipboard(log); err == nil {
			return wl, nil
		}
	}

	if clipboard.DetectX11() {
		if x11, err := clipboard.NewX11Clipboard(log); err == nil {
			return x11, nil
		}
	}

	// Session env absent or unknown — probe both.
	if wl, err := clipboard.NewWaylandClipboard(log); err == nil {
		return wl, nil
	}

	if x11, err := clipboard.NewX11Clipboard(log); err == nil {
		return x11, nil
	}

	return nil, clipboard.ErrNoBackend
}

// buildSessionClipboardReader mirrors buildSessionClipboard for the
// read side: wl-paste preferred on Wayland, xclip on X11, both probed
// when the session type is unknown. Returns clipboard.ErrNoBackend when
// nothing is in PATH — callers should treat that as "skip restore".
func buildSessionClipboardReader(log *slog.Logger) (SessionClipboardReader, error) {
	if clipboard.DetectWayland() {
		if r, err := clipboard.NewWaylandClipboardReader(log); err == nil {
			return r, nil
		}
	}

	if clipboard.DetectX11() {
		if r, err := clipboard.NewX11ClipboardReader(log); err == nil {
			return r, nil
		}
	}

	if r, err := clipboard.NewWaylandClipboardReader(log); err == nil {
		return r, nil
	}

	if r, err := clipboard.NewX11ClipboardReader(log); err == nil {
		return r, nil
	}

	return nil, clipboard.ErrNoBackend
}

// buildSessionAutopaster detects the session type and returns the appropriate
// autopaste backend. Wayland is never mixed with X11 to avoid injecting
// keystrokes into the wrong surface.
//
// The "uinput" backend is session-agnostic (talks to /dev/uinput, the
// compositor routes events) and is therefore checked before the Wayland/X11
// split.
//
// factory: session detection selects Wayland or X11 concrete type at runtime;
// SessionAutopaster is the only stable contract.
func buildSessionAutopaster(cmd string, log *slog.Logger) (SessionAutopaster, error) {
	if cmd == clipboard.AutopasteBackendUinput {
		ua, err := clipboard.NewUinputAutopaster(log)
		if err != nil {
			return nil, fmt.Errorf("output builder: %w", err)
		}

		return ua, nil
	}

	if clipboard.DetectWayland() {
		return buildWaylandAutopaster(cmd, log)
	}

	if clipboard.DetectX11() {
		return buildX11Autopaster(cmd, log)
	}

	// Session unknown: probe both, Wayland first.
	wa, err := clipboard.NewWaylandAutopaster(cmd, log)
	if err == nil {
		return wa, nil
	}

	if errors.Is(err, clipboard.ErrUnsupportedAutopasteBackend) {
		return nil, fmt.Errorf("output builder: %w", err)
	}

	return buildX11Autopaster(cmd, log)
}

// buildWaylandAutopaster creates a Wayland autopaster. Does not fall through
// to X11 — mixing protocols risks injecting keystrokes into the wrong surface.
func buildWaylandAutopaster(cmd string, log *slog.Logger) (SessionAutopaster, error) {
	wa, err := clipboard.NewWaylandAutopaster(cmd, log)
	if err == nil {
		return wa, nil
	}

	if errors.Is(err, clipboard.ErrUnsupportedAutopasteBackend) {
		return nil, fmt.Errorf("output builder: %w", err)
	}

	return nil, fmt.Errorf(
		"%w: wayland session detected but no wayland autopaste binary found",
		clipboard.ErrNoAutopasteBackend,
	)
}

// buildX11Autopaster creates an X11 autopaster.
func buildX11Autopaster(cmd string, log *slog.Logger) (SessionAutopaster, error) {
	xa, err := clipboard.NewX11Autopaster(cmd, log)
	if err == nil {
		return xa, nil
	}

	if errors.Is(err, clipboard.ErrUnsupportedAutopasteBackend) {
		return nil, fmt.Errorf("output builder: %w", err)
	}

	return nil, clipboard.ErrNoAutopasteBackend
}

// snapshotterAdapter converts a SessionClipboardReader (returning the
// pkg/clipboard Snapshot type) into the consumer-side
// output.ClipboardSnapshotter (returning output.ClipboardSnapshot).
// Two near-identical structs exist so that internal/adapters/output can
// stay free of the pkg/clipboard import.
type snapshotterAdapter struct {
	reader SessionClipboardReader
}

func (a snapshotterAdapter) Snapshot(ctx context.Context) (output.ClipboardSnapshot, error) {
	snap, err := a.reader.Snapshot(ctx)
	if err != nil {
		return output.ClipboardSnapshot{}, fmt.Errorf("snapshotter adapter: %w", err)
	}

	return output.ClipboardSnapshot{
		MIME:  snap.MIME,
		Data:  snap.Data,
		Empty: snap.Empty,
	}, nil
}

// BuildOutput wires the user's preferred output mode. Four branches,
// chosen by cfg.Output.Mode (canonical after LoadVoice promotion):
//
//  1. "stdout": print to stdout.
//  2. "clipboard" (default) / "": session-aware clipboard (Wayland wl-copy
//     or X11 xclip) with stdout fallback.
//  3. "clipboard_autopaste": clipboard + simulated Ctrl+V. Session-aware:
//     Wayland → wtype/ydotool, X11 → xdotool. If the binary is missing the
//     daemon degrades to plain clipboard. If the backend name is unrecognised
//     (config typo), the error is returned to the caller.
//  4. unknown mode: returns an error — not a silent clipboard fallback.
//
// The "no clipboard backend" path is logged at INFO, not WARN: on headless /
// CI machines this is the expected configuration, not a misbehaviour.
//
// factory: output implementation chosen at runtime by mode config;
// voice.Output is the only stable contract for the caller.
func BuildOutput(cfg *config.VoiceConfig, log *slog.Logger) (voice.Output, error) {
	return buildOutputWith(cfg, log, buildSessionClipboard, buildSessionAutopaster, buildSessionClipboardReader)
}

// factory: output implementation chosen at runtime by mode config;
// voice.Output is the only stable contract for the caller.
func buildOutputWith(
	cfg *config.VoiceConfig,
	log *slog.Logger,
	newClip clipboardBuilderFn,
	newAutopaste autopasteBuilderFn,
	newReader clipboardReaderBuilderFn,
) (voice.Output, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	// stdoutOut is wired ONLY for the explicit `mode: stdout` case. Every
	// other path (no-clipboard fallback, runtime clipboard error fallback,
	// nil-cfg defensive default) goes through logOut: mixing plain-text
	// transcripts into a JSON-logging daemon's stdout breaks log shippers
	// and forces operators to parse two formats.
	stdoutOut := output.NewStdoutOutput()
	logOut := output.NewLogOutput(log)

	if cfg == nil {
		log.Warn("voice: nil config in BuildOutput, defaulting to debug-log output")

		return logOut, nil
	}

	// Resolve output mode.
	mode := cfg.Output.Mode

	switch mode {
	case config.VoiceOutputModeStdout:
		return stdoutOut, nil
	case "", config.VoiceOutputModeClipboard, config.VoiceOutputModeClipboardAutopaste:
		// handled below
	default:
		return nil, fmt.Errorf("voice: unknown output mode %q", mode)
	}

	// Session-aware clipboard: auto-detects Wayland (wl-copy) vs X11 (xclip).
	clip, clipErr := newClip(log)
	if clipErr != nil {
		log.Info("voice: no clipboard backend, transcripts will be logged at DEBUG",
			slog.Any("reason", clipErr),
		)

		return logOut, nil
	}

	// Clipboard runtime fallback also routes through the structured log,
	// not stdout — same JSON-only invariant as above.
	clipboardOut := output.NewClipboardOutput(clip, logOut, log)

	if mode != config.VoiceOutputModeClipboardAutopaste {
		return clipboardOut, nil
	}

	return buildAutopasteOutput(cfg, log, newAutopaste, newReader, clip, clipboardOut)
}

// buildAutopasteOutput wires the autopaste layer on top of clipboard
// output, and — when cfg.Output.RestoreClipboard is true — the
// snapshot-and-restore layer on top of that.
func buildAutopasteOutput(
	cfg *config.VoiceConfig,
	log *slog.Logger,
	newAutopaste autopasteBuilderFn,
	newReader clipboardReaderBuilderFn,
	clip SessionClipboard,
	clipboardOut voice.Output,
) (voice.Output, error) {
	paster, autopasteErr := newAutopaste(cfg.Output.AutopasteCommand, log)
	if autopasteErr != nil {
		if errors.Is(autopasteErr, clipboard.ErrUnsupportedAutopasteBackend) {
			log.Error("voice: unsupported autopaste backend — check autopaste_command in config",
				slog.String("autopaste_command", cfg.Output.AutopasteCommand),
				slog.Any("err", autopasteErr),
			)

			return nil, fmt.Errorf("voice: build output: %w", autopasteErr)
		}

		log.Warn("voice: autopaste requested but no backend available, falling back to clipboard-only",
			slog.String("autopaste_command", cfg.Output.AutopasteCommand),
			slog.Any("err", autopasteErr),
		)

		return clipboardOut, nil
	}

	out := output.NewClipboardAutopasteOutput(clipboardOut, paster, 0, log)

	if !cfg.Output.RestoreClipboard {
		log.Info("voice: clipboard restore disabled")

		return out, nil
	}

	log.Info("voice: clipboard restore enabled")

	// Restore requested but reader unavailable → degrade to "paste only,
	// no restore" with a WARN so the user sees why their previous copy
	// vanished. Continuing is preferred over failing daemon startup —
	// the transcript still gets pasted, which is the primary feature.
	reader, readerErr := newReader(log)
	if readerErr != nil {
		log.Warn("voice: clipboard restore requested but no reader backend available",
			slog.Any("err", readerErr),
		)

		return out, nil
	}

	return out.WithClipboardRestore(snapshotterAdapter{reader: reader}, clip), nil
}
