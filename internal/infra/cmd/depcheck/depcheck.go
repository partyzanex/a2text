// Package depcheck probes system dependencies for the voice daemon and its
// hidden CLI modes. It answers the question "can this execution path run?"
// before anything is started, so the user sees a human-readable error rather
// than a crash two layers deep.
//
// Entry point: [CheckMode]. Tests substitute a fake [Env] to avoid PATH,
// filesystem, and network dependencies.
package depcheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/partyzanex/a2text/pkg/hotkey"

	"github.com/partyzanex/a2text/internal/infra/config"
)

// CLIMode identifies which execution path is being checked.
// Each mode has a distinct set of required and optional dependencies.
type CLIMode int

const (
	// ModeDaemon is the default long-running daemon mode.
	// All applicable dependencies are probed (platform, capture, STT, clipboard, autopaste).
	ModeDaemon CLIMode = iota

	// ModeRecord is the hidden --record mode (capture + STT + output).
	// Same as daemon but without the platform info dep.
	ModeRecord

	// ModeFileWAV is --file with a WAV/PCM input (no audio conversion needed).
	// Only the STT dep is probed.
	ModeFileWAV

	// ModeFileAudio is --file with a non-WAV input (conversion required).
	// ffmpeg + STT are probed.
	ModeFileAudio
)

// Env groups the probe callbacks. Production callers use [DefaultEnv]; tests
// substitute fakes to avoid PATH, filesystem, and network dependencies.
type Env struct {
	// LookPath reports the path of a named binary in PATH, mirroring exec.LookPath.
	LookPath func(name string) (string, error)

	// StatFile reports whether a file exists, mirroring os.Stat.
	StatFile func(name string) (os.FileInfo, error)

	// HTTPHead makes an HTTP HEAD request and returns the status code.
	// A non-nil error means the server could not be reached.
	HTTPHead func(ctx context.Context, url string) (statusCode int, err error)

	// WhisperCppAvailable reports whether this binary was compiled with the
	// whisper-cpp CGo backend linked in (determined by the "whisper" build tag).
	WhisperCppAvailable func() bool

	// PortalAvailable reports whether org.freedesktop.portal.GlobalShortcuts
	// is registered on the D-Bus session bus. Backed by hotkey.IsPortalAvailable
	// in production; tests may inject a stub. nil means "skip the probe".
	PortalAvailable func() bool
}

// DefaultEnv returns an Env backed by the real OS, filesystem, and network.
func DefaultEnv() Env {
	return Env{
		LookPath:            exec.LookPath,
		StatFile:            os.Stat,
		HTTPHead:            defaultHTTPHead,
		WhisperCppAvailable: defaultWhisperCppAvailable,
		PortalAvailable:     hotkey.IsPortalAvailable,
	}
}

// CheckResult is the outcome of probing one dependency.
type CheckResult struct {
	Detail string // human-friendly path or status, e.g. "pw-record at /usr/bin/pw-record"
	Found  bool
}

// Dependency describes one system dependency and how to probe it.
type Dependency struct {
	// Check probes the actual availability of the dependency.
	// It must not modify any global state and should return quickly.
	// ctx is forwarded from the caller — probes that make network or
	// blocking I/O calls must respect cancellation.
	// Placed first to minimise GC pointer scan range.
	Check func(ctx context.Context, env Env) CheckResult
	// Name is the short identifier printed in depcheck output.
	Name string
	// Group groups related deps in the report (System, Audio, STT, Clipboard, Autopaste).
	Group string
	// InstallHint is the install command shown when the dep is missing.
	InstallHint string
	// RequiredFor is a human description used in [MissingDependencyError] messages.
	RequiredFor string
	// Optional marks deps that trigger WARN (not fatal) when missing.
	// Missing optional deps cause a graceful degradation; missing required deps → exit 1.
	Optional bool
}

// MissingDep is a dependency that failed its probe.
type MissingDep struct {
	Dep Dependency
}

// MissingDependencyError is the fatal error emitted when a required dep is absent.
// It formats as:
//
//	missing dependency: ffmpeg
//	  required for: audio conversion to WAV
//	  install: sudo apt install ffmpeg
type MissingDependencyError struct {
	Name        string
	RequiredFor string
	InstallHint string
}

func (e *MissingDependencyError) Error() string {
	return fmt.Sprintf(
		"missing dependency: %s\n  required for: %s\n  install: %s",
		e.Name, e.RequiredFor, e.InstallHint,
	)
}

// CheckMode returns all dependencies applicable for the given mode and config,
// plus the subset that failed their probe. Callers inspect [MissingDep.Dep.Optional]
// to distinguish optional warnings from fatal failures.
//
// A nil cfg always returns a single fatal missing dep describing the wiring bug.
func CheckMode(ctx context.Context, mode CLIMode, cfg *config.VoiceConfig, env Env) ([]Dependency, []MissingDep) {
	if cfg == nil {
		dep := nilConfigDep()

		return []Dependency{dep}, []MissingDep{{Dep: dep}}
	}

	deps := buildDeps(mode, cfg)

	var missing []MissingDep

	for i := range deps {
		dep := &deps[i]
		if res := dep.Check(ctx, env); !res.Found {
			missing = append(missing, MissingDep{Dep: *dep})
		}
	}

	return deps, missing
}
