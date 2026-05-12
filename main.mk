# Set the directory for local binaries relative to the current directory
LOCAL_BIN=$(CURDIR)/bin

# Define the path environment variable for make operations, including the local bin directory
MAKE_PATH=$(LOCAL_BIN):/bin:/usr/bin:/usr/local/bin

# Define a target to ensure the local binary directory exists
.PHONY: bin-default
bin-default:
	@mkdir -p $(LOCAL_BIN)

# Define a target to setup a standard .gitignore file by cloning a specific repository
.gitignore:
	@tmpdir=$$(mktemp -d) && \
	git clone --depth 1 --single-branch https://github.com/partyzanex/go-makefile.git $$tmpdir && \
	cp $$tmpdir/template.gitignore $(CURDIR)/.gitignore

# Define a pattern rule that associates any target with its default version
%: %-default
	@true
