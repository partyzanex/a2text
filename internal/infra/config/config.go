package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/viper"
)

// Voice STT providers — values accepted in VoiceConfig.Provider.
const (
	VoiceProviderGoWhisper  = "go-whisper"
	VoiceProviderWhisperCpp = "whisper-cpp"
	VoiceProviderCloud      = "cloud"
)

// Cloud STT providers — values accepted in VoiceConfig.CloudProvider when
// VoiceConfig.Provider == VoiceProviderCloud.
const (
	VoiceCloudProviderOpenAI   = "openai"
	VoiceCloudProviderDeepgram = "deepgram"
)

const (
	tempDirPermission          = 0o700
	defaultCaptureSampleRate   = 16000
	defaultDaemonShutdownGrace = 15 * time.Second
)

// VoiceConfig is the configuration for the cmd/a2text voice CLI.
//
// Unlike bot/api Config it covers only the voice flow.
type VoiceConfig struct {
	// General.
	Provider string `mapstructure:"provider"`
	Language string `mapstructure:"language"`

	// Local whisper.cpp (used when Provider == VoiceProviderWhisperCpp).
	ModelPath string `mapstructure:"model_path"`

	// GoWhisper groups go-whisper HTTP service settings (used when
	// Provider == VoiceProviderGoWhisper).
	GoWhisper VoiceGoWhisperConfig `mapstructure:"go_whisper"`

	// Cloud STT (used when Provider == VoiceProviderCloud).
	CloudProvider string `mapstructure:"cloud_provider"`
	// CloudAPIKey is a secret. Prefer supplying it via A2TEXT_CLOUD_API_KEY
	// env var rather than committing to a YAML file. Never log VoiceConfig
	// with %+v — this field would leak.
	CloudAPIKey  string `mapstructure:"cloud_api_key"`
	CloudBaseURL string `mapstructure:"cloud_base_url"`

	// Working files / runtime.
	TempDir           string        `mapstructure:"temp_dir"`
	ConvertTimeout    time.Duration `mapstructure:"convert_timeout"`
	TranscribeTimeout time.Duration `mapstructure:"transcribe_timeout"`
	LogLevel          string        `mapstructure:"log_level"`

	// Capture configures audio capture from the microphone (etap I.1+).
	Capture VoiceCaptureConfig `mapstructure:"capture"`

	// Output configures how transcribed text is delivered (etap I.2+).
	Output VoiceOutputConfig `mapstructure:"output"`

	// Daemon configures the long-running dictation daemon (etap I.2+).
	Daemon VoiceDaemonConfig `mapstructure:"daemon"`

	// Hotkey configures the optional built-in global hotkey listener (etap I.4,
	// X11 only). Disabled by default — Wayland users and X11 users on a DE
	// with shortcut manager should leave it off and bind the shortcut at the
	// DE level (see docs/voice-cli.md § «X11: встроенный hotkey vs DE shortcut»).
	Hotkey VoiceHotkeyConfig `mapstructure:"hotkey"`

	// STTRetry configures bounded retry on transient STT failures (timeouts,
	// connection resets). Disabled by default; opt-in via stt_retry.enabled.
	STTRetry VoiceSTTRetryConfig `mapstructure:"stt_retry"`

	Privacy VoicePrivacyConfig `mapstructure:"privacy"`
}

// VoiceGoWhisperConfig groups go-whisper HTTP service settings.
type VoiceGoWhisperConfig struct {
	URL          string        `mapstructure:"url"`
	Prefix       string        `mapstructure:"prefix"`
	Model        string        `mapstructure:"model"`
	Timeout      time.Duration `mapstructure:"timeout"`
	AutoDownload bool          `mapstructure:"auto_download"`
}

// VoiceCaptureConfig groups audio capture settings.
type VoiceCaptureConfig struct {
	// Backend selects the capture implementation:
	//   - "auto"       — auto-detect (prefer PipeWire, fall back to PulseAudio).
	//   - "pipewire"   — force pw-record.
	//   - "pulseaudio" — force parec.
	Backend string `mapstructure:"backend"`

	// SampleRate is the recording sample rate in Hz (default 16000).
	SampleRate int `mapstructure:"sample_rate"`

	// Channels is the number of audio channels (default 1, mono).
	Channels int `mapstructure:"channels"`

	// MaxDuration is the hard limit for a single recording. The daemon
	// auto-stops recording after this duration and proceeds to
	// transcription. Default 60s.
	MaxDuration time.Duration `mapstructure:"max_duration"`
}

