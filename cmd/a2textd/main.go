// Package main is the entry point for the a2textd voice dictation daemon.
//
// a2textd is the system-side process introduced for the split-user
// architecture: it owns evdev hotkey reading, audio capture, STT,
// uinput autopaste, and the provider-credentials store. It exposes a
// gRPC + mTLS control plane on the loopback interface for the
// user-side `a2text` UI binary. This binary intentionally has no GUI,
// no tray, and no dependency on a graphical session.
//
// Architecture decisions live in docs/adr (ADR-0001 split-user binary
// split, ADR-0002 loopback gRPC/mTLS control plane). Plain HTTP and
// Unix-domain sockets are explicitly out of scope.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	clid "github.com/partyzanex/a2text/internal/infra/clid"
)

func main() {
	err := clid.NewCommand().Run(context.Background(), os.Args)
	if err == nil {
		os.Exit(0)
	}

	var exitErr cli.ExitCoder
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}

	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
