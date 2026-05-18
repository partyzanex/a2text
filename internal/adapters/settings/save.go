// Package settings provides a Fyne-based settings window for a2text.
package settings

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/partyzanex/a2text/internal/infra/config"
)

const (
	configDirPerm  = 0o700
	configFilePerm = 0o600
	appDirName     = "a2text"
	configFileName = "config.yaml"
)

// ConfigPath returns the user-writable config file path:
// $XDG_CONFIG_HOME/a2text/config.yaml (typically ~/.config/a2text/config.yaml).
func ConfigPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("settings: UserConfigDir: %w", err)
	}

	return filepath.Join(base, appDirName, configFileName), nil
}

// SaveConfig writes the settings-relevant fields of cfg back to the XDG user
// config file as YAML.
//
// The existing file is read first; only the keys exposed in the Settings UI
// are updated, so all other settings (timeouts, TLS certs, etc.) are
// preserved. The directory is created if absent.
func SaveConfig(cfg *config.VoiceConfig) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	if err = os.MkdirAll(filepath.Dir(path), configDirPerm); err != nil {
		return fmt.Errorf("settings: mkdir %s: %w", filepath.Dir(path), err)
	}

	existing := make(map[string]any)

	if raw, readErr := os.ReadFile(filepath.Clean(path)); readErr == nil {
		if unmarshalErr := yaml.Unmarshal(raw, &existing); unmarshalErr != nil {
			existing = make(map[string]any)
		}
	}

	applyToMap(existing, cfg)

	data, err := yaml.Marshal(existing)
	if err != nil {
		return fmt.Errorf("settings: marshal config: %w", err)
	}

	if err = os.WriteFile(path, data, configFilePerm); err != nil {
		return fmt.Errorf("settings: write %s: %w", path, err)
	}

	return nil
}

// applyToMap writes all UI-exposed fields of cfg into dst using their
// canonical yaml key names (matching the mapstructure tags in VoiceConfig).
func applyToMap(dst map[string]any, cfg *config.VoiceConfig) {
	dst["provider"] = cfg.Provider
	dst["language"] = cfg.Language
	dst["model_path"] = cfg.ModelPath
	dst["whisper_cpp_models_dir"] = cfg.WhisperCppModelsDir
	dst["temp_dir"] = cfg.TempDir
	dst["convert_timeout"] = cfg.ConvertTimeout.String()
	dst["transcribe_timeout"] = cfg.TranscribeTimeout.String()
	dst["log_level"] = cfg.LogLevel

	applyOpenAIToMap(dst, cfg)
	applyDeepgramToMap(dst, cfg)
	applyGoWhisperToMap(dst, cfg)
	applyHotkeyToMap(dst, cfg)
	applyOutputToMap(dst, cfg)
	applyCaptureToMap(dst, cfg)
	applyDaemonToMap(dst, cfg)
	applySTTRetryToMap(dst, cfg)
	applyPrivacyToMap(dst, cfg)
}

func applyOpenAIToMap(dst map[string]any, cfg *config.VoiceConfig) {
	openai, _ := dst["openai"].(map[string]any)
	if openai == nil {
		openai = make(map[string]any)
	}

	openai["base_url"] = cfg.OpenAI.BaseURL
	openai["model"] = cfg.OpenAI.Model

	if cfg.OpenAI.APIKey != "" {
		openai["api_key"] = cfg.OpenAI.APIKey
	}

	dst["openai"] = openai
}

func applyDeepgramToMap(dst map[string]any, cfg *config.VoiceConfig) {
	deepgram, _ := dst["deepgram"].(map[string]any)
	if deepgram == nil {
		deepgram = make(map[string]any)
	}

	deepgram["base_url"] = cfg.Deepgram.BaseURL
	deepgram["model"] = cfg.Deepgram.Model
	deepgram["streaming"] = cfg.Deepgram.Streaming

	if cfg.Deepgram.APIKey != "" {
		deepgram["api_key"] = cfg.Deepgram.APIKey
	}

	dst["deepgram"] = deepgram
}