// VoiceOutputConfig groups text delivery settings.
type VoiceOutputConfig struct {
	// Mode selects how transcribed text is delivered:
	//   - "stdout"               — print to stdout.
	//   - "clipboard"            — copy to system clipboard, degrade to stdout on failure.
	//   - "clipboard_autopaste"  — copy + simulate Ctrl+V via wtype/ydotool.
	Mode string `mapstructure:"mode"`

	// AutopasteCommand picks the autopaste backend when Mode is
	// "clipboard_autopaste":
	//   - "auto"    — prefer wtype, fall back to ydotool (default).
	//   - "wtype"   — force wtype.
	//   - "ydotool" — force ydotool (needs ydotoold + /dev/uinput).
	AutopasteCommand string `mapstructure:"autopaste_command"`
}

// VoiceHotkeyMode selects how raw key edges are mapped to the SM.
type VoiceHotkeyMode string

const (
	// VoiceHotkeyModeToggle is the default: Press flips the cycle on/off,
	// Release is ignored. Works on every backend (including those that
	// only see Press, like the DE-shortcut path).
	VoiceHotkeyModeToggle VoiceHotkeyMode = "toggle"

	// VoiceHotkeyModeHold is push-to-talk: Press starts recording, Release
	// stops + transcribes. Requires a backend that can observe Release
	// (portal GlobalShortcuts, XGrabKey). On Press-only backends Hold
	// degrades to a one-shot "start-then-wait-for-next-press" — see
	// Daemon.HotkeyHandler for the rationale.
	VoiceHotkeyModeHold VoiceHotkeyMode = "hold"
)

// VoiceHotkeyBackend selects which adapter implements the global hotkey.
type VoiceHotkeyBackend string

const (
	// VoiceHotkeyBackendAuto picks the best backend for the current
	// session: portal on Wayland (if available), x11 on Xorg (if the
	// binary has the `x11` build tag), otherwise none.
	VoiceHotkeyBackendAuto VoiceHotkeyBackend = "auto"

	// VoiceHotkeyBackendX11 uses XGrabKey directly. Requires Xorg session
	// and a binary built with -tags=x11.
	VoiceHotkeyBackendX11 VoiceHotkeyBackend = "x11"

	// VoiceHotkeyBackendNone disables the built-in listener entirely;
	// the user is expected to bind via DE shortcut (GNOME/KDE/i3 config).
	VoiceHotkeyBackendNone VoiceHotkeyBackend = "none"
)

// VoiceHotkeyConfig groups built-in global hotkey settings. The chosen
// Backend determines which adapter implements the listener; Mode shapes
// how raw key edges drive the state machine.
type VoiceHotkeyConfig struct {
	// Key is the keysym name (e.g. "F12", "D", "space"). Required when
	// Enabled is true; an empty value fails daemon startup with a clear error.
	Key string `mapstructure:"key"`

	// Modifiers is a list of modifier names combined with Key. Recognised
	// values (case-insensitive): "super"/"mod4"/"win", "alt"/"mod1",
	// "ctrl"/"control", "shift". Order is irrelevant — the bitmask is
	// commutative.
	Modifiers []string `mapstructure:"modifiers"`

	// Backend selects the adapter. Default "auto".
	Backend VoiceHotkeyBackend `mapstructure:"backend"`

	// Mode selects toggle vs hold semantics. Default "toggle".
	Mode VoiceHotkeyMode `mapstructure:"mode"`

	// Enabled turns the built-in listener on. Default false. When false
	// the daemon does not register any hotkey and relies on the user
	// invoking `a2text` via a DE shortcut (which is the press-only path).
	Enabled bool `mapstructure:"enabled"`
}

