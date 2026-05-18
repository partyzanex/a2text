package depcheck

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/partyzanex/a2text/internal/infra/config"
)

// Group labels for depcheck output. Match the labels used in daemon log lines.
const (
	GroupSystem    = "System"
	GroupAudio     = "Audio"
	GroupSTT       = "STT"
	GroupClipboard = "Clipboard"
	GroupAutopaste = "Autopaste"
	GroupHotkey    = "Hotkey"
)

const sttRequiredFor = "speech-to-text transcription"

// maxLabelLen is the maximum length (in runes) of a user-controlled string
// that depcheck may embed in Dependency.Name or Dependency.InstallHint.
// Truncation prevents log-line explosions from garbage config values.
const maxLabelLen = 64

// sanitizeLabel makes a user-controlled string safe for use in depcheck output.
// It replaces control characters (newlines, tabs, …) with spaces, trims
// surrounding whitespace, and truncates to maxLabelLen runes.
func sanitizeLabel(raw string) string {
	raw = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 {
			return ' '
		}

		return r
	}, raw)

	raw = strings.TrimSpace(raw)

	if utf8.RuneCountInString(raw) > maxLabelLen {
		runes := []rune(raw)
		raw = string(runes[:maxLabelLen]) + "…"
	}

	return raw
}

// effectiveOutputMode resolves the nested Output.Mode vs deprecated flat
// OutputMode with trim+lower normalization. Mirrors the logic in daemon.go
// buildOutputWith so depcheck and runtime routing cannot diverge.
func effectiveOutputMode(cfg *config.VoiceConfig) string {
	return strings.ToLower(strings.TrimSpace(cfg.Output.Mode))
}

// effectiveAutopasteCommand resolves nested Output.AutopasteCommand vs
// deprecated flat AutopasteCommand with trim+lower normalization.
func effectiveAutopasteCommand(cfg *config.VoiceConfig) string {
	return strings.ToLower(strings.TrimSpace(cfg.Output.AutopasteCommand))
}

// buildDeps returns the ordered list of Dependency values applicable to the
// given mode and config. Probes are not run here; CheckMode runs them.
func buildDeps(mode CLIMode, cfg *config.VoiceConfig) []Dependency {
	if cfg == nil {
		return []Dependency{nilConfigDep()}
	}

	var deps []Dependency

	switch mode {
	case ModeDaemon:
		deps = append(deps, platformDep())
		deps = append(deps, captureDeps()...)
		deps = append(deps, sttDeps(cfg, true)...)
		deps = append(deps, clipboardDep())
		deps = append(deps, autopasteDeps(cfg)...)
		deps = append(deps, hotkeyDeps(cfg)...)

	case ModeRecord:
		// Same as daemon without platform info: daemon bootstraps it; record is one-shot.
		deps = append(deps, captureDeps()...)
		deps = append(deps, sttDeps(cfg, true)...)
		deps = append(deps, clipboardDep())
		deps = append(deps, autopasteDeps(cfg)...)

	case ModeFileWAV:
		// WAV file is already in the correct format — no conversion step.
		deps = append(deps, sttDeps(cfg, false)...)

	case ModeFileAudio:
		// Non-WAV input requires ffmpeg conversion before STT.
		deps = append(deps, ffmpegDep())
		deps = append(deps, sttDeps(cfg, false)...)

	default:
		// Unknown mode is an internal wiring bug — surface it as a fatal dep
		// instead of silently returning an empty list (which would look like
		// "all deps satisfied").
		return []Dependency{{
			Name:        "mode",
			Group:       GroupSystem,
			RequiredFor: "dependency check",
			InstallHint: fmt.Sprintf("internal error: unknown depcheck mode %d", int(mode)),
			Check:       func(_ context.Context, _ Env) CheckResult { return CheckResult{} },
		}}
	}

	return deps
}

// nilConfigDep is the sentinel dep emitted when cfg is nil (internal wiring bug).
func nilConfigDep() Dependency {
	return Dependency{
		Name:        "config",
		Group:       GroupSystem,
		InstallHint: "internal error: nil voice config — check daemon wiring",
		RequiredFor: "daemon startup",
		Check:       func(_ context.Context, _ Env) CheckResult { return CheckResult{Found: false} },
	}
}

func platformDep() Dependency {
	return Dependency{
		Name:  "platform",
		Group: GroupSystem,
		Check: func(_ context.Context, _ Env) CheckResult {
			return CheckResult{Found: true, Detail: runtime.GOOS + "/" + runtime.GOARCH}
		},
	}
}