func applyGoWhisperToMap(dst map[string]any, cfg *config.VoiceConfig) {
	goWhisper, _ := dst["go_whisper"].(map[string]any)
	if goWhisper == nil {
		goWhisper = make(map[string]any)
	}

	goWhisper["url"] = cfg.GoWhisper.URL
	goWhisper["model"] = cfg.GoWhisper.Model
	// Legacy "prefix" key intentionally not written — it is migrated into
	// "url" on load. Remove the stale key if a previous version left one.
	delete(goWhisper, "prefix")
	goWhisper["timeout"] = cfg.GoWhisper.Timeout.String()
	goWhisper["auto_download"] = cfg.GoWhisper.AutoDownload
	dst["go_whisper"] = goWhisper
}

func applyHotkeyToMap(dst map[string]any, cfg *config.VoiceConfig) {
	hotkey, _ := dst["hotkey"].(map[string]any)
	if hotkey == nil {
		hotkey = make(map[string]any)
	}

	hotkey["key"] = cfg.Hotkey.Key
	hotkey["mode"] = string(cfg.Hotkey.Mode)
	hotkey["modifiers"] = splitTrimmed(strings.Join(cfg.Hotkey.Modifiers, ","))
	dst["hotkey"] = hotkey

	// Strip legacy fields that older configs may have persisted so we don't
	// resurrect them on rewrite.
	delete(hotkey, "backend")
	delete(hotkey, "enabled")
}

func applyOutputToMap(dst map[string]any, cfg *config.VoiceConfig) {
	output, _ := dst["output"].(map[string]any)
	if output == nil {
		output = make(map[string]any)
	}

	output["mode"] = cfg.Output.Mode
	output["autopaste_command"] = cfg.Output.AutopasteCommand
	output["restore_clipboard"] = cfg.Output.RestoreClipboard
	dst["output"] = output
}

func applyCaptureToMap(dst map[string]any, cfg *config.VoiceConfig) {
	capture, _ := dst["capture"].(map[string]any)
	if capture == nil {
		capture = make(map[string]any)
	}

	capture["backend"] = cfg.Capture.Backend
	capture["sample_rate"] = cfg.Capture.SampleRate
	capture["channels"] = cfg.Capture.Channels
	capture["max_duration"] = cfg.Capture.MaxDuration.String()
	capture["silence_threshold_dbfs"] = cfg.Capture.SilenceThresholdDBFS
	dst["capture"] = capture
}

func applyDaemonToMap(dst map[string]any, cfg *config.VoiceConfig) {
	daemon, _ := dst["daemon"].(map[string]any)
	if daemon == nil {
		daemon = make(map[string]any)
	}

	daemon["shutdown_grace_period"] = cfg.Daemon.ShutdownGracePeriod.String()
	dst["daemon"] = daemon
}

func applySTTRetryToMap(dst map[string]any, cfg *config.VoiceConfig) {
	sttRetry, _ := dst["stt_retry"].(map[string]any)
	if sttRetry == nil {
		sttRetry = make(map[string]any)
	}

	sttRetry["enabled"] = cfg.STTRetry.Enabled
	sttRetry["initial_delay"] = cfg.STTRetry.InitialDelay.String()
	sttRetry["max_delay"] = cfg.STTRetry.MaxDelay.String()
	sttRetry["max_attempts"] = cfg.STTRetry.MaxAttempts
	dst["stt_retry"] = sttRetry
}

func applyPrivacyToMap(dst map[string]any, cfg *config.VoiceConfig) {
	privacy, _ := dst["privacy"].(map[string]any)
	if privacy == nil {
		privacy = make(map[string]any)
	}

	privacy["log_transcript"] = cfg.Privacy.LogTranscript
	privacy["keep_audio"] = cfg.Privacy.KeepAudio
	dst["privacy"] = privacy
}

func splitTrimmed(ss string) []string {
	parts := strings.Split(ss, ",")
	out := make([]string, 0, len(parts))

	for _, pp := range parts {
		if tt := strings.TrimSpace(pp); tt != "" {
			out = append(out, tt)
		}
	}

	return out
}