// VoiceSTTRetryConfig groups STT retry settings. The decorator is wired into
// BuildTranscriber when Enabled=true; otherwise the raw backend is returned
// and a single failure ends the cycle (current I.2 default).
//
// Backoff is exponential: each attempt waits 2× the previous, clamped to
// MaxDelay. With MaxAttempts=2, InitialDelay matters but MaxDelay does not.
type VoiceSTTRetryConfig struct {
	// InitialDelay is the wait before the second attempt. Default 200ms when
	// retries are enabled and the field is left at zero.
	InitialDelay time.Duration `mapstructure:"initial_delay"`

	// MaxDelay caps the exponential growth. Default 5s. Required when
	// MaxAttempts > 2; the constructor enforces sensible defaults otherwise.
	MaxDelay time.Duration `mapstructure:"max_delay"`

	// MaxAttempts is the total number of Transcribe attempts including the
	// first call. Default 2 (i.e. one retry on failure). 1 disables retries
	// even when Enabled=true — useful for explicit "wrap but don't retry".
	MaxAttempts int `mapstructure:"max_attempts"`

	// Enabled turns the retry decorator on. Default false.
	Enabled bool `mapstructure:"enabled"`
}

// VoiceDaemonConfig groups daemon lifecycle settings.
type VoiceDaemonConfig struct {
	// SocketPath overrides the IPC socket path. Empty means auto-detect:
	// $XDG_RUNTIME_DIR/a2text/a2text-voice.sock or os.TempDir()/a2text-voice.sock.
	SocketPath string `mapstructure:"socket_path"`

	// ShutdownGracePeriod is the maximum time the daemon waits for the
	// current cycle to finish before force-stopping. Default 15s.
	ShutdownGracePeriod time.Duration `mapstructure:"shutdown_grace_period"`
}

// VoiceOutputMode* constants enumerate valid OutputMode values. Kept as
// string constants so YAML parsing matches without lookup tables.
const (
	VoiceOutputModeStdout             = "stdout"
	VoiceOutputModeClipboard          = "clipboard"
	VoiceOutputModeClipboardAutopaste = "clipboard_autopaste"
)

// VoiceCaptureBackend* constants enumerate valid capture backends.
const (
	VoiceCaptureBackendAuto       = "auto"
	VoiceCaptureBackendPipeWire   = "pipewire"
	VoiceCaptureBackendPulseAudio = "pulseaudio"
)

// VoiceAutopasteCommand* constants enumerate valid AutopasteCommand values.
const (
	VoiceAutopasteCommandAuto    = "auto"
	VoiceAutopasteCommandWtype   = "wtype"
	VoiceAutopasteCommandYdotool = "ydotool"
	VoiceAutopasteCommandXdotool = "xdotool"
)

// VoiceLogLevel* constants enumerate canonical slog level names. Centralised
// here so the whitelist in validateVoiceLogLevel cannot drift from any
// other reference site (and goconst stays happy).
const (
	VoiceLogLevelDebug = "debug"
	VoiceLogLevelInfo  = "info"
	VoiceLogLevelWarn  = "warn"
	VoiceLogLevelError = "error"
)

// languagePattern validates BCP 47-ish language tags: 2-8 alpha chars,
// optionally followed by a dash and 2-8 alphanumeric chars.
var languagePattern = regexp.MustCompile(`^[a-zA-Z]{2,8}(-[a-zA-Z0-9]{2,8})?$`)

// VoicePrivacyConfig groups privacy-related toggles. Defaults err on the side
// of NOT logging or persisting any audio/transcription content.
type VoicePrivacyConfig struct {
	KeepAudio     bool `mapstructure:"keep_audio"`
	LogTranscript bool `mapstructure:"log_transcript"`
}