const captureDistroHint = "Debian/Ubuntu: apt install pipewire-bin or pulseaudio-utils; " +
	"Fedora: dnf install pipewire-utils pulseaudio-utils; " +
	"Arch: pacman -S pipewire pulseaudio-utils"

// captureDeps returns the audio-capture dependency list.
// Capture is Linux-only (ADR-0011): non-Linux returns a single required-missing dep so
// depcheck surfaces the platform limitation instead of silently skipping the check.
func captureDeps() []Dependency {
	if runtime.GOOS != "linux" {
		return []Dependency{{
			Name:        "capture",
			Group:       GroupAudio,
			InstallHint: "microphone capture is only implemented on Linux (see ADR-0011)",
			RequiredFor: "microphone recording",
			Check:       func(_ context.Context, _ Env) CheckResult { return CheckResult{Found: false} },
		}}
	}

	return []Dependency{{
		Name:        "capture",
		Group:       GroupAudio,
		InstallHint: captureDistroHint,
		RequiredFor: "microphone recording",
		Check: func(_ context.Context, env Env) CheckResult {
			if _, err := env.LookPath("pw-record"); err == nil {
				return CheckResult{Found: true, Detail: "pw-record"}
			}

			if _, err := env.LookPath("parecord"); err == nil {
				return CheckResult{Found: true, Detail: "parecord"}
			}

			return CheckResult{}
		},
	}}
}

func ffmpegDep() Dependency {
	return Dependency{
		Name:  "ffmpeg",
		Group: GroupAudio,
		InstallHint: "Debian/Ubuntu: apt install ffmpeg; " +
			"Fedora: dnf install ffmpeg; " +
			"Arch: pacman -S ffmpeg",
		RequiredFor: "audio conversion to WAV",
		Check: func(_ context.Context, env Env) CheckResult {
			if _, err := env.LookPath("ffmpeg"); err != nil {
				return CheckResult{}
			}

			return CheckResult{Found: true, Detail: "ffmpeg"}
		},
	}
}

// sttDeps returns the STT dependency list for the given provider.
// withConversion=true adds ffmpeg for whisper-cpp (needed in daemon/record mode
// to convert incoming audio; not needed for --file path.wav where the input is
// already in the correct format).
//
// Contract: cfg must not be nil (caller buildDeps guarantees this).
func sttDeps(cfg *config.VoiceConfig, withConversion bool) []Dependency {
	if cfg == nil {
		return []Dependency{nilConfigDep()}
	}

	switch cfg.Provider {
	case config.VoiceProviderGoWhisper:
		return goWhisperDeps(cfg)
	case config.VoiceProviderOpenAI:
		return openAIDeps(cfg)
	case config.VoiceProviderDeepgram:
		return deepgramDeps(cfg)
	case config.VoiceProviderWhisperCpp:
		return whisperCppDeps(cfg, withConversion)
	default:
		return unknownProviderDep(cfg.Provider)
	}
}

// goWhisperDeps builds the go-whisper dependency entry.
// Contract: cfg must not be nil.
func goWhisperDeps(cfg *config.VoiceConfig) []Dependency {
	if cfg == nil {
		return []Dependency{nilConfigDep()}
	}

	if cfg.GoWhisper.URL == "" {
		return []Dependency{{
			Name:        config.VoiceProviderGoWhisper,
			Group:       GroupSTT,
			InstallHint: "set go_whisper.url in config (e.g. http://localhost:9081)",
			RequiredFor: sttRequiredFor,
			Check:       func(_ context.Context, _ Env) CheckResult { return CheckResult{} },
		}}
	}

	// Probe the model-list endpoint: any HTTP response (even 4xx) proves the
	// service is up. A network error means the daemon cannot reach it.
	probeURL := strings.TrimRight(cfg.GoWhisper.URL, "/") + "/model"

	return []Dependency{{
		Name:  config.VoiceProviderGoWhisper,
		Group: GroupSTT,
		InstallHint: "start the go-whisper service (e.g. docker compose up go-whisper); " +
			"verify go_whisper.url and go_whisper.prefix in config",
		RequiredFor: sttRequiredFor,
		Check: func(ctx context.Context, env Env) CheckResult {
			if _, err := env.HTTPHead(ctx, probeURL); err != nil {
				return CheckResult{}
			}

			return CheckResult{Found: true, Detail: sanitizeURL(cfg.GoWhisper.URL)}
		},
	}}
}

