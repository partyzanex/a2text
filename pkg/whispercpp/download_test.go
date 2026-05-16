package whispercpp_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/pkg/whispercpp"
)

// testPayload is the body the fake mirrors serve. The exact bytes don't
// matter for the downloader tests — magic-byte validation is the
// downstream concern.
var testPayload = strings.Repeat("ggml-bytes-", 8192) //nolint:gochecknoglobals // shared fixture across tests

func newOKServer(t *testing.T, body string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)

		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func newFailServer(t *testing.T, code int) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestDownload_HappyPath_HuggingFace(t *testing.T) {
	t.Parallel()

	srv := newOKServer(t, testPayload)

	dl := &whispercpp.Downloader{
		Sources: []whispercpp.Source{
			{Name: "test", URLFor: func(string) string { return srv.URL }},
		},
	}

	dir := t.TempDir()
	path, err := dl.Download(context.Background(), "ggml-tiny.bin", dir, nil)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "ggml-tiny.bin"), path)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, testPayload, string(got))
}

func TestDownload_FallbackOnFirstSourceFailure(t *testing.T) {
	t.Parallel()

	failing := newFailServer(t, http.StatusInternalServerError)
	ok := newOKServer(t, testPayload)

	dl := &whispercpp.Downloader{
		Sources: []whispercpp.Source{
			{Name: "primary", URLFor: func(string) string { return failing.URL }},
			{Name: "fallback", URLFor: func(string) string { return ok.URL }},
		},
	}

	dir := t.TempDir()
	path, err := dl.Download(context.Background(), "ggml-small.bin", dir, nil)
	require.NoError(t, err)

	stat, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, int64(len(testPayload)), stat.Size())
}

func TestDownload_AllSourcesFail(t *testing.T) {
	t.Parallel()

	srv1 := newFailServer(t, http.StatusNotFound)
	srv2 := newFailServer(t, http.StatusBadGateway)

	dl := &whispercpp.Downloader{
		Sources: []whispercpp.Source{
			{Name: "a", URLFor: func(string) string { return srv1.URL }},
			{Name: "b", URLFor: func(string) string { return srv2.URL }},
		},
	}

	_, err := dl.Download(context.Background(), "ggml-base.bin", t.TempDir(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "all mirrors failed")
}

func TestDownload_SkipsWhenAlreadyPresent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	existing := filepath.Join(dir, "ggml-base.bin")
	require.NoError(t, os.WriteFile(existing, []byte("already here"), 0o600))

	called := false
	dl := &whispercpp.Downloader{
		Sources: []whispercpp.Source{{
			Name: "should-not-run",
			URLFor: func(string) string {
				called = true

				return "http://127.0.0.1:0"
			},
		}},
	}

	path, err := dl.Download(context.Background(), "ggml-base.bin", dir, nil)
	require.NoError(t, err)
	require.Equal(t, existing, path)
	require.False(t, called, "Download must not consult Sources when target already exists")
}

func TestDownload_PartialIsCleanedUpOnFailure(t *testing.T) {
	t.Parallel()

	// Server starts streaming OK, then drops the connection mid-body so
	// our copy returns an error and we can assert .partial is gone.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("only-some-bytes"))

		// Force-close the conn to make the client read fail.
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}

		conn, _, hjErr := hj.Hijack()
		if hjErr != nil {
			return
		}

		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)

	dl := &whispercpp.Downloader{
		Sources: []whispercpp.Source{{Name: "flaky", URLFor: func(string) string { return srv.URL }}},
	}

	dir := t.TempDir()

	_, err := dl.Download(context.Background(), "ggml-medium.bin", dir, nil)
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(dir, "ggml-medium.bin"+".partial"))
	require.True(t, os.IsNotExist(statErr), "partial file must be removed on failure, got: %v", statErr)
}

func TestDownload_ProgressCallback(t *testing.T) {
	t.Parallel()

	// Use a body large enough to span several progress-tick windows.
	body := strings.Repeat("x", 256*1024)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		// Trickle the body so the throttled callback has reason to fire
		// more than once.
		for i := 0; i < len(body); i += 32 * 1024 {
			end := min(i+32*1024, len(body))

			_, _ = w.Write([]byte(body[i:end]))

			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}

			time.Sleep(80 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)

	dl := &whispercpp.Downloader{
		Sources: []whispercpp.Source{{Name: "trickle", URLFor: func(string) string { return srv.URL }}},
	}

	var (
		mu     sync.Mutex
		ticks  []whispercpp.Progress
		lastOk whispercpp.Progress
	)

	progressFn := func(p whispercpp.Progress) {
		mu.Lock()
		defer mu.Unlock()

		ticks = append(ticks, p)
		lastOk = p
	}

	dir := t.TempDir()

	_, err := dl.Download(context.Background(), "ggml-x.bin", dir, progressFn)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	require.NotEmpty(t, ticks)
	require.Equal(t, int64(len(body)), lastOk.Done, "final tick must report full size")
	require.Equal(t, int64(len(body)), lastOk.Total)
}

func TestDownload_RejectsEmptyArgs(t *testing.T) {
	t.Parallel()

	dl := &whispercpp.Downloader{}

	_, err := dl.Download(context.Background(), "", t.TempDir(), nil)
	require.Error(t, err)

	_, err = dl.Download(context.Background(), "ggml.bin", "", nil)
	require.Error(t, err)
}

func TestDownload_CtxCancelStopsBetweenSources(t *testing.T) {
	t.Parallel()

	fail := newFailServer(t, http.StatusServiceUnavailable)

	ctx, cancel := context.WithCancel(context.Background())

	dl := &whispercpp.Downloader{
		Sources: []whispercpp.Source{
			{Name: "1", URLFor: func(string) string {
				cancel()

				return fail.URL
			}},
			{Name: "2", URLFor: func(string) string {
				t.Errorf("second mirror must not be tried after ctx cancel")

				return fail.URL
			}},
		},
	}

	_, err := dl.Download(ctx, "ggml-x.bin", t.TempDir(), nil)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

func TestDefaultSources_BuildPlausibleURLs(t *testing.T) {
	t.Parallel()

	require.GreaterOrEqual(t, len(whispercpp.DefaultSources), 2, "production must have at least 2 mirrors for fallback")

	for _, src := range whispercpp.DefaultSources {
		url := src.URLFor("ggml-tiny.bin")
		require.True(t, strings.HasPrefix(url, "https://"), "%s URL must be https, got %q", src.Name, url)
		require.Contains(t, url, "ggml-tiny.bin")
	}
}