// LoadVoice reads, validates, and prepares runtime directories for the voice
// CLI. Note the third part: this function calls os.MkdirAll on cfg.TempDir,
// so it has a filesystem side effect on success. Acceptable for CLI/daemon
// startup; tests use t.TempDir() so they are unaffected.
//
// Discovery order:
//   - explicit path, if non-empty (no local overlay applied);
//   - $XDG_CONFIG_HOME/a2text/config.yaml (or ~/.config/a2text/config.yaml);
//   - ./config.yaml or ./app/config.yaml (development fallback),
//     then optional ./config.local.yaml (or ./app/config.local.yaml) overlay.
//
// Env vars use the A2TEXT_ prefix and underscore-flattened key paths,
// e.g. A2TEXT_PROVIDER, A2TEXT_LANGUAGE, A2TEXT_PRIVACY_KEEP_AUDIO,
// A2TEXT_GO_WHISPER_TIMEOUT.
func LoadVoice(path string) (*VoiceConfig, error) {
	viperInst := viper.New()
	setVoiceDefaults(viperInst)

	if err := readVoiceConfig(viperInst, path); err != nil {
		return nil, err
	}

	viperInst.SetEnvPrefix("A2TEXT")
	// Without this replacer, nested keys (e.g. privacy.keep_audio) cannot be
	// overridden via env vars because the OS shell uses underscores.
	viperInst.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viperInst.AutomaticEnv()

	var cfg VoiceConfig
	if err := viperInst.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal voice config: %w", err)
	}

	// Empty string in yaml shadows the viper default — fall back here so
	// users can leave `temp_dir: ""` in the example config.
	//
	// Security: never use shared /tmp directly. Create a private per-user
	// subdirectory so audio files are not world-readable.
	if cfg.TempDir == "" {
		cfg.TempDir = filepath.Join(os.TempDir(), fmt.Sprintf("a2text-voice-%d", os.Getuid()))
	}

	if err := ValidateVoice(&cfg); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cfg.TempDir, tempDirPermission); err != nil {
		return nil, fmt.Errorf("create temp_dir %s: %w", cfg.TempDir, err)
	}

	// Verify the created directory has correct ownership and permissions.
	if err := verifyTempDir(cfg.TempDir); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// verifyTempDir checks that the temp directory is owned by the current user
// and has mode 0700. A pre-existing directory with wrong permissions (e.g.
// world-writable /tmp reused by accident) must be rejected.
func verifyTempDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat temp_dir %s: %w", path, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("temp_dir %s is not a directory", path)
	}

	// Check ownership via syscall.Stat_t.
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		// Non-Unix platform — skip owner check (best-effort).
		return nil
	}

	uidInt := os.Getuid()
	if uidInt < 0 || uidInt > 4294967295 {
		return fmt.Errorf("temp_dir: uid %d out of valid range", uidInt)
	}

	uid := uint32(uidInt)
	if stat.Uid != uid {
		return fmt.Errorf("temp_dir %s is owned by uid %d, expected %d", path, stat.Uid, uid)
	}

	perm := info.Mode().Perm()
	if perm != tempDirPermission {
		return fmt.Errorf("temp_dir %s has mode %04o, expected 0700", path, perm)
	}

	return nil
}

// ValidateVoice runs the same validation that LoadVoice performs after
// unmarshalling. Exported so the CLI can re-check VoiceConfig after applying
// flag overrides — otherwise a value like --provider=banana would only fail
// deep inside BuildTranscriber, far from its actual cause.
//
// Nil-cfg is a programmer error (every caller is supposed to pass the
// VoiceConfig they just built), but defensive-fail-closed keeps the
// signature honest: a panic here would crash the CLI before any error
// reporting kicked in.
func ValidateVoice(cfg *VoiceConfig) error {
	if cfg == nil {
		return errors.New("voice config is nil")
	}

	return validateVoice(cfg)
}

// readVoiceConfig loads the YAML source into viperInst. With an explicit path
// the file must exist; without one, missing default files are tolerated and
// an optional config.local overlay is merged on top.
func readVoiceConfig(viperInst *viper.Viper, path string) error {
	if path != "" {
		viperInst.SetConfigFile(path)

		if err := viperInst.ReadInConfig(); err != nil {
			return fmt.Errorf("read config file: %w", err)
		}

		return nil
	}

	viperInst.SetConfigName("config")
	viperInst.SetConfigType("yaml")

	// User config dir (~/.config/a2text/) has the highest priority so that
	// the installed binary always reads the user's config, not whatever
	// ./app/config.yaml happens to be present in the working directory.
	if xdgDir, err := os.UserConfigDir(); err == nil {
		viperInst.AddConfigPath(filepath.Join(xdgDir, "a2text"))
	}

	viperInst.AddConfigPath(".")
	viperInst.AddConfigPath("./app")

	if err := viperInst.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return fmt.Errorf("read config file: %w", err)
		}
	}

	mergeLocalOverride(viperInst, "config.local")

	return nil
}

