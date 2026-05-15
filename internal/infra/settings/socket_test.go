package settings

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/partyzanex/a2text/internal/infra/config"
)

// TestSocketPathConfig_Empty verifies that empty socket path
// remains empty in config.
func TestSocketPathConfig_Empty(t *testing.T) {
	cfg := &config.VoiceConfig{
		Daemon: config.VoiceDaemonConfig{
			SocketPath: "",
		},
	}

	assert.Empty(t, cfg.Daemon.SocketPath)
}

// TestSocketPathConfig_Custom verifies that custom socket path
// is preserved in config.
func TestSocketPathConfig_Custom(t *testing.T) {
	customPath := "/tmp/a2text.sock"
	cfg := &config.VoiceConfig{
		Daemon: config.VoiceDaemonConfig{
			SocketPath: customPath,
		},
	}

	assert.Equal(t, customPath, cfg.Daemon.SocketPath)
}

// TestSocketPathSaveToMap verifies that socket path is persisted
// when saving config to map (used for YAML marshalling).
func TestSocketPathSaveToMap(t *testing.T) {
	customPath := "/tmp/custom-socket.sock"
	cfg := &config.VoiceConfig{
		Daemon: config.VoiceDaemonConfig{
			SocketPath: customPath,
		},
	}

	dst := make(map[string]any)
	applyDaemonToMap(dst, cfg)

	daemon, ok := dst["daemon"].(map[string]any)
	assert.True(t, ok, "daemon key must exist and be a map")

	socketPath, ok := daemon["socket_path"].(string)
	assert.True(t, ok, "socket_path must be a string")
	assert.Equal(t, customPath, socketPath)
}

// TestSocketPathEmpty_SaveToMap verifies that empty socket path
// is preserved when saving to map.
func TestSocketPathEmpty_SaveToMap(t *testing.T) {
	cfg := &config.VoiceConfig{
		Daemon: config.VoiceDaemonConfig{
			SocketPath: "",
		},
	}

	dst := make(map[string]any)
	applyDaemonToMap(dst, cfg)

	daemon := dst["daemon"].(map[string]any)
	socketPath := daemon["socket_path"].(string)

	assert.Empty(t, socketPath)
}
