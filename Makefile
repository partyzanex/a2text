include go.mk

# `make` with no arguments runs the binary build, not whatever target
# go.mk happened to declare first. Set explicitly so the default never
# silently shifts when go.mk gets updated.
.DEFAULT_GOAL := build

# --- Configuration ---

BINARY_NAME := a2text
CMD_PATH    := ./cmd/a2text
BIN_DIR     := ./bin
ICON_BUILD  := $(BIN_DIR)/icons

WHISPER_DIR := ./whisper.cpp
WHISPER_LIB := $(WHISPER_DIR)/build/src/libwhisper.a

# Standard GNU install layout. PREFIX = where things end up at runtime
# (baked into the .desktop Exec= line and used by the running binary).
# DESTDIR = transient staging root for packagers (rpmbuild,
# dpkg-buildpackage, nfpm).
#
# Auto-pick PREFIX based on the caller:
#   - DESTDIR set                → /usr/local (packagers stage a system tree)
#   - real `make install` as root → /usr/local (system-wide)
#   - real `make install` as user → $HOME/.local (per-user, no sudo)
#
# Users can still force a prefix with `make install PREFIX=/opt/...`.
# The explicit `install-user` / `install-system` targets below pin the
# choice regardless of caller UID — useful when running as root to drop
# into a specific home directory, or under sudo to force /usr/local.
IS_ROOT := $(filter 0,$(shell id -u 2>/dev/null))

ifeq ($(DESTDIR),)
  ifeq ($(IS_ROOT),)
    PREFIX ?= $(HOME)/.local
  else
    PREFIX ?= /usr/local
  endif
else
  PREFIX ?= /usr/local
endif

DATADIR := $(PREFIX)/share

DESKTOP_ID    := io.github.partyzanex.a2text
DESKTOP_SRC   := dist/$(DESKTOP_ID).desktop
ICON_SIZES    := 64 128 256
ICON_FILES    := $(patsubst %,$(ICON_BUILD)/%.png,$(ICON_SIZES))
ICON_SVG      := assets/icons/a2t-state-inactive.svg
# Files whose change invalidates every pre-rendered icon: the source
# SVG, the renderer entry point, and the underlying drawing code. Keep
# this list tight — adding noise here forces unnecessary regeneration.
ICON_DEPS     := $(ICON_SVG) cmd/genappicon/main.go assets/staticon.go assets/embed.go
HICOLOR_BASE  := $(DATADIR)/icons/hicolor

# DEST_* paths are where files actually land on disk during this
# install run. DESTDIR is prepended for packaging; in the normal
# `make install` case it is empty and these collapse to the real prefix.
DEST_BIN     := $(DESTDIR)$(PREFIX)/bin
DEST_DESKTOP := $(DESTDIR)$(DATADIR)/applications
DEST_HICOLOR := $(DESTDIR)$(HICOLOR_BASE)

# Explicit per-user / system prefixes for the *-user / *-system targets
# that force the layout regardless of caller UID. These never use DESTDIR.
USER_PREFIX  := $(HOME)/.local
USER_DATADIR := $(HOME)/.local/share
USER_BIN     := $(USER_PREFIX)/bin
USER_DESKTOP := $(USER_DATADIR)/applications
USER_HICOLOR := $(USER_DATADIR)/icons/hicolor

# Host OS detection. Desktop entries (XDG .desktop files) are a
# freedesktop.org convention used by Linux and BSD; macOS/Windows have
# their own packaging stacks, so install-desktop no-ops there.
UNAME_S := $(shell uname -s 2>/dev/null || echo unknown)
IS_XDG  := $(filter $(UNAME_S),Linux FreeBSD OpenBSD NetBSD DragonFly)

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
build: $(WHISPER_LIB) $(ICON_FILES)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS='$(CGO_LDFLAGS)' \
	go build -tags whisper -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_PATH)

# Each $(ICON_BUILD)/<size>.png is a real file target: make compares the
# mtime against $(ICON_DEPS) and skips regeneration when nothing
# changed. This is what `build-icons`-as-phony cost us — every `make
# build` re-ran the 3 generators and races leaked into `make -j
# install`, where `build` and `install-desktop` both depended on the
# same phony. With per-file recipes the build skips on no-op, and each
# size has its own pipeline + exit code so a renderer failure cannot
# hide behind a successful next iteration.
#
# tmp+mv keeps the output atomic — a Ctrl+C mid-`go run` would
# otherwise leave a half-written PNG that the next `make` would treat
# as up-to-date (newer than its deps) and ship to install.
$(ICON_BUILD):
	@mkdir -p $@