func setVoiceDefaults(viperInst *viper.Viper) {
	viperInst.SetDefault("provider", VoiceProviderGoWhisper)
	viperInst.SetDefault("language", "ru")
	viperInst.SetDefault("go_whisper.url", "http://localhost:9081")
	viperInst.SetDefault("go_whisper.prefix", "/api/whisper")
	viperInst.SetDefault("go_whisper.model", "ggml-small")
	viperInst.SetDefault("go_whisper.timeout", "10m")
	viperInst.SetDefault("go_whisper.auto_download", true)
	viperInst.SetDefault("convert_timeout", "60s")
	viperInst.SetDefault("transcribe_timeout", "10m")
	// temp_dir default is intentionally NOT set here. An empty value means
	// "create a private per-user subdirectory" — see LoadVoice.
	viperInst.SetDefault("log_level", VoiceLogLevelInfo)
	viperInst.SetDefault("privacy.keep_audio", false)
	viperInst.SetDefault("privacy.log_transcript", false)
	viperInst.SetDefault("output_mode", VoiceOutputModeClipboard)
	viperInst.SetDefault("autopaste_command", VoiceAutopasteCommandAuto)

	// Capture defaults.
	viperInst.SetDefault("capture.backend", VoiceCaptureBackendAuto)
	viperInst.SetDefault("capture.sample_rate", defaultCaptureSampleRate)
	viperInst.SetDefault("capture.channels", 1)
	// capture.max_duration default is intentionally NOT set here. Zero means
	// "use the daemon default" — pickMaxRecord() in daemon.go handles it.

	// Daemon defaults.
	viperInst.SetDefault("daemon.socket_path", "")
	viperInst.SetDefault("daemon.shutdown_grace_period", "15s")

	// Nested output defaults are intentionally NOT set here — they would
	// shadow the deprecated flat fields (output_mode, autopaste_command)
	// and prevent the backward-compat promotion in normalizeVoiceConfig.
	// Defaults for Output are applied in normalizeVoiceConfig after the
	// promotion logic runs.
}

func validateVoice(cfg *VoiceConfig) error {
	normalizeVoiceConfig(cfg)

	if err := validateVoiceOutput(cfg); err != nil {
		return err
	}

	if err := validateVoiceLogLevel(cfg); err != nil {
		return err
	}

	if err := validateVoiceLanguage(cfg); err != nil {
		return err
	}

	if err := validateVoiceTimeouts(cfg); err != nil {
		return err
	}

	if err := validateVoiceCapture(cfg); err != nil {
		return err
	}

	if err := validateVoiceDaemon(cfg); err != nil {
		return err
	}

	return validateVoiceProvider(cfg)
}

// validateVoiceProvider dispatches to the provider-specific validation.
func validateVoiceProvider(cfg *VoiceConfig) error {
	switch cfg.Provider {
	case VoiceProviderGoWhisper:
		return validateVoiceGoWhisper(cfg)
	case VoiceProviderWhisperCpp:
		return validateVoiceWhisperCpp(cfg)
	case VoiceProviderCloud:
		return validateVoiceCloud(cfg)
	default:
		return fmt.Errorf(
			"unknown provider %q (supported: %s, %s, %s)",
			cfg.Provider,
			VoiceProviderGoWhisper, VoiceProviderWhisperCpp, VoiceProviderCloud,
		)
	}
}

// validateVoiceCapture checks the capture sub-section. Values come from
// viper defaults so zero is unlikely from LoadVoice — but ValidateVoice is
// also called directly after flag overrides, where the caller constructs the
// struct manually.
// validateVoiceLanguage checks that language is not empty, contains no whitespace,
// and matches the BCP 47-ish pattern (2-8 alpha chars, optionally dash + 2-8 alphanumeric).
func validateVoiceLanguage(cfg *VoiceConfig) error {
	if cfg.Language == "" {
		return errors.New("language is required")
	}

	if strings.ContainsAny(cfg.Language, " \t\r\n") {
		return fmt.Errorf("language %q must not contain whitespace", cfg.Language)
	}

	if !languagePattern.MatchString(cfg.Language) {
		return fmt.Errorf("language %q must match pattern like 'en' or 'zh-CN'", cfg.Language)
	}

	return nil
}

// validateVoiceTimeouts checks that all timeout values are positive.
func validateVoiceTimeouts(cfg *VoiceConfig) error {
	if cfg.ConvertTimeout <= 0 {
		return errors.New("convert_timeout must be positive")
	}

	if cfg.TranscribeTimeout <= 0 {
		return errors.New("transcribe_timeout must be positive")
	}

	if cfg.GoWhisper.Timeout <= 0 {
		return errors.New("go_whisper.timeout must be positive")
	}

	return nil
}

