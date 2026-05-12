# a2text — Voice CLI Application

Speech-to-text transcription from audio files. Supports local whisper.cpp, go-whisper HTTP service, and cloud providers (OpenAI, Deepgram).

## Build

```bash
# Pure Go (go-whisper, cloud providers)
make build

# X11 hotkey support (XGrabKey via libX11)
make build-x11

# Local whisper.cpp (requires gcc, g++, cmake, ffmpeg)
make whisper-deps
make build-whisper
make model-download
```

## Usage

```bash
./bin/a2text <audio.wav>           # Transcribe file
./bin/a2text --help                # Show all options
```

Configuration: `app/config.yaml` or `app/config.local.yaml` (local overrides, gitignored).

## STT Providers

| Provider | Setup | Notes |
|----------|-------|-------|
| **go-whisper** | HTTP service, Docker | Pure Go, no CGo required |
| **whisper.cpp** | Local build | CGo required, `-tags whisper` |
| **OpenAI** | API key | Cloud fallback |
| **Deepgram** | API key | Cloud fallback |

## Development

```bash
make test                   # Run all tests
make test-x11              # X11 hotkey live tests (requires X server)
make test-whisper          # whisper.cpp tests
make lint                  # golangci-lint
make deps                  # go mod tidy + download

# Docker Compose (go-whisper + postgres dev stack)
make dev-up
make models-pull           # Download model
make dev-down
```

## Packages

Repository is split into reusable libraries (`pkg/`) and application-specific code (`internal/`).

### `pkg/` — reusable libraries

| Path | Purpose |
|------|---------|
| [`pkg/stt`](pkg/stt) | Speech-to-text clients: OpenAI, Deepgram, go-whisper, whisper.cpp, fallback chain, retry wrapper |
| [`pkg/audio`](pkg/audio), [`pkg/audio/wav`](pkg/audio/wav) | ffmpeg-based audio conversion, probing, WAV decoding, passthrough |
| [`pkg/capture`](pkg/capture) | Microphone capture via `pw-record` / `parecord` subprocess |
| [`pkg/hotkey`](pkg/hotkey) | Global hotkey listeners: X11 (XGrabKey via CGo) and xdg-desktop-portal GlobalShortcuts |
| [`pkg/clipboard`](pkg/clipboard) | Clipboard write + autopaste for X11 (xdotool) and Wayland (wl-copy / wtype / ydotool) |
| [`pkg/session`](pkg/session) | Detect X11 / Wayland session type from environment |
| [`pkg/sttx`](pkg/sttx) | Shared sentinel errors used across STT and audio (`ErrTranscribeFailed`, `ErrConversionFailed`, etc.) |

These packages have no dependencies on `internal/` and can be imported from other Go projects:

```go
import "github.com/partyzanex/a2text/pkg/stt"
```

### `internal/` — a2text application

| Path | Purpose |
|------|---------|
| `internal/domain` | App-specific sentinel errors (`ErrQueueFull`, `ErrUserBusy`, `ErrCooldown`) |
| `internal/usecases/voice` | Voice daemon state machine, hotkey orchestration, record/transcribe flow |
| `internal/usecases/transcribe` | Transcriber interface for use cases outside voice |
| `internal/adapters/ipc` | Unix-socket protocol between CLI client and voice daemon |
| `internal/adapters/output` | Stdout / clipboard / autopaste delivery |
| `internal/infra/factory` | Transcriber provider selection (`factory.Build` + `factory.Config`) |
| `internal/infra/config` | Viper-backed YAML config loader |
| `internal/infra/cmd` | CLI wiring (urfave/cli v3) |

## Documentation

- [`docs/voice-cli.md`](docs/voice-cli.md) — Installation, hotkey setup, troubleshooting
- [`docs/ADR`](docs/adr/) — Architecture decision records

## License

[See LICENSE file if present]
