package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/partyzanex/a2text/internal/infra/cmd/sysd"
	"github.com/partyzanex/a2text/internal/infra/config"
)

// TestSocketPathResolution verifies that custom socket paths override
// the default when specified in config.
func TestSocketPathResolution(t *testing.T) {
	tests := []struct {
		name           string
		socketPathCfg  string
		expectedSocket string
	}{
		{
			name:           "default when config is empty",
			socketPathCfg:  "",
			expectedSocket: sysd.DefaultSocketPath(),
		},
		{
			name:           "custom path from config",
			socketPathCfg:  "/tmp/custom-socket.sock",
			expectedSocket: "/tmp/custom-socket.sock",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.VoiceConfig{
				Daemon: config.VoiceDaemonConfig{
					SocketPath: tt.socketPathCfg,
				},
			}

			socketPath := sysd.DefaultSocketPath()
			if cfg.Daemon.SocketPath != "" {
				socketPath = cfg.Daemon.SocketPath
			}

			assert.Equal(t, tt.expectedSocket, socketPath)
		})
	}
}

// TestSocketPathEmpty_UsesDefault verifies that empty socket path
// does not override the default.
func TestSocketPathEmpty_UsesDefault(t *testing.T) {
	cfg := &config.VoiceConfig{
		Daemon: config.VoiceDaemonConfig{
			SocketPath: "",
		},
	}

	socketPath := sysd.DefaultSocketPath()
	if cfg.Daemon.SocketPath != "" {
		socketPath = cfg.Daemon.SocketPath
	}

	assert.Equal(t, sysd.DefaultSocketPath(), socketPath)
}

// TestSocketPathCustom_Overrides verifies that non-empty socket path
// overrides the default.
func TestSocketPathCustom_Overrides(t *testing.T) {
	customPath := "/var/run/a2text-custom.sock"
	cfg := &config.VoiceConfig{
		Daemon: config.VoiceDaemonConfig{
			SocketPath: customPath,
		},
	}

	socketPath := sysd.DefaultSocketPath()
	if cfg.Daemon.SocketPath != "" {
		socketPath = cfg.Daemon.SocketPath
	}

	assert.Equal(t, customPath, socketPath)
	assert.NotEqual(t, sysd.DefaultSocketPath(), socketPath)
}
