package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/partyzanex/a2text/internal/infra/config"
)

// TestProviderModel_NilConfig verifies the function tolerates a nil config
// pointer — depcheck-degraded daemons can reach the cycle-completed log
// path without a fully wired config.
func TestProviderModel_NilConfig(t *testing.T) {
	assert.Empty(t, providerModel(nil))
}

// TestProviderModel_GoWhisper returns the configured Model field for the
// go-whisper HTTP provider.
func TestProviderModel_GoWhisper(t *testing.T) {
	cfg := &config.VoiceConfig{
		Provider: config.VoiceProviderGoWhisper,
		GoWhisper: config.VoiceGoWhisperConfig{
			Model: "ggml-large-v3-turbo",
		},
	}

	assert.Equal(t, "ggml-large-v3-turbo", providerModel(cfg))
}

// TestProviderModel_WhisperCpp_BasenameOnly strips directory prefixes from
// ModelPath so the log shows just the model filename, not its full path.
func TestProviderModel_WhisperCpp_BasenameOnly(t *testing.T) {
	cfg := &config.VoiceConfig{
		Provider:  config.VoiceProviderWhisperCpp,
		ModelPath: "/home/user/.local/share/a2text/models/ggml-small.bin",
	}

	assert.Equal(t, "ggml-small.bin", providerModel(cfg))
}

// TestProviderModel_WhisperCpp_EmptyPath returns empty when no model has
// been configured yet (degraded startup, fresh install).
func TestProviderModel_WhisperCpp_EmptyPath(t *testing.T) {
	cfg := &config.VoiceConfig{Provider: config.VoiceProviderWhisperCpp}

	assert.Empty(t, providerModel(cfg))
}

// TestProviderModel_OpenAI returns the configured model for the OpenAI provider.
func TestProviderModel_OpenAI(t *testing.T) {
	cfg := &config.VoiceConfig{
		Provider: config.VoiceProviderOpenAI,
		OpenAI:   config.VoiceOpenAIConfig{Model: "whisper-1"},
	}

	assert.Equal(t, "whisper-1", providerModel(cfg))
}

// TestProviderModel_OpenAI_EmptyModel falls back to the provider name when
// no model is set.
func TestProviderModel_OpenAI_EmptyModel(t *testing.T) {
	cfg := &config.VoiceConfig{Provider: config.VoiceProviderOpenAI}

	assert.Equal(t, config.VoiceProviderOpenAI, providerModel(cfg))
}

// TestProviderModel_Deepgram returns the configured Deepgram model.
func TestProviderModel_Deepgram(t *testing.T) {
	cfg := &config.VoiceConfig{
		Provider: config.VoiceProviderDeepgram,
		Deepgram: config.VoiceDeepgramConfig{Model: "nova-2"},
	}

	assert.Equal(t, "nova-2", providerModel(cfg))
}

// TestProviderModel_UnknownProvider returns empty when Provider holds a
// value outside the known enum — defensive default rather than panic.
func TestProviderModel_UnknownProvider(t *testing.T) {
	cfg := &config.VoiceConfig{Provider: "ftp-streamer"}

	assert.Empty(t, providerModel(cfg))
}
