include go.mk

BINARY_NAME   := a2text
CMD_PATH      := ./cmd/a2text
BIN_DIR       := ./bin

WHISPER_DIR := ./whisper.cpp
UNAME_S     := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
    LIB_EXT := dylib
else
    LIB_EXT := so
endif
WHISPER_LIB := $(WHISPER_DIR)/build/src/libwhisper.$(LIB_EXT)
MODEL_URL   := https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin
MODEL_PATH  := ./ggml-small.bin
WHISPER_MODEL_PATH ?= $(MODEL_PATH)

GO_WHISPER_URL    ?= http://localhost:9081
GO_WHISPER_MODELS ?= ggml-small.bin
GO_WHISPER_MODEL  ?= ggml-small

CGO_CFLAGS  := -I$(CURDIR)/$(WHISPER_DIR)/include -I$(CURDIR)/$(WHISPER_DIR)/ggml/include
CGO_LDFLAGS := -L$(CURDIR)/$(WHISPER_DIR)/build/src -L$(CURDIR)/$(WHISPER_DIR) -lwhisper -lm -lstdc++

PREFIX         ?= $(HOME)/.local
DESTDIR        ?=
XDG_DATA_HOME  ?= $(HOME)/.local/share
XDG_CONFIG_HOME ?= $(HOME)/.config

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/partyzanex/a2text/internal/infra/cmd.Version=$(VERSION) \
           -X github.com/partyzanex/a2text/internal/infra/cmd.Commit=$(COMMIT)

# --- Build ---

.PHONY: build
build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_PATH)

# build-wayland: pure-Go build, no X11. Hotkey falls back to unsupported stub on Wayland.
.PHONY: build-wayland
build-wayland: build

# build-x11: enables X11 hotkey backend (XGrabKey via libX11/CGo).
# Requires libX11-dev (apt: libx11-dev).
.PHONY: build-x11
build-x11:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 go build -tags=x11 -ldflags '$(LDFLAGS)' \
		-o $(BIN_DIR)/$(BINARY_NAME) $(CMD_PATH)

# build-whisper: CGo build with local whisper.cpp STT provider.
.PHONY: build-whisper
build-whisper: $(WHISPER_LIB)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" \
	go build -tags whisper -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_PATH)

# --- Test ---

# test-all: lint + all test suites (unit, integration, whisper CGo, go-whisper).
# test-x11 is excluded — it requires a live X server; run manually with xvfb-run.
.PHONY: test-all
test-all: lint test test-integration test-whisper test-gowhisper

.PHONY: test
test:
	@CGO_ENABLED=1 go test -v -count=1 -race ./...

.PHONY: test-integration
test-integration:
	@CGO_ENABLED=1 go test -v -count=1 -race -tags=integration ./...

# test-x11: runs X11 hotkey live tests against the host X server.
# Skips when DISPLAY is unset or xdotool is missing. For headless: xvfb-run -a make test-x11
.PHONY: test-x11
test-x11:
	@CGO_ENABLED=1 go test -v -count=1 -race -tags="x11 x11live" \
		-run X11HotkeyKeypress ./pkg/hotkey/

.PHONY: test-whisper
test-whisper: $(WHISPER_MODEL_PATH)
	CGO_ENABLED=1 CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" \
	LD_LIBRARY_PATH="$(CURDIR)/$(WHISPER_DIR)/build/src:$(CURDIR)/$(WHISPER_DIR):$$LD_LIBRARY_PATH" \
	WHISPER_MODEL_PATH="$(abspath $(WHISPER_MODEL_PATH))" \
	go test -v -tags whisper -race -count=1 ./pkg/stt/... -coverprofile=cover.out

$(MODEL_PATH):
	@echo "Downloading ggml-small.bin model..."
	wget -O $(MODEL_PATH) $(MODEL_URL)
	@echo "Model downloaded to $(MODEL_PATH)"

.PHONY: test-gowhisper
test-gowhisper:
	@CGO_ENABLED=1 go test -v -count=1 -race -tags=integration ./tests/...

.PHONY: lint
lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run -c .golangci.yml

.PHONY: lint-fix
lint-fix:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run -c .golangci.yml --fix

