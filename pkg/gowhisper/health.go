// Package gowhisper holds small helpers for talking to a remote
// go-whisper HTTP service. Today it only exposes a connection check
// used by the settings UI; transcription itself still goes through
// pkg/stt because that path predates this package.
package gowhisper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// healthEndpoint is the relative path go-whisper exposes that returns
// the list of installed models. It is the closest thing the service
// has to a /health endpoint and doubles as proof that the URL prefix
// (e.g. /api/whisper) is correctly configured.
const healthEndpoint = "/model"

// CheckResult is what a successful health probe reports.
type CheckResult struct {
	// Status is the HTTP status text the server replied with (e.g.
	// "200 OK"). Useful when the server is reachable but returns an
	// unexpected code.
	Status string

	// Models is the list of model IDs the server advertises. Empty
	// when the server returned an unparseable body.
	Models []string

	// Elapsed is how long the round-trip took.
	Elapsed time.Duration
}

// modelEntry mirrors a single element of go-whisper's /model
// response. Only the id is decoded; other fields are ignored.
type modelEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Check performs a GET against <baseURL>/model with the given timeout
// and returns the parsed result. Any non-2xx response, broken JSON,
// dial failure, or context cancellation maps to a non-nil error — the
// caller decides how to surface it.
func Check(ctx context.Context, baseURL string, timeout time.Duration) (CheckResult, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return CheckResult{}, errors.New("gowhisper: baseURL is empty")
	}

	url := strings.TrimRight(baseURL, "/") + healthEndpoint

	if timeout <= 0 {
		const defaultTimeout = 5 * time.Second

		timeout = defaultTimeout
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return CheckResult{}, fmt.Errorf("gowhisper: build request: %w", err)
	}

	client := &http.Client{Timeout: timeout}

	started := time.Now()

	resp, err := client.Do(req)
	if err != nil {
		return CheckResult{}, fmt.Errorf("gowhisper: connect %s: %w", url, err)
	}

	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			// Best-effort cleanup: the probe result already captured
			// what the user cares about; failing to close is a
			// soft-fail.
			_ = closeErr
		}
	}()

	elapsed := time.Since(started)

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return CheckResult{}, fmt.Errorf("gowhisper: unexpected status %s", resp.Status)
	}

	const maxBodyBytes = 64 * 1024

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return CheckResult{}, fmt.Errorf("gowhisper: read body: %w", err)
	}

	models := parseModelList(body)

	return CheckResult{
		Status:  resp.Status,
		Models:  models,
		Elapsed: elapsed,
	}, nil
}

// parseModelList accepts both shapes go-whisper has been observed to
// return: a flat JSON array of {id, name} entries, and a wrapper
// object with a "models" field. Best-effort — an empty list on parse
// failure is fine because the caller already knows the HTTP probe
// succeeded.
func parseModelList(body []byte) []string {
	var flat []modelEntry
	if err := json.Unmarshal(body, &flat); err == nil {
		return idsOf(flat)
	}

	var wrapped struct {
		Models []modelEntry `json:"models"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil {
		return idsOf(wrapped.Models)
	}

	return nil
}

func idsOf(entries []modelEntry) []string {
	ids := make([]string, 0, len(entries))
	for i := range entries {
		if entries[i].ID != "" {
			ids = append(ids, entries[i].ID)
		}
	}

	return ids
}