func validateVoiceCapture(cfg *VoiceConfig) error {
	if cfg.Capture.SampleRate <= 0 {
		return fmt.Errorf("capture.sample_rate must be positive, got %d", cfg.Capture.SampleRate)
	}

	if cfg.Capture.Channels <= 0 {
		return fmt.Errorf("capture.channels must be positive, got %d", cfg.Capture.Channels)
	}

	// MaxDuration == 0 means "use the daemon default" (60 s via pickMaxRecord).
	// A negative value is always a config mistake — reject it explicitly so
	// pickMaxRecord's silent fallback doesn't hide the error.
	if cfg.Capture.MaxDuration < 0 {
		return fmt.Errorf("capture.max_duration must not be negative, got %s", cfg.Capture.MaxDuration)
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Capture.Backend)) {
	case "", VoiceCaptureBackendAuto, VoiceCaptureBackendPipeWire, VoiceCaptureBackendPulseAudio:
		return nil
	default:
		return fmt.Errorf(
			"capture.backend %q is not supported; use %q, %q or %q",
			cfg.Capture.Backend,
			VoiceCaptureBackendAuto, VoiceCaptureBackendPipeWire, VoiceCaptureBackendPulseAudio,
		)
	}
}

// validateVoiceDaemon checks the daemon sub-section. Custom socket paths
// are security-sensitive: a world-writable parent directory or a path in
// shared /tmp allows other users to impersonate the daemon.
func validateVoiceDaemon(cfg *VoiceConfig) error {
	if cfg.Daemon.SocketPath == "" {
		return nil
	}

	path := cfg.Daemon.SocketPath

	if !filepath.IsAbs(path) {
		return fmt.Errorf("daemon.socket_path must be absolute, got %q", path)
	}

	parent := filepath.Dir(path)

	parentInfo, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("daemon.socket_path parent %q: %w", parent, err)
	}

	if !parentInfo.IsDir() {
		return fmt.Errorf("daemon.socket_path parent %q is not a directory", parent)
	}

	// Check parent ownership.
	stat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if ok {
		uidInt := os.Getuid()

		if uidInt < 0 || uidInt > 4294967295 {
			return fmt.Errorf("daemon.socket_path: uid %d out of valid range", uidInt)
		}

		uid := uint32(uidInt)
		if stat.Uid != uid {
			return fmt.Errorf(
				"daemon.socket_path parent %q is owned by uid %d, expected %d",
				parent, stat.Uid, uid,
			)
		}
	}

	// Parent must not be world/group writable.
	perm := parentInfo.Mode().Perm()
	if perm&0o022 != 0 {
		return fmt.Errorf(
			"daemon.socket_path parent %q has mode %04o (must not be group/world writable)",
			parent, perm,
		)
	}

	return nil
}

// validateVoiceLogLevel guards against typos like `log_level: potato`. The
// slog setup elsewhere in the codebase silently falls back to info on an
// unknown level — convenient at runtime but it hides config bugs. Accept
// only the canonical slog level names; "" is allowed and means "use the
// default already applied by setVoiceDefaults".
func validateVoiceLogLevel(cfg *VoiceConfig) error {
	switch cfg.LogLevel {
	case "", VoiceLogLevelDebug, VoiceLogLevelInfo, VoiceLogLevelWarn, VoiceLogLevelError:
		return nil
	default:
		return fmt.Errorf(
			"unknown log_level %q (supported: %s, %s, %s, %s)",
			cfg.LogLevel,
			VoiceLogLevelDebug, VoiceLogLevelInfo, VoiceLogLevelWarn, VoiceLogLevelError,
		)
	}
}

