# a2text — Voice Dictation CLI for Linux

Speech-to-text dictation daemon with global hotkey, system tray icon, autopaste, and a Fyne settings GUI. Supports local whisper.cpp (CGo), the [`go-whisper`](https://github.com/ggml-org/whisper.cpp) HTTP service, and cloud providers (OpenAI, Deepgram).

Target platform: **Linux + Wayland** (GNOME tested). X11 fallback is supported via build tags/runtime detection.

## Features

### Recording & transcription

- **Push-to-talk hold mode** and **click-to-toggle mode** — `hotkey.mode: hold | toggle`. Hold needs a backend that sees both Press and Release (`evdev`).
- **Two hotkey backends**: `evdev` (reads `/dev/input/event*`, sees Press/Release on any session — requires `input` group membership) and `auto` (Wayland → evdev, X11 → XGrabKey when built with `-tags=x11`, otherwise registers a GNOME custom-keybinding that is Press-only).
- **Multiple STT providers** with the same wire protocol: local `whisper-cpp` (CGo, offline), remote `go-whisper` HTTP service, OpenAI cloud, Deepgram cloud (incl. streaming).
- **Fallback chain** and **retry decorator** — primary/secondary providers with exponential-backoff retries (`stt_retry`).
- **Silence gate** (`capture.silence_threshold_dbfs`) skips STT when the recording is below the dBFS threshold — saves API calls and avoids hallucinated transcripts from background noise.
- **Per-cycle max duration cap** (`capture.max_duration`) protects against runaway captures.
- **Capture backends**: `pw-record` (PipeWire) and `parec` (PulseAudio) — auto-detected.

### Output delivery

- **Three output modes**: `stdout`, `clipboard`, `clipboard-autopaste`.
- **Autopaste backends**: `uinput` (kernel virtual keyboard, recommended on Wayland), `wtype`, `ydotool`, `xdotool` — or `auto` picks the first that probes ready.
- **Clipboard snapshot/restore** — saves whatever was on the clipboard before delivery and restores it after, so the user's previous copy buffer is not clobbered (`output.restore_clipboard`).
- **Wayland + X11 clipboard backends** — `wl-copy`/`wl-paste` and `xclip`, auto-selected.

### UI & lifecycle

- **System-tray icon** with state-driven menu (idle / recording / transcribing / error).
- **Fyne v2 settings window** with live validation, debounced auto-save, and i18n (ru, en).
- **Self-bootstrap** — running `a2text` with no arguments and no live daemon socket starts one and sends `toggle` once the socket is ready.
- **Autostart on login** — toggle in the settings UI writes an XDG `.desktop` file under `~/.config/autostart/`.
- **First-run model download** — `whisper-cpp` provider auto-fetches `ggml-tiny.bin` into the XDG data dir; bigger models come from the **Download model** dialog.
- **IPC over Unix socket** — `toggle`, `start`, `stop`, `ping` (see [IPC protocol](#ipc-protocol)).
- **Audio archive** (optional) — keep every recording as WAV or OGG under `privacy.keep_audio_dir`.
- **Transcript log** (optional) — `privacy.log_transcript`. Off by default.

### Settings UI tabs

| Tab | What you configure |
|---|---|
| **STT** | Active provider, model picker, language, retry/fallback chain, provider-specific credentials (OpenAI / Deepgram / go-whisper URL). |
| **Capture & Hotkey** | Capture backend (auto / pw-record / parec), sample rate, channels, silence threshold, max recording duration; hotkey key + modifiers, mode (toggle / hold), backend (auto / evdev / none), autopaste command. |
| **Output** | Output mode (stdout / clipboard / clipboard-autopaste), restore-clipboard toggle. |
| **Privacy** | Transcript logging, audio archive (off / wav / ogg), archive directory. |
| **Process** | Autostart on login, temp directory, log level, UI language, shutdown grace period. |
| **About** | Version, commit, build info, links. |

> Hold mode + naked function key (e.g. F4) requires `backend: evdev` (or `auto` under Wayland). The GNOME DE shortcut path is Press-only and will degrade hold to repeated toggle on key autorepeat.

## Quick start

`make install` picks the layout from the caller:

- run as a regular user → installs into `$HOME/.local` (no sudo, no system files touched);
- run via `sudo` → installs into `/usr/local` (system-wide, what `.deb` consumers get).

```bash
make build
make install            # → ~/.local/bin/a2text                  (non-root)
sudo make install       # → /usr/local/bin/a2text                (root)
a2text                  # start daemon + tray + settings UI (auto-registers the GNOME hotkey)
```

`make install-user` / `make install-system` pin the layout explicitly, useful when you want to force one even though the caller's UID says otherwise.

The hotkey is auto-registered on every daemon start: a2text reads `hotkey.key` / `hotkey.modifiers` from the config and (on GNOME) installs the corresponding `custom-keybinding` in dconf. If the same binding is already in place the call is a silent no-op, so restarts cost nothing. Change the key in the settings UI or in `~/.config/a2text/config.yaml` and the next daemon start picks it up.

Default binding is **Super+R** (`hotkey.key: "R"`, `hotkey.modifiers: ["super"]`). Press the hotkey to start/stop recording. The transcript lands in the clipboard and is auto-pasted into the active window.

### Autostart on login

The settings UI exposes an **Autostart** checkbox under the "Process" tab. Toggling it writes (or removes) `$XDG_CONFIG_HOME/autostart/io.github.partyzanex.a2text.desktop` (fallback `~/.config/autostart/`). The entry runs `a2text --daemon` ~5 s into the graphical session so the tray host, clipboard, and DBus are ready before the daemon starts. No YAML flag is involved — the file's presence is the source of truth, so deleting it via GNOME Tweaks immediately reflects back into the checkbox.

### First-run whisper.cpp model

When `provider: whisper-cpp` and `model_path` is empty, the daemon downloads `ggml-tiny.bin` (~75 MB) into `whisper_cpp_models_dir` (default `$XDG_DATA_HOME/a2text/models` → `~/.local/share/a2text/models`) on first start and writes the resolved path back into the config. Pick a heavier model (`ggml-small.bin`, `ggml-large-v3-turbo.bin`, …) from the settings UI's **Download model** dialog once you want better quality on Russian dictation.

> **Note:** `make build` produces a single self-contained binary. whisper.cpp is statically linked via CGo, so there are no `.so` files to ship and `LD_LIBRARY_PATH` is not needed.

### Packaging (DESTDIR)

`install` and `install-system` honour `DESTDIR` for staging into a packaging root. The `.desktop` `Exec=` line and the hicolor icon paths use the real `PREFIX`, not the staging prefix — output is suitable for `dpkg-deb -b`, `rpmbuild`, or `nfpm`.

```bash
make install DESTDIR=/tmp/pkg PREFIX=/usr
# →  /tmp/pkg/usr/bin/a2text
#    /tmp/pkg/usr/share/applications/io.github.partyzanex.a2text.desktop
#    /tmp/pkg/usr/share/icons/hicolor/{64x64,128x128,256x256}/apps/io.github.partyzanex.a2text.png
```

When `DESTDIR` is non-empty the install skips `update-desktop-database` / `gtk-update-icon-cache` so the package's postinst hook can run them on the target machine instead.

## Requirements

- **Go ≥ 1.26.1** (see `go.mod`)
- **System packages** (Ubuntu 22.04 / 24.04):

```bash
sudo apt update
sudo apt install -y \
    build-essential pkg-config cmake git curl ffmpeg \
    libgl1-mesa-dev libx11-dev libxcursor-dev libxrandr-dev \
    libxinerama-dev libxi-dev libxxf86vm-dev \
    libayatana-appindicator3-dev libgtk-3-dev \
    pipewire-bin pipewire-pulse \
    wl-clipboard wtype ydotool xdotool xclip \
    zenity
```

| Group | Packages | Purpose |
|---|---|---|
| **Build** | `build-essential pkg-config cmake git curl` | Compiling Go, CGo, whisper.cpp, fetching submodules / models |
| **Audio** | `ffmpeg pipewire-bin pipewire-pulse` | Capture via `pw-record` (pipewire-bin) / `parec` (pipewire-pulse) + WAV conversion |
| **GUI** | `libgl1-mesa-dev libx11-dev libxcursor-dev libxrandr-dev libxinerama-dev libxi-dev libxxf86vm-dev libayatana-appindicator3-dev libgtk-3-dev` | Fyne settings window + system tray (XDG/SNI) |
| **Clipboard** | `wl-clipboard xclip` | Wayland (`wl-copy`/`wl-paste`) + X11 (`xclip`) clipboard backends |
| **Autopaste** | `wtype ydotool xdotool` | See backend table below. `auto` mode tries them in order. |
| **Dialogs** | `zenity` (optional: `kdialog` for KDE) | Native folder picker in settings UI; Fyne fallback works without them. |

Hotkey auto-register on GNOME uses `gsettings` from `libglib2.0-bin`, which Ubuntu ships by default with the desktop — no extra install in normal setups.

### Autopaste backends

| Backend | apt package | Wayland | X11 | Notes |
|---|---|---|---|---|
| `uinput` | — (kernel) | ✅ | ✅ | Virtual keyboard via `/dev/uinput`. Requires `sudo usermod -aG input $USER` + re-login. No extra package — module is built into the kernel. |
| `wtype` | `wtype` | ✅ | ❌ | Wayland virtual-keyboard protocol. Compositor support varies (GNOME 46+ OK, KDE wayland OK, sway OK). |
| `ydotool` | `ydotool` | ✅ | ✅ | Needs the `ydotoold` daemon running (`systemctl --user enable --now ydotool` after install). |
| `xdotool` | `xdotool` | ❌ | ✅ | X11 only — works under XWayland too but cannot inject into native Wayland windows. |
| `auto` | — | ✅ | ✅ | Picks the first available backend that the runtime probe confirms is wired up. |

Select via `output.autopaste_command` in config, or let `auto` choose.

## Build targets

| Target | What it does |
|---|---|
| `make build` | Build binary (`-tags whisper`) + render hicolor icons (64/128/256) into `bin/icons/`. Compiles whisper.cpp via CMake on first run. |
| `make build-icons` | Re-render `bin/icons/{64,128,256}.png` from the inactive-state SVG (via `cmd/genappicon`). Runs automatically as part of `build`. |
| `make install` | Auto layout: non-root → `$HOME/.local`, root (sudo) → `/usr/local`. Honours `DESTDIR`. |
| `make install-system` | Force system layout (`PREFIX=/usr/local`) regardless of caller UID. |
| `make install-user` | Force per-user layout (`$HOME/.local`). Never uses `DESTDIR`, never needs sudo. |
| `make install-desktop` / `install-desktop-user` | Just the `.desktop` entry + hicolor icons (auto vs forced per-user). |
| `make uninstall` / `uninstall-system` / `uninstall-user` | Symmetric removal targets. |
| `make clean` | Drop `bin/` (extends `go.mk`'s `clean`). |
| `make clean-all` | `clean` + drop `whisper.cpp/build` (forces a ~10-minute CMake rebuild next time). |
| `make test` | Run unit tests (`-race`, `-count=1`). |
| `make test-integration` | Run integration tests (extra build tags). |
| `make lint` / `lint-fix` | Run golangci-lint (optionally with auto-fix). |
| `make gen` | Run `go generate ./...` (regenerate i18n keys). |

`PREFIX` resolution order: explicit override (`make install PREFIX=/opt/a2text`) > caller UID (root → `/usr/local`, user → `$HOME/.local`) > packaging mode (`DESTDIR` set → `/usr/local`). The resolved prefix is printed at the top of every `install` run (`→ installing to PREFIX=...`) and baked into the `.desktop` `Exec=` line.

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
  key: "R"
  modifiers: ["super"]            # super | ctrl | alt | shift
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
cmd/genappicon/      # Build-time utility: renders hicolor app icon PNGs from the state SVG
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
  infra/autostart/    # XDG `.desktop` autostart entry (per-user enable/disable)
  infra/cli/          # CLI (urfave/cli v3)
  infra/config/       # Viper-backed YAML config with strict validation
  infra/daemon/       # Daemon lifecycle, self-bootstrap, IPC socket, model bootstrap
  infra/depcheck/     # Runtime dependency probes (pw-record, wl-copy, …)
  infra/factory/      # DI wiring (transcriber, capture, clipboard, autopaste)
  infra/setup/        # GNOME shortcut registration
  infra/sysd/         # XDG paths (config, data, runtime), socket location, PID-file helpers
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
