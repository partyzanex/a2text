include go.mk

# --- Configuration ---

BINARY_NAME := a2text
CMD_PATH    := ./cmd/a2text
BIN_DIR     := ./bin

WHISPER_DIR := ./whisper.cpp
WHISPER_LIB := $(WHISPER_DIR)/build/src/libwhisper.a

PREFIX        ?= $(HOME)/.local
DESTDIR       ?=
XDG_DATA_HOME ?= $(HOME)/.local/share

# Host OS detection. Desktop entries (XDG .desktop files) are a
# freedesktop.org convention used by Linux and BSD desktops; macOS and
# Windows have their own packaging conventions, so we skip the
# .desktop install there. Uname output: Linux, Darwin, FreeBSD,
# OpenBSD, NetBSD, MINGW64_NT-*, etc.
UNAME_S := $(shell uname -s 2>/dev/null || echo unknown)

BIN_OUT := $(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/partyzanex/a2text/internal/infra/cli.Version=$(VERSION) \
           -X github.com/partyzanex/a2text/internal/infra/cli.Commit=$(COMMIT)

CGO_CFLAGS  := -I$(CURDIR)/$(WHISPER_DIR)/include -I$(CURDIR)/$(WHISPER_DIR)/ggml/include
CGO_LDFLAGS := -L$(CURDIR)/$(WHISPER_DIR)/build/src -L$(CURDIR)/$(WHISPER_DIR)/build/ggml/src \
               -lwhisper -lggml -lggml-base -lggml-cpu -lm -lstdc++ -lgomp

# --- whisper.cpp static libraries ---
# BUILD_SHARED_LIBS=OFF gives static archives (.a). The Go binary
# pulls them in at link time, so the resulting bin/a2text is a single
# self-contained file — no LD_LIBRARY_PATH wrapper, no extra .so to
# install. libc/libstdc++/libGL/libX11 still come from the system as
# normal dynamic deps; that's the standard Linux desktop pattern.
$(WHISPER_LIB):
	git submodule update --init --recursive
	cd $(WHISPER_DIR) && cmake -B build \
	    -DBUILD_SHARED_LIBS=OFF \
	    -DWHISPER_BUILD_TESTS=OFF \
	    -DWHISPER_BUILD_EXAMPLES=OFF
	cd $(WHISPER_DIR) && cmake --build build --config Release

# --- Build (always with whisper.cpp + PipeWire CGO support) ---

.PHONY: build
build: $(WHISPER_LIB)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS='$(CGO_LDFLAGS)' \
	go build -tags whisper -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_PATH)

# --- Install ---
#
# whisper.cpp is linked statically, so the Go binary is fully
# self-contained — drop it into $PREFIX/bin and you're done.

.PHONY: install
install: build install-desktop
	install -Dm 755 $(BIN_DIR)/$(BINARY_NAME) $(BIN_OUT)

# install-desktop ships the freedesktop.org .desktop entry so the app
# shows up in the application menu. Only meaningful on Linux/BSD where
# $XDG_DATA_HOME/applications is honoured; on macOS/Windows we no-op
# with a hint so `make install` does not fail there.
.PHONY: install-desktop
install-desktop:
ifeq ($(filter $(UNAME_S),Linux FreeBSD OpenBSD NetBSD DragonFly),)
	@echo "install-desktop: skipping — no XDG desktop entry support on $(UNAME_S)"
else
	sed "s|Exec=.*|Exec=$(BIN_OUT)|" dist/a2text.desktop > /tmp/a2text.desktop
	install -Dm 644 /tmp/a2text.desktop $(XDG_DATA_HOME)/applications/a2text.desktop
	rm -f /tmp/a2text.desktop
	update-desktop-database $(XDG_DATA_HOME)/applications/ 2>/dev/null || true
endif

.PHONY: uninstall
uninstall: uninstall-desktop
	rm -f $(BIN_OUT)

.PHONY: uninstall-desktop
uninstall-desktop:
ifeq ($(filter $(UNAME_S),Linux FreeBSD OpenBSD NetBSD DragonFly),)
	@echo "uninstall-desktop: skipping — no XDG desktop entry support on $(UNAME_S)"
else
	rm -f $(XDG_DATA_HOME)/applications/a2text.desktop
	update-desktop-database $(XDG_DATA_HOME)/applications/ 2>/dev/null || true
endif

.PHONY: gen
# Runs all //go:generate directives in the repo. Currently regenerates
# internal/i18n/keys.gen.go from messages/en.toml. Required before test
# and lint so generated artefacts stay in sync with their sources.
gen:
	go generate ./...

# --- Test / Lint ---

.PHONY: test
test: gen $(WHISPER_LIB)
	CGO_ENABLED=1 CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS='$(CGO_LDFLAGS)' \
	LD_LIBRARY_PATH="$(CURDIR)/$(WHISPER_DIR)/build/src:$(CURDIR)/$(WHISPER_DIR)/build/ggml/src:$$LD_LIBRARY_PATH" \
	go test -tags whisper -count=1 -race ./...

.PHONY: test-integration
test-integration: gen $(WHISPER_LIB)
	CGO_ENABLED=1 CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS='$(CGO_LDFLAGS)' \
	LD_LIBRARY_PATH="$(CURDIR)/$(WHISPER_DIR)/build/src:$(CURDIR)/$(WHISPER_DIR)/build/ggml/src:$$LD_LIBRARY_PATH" \
	go test -tags integration,x11,linux,whisper -count=1 -race ./...

.PHONY: lint
lint: gen
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run -c .golangci.yml

.PHONY: lint-fix
lint-fix: gen
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run -c .golangci.yml --fix

# --- go.mk bootstrap ---

go.mk:
	@tmpdir=$$(mktemp -d) && \
	git clone --depth 1 --single-branch https://github.com/partyzanex/go-makefile.git $$tmpdir && \
	cp $$tmpdir/go.mk $(CURDIR)/go.mk
