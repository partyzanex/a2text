package factory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/internal/infra/factory"
)

func TestLazyErrorTranscriber_AllMethodsReturnCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("ggml model not found")
	stub := factory.NewLazyErrorTranscriber(cause)

	require.ErrorIs(t, errFromTranscribe(stub), cause)
	require.ErrorIs(t, stub.LoadModel("/x"), cause)
	require.ErrorIs(t, stub.ReloadModel("/y"), cause)

	_, err := stub.DetectLanguage(context.Background(), "/z")
	require.ErrorIs(t, err, cause)
}

func TestLazyErrorTranscriber_CloseIsNoOp(t *testing.T) {
	t.Parallel()

	stub := factory.NewLazyErrorTranscriber(errors.New("ignored"))
	require.NoError(t, stub.Close())
}

func TestLazyErrorTranscriber_NilCauseSubstitutesPlaceholder(t *testing.T) {
	t.Parallel()

	stub := factory.NewLazyErrorTranscriber(nil)
	require.Error(t, stub.Cause())
	require.ErrorContains(t, stub.Cause(), "not configured")
}

func TestLazyErrorTranscriber_CauseUnchanged(t *testing.T) {
	t.Parallel()

	orig := errors.New("boom")
	stub := factory.NewLazyErrorTranscriber(orig)

	require.Same(t, orig, stub.Cause(),
		"Cause must return the exact error passed in — wrapping happens only in the method results")
}

func errFromTranscribe(stub *factory.LazyErrorTranscriber) error {
	_, err := stub.Transcribe(context.Background(), "/path.wav", "ru")

	return err
}