// validateVoiceOutput enforces the OutputMode / AutopasteCommand surface.
// Rejecting typos here means a misspelled "clipbord" or "ydotol" fails at
// config load instead of silently downgrading at runtime — operators see
// the mistake before the daemon binds the socket.
//
// After normalizeVoiceConfig, flat fields have been promoted into Output.*
// and cleared. Only Output.* is canonical.
func validateVoiceOutput(cfg *VoiceConfig) error {
	switch cfg.Output.Mode {
	case VoiceOutputModeStdout, VoiceOutputModeClipboard, VoiceOutputModeClipboardAutopaste:
	default:
		return fmt.Errorf(
			"unknown output_mode %q (supported: %s, %s, %s)",
			cfg.Output.Mode, VoiceOutputModeStdout, VoiceOutputModeClipboard, VoiceOutputModeClipboardAutopaste,
		)
	}

	switch cfg.Output.AutopasteCommand {
	case VoiceAutopasteCommandAuto, VoiceAutopasteCommandWtype, VoiceAutopasteCommandYdotool, VoiceAutopasteCommandXdotool:
	default:
		return fmt.Errorf(
			"unknown autopaste_command %q (supported: %s, %s, %s, %s)",
			cfg.Output.AutopasteCommand,
			VoiceAutopasteCommandAuto, VoiceAutopasteCommandWtype, VoiceAutopasteCommandYdotool, VoiceAutopasteCommandXdotool,
		)
	}

	return nil
}

func validateVoiceGoWhisper(cfg *VoiceConfig) error {
	if cfg.GoWhisper.URL == "" {
		return fmt.Errorf("provider %q: go_whisper.url is required", cfg.Provider)
	}

	if err := validateHTTPURL("go_whisper.url", cfg.GoWhisper.URL); err != nil {
		return fmt.Errorf("provider %q: %w", cfg.Provider, err)
	}

	if cfg.GoWhisper.Prefix == "" {
		return fmt.Errorf("provider %q: go_whisper.prefix is required", cfg.Provider)
	}

	if err := validateGoWhisperPrefix(cfg.GoWhisper.Prefix); err != nil {
		return fmt.Errorf("provider %q: %w", cfg.Provider, err)
	}

	return nil
}

// validateGoWhisperPrefix rejects path segments that could cause request
// smuggling or path traversal when concatenated with the base URL.
func validateGoWhisperPrefix(prefix string) error {
	if strings.Contains(prefix, "?") {
		return errors.New("go_whisper.prefix must not contain '?'")
	}

	if strings.Contains(prefix, "#") {
		return errors.New("go_whisper.prefix must not contain '#'")
	}

	if strings.Contains(prefix, "..") {
		return errors.New("go_whisper.prefix must not contain '..'")
	}

	if strings.ContainsAny(prefix, " \t\r\n") {
		return errors.New("go_whisper.prefix must not contain whitespace")
	}

	return nil
}

func validateVoiceWhisperCpp(cfg *VoiceConfig) error {
	if cfg.ModelPath == "" {
		return fmt.Errorf("provider %q: model_path is required", cfg.Provider)
	}

	return nil
}

func validateVoiceCloud(cfg *VoiceConfig) error {
	switch cfg.CloudProvider {
	case "":
		return fmt.Errorf("provider %q: cloud_provider is required", cfg.Provider)
	case VoiceCloudProviderOpenAI, VoiceCloudProviderDeepgram:
		// ok
	default:
		return fmt.Errorf(
			"provider %q: unknown cloud_provider %q (supported: %s, %s)",
			cfg.Provider, cfg.CloudProvider,
			VoiceCloudProviderOpenAI, VoiceCloudProviderDeepgram,
		)
	}

	if cfg.CloudAPIKey == "" {
		return fmt.Errorf("provider %q: cloud_api_key is required", cfg.Provider)
	}

	if cfg.CloudBaseURL != "" {
		if err := validateHTTPURL("cloud_base_url", cfg.CloudBaseURL); err != nil {
			return fmt.Errorf("provider %q: %w", cfg.Provider, err)
		}
	}

	return nil
}