// openAIDeps builds the OpenAI Whisper cloud-provider dependency entry.
func openAIDeps(cfg *config.VoiceConfig) []Dependency {
	if cfg == nil {
		return []Dependency{nilConfigDep()}
	}

	return []Dependency{{
		Name:        config.VoiceProviderOpenAI,
		Group:       GroupSTT,
		InstallHint: "set openai.api_key (or A2TEXT_OPENAI_API_KEY env var)",
		RequiredFor: sttRequiredFor,
		Check: func(_ context.Context, _ Env) CheckResult {
			if cfg.OpenAI.APIKey == "" {
				return CheckResult{}
			}

			return CheckResult{Found: true, Detail: config.VoiceProviderOpenAI}
		},
	}}
}

// deepgramDeps builds the Deepgram cloud-provider dependency entry.
func deepgramDeps(cfg *config.VoiceConfig) []Dependency {
	if cfg == nil {
		return []Dependency{nilConfigDep()}
	}

	return []Dependency{{
		Name:        config.VoiceProviderDeepgram,
		Group:       GroupSTT,
		InstallHint: "set deepgram.api_key (or A2TEXT_DEEPGRAM_API_KEY env var)",
		RequiredFor: sttRequiredFor,
		Check: func(_ context.Context, _ Env) CheckResult {
			if cfg.Deepgram.APIKey == "" {
				return CheckResult{}
			}

			return CheckResult{Found: true, Detail: config.VoiceProviderDeepgram}
		},
	}}
}

// whisperCppDeps builds the whisper-cpp dependency entries.
// Contract: cfg must not be nil.
func whisperCppDeps(cfg *config.VoiceConfig, withConversion bool) []Dependency {
	if cfg == nil {
		return []Dependency{nilConfigDep()}
	}

	cppDep := Dependency{
		Name:  "whisper-cpp",
		Group: GroupSTT,
		InstallHint: "rebuild a2text with -tags whisper; " +
			"ensure model_path points to a downloaded GGML model",
		RequiredFor: sttRequiredFor,
		Check: func(_ context.Context, env Env) CheckResult {
			if !env.WhisperCppAvailable() {
				// Hard miss: binary was built without -tags whisper.
				// No model_path can save this — user must rebuild.
				return CheckResult{}
			}

			if cfg.ModelPath == "" {
				// Soft miss: binary is linked but user has not yet
				// pointed the daemon at a model file. Treat as "found"
				// for depcheck purposes so the daemon still boots and
				// the settings window can be opened to configure /
				// download a model. The actual STT call will surface
				// a clear runtime error if it fires before a model is
				// set.
				return CheckResult{Found: true, Detail: "linked; model not configured"}
			}

			if _, err := env.StatFile(cfg.ModelPath); err != nil {
				// Model path is set but file is missing — likely the
				// user moved or deleted it. Same boot-tolerant
				// treatment as the empty-path case: log via Detail
				// and let the user fix it in settings.
				return CheckResult{Found: true, Detail: "linked; model missing on disk"}
			}

			// Use basename only — full path may reveal sensitive home directory structure.
			return CheckResult{Found: true, Detail: "linked; model " + filepath.Base(cfg.ModelPath)}
		},
	}

	if withConversion {
		// Daemon / record: incoming audio needs ffmpeg → WAV conversion before whisper-cpp.
		return []Dependency{cppDep, ffmpegDep()}
	}

	return []Dependency{cppDep}
}

func unknownProviderDep(provider string) []Dependency {
	label := sanitizeLabel(provider)
	if label == "" {
		label = "<empty>"
	}

	return []Dependency{{
		Name:  label,
		Group: GroupSTT,
		InstallHint: fmt.Sprintf(
			"set provider to one of: %s, %s, %s, %s",
			config.VoiceProviderGoWhisper,
			config.VoiceProviderWhisperCpp,
			config.VoiceProviderOpenAI,
			config.VoiceProviderDeepgram,
		),
		RequiredFor: sttRequiredFor,
		Check:       func(_ context.Context, _ Env) CheckResult { return CheckResult{} },
	}}
}

// clipboardDep probes both clipboard backends in the same order as the runtime:
// wl-copy (Wayland) first, xclip (X11) second. Reporting "found" when either is
// present keeps depcheck honest — no spurious WARN when xclip is installed on an
// X11 session.
func clipboardDep() Dependency {
	return Dependency{
		Name:  "wl-copy / xclip",
		Group: GroupClipboard,
		InstallHint: "Wayland: apt install wl-clipboard / dnf install wl-clipboard / pacman -S wl-clipboard; " +
			"X11: apt install xclip / dnf install xclip / pacman -S xclip; " +
			"or set output.mode to stdout",
		RequiredFor: "clipboard output",
		Optional:    true,
		Check: func(_ context.Context, env Env) CheckResult {
			if _, err := env.LookPath("wl-copy"); err == nil {
				return CheckResult{Found: true, Detail: "wl-copy"}
			}

			if _, err := env.LookPath("xclip"); err == nil {
				return CheckResult{Found: true, Detail: "xclip"}
			}

			return CheckResult{}
		},
	}
}

