// Package whispercpp downloads GGML model files used by the whisper.cpp
// STT provider. The package is UI-agnostic — it reports progress via a
// callback and lets the caller render however it wants (CLI bar, Fyne
// progress widget, …).
//
// Sources are tried in order; the first reachable mirror wins. The
// download writes to a "<dest>.partial" temporary file and atomic-renames
// to its final location on success, so a partially-downloaded model can
// never be picked up as if it were complete.
package whispercpp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// defaultRequestTimeout caps a single GET. Whisper models are 75MB–3GB,
	// so a generous timeout is needed; callers can override via Downloader.HTTPClient.
	defaultRequestTimeout = 30 * time.Minute

	// progressInterval throttles ProgressFunc invocations so a high-bandwidth
	// download does not call the callback millions of times. The callback is
	// always invoked once with Done == Total at the end regardless.
	progressInterval = 200 * time.Millisecond

	partialSuffix = ".partial"
)

// Progress reports download progress. Total is -1 when the server does
// not send Content-Length (rare for HuggingFace/GitHub mirrors but
// possible for arbitrary hosts).
type Progress struct {
	Source string
	Done   int64
	Total  int64
}

// ProgressFunc receives Progress updates. Safe to be nil — callers
// uninterested in progress can pass nil rather than a no-op stub.
type ProgressFunc func(Progress)

// Source describes one mirror that knows how to serve a model by its
// canonical filename (e.g. "ggml-small.bin"). URL builders take the file
// name and return an absolute http(s) URL.
type Source struct {
	Name   string
	URLFor func(modelFile string) string
}

// DefaultSources is the production mirror list. HuggingFace is primary;
// ggml.ggerganov.com is the same fallback the upstream
// whisper.cpp/models/download-ggml-model.sh script uses.
//
//nolint:gochecknoglobals // immutable mirror list; the conventional way to surface "const" tables in Go.
var DefaultSources = []Source{
	{
		Name: "huggingface",
		URLFor: func(modelFile string) string {
			return "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/" + modelFile
		},
	},
	{
		Name: "ggml.ggerganov.com",
		URLFor: func(modelFile string) string {
			return "https://ggml.ggerganov.com/" + modelFile
		},
	},
}

// Downloader is the package's main entry point. The zero value is
// usable: it picks DefaultSources and a default http.Client.
type Downloader struct {
	HTTPClient *http.Client
	Sources    []Source
}

// Download fetches modelFile (e.g. "ggml-small.bin") into destDir,
// trying each configured source in order. Returns the absolute path of
// the saved file. The function is idempotent: if destDir/modelFile
// already exists and is non-empty, it returns immediately without
// touching the network.
//
// progressFn (optional) is invoked at ~5Hz with cumulative byte counts.
// It runs on the goroutine that calls Download; UI callers must marshal
// to their UI thread inside the callback.
//
// Cancel-safe via ctx: the partial file is left behind so a subsequent
// call can pick up where this one stopped (the loop overwrites the
// .partial; resume across runs is not implemented but is straightforward
// to add later — keeping the API surface small for now).
func (d *Downloader) Download(
	ctx context.Context,
	modelFile string,
	destDir string,
	progressFn ProgressFunc,
) (string, error) {
	if err := validateDownloadArgs(modelFile, destDir); err != nil {
		return "", err
	}

	const modelsDirMode = 0o750

	if err := os.MkdirAll(destDir, modelsDirMode); err != nil {
		return "", fmt.Errorf("whispercpp: mkdir %q: %w", destDir, err)
	}

	finalPath := filepath.Join(destDir, modelFile)
	if info, err := os.Stat(finalPath); err == nil && info.Size() > 0 {
		return finalPath, nil
	}

	sources := d.Sources
	if len(sources) == 0 {
		sources = DefaultSources
	}

	client := d.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultRequestTimeout}
	}

	return tryAllSources(ctx, client, sources, modelFile, finalPath, progressFn)
}

func validateDownloadArgs(modelFile, destDir string) error {
	if modelFile == "" {
		return errors.New("whispercpp: modelFile is empty")
	}

	if destDir == "" {
		return errors.New("whispercpp: destDir is empty")
	}

	return nil
}

