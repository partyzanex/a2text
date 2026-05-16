//go:build !whisper

// Tests in this file assert the stub-mode behaviour of the whisper.cpp
// provider — specifically that BuildTranscriber returns an error whose
// message tells the operator to rebuild with `-tags whisper`. They only
// make sense when the binary is built WITHOUT the whisper tag (CGO path
// disabled, stt.WhisperTranscriber resolves to the stub). With the tag
// active, the real CGO transcriber is wired in and the error path goes
// through model-loading failures instead, so the assertion would no
// longer hold.

package factory_test

import (
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/factory"
)

func (s *BuildersSuite) TestBuildTranscriber_WhisperCpp_NotEnabledInStageI0() {
	cfg := s.validCfg(config.VoiceProviderWhisperCpp)

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().Error(err)
	s.Nil(tr)
	s.Contains(err.Error(), "-tags whisper")
}

func (s *BuildersSuite) TestBuildTranscriber_WhisperCpp_ErrorMessageNoStageReference() {
	cfg := s.validCfg(config.VoiceProviderWhisperCpp)

	_, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().Error(err)
	// Error must mention the build tag but must not carry stale stage references.
	s.Contains(err.Error(), "-tags whisper")
	s.NotContains(err.Error(), "stage I.0")
}