// autopasteDeps returns the autopaste dependency list.
// Returns nil when OutputMode is not clipboard_autopaste — no point surfacing
// "wtype not found" on every restart for the 95% of users who use plain clipboard.
func autopasteDeps(cfg *config.VoiceConfig) []Dependency {
	if cfg == nil {
		return nil
	}

	// Normalise to match the adapter contract. ValidateVoice already trims, but
	// mirroring here keeps depcheck honest when VoiceConfig is built outside LoadVoice.
	mode := effectiveOutputMode(cfg)

	if mode != config.VoiceOutputModeClipboardAutopaste {
		return nil
	}

	cmd := effectiveAutopasteCommand(cfg)

	switch cmd {
	case config.VoiceAutopasteCommandWtype:
		return []Dependency{probeBinaryDep(
			GroupAutopaste,
			config.VoiceAutopasteCommandWtype,
			"Wayland key injection (autopaste)",
			"Debian/Ubuntu: apt install wtype; Fedora: dnf install wtype; Arch: pacman -S wtype",
		)}

	case config.VoiceAutopasteCommandYdotool:
		return []Dependency{probeBinaryDep(
			GroupAutopaste,
			config.VoiceAutopasteCommandYdotool,
			"Wayland key injection (autopaste)",
			"Debian/Ubuntu: apt install ydotool; Fedora: dnf install ydotool; Arch: pacman -S ydotool"+
				" (also ensure ydotoold is running and /dev/uinput is writable)",
		)}

	case config.VoiceAutopasteCommandXdotool:
		return []Dependency{probeBinaryDep(
			GroupAutopaste,
			config.VoiceAutopasteCommandXdotool,
			"X11/XWayland key injection (autopaste)",
			"Debian/Ubuntu: apt install xdotool; Fedora: dnf install xdotool; Arch: pacman -S xdotool",
		)}

	case config.VoiceAutopasteCommandUinput:
		return []Dependency{uinputAutopasteDep()}

	case "", config.VoiceAutopasteCommandAuto:
		return []Dependency{autoAutopasteDep()}

	default:
		return []Dependency{unknownAutopasteDep(cmd)}
	}
}

// uinputAutopasteDep returns a dependency for the persistent Go uinput backend.
// Verifies /dev/uinput exists, is writable, and (on Linux) the current user
// is in the `input` group — the most common cause of silent autopaste
// failure on Wayland.
func uinputAutopasteDep() Dependency {
	return Dependency{
		Name:        config.VoiceAutopasteCommandUinput,
		Group:       GroupAutopaste,
		RequiredFor: "Wayland/X11 key injection via persistent uinput virtual keyboard",
		InstallHint: "add user to input group (sudo usermod -aG input $USER) and re-login, " +
			"or grant per-user ACL: sudo setfacl -m u:$USER:rw /dev/uinput",
		Check: func(_ context.Context, env Env) CheckResult {
			return checkUinputAccess(env)
		},
	}
}

// checkUinputAccess inspects /dev/uinput accessibility and user group
// membership. Returns a CheckResult with a human-readable Detail describing
// the first blocker, or Found=true when everything passes.
func checkUinputAccess(env Env) CheckResult {
	const path = "/dev/uinput"

	if _, err := env.StatFile(path); err != nil {
		return CheckResult{Detail: path + " missing — kernel uinput module not loaded?"}
	}

	if err := syscall.Access(path, unix.W_OK); err != nil {
		groups, gerr := userGroupNames()
		if gerr != nil {
			return CheckResult{Detail: path + " not writable; group lookup failed: " + gerr.Error()}
		}

		if !slices.Contains(groups, "input") {
			return CheckResult{
				Detail: path + " not writable; user not in 'input' group (groups: " +
					strings.Join(groups, ",") + ")",
			}
		}

		return CheckResult{Detail: path + " not writable for current user"}
	}

	return CheckResult{Found: true, Detail: path + " writable"}
}

// userGroupNames returns the supplementary group names of the current user.
// Empty slice on any error; depcheck never fails because group lookup fails.
func userGroupNames() ([]string, error) {
	usr, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("depcheck: lookup current user: %w", err)
	}

	gids, err := usr.GroupIds()
	if err != nil {
		return nil, fmt.Errorf("depcheck: lookup group ids: %w", err)
	}

	names := make([]string, 0, len(gids))

	for _, gid := range gids {
		grp, err := user.LookupGroupId(gid)
		if err != nil {
			continue
		}

		names = append(names, grp.Name)
	}

	return names, nil
}

