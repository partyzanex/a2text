package cmd

// Shared flag names. Kept in one place so the cli.Flag definition and the
// cmd.String/cmd.Bool read sites cannot drift.
const (
	FlagConfig = "config"

	// FlagProvider overrides config.Provider for one invocation
	// (useful when switching between go-whisper, whisper-cpp, openai, deepgram during dev).
	FlagProvider = "provider"

	// FlagModelPath overrides config.ModelPath (path to a local GGML model).
	// Relevant only with --provider=whisper-cpp.
	//
	// API keys are intentionally NOT exposed as flags — they are secrets
	// and would land in shell history. Use A2TEXT_OPENAI_API_KEY / A2TEXT_DEEPGRAM_API_KEY
	// env vars (read by viper inside config.LoadVoice) instead.
	FlagModelPath = "model-path"

	// FlagLanguage overrides config.Language.
	FlagLanguage = "lang"

	// FlagLogLevel overrides config.LogLevel.
	FlagLogLevel = "log-level"

	// FlagDaemon forces daemon-only startup: acquire the lock, bind the
	// socket, and serve. No bootstrap toggle, no race-retry. The official
	// entry point for systemd units (where auto-toggle would cause the
	// service to immediately exit if a daemon was already running).
	FlagDaemon = "daemon"

	// FlagPprof enables the standard net/http/pprof endpoints on the given
	// host:port address (e.g. "127.0.0.1:6060"). Empty / unset = disabled.
	// Useful for diagnosing memory growth across many voice cycles:
	//   go tool pprof -http=:9999 http://127.0.0.1:6060/debug/pprof/heap
	//
	// Bind to loopback unless you really mean to expose it — pprof gives
	// arbitrary stack and heap inspection to whoever can connect.
	FlagPprof = "pprof"
)
