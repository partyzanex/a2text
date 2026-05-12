# Conditionally include main.mk if LOCAL_BIN is not defined
ifndef LOCAL_BIN
include main.mk
endif

# main.mk download and setup block
main.mk:
	@tmpdir=$$(mktemp -d) && \
	git clone --depth 1 --single-branch https://github.com/partyzanex/go-makefile.git $$tmpdir && \
	cp $$tmpdir/main.mk $(CURDIR)/main.mk

# Define default versions for various tools
GOLANGCI_LINT_VERSION := latest
GOOSE_VERSION := latest
PG_WAIT_VERSION := latest
GRPCURL_VERSION := latest
MOCKGEN_VERSION := latest
CLI_CONFIG_GEN_VERSION := latest
OGEN_VERSION := latest

# Collect all version variables into one variable for easy management
VERSIONS := GOLANGCI_LINT_VERSION GOOSE_VERSION PG_WAIT_VERSION GRPCURL_VERSION MOCKGEN_VERSION CLI_CONFIG_GEN_VERSION OGEN_VERSION

# Check for version override provided via command line
ifneq (,$(findstring v,$(MAKECMDGOALS)))
    # Extract the specified version
    NEW_VERSION := $(filter v%,$(MAKECMDGOALS))
    # Set all version variables to the new version if provided
    ifneq (,$(NEW_VERSION))
        $(foreach var,$(VERSIONS),$(eval $(var) := $(NEW_VERSION)))
    endif
endif

# Create a pattern rule for version goals that do nothing but allow the make to succeed
v%:
	@true

# Define binaries' paths
GOLANGCI_LINT_BIN :=$(LOCAL_BIN)/golangci-lint
GOOSE_BIN :=$(LOCAL_BIN)/goose
PG_WAIT_BIN :=$(LOCAL_BIN)/pg-wait
GRPCURL_BIN :=$(LOCAL_BIN)/grpcurl
MOCKGEN_BIN :=$(LOCAL_BIN)/mockgen
CLI_CONFIG_GEN_BIN :=$(LOCAL_BIN)/cli-config-gen
OGEN_BIN :=$(LOCAL_BIN)/ogen

CGO_ENABLED := 0

# Default test target
.PHONY: test-default
test-default:
	@CGO_ENABLED=$(CGO_ENABLED) go test -v -count=1 -race ./...

# Target to install golangci-lint
.PHONY: golangci-lint-install
golangci-lint-install:
	$(call go_build_install,github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION),$(GOLANGCI_LINT_BIN))

# Target to run linting
.PHONY: lint-default
lint-default: golangci-lint-install
	@$(GOLANGCI_LINT_BIN) run

# Target to install goose
.PHONY: goose-install
goose-install:
	$(call go_build_install,github.com/pressly/goose/v3/cmd/goose,$(GOOSE_VERSION),$(GOOSE_BIN))

# Target to install pg-wait
.PHONY: pg-wait-install
pg-wait-install:
	$(call go_build_install,github.com/partyzanex/pg-wait/cmd/pg-wait,$(PG_WAIT_VERSION),$(PG_WAIT_BIN))

# Target to wait for PostgreSQL readiness
.PHONY: pg-wait
pg-wait: pg-wait-install
ifdef POSTGRES_DSN
	@$(PG_WAIT_BIN) -d $(POSTGRES_DSN) -v
else
	@echo "POSTGRES_DSN is undefined!"
endif

# Targets to run goose migrations up and down
.PHONY: goose-up
goose-up: goose-install
ifdef POSTGRES_DSN
	@$(GOOSE_BIN) -dir $(CURDIR)/migrations postgres $(POSTGRES_DSN) up
else
	@echo "POSTGRES_DSN is undefined!"
endif

.PHONY: goose-down
goose-down: goose-install
ifdef POSTGRES_DSN
	@$(GOOSE_BIN) -dir $(CURDIR)/migrations postgres $(POSTGRES_DSN) down
else
	@echo "POSTGRES_DSN is undefined!"
endif

# Define migrations path
MIGRATIONS_PATH := $(CURDIR)/migrations

# Target to create a new goose migration
.PHONY: goose-migration
goose-migration: goose-install
ifeq ($(wildcard $(MIGRATIONS_PATH)),)
	@mkdir -p $(MIGRATIONS_PATH)
endif
	@read -p "Enter migration name: " migration_name; \
	$(GOOSE_BIN) -dir $(MIGRATIONS_PATH) create $$migration_name sql

# Target to install grpcurl
.PHONY: grpcurl-install
grpcurl-install:
	$(call go_build_install,github.com/fullstorydev/grpcurl/cmd/grpcurl,$(GRPCURL_VERSION),$(GRPCURL_BIN))

