package cmd

import (
	"log/slog"
	"os"
)

// Version and Commit are set at build time via -ldflags:
//
//	go build -ldflags "-X github.com/partyzanex/a2text/internal/infra/cmd.Version=v2.0.0
//	  -X github.com/partyzanex/a2text/internal/infra/cmd.Commit=abc1234"
//
//nolint:gochecknoglobals // set via ldflags at build time
var (
	Version = "dev"
	Commit  = "unknown"
)

func CreateLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: lvl,
	})

	logger := slog.New(handler).With(
		slog.String("version", Version),
		slog.String("commit", Commit),
	)
	slog.SetDefault(logger)

	return logger
}