func tryAllSources(
	ctx context.Context,
	client *http.Client,
	sources []Source,
	modelFile, finalPath string,
	progressFn ProgressFunc,
) (string, error) {
	var lastErr error

	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("whispercpp: %w", err)
		}

		url := source.URLFor(modelFile)
		if err := fetchOne(ctx, client, source.Name, url, finalPath, progressFn); err != nil {
			lastErr = fmt.Errorf("source %s: %w", source.Name, err)

			continue
		}

		return finalPath, nil
	}

	if lastErr == nil {
		lastErr = errors.New("no sources configured")
	}

	return "", fmt.Errorf("whispercpp: all mirrors failed: %w", lastErr)
}

// fetchOne performs the actual GET-download-rename dance for one mirror.
// Splits writes into a sibling .partial file so the final path never
// holds half-written data.
func fetchOne(
	ctx context.Context,
	client *http.Client,
	sourceName, url, finalPath string,
	progressFn ProgressFunc,
) (retErr error) {
	resp, err := openHTTP(ctx, client, url)
	if err != nil {
		return err
	}

	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close response: %w", closeErr)
		}
	}()

	partialPath := finalPath + partialSuffix

	if err := writePartial(resp, partialPath, sourceName, progressFn); err != nil {
		removePartial(partialPath)

		return err
	}

	if err := os.Rename(partialPath, finalPath); err != nil {
		removePartial(partialPath)

		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// openHTTP issues the GET and returns the response. Caller owns the
// response body and must close it.
func openHTTP(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("status %s", resp.Status)
		if closeErr := resp.Body.Close(); closeErr != nil {
			return nil, errors.Join(statusErr, fmt.Errorf("close response: %w", closeErr))
		}

		return nil, statusErr
	}

	return resp, nil
}

// writePartial streams the response body into partialPath, closing the
// file before returning. On error the file may still exist — the caller
// must remove it.
func writePartial(
	resp *http.Response,
	partialPath, sourceName string,
	progressFn ProgressFunc,
) (retErr error) {
	out, err := os.Create(filepath.Clean(partialPath))
	if err != nil {
		return fmt.Errorf("create partial: %w", err)
	}

	defer func() {
		if closeErr := out.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close partial: %w", closeErr)
		}
	}()

	if err := copyWithProgress(resp.Body, out, sourceName, resp.ContentLength, progressFn); err != nil {
		return err
	}

	return nil
}

// removePartial best-effort deletes a partial file, ignoring "doesn't
// exist" which is normal when a download failed before file creation.
func removePartial(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		// Caller already has a more interesting error in flight; the
		// stale partial is harmless and will be overwritten on retry.
		_ = err
	}
}

// copyWithProgress is io.Copy with throttled callbacks. Always emits a
// final Done=Total tick when the body ends, so UI bars can finish at
// 100% even when total bytes were just under progressInterval.
func copyWithProgress(
	src io.Reader,
	dst io.Writer,
	sourceName string,
	total int64,
	progressFn ProgressFunc,
) error {
	const bufSize = 64 * 1024

	buf := make([]byte, bufSize)

	var done int64

	lastTick := time.Now()

	for {
		nRead, readErr := src.Read(buf)
		if nRead > 0 {
			nWritten, writeErr := dst.Write(buf[:nRead])
			if writeErr != nil {
				return fmt.Errorf("write: %w", writeErr)
			}

			done += int64(nWritten)

			if progressFn != nil && time.Since(lastTick) >= progressInterval {
				progressFn(Progress{Source: sourceName, Done: done, Total: total})

				lastTick = time.Now()
			}
		}

		if errors.Is(readErr, io.EOF) {
			break
		}

		if readErr != nil {
			return fmt.Errorf("read: %w", readErr)
		}
	}

	if progressFn != nil {
		finalTotal := total
		if finalTotal < 0 {
			finalTotal = done
		}

		progressFn(Progress{Source: sourceName, Done: done, Total: finalTotal})
	}

	return nil
}