// autoAutopasteDep returns the dependency for the "auto" autopaste backend
// which probes for wtype or ydotool.
func autoAutopasteDep() Dependency {
	return Dependency{
		Name: config.VoiceAutopasteCommandWtype + "/" +
			config.VoiceAutopasteCommandYdotool + "/" +
			config.VoiceAutopasteCommandXdotool,
		Group: GroupAutopaste,
		InstallHint: "install wtype OR ydotool OR xdotool for autopaste; " +
			"Debian/Ubuntu: apt install wtype; Fedora: dnf install wtype; Arch: pacman -S wtype",
		RequiredFor: "key injection (autopaste)",
		Optional:    true,
		Check: func(_ context.Context, env Env) CheckResult {
			for _, name := range []string{
				config.VoiceAutopasteCommandWtype,
				config.VoiceAutopasteCommandYdotool,
				config.VoiceAutopasteCommandXdotool,
			} {
				if _, err := env.LookPath(name); err == nil {
					return CheckResult{Found: true, Detail: name}
				}
			}

			return CheckResult{}
		},
	}
}

// unknownAutopasteDep returns a fatal dependency for an unrecognised
// autopaste_command value.
func unknownAutopasteDep(cmd string) Dependency {
	return Dependency{
		Name:  "autopaste_command",
		Group: GroupAutopaste,
		InstallHint: fmt.Sprintf(
			"unsupported autopaste_command %q; use %q, %q, %q, %q or %q",
			sanitizeLabel(cmd),
			config.VoiceAutopasteCommandAuto,
			config.VoiceAutopasteCommandWtype,
			config.VoiceAutopasteCommandYdotool,
			config.VoiceAutopasteCommandXdotool,
			config.VoiceAutopasteCommandUinput,
		),
		RequiredFor: "autopaste backend selection",
		Check:       func(_ context.Context, _ Env) CheckResult { return CheckResult{} },
	}
}

// hotkeyDeps returns the dependencies for the built-in global hotkey listener.
// evdev is the only backend; the listener is always active.
func hotkeyDeps(cfg *config.VoiceConfig) []Dependency {
	if cfg == nil {
		return nil
	}

	return []Dependency{evdevHotkeyDep()}
}

// firstReadable returns true if the given /dev/input/event* path can be
// opened for reading. The file is closed before returning; a Close error
// (rare on a fresh just-opened device) is logged-and-swallowed because
// failing the probe here would mask the real signal — "the user has read
// access" — that the open succeeded.
func firstReadable(path string) bool {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return false
	}

	if closeErr := file.Close(); closeErr != nil {
		// stderr is the only sink available from a probe Check (no logger
		// is plumbed through). The line is rare and operator-visible.
		fmt.Fprintf(os.Stderr, "depcheck: close %s after probe: %v\n", path, closeErr)
	}

	return true
}

// evdevHotkeyDep probes /dev/input for at least one readable event device.
// The backend itself iterates all event nodes and skips unreadable ones, so
// the probe answers "does the daemon have ANY access at all?" — usually a
// proxy for "is the user in the input group?".
func evdevHotkeyDep() Dependency {
	return Dependency{
		Name:        "evdev",
		Group:       GroupHotkey,
		RequiredFor: "Linux raw key events (hotkey backend=evdev)",
		InstallHint: "ensure the user is in the 'input' group: sudo usermod -aG input $USER && relogin; " +
			"or set ACLs on /dev/input/event*",
		Check: func(_ context.Context, _ Env) CheckResult {
			matches, err := filepath.Glob("/dev/input/event*")
			if err != nil || len(matches) == 0 {
				return CheckResult{Detail: "no /dev/input/event* devices"}
			}

			for _, path := range matches {
				if firstReadable(path) {
					return CheckResult{Found: true, Detail: path}
				}
			}

			return CheckResult{Detail: "no readable /dev/input/event* device"}
		},
	}
}

// probeBinaryDep is a helper for explicit-backend branches that probe one binary.
func probeBinaryDep(group, name, requiredFor, installHint string) Dependency {
	return Dependency{
		Name:        name,
		Group:       group,
		InstallHint: installHint,
		RequiredFor: requiredFor,
		Optional:    true,
		Check: func(_ context.Context, env Env) CheckResult {
			if _, err := env.LookPath(name); err != nil {
				return CheckResult{}
			}

			return CheckResult{Found: true, Detail: name}
		},
	}
}