.PHONY: deps
deps:
	go mod tidy
	go mod download

# --- Whisper ---

.PHONY: whisper-deps
whisper-deps:
	@command -v gcc    >/dev/null || (echo "Please install gcc"    && exit 1)
	@command -v g++    >/dev/null || (echo "Please install g++"    && exit 1)
	@command -v cmake  >/dev/null || (echo "Please install cmake"  && exit 1)
	@command -v ffmpeg >/dev/null || (echo "Please install ffmpeg" && exit 1)
	@echo "All system dependencies found"

.PHONY: whisper-submodule
whisper-submodule:
	git submodule update --init --recursive

$(WHISPER_LIB):
	git submodule update --init --recursive
	@echo "Building whisper.cpp shared library..."
	cd $(WHISPER_DIR) && cmake -B build -DBUILD_SHARED_LIBS=ON -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_EXAMPLES=OFF
	cd $(WHISPER_DIR) && cmake --build build --config Release

.PHONY: whisper-build
whisper-build: $(WHISPER_LIB)

.PHONY: model-download
model-download: $(MODEL_PATH)

# --- go-whisper (Docker Compose) ---

COMPOSE_FILE    := .ci/docker/docker-compose.yml
COMPOSE_PROJECT := a2text
COMPOSE         := docker compose -p $(COMPOSE_PROJECT) -f $(COMPOSE_FILE)

.PHONY: dev-up
dev-up:
	$(COMPOSE) up -d --remove-orphans go-whisper

.PHONY: dev-down
dev-down:
	$(COMPOSE) down

.PHONY: models-pull
models-pull:
	@for model in $(GO_WHISPER_MODELS); do \
		echo "==> pulling $$model via $(GO_WHISPER_URL)"; \
		curl -fsS -N \
			-H "Accept: text/event-stream" \
			-H "Content-Type: application/json" \
			-X POST \
			-d "{\"model\":\"$$model\"}" \
			$(GO_WHISPER_URL)/api/whisper/model || exit 1; \
		echo; \
	done

.PHONY: models-list
models-list:
	@curl -fsS $(GO_WHISPER_URL)/api/whisper/model

# --- Install ---

# install: build the binary and register it as a desktop app.
# The .desktop file (Exec=/usr/local/bin/a2text) must match the installed path
# so xdg-desktop-portal can identify the process and grant GlobalShortcuts.
.PHONY: install
install: build
	install -Dm 755 $(BIN_DIR)/$(BINARY_NAME) $(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME)
	sed "s|Exec=.*|Exec=$(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME)|" \
		dist/a2text.desktop > /tmp/a2text.desktop
	install -Dm 644 /tmp/a2text.desktop $(XDG_DATA_HOME)/applications/a2text.desktop
	rm -f /tmp/a2text.desktop
	update-desktop-database $(XDG_DATA_HOME)/applications/ 2>/dev/null || true
	@if [ ! -f $(XDG_CONFIG_HOME)/a2text/config.yaml ]; then \
		install -Dm 644 app/config.yaml $(XDG_CONFIG_HOME)/a2text/config.yaml; \
		echo "Default config written to $(XDG_CONFIG_HOME)/a2text/config.yaml"; \
	else \
		echo "Config already exists at $(XDG_CONFIG_HOME)/a2text/config.yaml — skipping"; \
	fi

.PHONY: uninstall
uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME)
	rm -f $(XDG_DATA_HOME)/applications/a2text.desktop
	update-desktop-database $(XDG_DATA_HOME)/applications/ 2>/dev/null || true

# install-hotkey: register the global keyboard shortcut via `a2text setup`.
# Requires the binary to be installed first (make install).
.PHONY: install-hotkey
install-hotkey:
	$(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME) setup

# uninstall-hotkey: remove the keyboard shortcut registered by install-hotkey.
.PHONY: uninstall-hotkey
uninstall-hotkey:
	$(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME) setup --undo

# --- go.mk bootstrap ---

go.mk:
	@tmpdir=$$(mktemp -d) && \
	git clone --depth 1 --single-branch https://github.com/partyzanex/go-makefile.git $$tmpdir && \
	cp $$tmpdir/go.mk $(CURDIR)/go.mk
