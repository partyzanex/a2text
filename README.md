# a2text — Voice Dictation CLI for Linux

Speech-to-text dictation daemon with global hotkey, system tray, autopaste, and a Fyne settings GUI. Supports local whisper.cpp, the [`go-whisper`](https://github.com/ggml-org/whisper.cpp) HTTP service, and cloud providers (OpenAI, Deepgram).

Target platform: **Linux + Wayland** (GNOME tested). X11 path was removed.

## Quick start

```bash
make install            # builds with -tags whisper and installs to ~/.local/bin
make install-hotkey     # registers F4 (default) as GNOME global shortcut
a2text                  # first run starts daemon + tray
```

Press the hotkey to start/stop recording. Transcript lands in the clipboard (and is auto-pasted if enabled).

## Build targets

| Target | Output | CGo / native deps |
|---|---|---|
| `make build` | pure-Go binary (`go-whisper` + cloud only) | none |
| `make build-wayland` | alias for `build` | none |
| `make build-whisper` | builds whisper.cpp via CMake, then `-tags whisper` | gcc, g++, cmake, ffmpeg |
| `make install` | runs `build-whisper`, copies to `~/.local/bin/a2text`, installs `.desktop` | same as `build-whisper` |
| `make install-hotkey` | registers GNOME global shortcut (via `a2text setup`) | none |
| `make model-download` | downloads `ggml-large-v3-turbo.bin` into `~/.local/share/a2text/models/` | curl |
| `make test` | `go test ./...` | none |
| `make test-whisper` | whisper.cpp tests under `-tags whisper` | whisper-build done |
| `make lint` | golangci-lint | none |
| `make dev-up` / `make dev-down` | docker-compose stack (go-whisper + postgres) | docker |

## Configuration

Default config: `app/config.yaml` (committed). User overrides: `~/.config/a2text/config.yaml` (XDG) or `app/config.local.yaml` (gitignored).

Key fields:

```yaml
provider: "go-whisper"            # go-whisper | whisper-cpp | cloud
language: "ru"                    # STT hint
ui_language: ""                   # Settings UI locale (ru | en)

go_whisper:
  url: "http://localhost:9081/api/whisper"
  model: "ggml-large-v3-turbo"
  timeout: "10m"
  auto_download: true

# whisper.cpp
model_path: ""                    # full path to .bin
whisper_cpp_models_dir: ""        # directory scanned for .bin models (default: ~/.local/share/a2text/models)

cloud_provider: ""                # openai | deepgram
cloud_api_key: ""                 # prefer A2TEXT_CLOUD_API_KEY env
cloud_base_url: ""

temp_dir: ""                      # session temp dir; empty = auto
convert_timeout: "60s"
transcribe_timeout: "10m"

capture:
  backend: "auto"                 # auto | pw-record | parec
  sample_rate: 16000
  channels: 1
  max_duration: "60s"
  silence_threshold_dbfs: -42

output:
  mode: "clipboard"               # stdout | clipboard | clipboard-autopaste
  autopaste_command: "auto"       # auto | uinput | wtype | ydotool | xdotool
  restore_clipboard: true         # restore prior clipboard after paste

hotkey:
  enabled: true
  key: "F4"
  modifiers: []                   # ["ctrl","alt","shift","super"]
  mode: "toggle"                  # toggle | hold
  backend: "auto"                 # auto | none

privacy:
  log_transcript: false
  keep_audio: false
  keep_audio_dir: ""
  keep_audio_format: "wav"        # wav | ogg

stt_retry:
  enabled: false
  initial_delay: "200ms"
  max_delay: "5s"
  max_attempts: 2

daemon:
  socket_path: ""                 # default: $XDG_RUNTIME_DIR/a2text/a2text-voice.sock
  shutdown_grace_period: "15s"

log_level: "info"                 # debug | info | warn | error
```

## STT providers

| Provider | Setup | Notes |
|---|---|---|
| **go-whisper** | HTTP service (Docker via `make dev-up`) | Pure Go, no CGo |
| **whisper.cpp** | `make build-whisper` + model in `whisper_cpp_models_dir` | CGo, `-tags whisper` |
| **OpenAI** | `cloud_api_key` or `A2TEXT_CLOUD_API_KEY` env | Cloud |
| **Deepgram** | `cloud_provider: deepgram` + key | Cloud |

## Hotkey on Wayland

Wayland does not support global hotkey capture from user processes. a2text registers the hotkey as a **GNOME custom shortcut** via `gsettings` (binding → `a2text` binary path). Pressing the key launches `a2text` without args; the new process detects the running daemon via the IPC socket and sends a Toggle command.

```bash
a2text setup            # register hotkey
a2text setup --undo     # unregister
```

KDE / sway: bind your DE shortcut manager to run `a2text`.

## Autopaste backends

Selected via `output.autopaste_command`:

- `uinput` — virtual keyboard via `/dev/uinput` (requires user in `input` group + re-login)
- `wtype` — Wayland keystroke injection (subset of compositors)
- `ydotool` — needs `ydotoold` running
- `xdotool` — X11 fallback (won't work on pure Wayland)
- `auto` — picks the first available

## Project layout

### `pkg/` — reusable libraries (no `internal/` deps)

| Path | Purpose |
|---|---|
| [`pkg/stt`](pkg/stt) | STT clients: OpenAI, Deepgram, go-whisper, whisper.cpp, fallback + retry |
| [`pkg/gowhisper`](pkg/gowhisper) | go-whisper HTTP client |
| [`pkg/whispercpp`](pkg/whispercpp) | whisper.cpp model downloader |
| [`pkg/audio`](pkg/audio), [`pkg/audio/wav`](pkg/audio/wav) | ffmpeg-based conversion, probing, WAV decode |
| [`pkg/audioarchive`](pkg/audioarchive) | Optional persistent audio capture (WAV/OGG) |
| [`pkg/capture`](pkg/capture) | Microphone capture via `pw-record` / `parec` |
| [`pkg/clipboard`](pkg/clipboard) | Wayland (`wl-copy`) + xclip fallback, autopaste backends |
| [`pkg/hotkey`](pkg/hotkey) | Hotkey orchestration types (Wayland is DE-driven; see `infra/setup`) |
| [`pkg/session`](pkg/session) | Detect session type from environment |
| [`pkg/sttx`](pkg/sttx) | Shared sentinel errors (`ErrTranscribeFailed`, `ErrConversionFailed`, …) |

```go
import "github.com/partyzanex/a2text/pkg/stt"
```

### `internal/` — application

| Path | Purpose |
|---|---|
| `internal/domain` | App sentinel errors and core types |
| `internal/usecases/voice` | Voice daemon state machine, hotkey orchestration, record/transcribe flow |
| `internal/usecases/transcribe` | Transcriber interface for non-voice consumers |
| `internal/adapters/ipc` | Unix-socket protocol between client and daemon |
| `internal/adapters/output` | Stdout / clipboard / autopaste delivery, clipboard snapshot-restore |
| `internal/adapters/settings` | Fyne v2 settings window (tabs: STT / Capture+Hotkey / Daemon) |
| `internal/adapters/tray` | System tray (SVG icons, state-driven menu) |
| `internal/adapters/ui` | Shared Fyne theme + components |
| `internal/i18n` | TOML message catalogues (ru, en) |
| `internal/infra/cli` | CLI wiring (urfave/cli v3) |
| `internal/infra/config` | Viper-backed YAML loader with strict key validation |
| `internal/infra/daemon` | Daemon lifecycle, IPC socket, race-free startup |
| `internal/infra/depcheck` | Runtime dependency probes (pw-record, wl-copy, …) |
| `internal/infra/factory` | Provider selection (transcriber, capture, clipboard, autopaste) |
| `internal/infra/setup` | `a2text setup` — GNOME shortcut registration |
| `internal/infra/sysd` | XDG paths, socket location helpers |

## Settings GUI

`a2text` launches a Fyne window on first start. Tabs:

- **STT** — provider, language, go-whisper/whisper.cpp/cloud cards, retry policy
- **Capture + Hotkey** — capture backend, sample rate, hotkey binding (interactive capture), mode/backend, output mode + autopaste + restore-clipboard
- **Daemon** — IPC socket path, grace period, temp dir (folder picker), timeouts, log level, privacy (keep audio dir/format)

Values are saved on field change (debounced). The daemon hot-reloads selected components (transcriber, output) without restart where possible.

## Logs and state

- Daemon log: stdout (or systemd journal if launched as a service)
- IPC socket: `$XDG_RUNTIME_DIR/a2text/a2text-voice.sock`
- Lock file: same directory, `a2text-voice.lock`
- Default models dir: `~/.local/share/a2text/models/`

## License

See LICENSE if present.