$(ICON_BUILD)/%.png: $(ICON_DEPS) | $(ICON_BUILD)
	@echo "  GEN  $@"
	@go run ./cmd/genappicon -size $* > $@.tmp && mv $@.tmp $@

# build-icons stays as a convenience alias so `make build-icons` keeps
# working after the phony→file-target migration. It is itself phony but
# delegates to the file targets, so it is still mtime-aware.
.PHONY: build-icons
build-icons: $(ICON_FILES)

# --- Install (system-wide) ---
#
# whisper.cpp is linked statically, so the Go binary is fully
# self-contained — drop it into $(PREFIX)/bin and you are done.
# DESTDIR is honoured so packaging tools (nfpm, dpkg-buildpackage) can
# stage the tree without touching the real filesystem.

# check-install-perms catches the corner case where the auto-routed
# PREFIX is unwritable — typically `make install PREFIX=/opt/foo` as a
# non-root user, or a sudo run with a HOME-pointed PREFIX. Skipped when
# DESTDIR is set (packagers write into a staging directory they own).
.PHONY: check-install-perms
check-install-perms:
ifeq ($(DESTDIR),)
	@for dir in $(PREFIX)/bin $(DATADIR); do \
	    parent=$$(dirname $$dir); \
	    while [ ! -e "$$parent" ] && [ "$$parent" != "/" ]; do parent=$$(dirname $$parent); done; \
	    if [ ! -w "$$parent" ]; then \
	        echo "make install: no write access to $$parent (need $$dir)."; \
	        echo "  → run 'sudo make install' for a system install ($(PREFIX)),"; \
	        echo "  → or 'make install-user' for a per-user install ($(USER_PREFIX))."; \
	        exit 1; \
	    fi; \
	done
endif

# install banners the resolved layout up front so a sudo vs non-sudo
# call cannot quietly land in the wrong tree — the auto-detection in
# the PREFIX block above is opaque otherwise.
.PHONY: install
install: check-install-perms build install-desktop
	@echo "→ installing to PREFIX=$(PREFIX) (DESTDIR=$(DESTDIR))"
	install -d $(DEST_BIN)
	install -m 755 $(BIN_DIR)/$(BINARY_NAME) $(DEST_BIN)/$(BINARY_NAME)

# install-desktop ships the .desktop entry and hicolor icon set. Two
# things matter for packagers:
#   1. Exec= must point at the *final* path on the target machine
#      ($(PREFIX)/bin/...), not at $(DESTDIR)$(PREFIX)/... — otherwise
#      every package would carry the build host's staging directory.
#   2. update-desktop-database / gtk-update-icon-cache are skipped when
#      DESTDIR is non-empty: the package's postinst hook runs them on
#      the user's machine instead, and running them at build time would
#      poison the package with the builder's cache files.
.PHONY: install-desktop
install-desktop: $(ICON_FILES)
ifeq ($(IS_XDG),)
	@echo "install-desktop: skipping — no XDG desktop entry support on $(UNAME_S)"
else
	install -d $(DEST_DESKTOP)
	@tmp=$$(mktemp) && \
	    sed "s|^Exec=.*|Exec=$(PREFIX)/bin/$(BINARY_NAME)|" $(DESKTOP_SRC) > $$tmp && \
	    install -m 644 $$tmp $(DEST_DESKTOP)/$(DESKTOP_ID).desktop && \
	    rm -f $$tmp
	@for size in $(ICON_SIZES); do \
	    dir=$(DEST_HICOLOR)/$${size}x$${size}/apps; \
	    install -d $$dir; \
	    install -m 644 $(ICON_BUILD)/$$size.png $$dir/$(DESKTOP_ID).png; \
	done
ifeq ($(DESTDIR),)
	-update-desktop-database $(DEST_DESKTOP) 2>/dev/null
	-gtk-update-icon-cache -q -t $(HICOLOR_BASE) 2>/dev/null
endif
endif

.PHONY: uninstall
uninstall: uninstall-desktop
	rm -f $(DEST_BIN)/$(BINARY_NAME)

