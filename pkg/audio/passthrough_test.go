package audio

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type PassthroughConverterSuite struct {
	suite.Suite

	tmpDir string
}

func TestPassthroughConverterSuite(t *testing.T) {
	suite.Run(t, new(PassthroughConverterSuite))
}

func (s *PassthroughConverterSuite) SetupTest() {
	s.tmpDir = s.T().TempDir()
}

func (s *PassthroughConverterSuite) TestToWAV_ReturnsInputUnchanged() {
	path := filepath.Join(s.tmpDir, "audio.ogg")
	s.Require().NoError(os.WriteFile(path, []byte("fake"), 0o600))

	c := NewPassthroughConverter()
	out, err := c.ToWAVFromFile(context.Background(), path)
	s.Require().NoError(err)
	s.Equal(path, out)
}

func (s *PassthroughConverterSuite) TestToWAV_FileNotFound_ReturnsError() {
	c := NewPassthroughConverter()
	out, err := c.ToWAVFromFile(context.Background(), "/nonexistent/audio.opus")
	s.Require().Error(err)
	s.Empty(out)
}

func (s *PassthroughConverterSuite) TestToWAV_IgnoresContextCancellation() {
	path := filepath.Join(s.tmpDir, "x.wav")
	s.Require().NoError(os.WriteFile(path, []byte("fake"), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := NewPassthroughConverter()
	out, err := c.ToWAVFromFile(ctx, path)
	s.Require().NoError(err)
	s.Equal(path, out)
}