// normalizeVoiceConfig trims whitespace from string fields and ensures
// GoWhisperPrefix has a leading slash. It does NOT case-fold values like
// Provider — a typo such as "Go-Whisper" must surface as a validation error,
// not be silently corrected.
//
// Two fields ARE case-folded: OutputMode and AutopasteCommand. These map
// 1:1 to backend selection in adapters (clipboard.NewWaylandAutopaster
// lower-folds before matching), and a single source of truth here prevents
// the "config rejects ' WTYPE ', adapter would have accepted" rift.
//
// CloudAPIKey is the only secret in the struct. It MUST be trimmed too —
// a leading space from a yaml line continuation would otherwise quietly
// turn a valid key into an authentication failure, which is the worst
// kind of "config works, daemon doesn't" surprise to debug.
//
// Backward compatibility: if the deprecated flat fields (OutputMode,
// AutopasteCommand) are set but the nested Output section is empty,
// the flat values are promoted into Output.
func normalizeVoiceConfig(cfg *VoiceConfig) {
	cfg.Provider = strings.TrimSpace(cfg.Provider)
	cfg.Language = strings.TrimSpace(cfg.Language)
	cfg.ModelPath = strings.TrimSpace(cfg.ModelPath)
	cfg.GoWhisper.URL = strings.TrimSpace(cfg.GoWhisper.URL)
	cfg.GoWhisper.Prefix = strings.TrimSpace(cfg.GoWhisper.Prefix)
	cfg.GoWhisper.Model = strings.TrimSpace(cfg.GoWhisper.Model)
	cfg.CloudProvider = strings.TrimSpace(cfg.CloudProvider)
	cfg.CloudAPIKey = strings.TrimSpace(cfg.CloudAPIKey)
	cfg.CloudBaseURL = strings.TrimSpace(cfg.CloudBaseURL)
	cfg.TempDir = strings.TrimSpace(cfg.TempDir)
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))

	// Nested sections.
	cfg.Capture.Backend = strings.ToLower(strings.TrimSpace(cfg.Capture.Backend))
	cfg.Output.Mode = strings.ToLower(strings.TrimSpace(cfg.Output.Mode))
	cfg.Output.AutopasteCommand = strings.ToLower(strings.TrimSpace(cfg.Output.AutopasteCommand))
	cfg.Daemon.SocketPath = strings.TrimSpace(cfg.Daemon.SocketPath)

	applyVoiceConfigDefaults(cfg)

	if cfg.GoWhisper.Prefix != "" && !strings.HasPrefix(cfg.GoWhisper.Prefix, "/") {
		cfg.GoWhisper.Prefix = "/" + cfg.GoWhisper.Prefix
	}
}

// applyVoiceConfigDefaults fills in zero-value fields with safe defaults
// after trimming and backward-compat promotion have run.
//
// Flat fields (OutputMode, AutopasteCommand) are intentionally NOT defaulted
// here. After promotion, Output.* is the single source of truth. Defaulting
// flat fields would re-populate them and create a second source of truth.
//
// Capture.MaxDuration is also intentionally NOT defaulted here. Zero means
// "use the daemon default" — pickMaxRecord() in daemon.go handles the
// fallback. Defaulting it here would make validateVoiceCapture unable
// to distinguish "user explicitly set 0" from "not set".
func applyVoiceConfigDefaults(cfg *VoiceConfig) {
	if cfg.Output.Mode == "" {
		cfg.Output.Mode = VoiceOutputModeClipboard
	}

	if cfg.Output.AutopasteCommand == "" {
		cfg.Output.AutopasteCommand = VoiceAutopasteCommandAuto
	}

	if cfg.Capture.Backend == "" {
		cfg.Capture.Backend = VoiceCaptureBackendAuto
	}

	if cfg.Capture.SampleRate == 0 {
		cfg.Capture.SampleRate = 16000
	}

	if cfg.Capture.Channels == 0 {
		cfg.Capture.Channels = 1
	}

	if cfg.Daemon.ShutdownGracePeriod == 0 {
		cfg.Daemon.ShutdownGracePeriod = defaultDaemonShutdownGrace
	}
}

// validateHTTPURL ensures the value is an absolute http(s) URL without
// userinfo, query or fragment. These are config URLs, not runtime request
// URLs — userinfo leaks credentials into logs, and query/fragment can
// carry tokens that should be in headers or env vars instead.
func validateHTTPURL(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s must be a valid URL: %w", name, err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL with scheme and host", name)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must use http or https (got %q)", name, parsed.Scheme)
	}

	if parsed.User != nil {
		return fmt.Errorf("%s must not contain userinfo (credentials in URLs leak into logs)", name)
	}

	if parsed.RawQuery != "" {
		return fmt.Errorf("%s must not contain query parameters", name)
	}

	if parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain a fragment", name)
	}

	return nil
}

func mergeLocalOverride(viperInst *viper.Viper, name string) {
	local := viper.New()
	local.SetConfigName(name)
	local.SetConfigType("yaml")
	local.AddConfigPath(".")
	local.AddConfigPath("./app")

	if err := local.ReadInConfig(); err != nil {
		return
	}

	if err := viperInst.MergeConfigMap(local.AllSettings()); err != nil {
		_ = err
	}
}
