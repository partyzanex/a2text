package stt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// DeepgramDefaultBaseURL is the canonical base URL of the Deepgram REST API.
// Exposed for UI defaults so callers don't hard-code the same literal.
const DeepgramDefaultBaseURL = "https://api.deepgram.com"

// deepgramModelsTimeout caps the GET /v1/models probe. Short — UI uses it
// to populate a dropdown after the user pastes a key; nobody waits 60s for
// a list of options to appear.
const deepgramModelsTimeout = 10 * time.Second

// httpStatusClassReducer divides a status code into its class (2xx, 4xx, …).
// Named constant so gomnd doesn't complain about a literal `100`.
const httpStatusClassReducer = 100

// deepgramErrorBodyLimit caps how many bytes of a non-2xx response body we
// pre-read for the error message.
const deepgramErrorBodyLimit = 512

// deepgramModelsResponse mirrors the Deepgram /v1/models payload. Only the
// fields the UI needs are decoded; the API is allowed to grow without
// breaking us.
type deepgramModelsResponse struct {
	STT []struct {
		Name          string `json:"name"`
		CanonicalName string `json:"canonical_name"`
	} `json:"stt"`
}

// FetchDeepgramModels lists STT models available to the given API key.
// Returns canonical model names sorted alphabetically. baseURL may be empty
// — DeepgramDefaultBaseURL is used in that case.
//
// Returns an error on HTTP failure, non-2xx status, or malformed JSON.
// Callers should treat any error as "leave the current options alone".
func FetchDeepgramModels(ctx context.Context, apiKey, baseURL string) ([]string, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("deepgram: api key is empty")
	}

	endpoint, err := deepgramModelsEndpoint(baseURL)
	if err != nil {
		return nil, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, deepgramModelsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("deepgram: build models request: %w", err)
	}

	req.Header.Set("Authorization", "Token "+apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deepgram: models request failed: %w", err)
	}

	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			// Closing after a successful decode rarely matters; surface
			// only via logs (caller has no logger here, so we drop it).
			_ = closeErr
		}
	}()

	const httpStatusClassOK = 2

	if resp.StatusCode/httpStatusClassReducer != httpStatusClassOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, deepgramErrorBodyLimit))
		if readErr != nil {
			body = nil
		}

		return nil, fmt.Errorf("deepgram: models request returned %s: %s",
			resp.Status, strings.TrimSpace(string(body)))
	}

	var payload deepgramModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("deepgram: decode models response: %w", err)
	}

	return uniqueSortedCanonicalNames(payload), nil
}

// deepgramModelsEndpoint composes the absolute /v1/models URL on top of
// baseURL. Tolerates trailing slashes and a missing scheme on baseURL
// (defaults to https). Anything past the host (including /v1/listen left
// over from the transcription endpoint) is discarded — the models API
// lives at the API root and concatenating onto /v1/listen produces 404.
func deepgramModelsEndpoint(baseURL string) (string, error) {
	return deepgramAPIPath(baseURL, "/v1/models")
}

// deepgramAPIPath composes scheme://host + relPath, ignoring whatever path
// the caller has on baseURL. Tolerates a missing scheme (defaults to https).
func deepgramAPIPath(baseURL, relPath string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = DeepgramDefaultBaseURL
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("deepgram: invalid base url %q: %w", baseURL, err)
	}

	if parsed.Scheme == "" {
		parsed.Scheme = "https"
	}

	// Some hosts (no scheme + bare host) end up with Host="" and
	// Path=<host>; url.Parse is lossy that way. Re-parse with scheme to
	// recover Host.
	if parsed.Host == "" && parsed.Path != "" {
		parsed, err = url.Parse("https://" + baseURL)
		if err != nil {
			return "", fmt.Errorf("deepgram: invalid base url %q: %w", baseURL, err)
		}
	}

	return parsed.Scheme + "://" + parsed.Host + relPath, nil
}

// uniqueSortedCanonicalNames extracts canonical model names from the
// response, deduplicates them, and returns them in alphabetical order so
// the UI dropdown is stable across calls.
func uniqueSortedCanonicalNames(payload deepgramModelsResponse) []string {
	seen := make(map[string]struct{}, len(payload.STT))
	names := make([]string, 0, len(payload.STT))

	for i := range payload.STT {
		model := &payload.STT[i]

		name := strings.TrimSpace(model.CanonicalName)
		if name == "" {
			name = strings.TrimSpace(model.Name)
		}

		if name == "" {
			continue
		}

		if _, ok := seen[name]; ok {
			continue
		}

		seen[name] = struct{}{}

		names = append(names, name)
	}

	sort.Strings(names)

	return names
}
