# a2text — Voice Dictation CLI for Linux

Speech-to-text dictation daemon with global hotkey, system tray icon, autopaste, and a Fyne settings GUI. Supports local whisper.cpp (CGo), the [`go-whisper`](https://github.com/ggml-org/whisper.cpp) HTTP service, and cloud providers (OpenAI, Deepgram).

Target platform: **Linux + Wayland** (GNOME tested). X11 fallback is supported via build tags/runtime detection.

## Quick start

```bash
make build              # builds with -tags whisper (local inference enabled)
make install            # installs to ~/.local/bin/a2text + sets up libwhisper.so
make install-hotkey     # registers F4 (default) as GNOME global shortcut
a2text                  # starts daemon + tray + settings UI
```

Press **F4** to staРедактор субтитров А.Семкин Корректор А.Егороваrt/stop recording. The transcript lands in the clipboard and is auto-pasted into the active window.

## Installation

### 1. System dependencies (Ubuntu 22.04 / 24.04)

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
| **Audio** | `ffmpeg pipewire-pulse` | Capture via `pw-record` / `parecord` + WAV conversion |
| **GUI** | `libgl1-mesa-dev`, `libgtk-3-dev`, ... | Fyne settings window + tray icon |
| **Input** | `wl-clipboard`, `wtype`, `ydotool` | Wayland clipboard and keystroke injection |

### 2. Autopaste Setup

To use the reliable `uinput` backend (virtual keyboard) on Wayland:

```bash
sudo usermod -aG input $USER
# Log out and back in for the change to take effect.
```

### 3. Build & Install

```bash
make install
```
This builds the `whisper.cpp` shared libraries, the Go binary, and installs them to `~/.local/lib/a2text`. A wrapper script is created at `~/.local/bin/a2text` to handle library paths automatically.

## STT Backends

| Backend | Mode | Setup |
|---|---|---|
| **whisper.cpp** | Local | CGo-based. Download model into `~/.local/share/a2text/models/`. |
| **go-whisper** | Remote | HTTP service. No CGo required for the client application. |
| **OpenAI** | Cloud | Set `A2TEXT_OPENAI_API_KEY`. Excellent quality. |
| **Deepgram** | Cloud | Set `A2TEXT_DEEPGRAM_API_KEY`. Extremely fast. |

## Architecture & Code Standards

The project follows **Onion (Clean) Architecture** and **SOLID** principles:

- **`internal/domain`**: Pure entities and sentinel errors. No external deps.
- **`internal/usecase`**: Business rules (e.g., Voice State Machine). Uses **DIP** (interfaces).
- **`internal/adapters`**: Implementations for IPC, UI, Tray, and Output delivery.
- **`internal/infra`**: Wiring, Config (Viper), and CLI (urfave/cli v3).
- **`pkg/`**: Reusable libraries for audio processing, hotkeys, and STT clients.

## CLI & Integration

The binary `a2text` acts as both the daemon and the client.

- `a2text`: Toggles recording (or starts the daemon if not running).
- `a2text setup`: Registers the global shortcut in GNOME.
- `a2text --daemon`: Forced daemon mode (suitable for `systemd` units).

### IPC Protocol
The daemon listens on a Unix socket (default: `$XDG_RUNTIME_DIR/a2text/a2text-voice.sock`). You can send JSON commands via scripts:
- `toggle`: Start/stop recording.
- `start`: Begin capture.
- `stop`: End capture and transcribe.
- `ping`: Check daemon status.

## Configuration

Config file: `~/.config/a2text/config.yaml`.
Key sections:
- `provider`: `whisper-cpp` | `go-whisper` | `openai` | `deepgram`
- `capture`: sample rate, silence threshold, max duration.
- `output`: mode (clipboard/autopaste), restore clipboard toggle.
- `hotkey`: key, modifiers, and mode (toggle/hold).
- `privacy`: toggle audio history and transcript logging.

## License

See [LICENSE](LICENSE) for details.
