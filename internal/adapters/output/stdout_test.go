package output_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/internal/adapters/output"
)

type StdoutSuite struct {
	suite.Suite
}

func TestStdoutSuite(t *testing.T) {
	suite.Run(t, new(StdoutSuite))
}

func (s *StdoutSuite) TestDeliver_WritesTextWithNewline() {
	var buf bytes.Buffer

	out := output.NewStdoutOutputWithWriter(&buf)

	s.Require().NoError(out.Deliver(context.Background(), "hello world"))
	s.Equal("hello world\n", buf.String())
}

func (s *StdoutSuite) TestDeliver_EmptyTextStillWritesNewline() {
	var buf bytes.Buffer

	out := output.NewStdoutOutputWithWriter(&buf)

	s.Require().NoError(out.Deliver(context.Background(), ""))
	s.Equal("\n", buf.String())
}

func (s *StdoutSuite) TestDeliver_CancelledContextReturnsCtxErr() {
	var buf bytes.Buffer

	out := output.NewStdoutOutputWithWriter(&buf)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := out.Deliver(ctx, "ignored")
	s.Require().Error(err)
	s.Require().ErrorIs(err, context.Canceled)
	s.Empty(buf.String(), "writer must not receive input on cancelled ctx")
}

type errWriter struct{ err error }

func (w *errWriter) Write(_ []byte) (int, error) { return 0, w.err }

func (s *StdoutSuite) TestDeliver_WriterError_Propagates() {
	want := errors.New("disk full")
	out := output.NewStdoutOutputWithWriter(&errWriter{err: want})

	err := out.Deliver(context.Background(), "x")
	s.Require().Error(err)
	s.Require().ErrorIs(err, want)
}
