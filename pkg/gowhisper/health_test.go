package gowhisper_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/pkg/gowhisper"
)

func TestCheck_Success_FlatModelArray(t *testing.T) {
	t.Parallel()

	const body = `[{"id":"ggml-small","name":"small"},{"id":"ggml-base","name":"base"}]`

	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	res, err := gowhisper.Check(context.Background(), srv.URL+"/api/whisper", time.Second)
	require.NoError(t, err)
	require.Equal(t, "/api/whisper/model", gotPath)
	require.Equal(t, []string{"ggml-small", "ggml-base"}, res.Models)
	require.Contains(t, res.Status, "200")
	require.Positive(t, int64(res.Elapsed))
}

func TestCheck_Success_WrappedModelsObject(t *testing.T) {
	t.Parallel()

	body := `{"models":[{"id":"ggml-large-v3","name":"large"}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	res, err := gowhisper.Check(context.Background(), srv.URL, time.Second)
	require.NoError(t, err)
	require.Equal(t, []string{"ggml-large-v3"}, res.Models)
}

func TestCheck_NonOKStatusFails(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, err := gowhisper.Check(context.Background(), srv.URL, time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

func TestCheck_UnreachableHostFails(t *testing.T) {
	t.Parallel()

	// 127.0.0.1:1 is reserved for tcpmux. Almost never listening; if it
	// is on a particular CI runner the test still serves the negative-
	// path goal: any non-200 fails the check.
	_, err := gowhisper.Check(context.Background(), "http://127.0.0.1:1", 200*time.Millisecond)
	require.Error(t, err)
}

func TestCheck_EmptyURLRejected(t *testing.T) {
	t.Parallel()

	_, err := gowhisper.Check(context.Background(), "", time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty")
}

func TestCheck_AcceptsTrailingSlashInBaseURL(t *testing.T) {
	t.Parallel()

	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path

		_, _ = io.WriteString(w, "[]")
	}))
	t.Cleanup(srv.Close)

	_, err := gowhisper.Check(context.Background(), srv.URL+"/api/whisper/", time.Second)
	require.NoError(t, err)
	require.NotContains(t, gotPath, "//")
	require.True(t, strings.HasSuffix(gotPath, "/model"))
}

func TestCheck_TimeoutPropagates(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than the client's timeout to force a deadline.
		time.Sleep(300 * time.Millisecond)

		_, _ = io.WriteString(w, "[]")
	}))
	t.Cleanup(srv.Close)

	_, err := gowhisper.Check(context.Background(), srv.URL, 50*time.Millisecond)
	require.Error(t, err)
}