# Target to install mockgen
.PHONY: mockgen-install
mockgen-install:
	$(call go_build_install,github.com/golang/mock/mockgen,$(MOCKGEN_VERSION),$(MOCKGEN_BIN))

# Target to install cli-config-gen
.PHONY: cli-config-gen-install
cli-config-gen-install:
	$(call go_build_install,github.com/partyzanex/cli-config-gen/cmd/cli-config-gen,$(CLI_CONFIG_GEN_VERSION),$(CLI_CONFIG_GEN_BIN))

# Target to install ogen
.PHONY: ogen-install
ogen-install:
	$(call go_build_install,github.com/ogen-go/ogen/cmd/ogen,$(OGEN_VERSION),$(OGEN_BIN))

# Target for default code generation
.PHONY: gen-default
gen-default: mockgen-install
	@PATH=$(CURDIR)/bin:${PATH} go generate ./...

# Alias for code generation
.PHONY: mockgen-default
mockgen-default: gen

# Define default build and clean targets
.PHONY: build-default clean

# Define build directory, command directory, and build exclusions
BUILD_DIR := $(CURDIR)/bin
CMD_DIR := $(CURDIR)/cmd
EXCLUDE_BUILD ?=

# Target to build applications
build-default: gen
	@APP_VERSION=$$(git rev-parse --short HEAD) && \
	for dir in $(CMD_DIR)/*/; do \
		APP_NAME=$$(basename $$dir) && \
		if [ -z "$(EXCLUDE_BUILD)" ] || ! echo "$(EXCLUDE_BUILD)" | grep -wq "$$APP_NAME"; then \
			echo "\033[0;36mBuilding $$APP_NAME@$$APP_VERSION\033[0m" && \
			rm -f $(BUILD_DIR)/$$APP_NAME && \
			go build -o $(BUILD_DIR)/$$APP_NAME $$dir; \
		else \
			echo "\033[0;33mSkipping $$APP_NAME\033[0m"; \
		fi; \
	done

# Target to clean build artifacts
clean:
	@echo "\033[0;31mCleaning up...\033[0m" && \
	rm -f $(BUILD_DIR)/*

# go_build_install installs a binary from a Golang module with version control.
# Parameters:
# 1 - Module URI for building
# 2 - Module version (semver format or 'latest'), default 'latest'
# 3 - Full path to install the binary
# 4 - Build flags (optional)
# This function manages multiple versions for different services, using isolated builds in a temporary directory.
# Example of usage:
#   $(call go_build_install,github.com/example/module,latest,/usr/local/bin/module_binary,-tags=prod)
define go_build_install
	@command -v go >/dev/null 2>&1 || { echo "\033[0;31mERROR: Go runtime is not installed.\033[0m"; exit 1; }
	@command -v mktemp >/dev/null 2>&1 || { echo "\033[0;31mERROR: Unable to find mktemp tool.\033[0m"; exit 1; }
	@module_path=$(1); \
	binary_path=$(3); \
	build_flags=$(4); \
	version=$(2); \
	if [ -z "$$version" ]; then \
		version=latest; \
	fi; \
	if [ -f "$$binary_path@$$version" ]; then \
		if [ ! -e "$$binary_path" ] || [ ! -f "$$binary_path" ]; then \
			echo "\033[0;33mFIX: Broken symlink detected, relinking $$binary_path -> $$binary_path@$$version.\033[0m"; \
			ln -sf "$$binary_path@$$version" "$$binary_path"; \
		else \
			echo "\033[0;33mSKIP: Binary $$binary_path@$$version already installed.\033[0m"; \
		fi; \
		exit 0; \
	fi; \
	echo "\033[0;36mSTART: Installing $$module_path@$$version to $$binary_path.\033[0m"; \
	{ \
		set -e; \
		tmp_dir=$$(mktemp -d); \
		trap 'rm -rf "$$tmp_dir"' EXIT; \
		cd "$$tmp_dir"; \
		echo "\033[0;34mBUILD: Setting up build environment.\033[0m"; \
		go mod init temp; \
		go get "$$module_path@$$version"; \
		go build $$build_flags -o "$$binary_path@$$version" "$$module_path"; \
		ln -sf "$$binary_path@$$version" "$$binary_path"; \
		echo "\033[0;32mCOMPLETE: $$binary_path is now linked to $$binary_path@$$version.\033[0m"; \
		echo "\033[0;35m==========================================\033[0m"; \
	}
endef

# Define .golangci.yml template for linting with golangci-lint
.golangci.yml:
	@tmpdir=$$(mktemp -d) && \
	git clone --depth 1 --single-branch https://github.com/partyzanex/go-makefile.git $$tmpdir && \
	cp $$tmpdir/template.golangci.v2.yml $(CURDIR)/.golangci.yml