.PHONY: uninstall-desktop
uninstall-desktop:
ifeq ($(IS_XDG),)
	@echo "uninstall-desktop: skipping — no XDG desktop entry support on $(UNAME_S)"
else
	rm -f $(DEST_DESKTOP)/$(DESKTOP_ID).desktop
	rm -f $(DEST_DESKTOP)/a2text.desktop
	@for size in $(ICON_SIZES); do \
	    rm -f $(DEST_HICOLOR)/$${size}x$${size}/apps/$(DESKTOP_ID).png; \
	done
ifeq ($(DESTDIR),)
	-update-desktop-database $(DEST_DESKTOP) 2>/dev/null
	-gtk-update-icon-cache -q -t $(HICOLOR_BASE) 2>/dev/null
endif
endif

# --- Install (forced layout) ---
#
# install-system / install-user mirror install/uninstall but pin the
# layout regardless of the caller's UID. Useful when:
#   - running as root but wanting to land in someone's $HOME (rare);
#   - running as a regular user but wanting to write to a system tree
#     via DESTDIR staging without the auto-detection triggering;
#   - documenting which target is being invoked in CI scripts.
#
# install-user never touches DESTDIR (per-user installs are not what
# packagers stage).

.PHONY: install-system
install-system:
	$(MAKE) install PREFIX=/usr/local

.PHONY: uninstall-system
uninstall-system:
	$(MAKE) uninstall PREFIX=/usr/local

.PHONY: install-user
install-user: build install-desktop-user
	install -d $(USER_BIN)
	install -m 755 $(BIN_DIR)/$(BINARY_NAME) $(USER_BIN)/$(BINARY_NAME)

.PHONY: install-desktop-user
install-desktop-user: $(ICON_FILES)
ifeq ($(IS_XDG),)
	@echo "install-desktop-user: skipping — no XDG desktop entry support on $(UNAME_S)"
else
	install -d $(USER_DESKTOP)
	@tmp=$$(mktemp) && \
	    sed "s|^Exec=.*|Exec=$(USER_BIN)/$(BINARY_NAME)|" $(DESKTOP_SRC) > $$tmp && \
	    install -m 644 $$tmp $(USER_DESKTOP)/$(DESKTOP_ID).desktop && \
	    rm -f $$tmp
	@for size in $(ICON_SIZES); do \
	    dir=$(USER_HICOLOR)/$${size}x$${size}/apps; \
	    install -d $$dir; \
	    install -m 644 $(ICON_BUILD)/$$size.png $$dir/$(DESKTOP_ID).png; \
	done
	-update-desktop-database $(USER_DESKTOP) 2>/dev/null
	-gtk-update-icon-cache -q -t $(USER_HICOLOR) 2>/dev/null
endif

.PHONY: uninstall-user
uninstall-user: uninstall-desktop-user
	rm -f $(USER_BIN)/$(BINARY_NAME)

.PHONY: uninstall-desktop-user
uninstall-desktop-user:
ifeq ($(IS_XDG),)
	@echo "uninstall-desktop-user: skipping — no XDG desktop entry support on $(UNAME_S)"
else
	rm -f $(USER_DESKTOP)/$(DESKTOP_ID).desktop
	rm -f $(USER_DESKTOP)/a2text.desktop
	@for size in $(ICON_SIZES); do \
	    rm -f $(USER_HICOLOR)/$${size}x$${size}/apps/$(DESKTOP_ID).png; \
	done
	-update-desktop-database $(USER_DESKTOP) 2>/dev/null
	-gtk-update-icon-cache -q -t $(USER_HICOLOR) 2>/dev/null
endif

# --- Clean ---
#
# Drops everything `make build` produces. whisper.cpp/build is left
# alone by default — rebuilding it costs ~10 minutes and the artefact
# never goes stale unless the submodule itself moves. Use `clean-all`
# to drop it too.

# go.mk already declares a `clean` target that wipes $(BUILD_DIR).
# Extend it via a double-colon-style hook instead of redefining the
# recipe — `clean-extra` is appended as a prerequisite of `clean` so
# `make clean` removes both go.mk's BUILD_DIR and our bin/ tree without
# a "redefinition of target" warning.
clean: clean-extra

.PHONY: clean-extra
clean-extra:
	rm -rf $(BIN_DIR)

.PHONY: clean-all
clean-all: clean
	rm -rf $(WHISPER_DIR)/build

# --- Codegen ---

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
