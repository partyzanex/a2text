package clipboard

import (
	"context"
	"time"
)

//go:generate go run go.uber.org/mock/mockgen@latest -package=clipboard -destination=runner_mocks_test.go -source=runner.go

// CopyRunner is the seam used to mock clipboard subprocess execution in tests.
// Implemented by execCopyRunner; tests inject via this interface.
type CopyRunner interface {
	LookPath(name string) (string, error)
	Run(ctx context.Context, name string, args []string, stdin []byte, timeout time.Duration) error
}

// PasteRunner is the seam used to mock autopaste subprocess execution in tests.
// Implemented by execPasteRunner; tests inject via this interface.
type PasteRunner interface {
	LookPath(name string) (string, error)
	Run(ctx context.Context, name string, args []string, timeout time.Duration) error
}
