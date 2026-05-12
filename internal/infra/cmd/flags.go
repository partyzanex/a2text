package cmd

// Shared flag names. Kept in one place so the cli.Flag definition and the
// cmd.String/cmd.Bool read sites cannot drift.
const (
	FlagConfig = "config"
	// FlagFile is a hidden dev flag: --file PATH transcribes a single audio
	// file and prints the result to stdout. Used for smoke tests, CI
	// integration tests, and interactive debugging. Not advertised in --help.
	FlagFile = "file"

	// FlagRecord is a hidden dev flag: --record DURATION captures audio
	// from the default microphone for the given duration, transcribes it,
	// and prints the result to stdout. Used for smoke tests of the capture
	// adapter and end-to-end pipeline. Not advertised in --help.
	FlagRecord = "record"

	// FlagProvider overrides config.Provider for one invocation
	// (useful when switching between go-whisper and cloud during dev).
	FlagProvider = "provider"

	// FlagCloudProvider overrides config.CloudProvider (openai | deepgram).
	// Relevant only with --provider=cloud.
	FlagCloudProvider = "cloud-provider"

	// FlagModelPath overrides config.ModelPath (path to a local GGML model).
	// Relevant only with --provider=whisper-cpp.
	//
	// CloudAPIKey is intentionally NOT exposed as a flag — it is a secret
	// and would land in shell history. Use the A2TEXT_CLOUD_API_KEY env var
	// (read by viper inside config.LoadVoice) instead.
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
)
