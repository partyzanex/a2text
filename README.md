# a2text — Voice Dictation CLI for Linux

Speech-to-text dictation daemon with global hotkey, system tray icon, autopaste, and a Fyne settings GUI. Supports local whisper.cpp (CGo), the [`go-whisper`](https://github.com/ggml-org/whisper.cpp) HTTP service, and cloud providers (OpenAI, Deepgram).

Target platform: **Linux + Wayland** (GNOME tested). X11 fallback is supported via build tags/runtime detection.

## Quick start

```bash
make build              # build binary with whisper.cpp (CGo, -tags whisper)
make install            # install to ~/.local/bin/a2text + .desktop entry
a2text setup            # register F4 as GNOME global shortcut
a2text                  # start daemon + tray + settings UI
```

Press **F4** to start/stop recording. The transcript lands in the clipboard and is auto-pasted into the active window.

> **Note:** `make install` builds a single self-contained binary. There are no separate `.so` files to manage — whisper.cpp is statically linked via CGo.

## Requirements

- **Go ≥ 1.26** (see `go.mod`; tested with Go 1.26.1)
- **System packages** (Ubuntu 22.04 / 24.04):

```bash
sudo apt update
sudo apt install -y \
    build-essential pkg-config cmake git curl ffmpeg \
    libgl1-mesa-dev libx11-dev libxcursor-dev libxrandr-dev \
    libxinerama-dev libxi-dev libxxf86vm-dev \
    libayatana-appindicator3-dev libgtk-3-dev \
    pipewire-pulse wl-clipboard zenity
```

| Group | Packages | Purpose |
|---|---|---|
| **Build** | `build-essential pkg-config cmake` | Compiling Go, CGo, and whisper.cpp |
| **Audio** | `ffmpeg pipewire-pulse` | Capture via `pw-record` / `parec` + WAV conversion |
| **GUI** | `libgl1-mesa-dev libgtk-3-dev ...` | Fyne settings window + system tray |
| **Input** | `wl-clipboard wtype ydotool` | Wayland clipboard and keystroke injection |

### Autopaste backends

| Backend | Wayland | X11 | Notes |
|---|---|---|---|
| `uinput` | ✅ | ✅ | Virtual keyboard via `/dev/uinput`. Requires `sudo usermod -aG input $USER` + re-login. |
| `wtype` | ✅ | ❌ | Wayland virtual-keyboard protocol. Compositor support varies. |
| `ydotool` | ✅ | ✅ | Needs `ydotoold` running. |
| `xdotool` | ❌ | ✅ | X11 only. |
| `auto` | ✅ | ✅ | Picks the first available backend. |

Select via `output.autopaste_command` in config, or let `auto` choose.

## Build targets

| Target | What it does |
|---|---|
| `make build` | Build with `-tags whisper`; compiles whisper.cpp via CMake on first run |
| `make install` | Build + copy to `~/.local/bin/a2text` + install `.desktop` entry |
| `make install-desktop` | Install `.desktop` entry only |
| `make uninstall` | Remove binary and `.desktop` entry |
| `make test` | Run unit tests |
| `make test-integration` | Run integration tests |
| `make lint` | Run golangci-lint |
| `make lint-fix` | Run golangci-lint with auto-fix |
| `make gen` | Run `go generate ./...` (regenerate i18n keys) |

## STT backends

| Backend | Mode | Setup |
|---|---|---|
| **whisper.cpp** | Local (CGo) | `make build` + model in `~/.local/share/a2text/models/` |
| **go-whisper** | Remote HTTP | Docker service `go-whisper` at `http://localhost:9081`; no CGo for the client |
| **OpenAI** | Cloud API | Set `A2TEXT_OPENAI_API_KEY`; configure `openai` section in config |
| **Deepgram** | Cloud API | Set `A2TEXT_DEEPGRAM_API_KEY`; supports streaming mode |
| **Fallback** | Proxy | Configure with `fallback_primary` + `fallback_secondary` providers |
| **Retry** | Decorator | Enable `stt_retry` in config for automatic retries with exponential backoff |

## CLI reference

```bash
a2text                          # toggle recording (or self-bootstrap as daemon)
a2text --daemon                 # start as daemon only (for systemd units)
a2text setup                    # register GNOME global shortcut
a2text setup --undo             # remove the shortcut
a2text --provider whisper-cpp   # override STT provider for this invocation
a2text --lang en                # override language
a2text --log-level debug        # override log level
a2text --config /path/to/cfg    # use a custom config file
a2text --pprof 127.0.0.1:6060   # enable pprof endpoint
```

Hidden developer flags (not shown in `--help`):

| Flag | Purpose |
|---|---|
| `--file PATH` | Transcribe a single audio file to stdout |
| `--record DURATION` | Record from mic for the given duration, transcribe, print to stdout |

### Environment variables

All config keys can be overridden via `A2TEXT_`-prefixed env vars (`.` becomes `_`):

| Variable | Maps to |
|---|---|
| `A2TEXT_PROVIDER` | `provider` |
| `A2TEXT_LANGUAGE` | `language` |
| `A2TEXT_LOG_LEVEL` | `log_level` |
| `A2TEXT_CONFIG` | Config file path |
| `A2TEXT_OPENAI_API_KEY` | `openai.api_key` |
| `A2TEXT_DEEPGRAM_API_KEY` | `deepgram.api_key` |

## IPC protocol

The daemon listens on `$XDG_RUNTIME_DIR/a2text/a2text-voice.sock`. Connect via `socat`:

```bash
echo '{"version": 1, "id": "1", "command": "toggle"}' | socat - UNIX-CONNECT:$XDG_RUNTIME_DIR/a2text/a2text-voice.sock
```

**Commands:** `toggle`, `start`, `stop`, `ping`

**Response fields:** `ok`, `state` (idle/recording/transcribing/error), `message`, `last_error`, `error_code`.

## Configuration

Config file: `~/.config/a2text/config.yaml` (XDG). Defaults in `app/config.yaml`.

Key sections:

```yaml
provider: "go-whisper"            # go-whisper | whisper-cpp | openai | deepgram
language: "ru"                    # STT language hint ("auto" for detection)
ui_language: ""                   # Settings UI locale (ru | en)

go_whisper:
  url: "http://localhost:9081/api/whisper"
  model: "ggml-large-v3-turbo"
  timeout: "10m"
  auto_download: true

openai:
  api_key: ""                     # or use A2TEXT_OPENAI_API_KEY env var
  base_url: ""
  model: "whisper-1"

deepgram:
  api_key: ""                     # or use A2TEXT_DEEPGRAM_API_KEY env var
  base_url: ""
  model: "nova-2"
  streaming: false                # enable websocket streaming mode

model_path: ""                    # full path to .bin (for whisper.cpp)
whisper_cpp_models_dir: ""        # directory scanned for .bin models

capture:
  backend: "auto"                 # auto | pw-record | parec
  sample_rate: 16000
  channels: 1
  max_duration: "60s"
  silence_threshold_dbfs: -45.0

output:
  mode: "clipboard"               # stdout | clipboard | clipboard-autopaste
  autopaste_command: "auto"       # auto | uinput | wtype | ydotool | xdotool
  restore_clipboard: true

hotkey:
  enabled: true
  key: "F4"
  modifiers: []
  mode: "toggle"                  # toggle | hold
  backend: "auto"                 # auto | evdev | none

stt_retry:
  enabled: false
  initial_delay: "200ms"
  max_delay: "5s"
  max_attempts: 2

privacy:
  log_transcript: false
  keep_audio: false
  keep_audio_dir: ""
  keep_audio_format: "wav"        # wav | ogg

daemon:
  socket_path: ""
  shutdown_grace_period: "15s"

log_level: "info"                 # debug | info | warn | error
```

## Project layout

```
cmd/a2text/          # Entry point
internal/
  domain/             # Sentinel errors, core types (zero deps)
  usecases/voice/     # Voice state machine, record/transcribe orchestration
  usecases/transcribe/# Transcriber interface (used by consumers, DIP)
  adapters/ipc/       # Unix-socket client/server protocol
  adapters/output/    # Stdout, clipboard, autopaste delivery
  adapters/settings/  # Fyne v2 settings window
  adapters/tray/      # System tray icon + state-driven menu
  adapters/ui/        # Shared Fyne theme
  i18n/               # TOML message catalogues (ru, en)
  infra/cli/          # CLI (urfave/cli v3)
  infra/config/       # Viper-backed YAML config with strict validation
  infra/daemon/       # Daemon lifecycle, self-bootstrap, IPC socket
  infra/depcheck/     # Runtime dependency probes (pw-record, wl-copy, …)
  infra/factory/      # DI wiring (transcriber, capture, clipboard, autopaste)
  infra/setup/        # GNOME shortcut registration
  infra/sysd/        # XDG paths, socket location, PID-file helpers
pkg/
  audio/              # ffmpeg-based audio conversion, probing, RMS
  audio/wav/          # WAV decoder (pcm_s16le, 16 kHz, mono)
  audioarchive/       # Optional persistent audio archiver (WAV/OGG)
  capture/            # Microphone capture via pw-record / parec
  clipboard/          # Wayland + X11 clipboard, autopaste backends
  gowhisper/          # go-whisper HTTP health probe
  hotkey/             # Hotkey orchestration types
  session/            # Detect X11/Wayland session from environment
  stt/                # STT clients: whisper.cpp, go-whisper, OpenAI, Deepgram, fallback, retry
  sttx/               # Shared sentinel errors (ErrTranscribeFailed, …)
  whispercpp/         # Model downloader (HuggingFace mirrors)
```

## Architecture

The project follows **Onion (Clean) Architecture** and **SOLID**:

| Layer | May import | Owns |
|---|---|---|
| `domain` | stdlib only | Sentinel errors, value objects |
| `usecases` | domain + stdlib | Business logic, interface definitions (DIP) |
| `adapters` | usecases + domain + stdlib | IPC, UI, tray, output delivery |
| `infra` | all layers (composition root) | CLI, config, DI factories, daemon lifecycle |

Key principles:
- Interfaces are defined in the **consumer** package (DIP), not the implementation.
- Constructor functions return **concrete types**, not interfaces.
- `pkg/` packages have no `internal/` dependencies — they are reusable libraries.
