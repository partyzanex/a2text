ifndef LOCAL_BIN
include go.mk
endif

go.mk:
	@tmpdir=$$(mktemp -d) && \
	git clone --depth 1 --single-branch https://github.com/partyzanex/go-makefile.git $$tmpdir && \
	cp $$tmpdir/go.mk $(CURDIR)/go.mk

# versions
PROTOC_GRPC_GATEWAY_VERSION := latest
PROTOC_GEN_GO_VERSION := latest
PROTOC_GEN_GO_GRPC_VERSION := latest
PROTOC_VERSION := 27.1
PROTOLINT_VERSION := latest

# paths
PROTOC_GRPC_GATEWAY_BIN=$(LOCAL_BIN)/protoc-gen-grpc-gateway
PROTOC_GEN_OPENAPIV2_BIN=$(LOCAL_BIN)/protoc-gen-openapiv2
PROTOC_GEN_GO_GRPC_BIN=$(LOCAL_BIN)/protoc-gen-go-grpc
PROTOC_GEN_GO_BIN=$(LOCAL_BIN)/protoc-gen-go
PROTOC_BIN=$(LOCAL_BIN)/protoc
PROTOLINT_BIN := $(LOCAL_BIN)/protolint

.PHONY: protoc-install protoc-grpc-gateway-install protoc-gen-openapiv2-install protoc-gen-go-grpc-install protoc-gen-go-install protolint-install

.PHONY: protolint-install
protolint-install: bin
	$(call go_build_install,github.com/yoheimuta/protolint/cmd/protolint,$(PROTOLINT_VERSION),$(PROTOLINT_BIN))

protoc-grpc-gateway-install: bin
	$(call go_build_install,github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway,$(PROTOC_GRPC_GATEWAY_VERSION),$(PROTOC_GRPC_GATEWAY_BIN))

protoc-gen-openapiv2-install: bin
	$(call go_build_install,github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2,$(PROTOC_GRPC_GATEWAY_VERSION),$(PROTOC_GEN_OPENAPIV2_BIN))

protoc-gen-go-grpc-install: bin
	$(call go_build_install,google.golang.org/grpc/cmd/protoc-gen-go-grpc,$(PROTOC_GEN_GO_GRPC_VERSION),$(PROTOC_GEN_GO_GRPC_BIN))

protoc-gen-go-install: bin
	$(call go_build_install,google.golang.org/protobuf/cmd/protoc-gen-go,$(PROTOC_GEN_GO_VERSION),$(PROTOC_GEN_GO_BIN))

protoc-install: protoc-grpc-gateway-install protoc-gen-openapiv2-install protoc-gen-go-grpc-install protoc-gen-go-install
ifeq ($(wildcard $(PROTOC_BIN)),)
ifeq ($(shell uname -s), Darwin)
	tmp=$$(mktemp -d) && zip=$$tmp/protoc.zip && \
	curl -sSL https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/protoc-$(PROTOC_VERSION)-osx-universal_binary.zip -o $$zip && \
	cd $$tmp && unzip $$zip &> /dev/null && cp $$tmp/bin/protoc $(PROTOC_BIN)
endif
ifeq ($(shell uname -s), Linux)
	curl -L https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/protoc-$(PROTOC_VERSION)-$(shell uname -s | sed 's/Darwin/osx/')-$(shell uname -m).zip | bsdtar -xvf- bin
endif
ifeq ($(OS),Windows_NT)
	tmp=$$(mktemp -d) && zip=$$tmp/protoc.zip && \
	curl -k -L https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/protoc-$(PROTOC_VERSION)-win64.zip -o $$zip && \
	unzip $$zip -d $$tmp &> /dev/null && ls $$tmp && cp $$tmp/bin/protoc.exe $(PROTOC_BIN)
endif
	@chmod +x $(PROTOC_BIN) && echo "protoc installed!"
endif

PROTO_DIR := $(CURDIR)/proto

.PHONY: proto-deps
proto-deps:
ifeq ($(wildcard $(PROTO_DIR)/google),)
# for osx
ifeq ($(shell uname -s), Darwin)
	@tmp=$$(mktemp -d) && zip=$$tmp/protoc.zip && \
	curl -k -L https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/protoc-$(PROTOC_VERSION)-win64.zip -o $$zip && \
	cd $$tmp && unzip $$zip &> /dev/null && mkdir -p $(PROTO_DIR) && cp -R $$tmp/include/google $(PROTO_DIR)
endif
# for linux
ifeq ($(shell uname -s), Linux)
	tmp=$$(mktemp -d) && zip=$$tmp/protoc.zip && echo "$$zip" && \
	curl -L https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/protoc-$(PROTOC_VERSION)-$(shell uname -s | sed 's/Darwin/osx/')-$(shell uname -m).zip -o $$zip && \
	cd $$tmp && cat $$zip | bsdtar -xvf- && mkdir -p $(PROTO_DIR) && cp -R $$tmp/include/google $(PROTO_DIR)
endif
	@mkdir -p $(PROTO_DIR)/google/api && \
 	for pf in annotations http; \
	do \
		curl -L -o $(PROTO_DIR)/google/api/$$pf.proto https://raw.githubusercontent.com/googleapis/googleapis/master/google/api/$$pf.proto; \
	done
endif

GENERATED_GO_PATH ?= $(CURDIR)/pkg/proto
EXCLUDE_DIRS ?= google
GO_VERSION ?= 1.24

.PHONY: proto-default
proto-default: protoc-install proto-deps
	@rm -rf $(GENERATED_GO_PATH) && mkdir -p $(GENERATED_GO_PATH) && \
	PATH=$(MAKE_PATH) $(PROTOC_BIN) \
		--proto_path=$(PROTO_DIR): \
		--go_opt=paths=source_relative \
		--go_out=$(GENERATED_GO_PATH) \
		--go-grpc_opt=paths=source_relative \
		--go-grpc_out=$(GENERATED_GO_PATH) \
		--grpc-gateway_opt=paths=source_relative \
		--grpc-gateway_out=$(GENERATED_GO_PATH) \
		$$(find $(PROTO_DIR) -not \( -path $(PROTO_DIR)/google -prune \) -iname "*.proto")
	rm -rf $(GENERATED_GO_PATH)/google
	@for dir in $(GENERATED_GO_PATH)/*/; do \
	  exclude=$$(basename $$dir); \
	  if [ -z "$(EXCLUDE_DIRS)" ] || ! echo "$(EXCLUDE_DIRS)" | grep -wq "$$exclude"; then \
	    echo "\033[0;36mgo mod init for $$exclude\033[0m"; \
	    cd $$dir && \
	  	go mod init && go mod tidy -go=$(GO_VERSION) && go mod vendor; \
	  else \
	    echo "\033[0;33mSkipping $$exclude\033[0m"; \
	  fi; \
	done

.PHONY: proto-lint
proto-lint: protolint-install
	@$(PROTOLINT_BIN) lint --config_path=$(CURDIR)/.protolint.yml $(PROTO_DIR) && \
	echo "lint done!"

.protolint.yml:
	@tmpdir=$$(mktemp -d) && \
	git clone --depth 1 --single-branch https://github.com/partyzanex/go-makefile.git $$tmpdir && \
	cp $$tmpdir/template.protolint.yml $(CURDIR)/.protolint.yml
