// Package main is the entry point for the a2text voice CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	cmd "github.com/partyzanex/a2text/internal/infra/cmd"
)

func main() {
	err := cmd.NewCommand().Run(context.Background(), os.Args)
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
