package stt

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

// deepgramUsageTimeout caps the projects/balances probes. UI uses the
// result to populate a one-line caption — nobody waits 30s for that.
const deepgramUsageTimeout = 10 * time.Second

// deepgramBalanceUnit* are the canonical unit strings Deepgram returns on
// balance entries. Switch arms in formatOneBalance use them; tests assert
// against the same constants.
const (
	deepgramBalanceUnitUSD   = "usd"
	deepgramBalanceUnitHour  = "hour"
	deepgramBalanceUnitHours = "hours"
)

// ErrDeepgramInsufficientScope indicates the API key lacks billing:read
// (or whichever scope the endpoint required). Callers can render a
// targeted hint instead of a generic "unavailable" caption.
var ErrDeepgramInsufficientScope = errors.New("deepgram: api key lacks required scope")

// DeepgramBalance is a single credit bucket on a Deepgram project. Plans
// without prepaid credits return zero balances; UI callers should treat
// an empty slice as "no balance reporting available on this plan".
type DeepgramBalance struct {
	// Amount is the remaining credit in Units. May be a fractional value
	// (USD) or a count (hours, depending on plan).
	Amount float64 `json:"amount"`

	// Units is the unit of Amount: "usd", "hour", etc. Lowercase.
	Units string `json:"units"`
}

type deepgramProjectsResponse struct {
	Projects []struct {
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
	} `json:"projects"`
}

type deepgramBalancesResponse struct {
	Balances []DeepgramBalance `json:"balances"`
}

// FetchDeepgramBalance discovers the first project owned by the API key
// and returns its remaining balances. Two round trips: GET /v1/projects
// → GET /v1/projects/{id}/balances. baseURL may be empty —
// DeepgramDefaultBaseURL is used.
//
// Errors are surfaced verbatim; callers should treat any error as
// "balance unavailable" and hide the UI element rather than alarm the user.
// A 403 typically means the key lacks usage:read scope; this is a config
// issue on Deepgram's side, not a code bug.
func FetchDeepgramBalance(ctx context.Context, apiKey, baseURL string) ([]DeepgramBalance, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("deepgram: api key is empty")
	}

	probeCtx, cancel := context.WithTimeout(ctx, deepgramUsageTimeout)
	defer cancel()

	projectID, err := fetchDeepgramFirstProject(probeCtx, apiKey, baseURL)
	if err != nil {
		return nil, err
	}

	return fetchDeepgramBalances(probeCtx, apiKey, baseURL, projectID)
}

// fetchDeepgramFirstProject lists projects and returns the first one. Most
// users have a single project; for multi-project keys we pick the first
// alphabetically by API order (Deepgram returns by creation date).
func fetchDeepgramFirstProject(ctx context.Context, apiKey, baseURL string) (string, error) {
	endpoint, err := deepgramAPIPath(baseURL, "/v1/projects")
	if err != nil {
		return "", err
	}

	var payload deepgramProjectsResponse
	if err := doDeepgramGET(ctx, endpoint, apiKey, &payload); err != nil {
		return "", err
	}

	if len(payload.Projects) == 0 {
		return "", errors.New("deepgram: no projects visible to this api key")
	}

	return payload.Projects[0].ProjectID, nil
}

// fetchDeepgramBalances pulls the balances list for a specific project.
func fetchDeepgramBalances(
	ctx context.Context, apiKey, baseURL, projectID string,
) ([]DeepgramBalance, error) {
	endpoint, err := deepgramAPIPath(baseURL, "/v1/projects/"+projectID+"/balances")
	if err != nil {
		return nil, err
	}

	var payload deepgramBalancesResponse
	if err := doDeepgramGET(ctx, endpoint, apiKey, &payload); err != nil {
		return nil, err
	}

	return payload.Balances, nil
}

// FormatDeepgramBalances renders a slice of balances into a one-line
// human caption suitable for a UI label. Empty slice yields an empty
// string so callers can hide the field.
func FormatDeepgramBalances(balances []DeepgramBalance) string {
	if len(balances) == 0 {
		return ""
	}

	parts := make([]string, 0, len(balances))

	for i := range balances {
		bal := &balances[i]
		parts = append(parts, formatOneBalance(bal))
	}

	return strings.Join(parts, ", ")
}

func formatOneBalance(bal *DeepgramBalance) string {
	unit := strings.ToLower(strings.TrimSpace(bal.Units))

	switch unit {
	case deepgramBalanceUnitUSD:
		return fmt.Sprintf("$%.2f", bal.Amount)
	case deepgramBalanceUnitHour, deepgramBalanceUnitHours:
		return fmt.Sprintf("%.2f h", bal.Amount)
	default:
		if unit == "" {
			return fmt.Sprintf("%.2f", bal.Amount)
		}

		return fmt.Sprintf("%.2f %s", bal.Amount, unit)
	}
}

// isInsufficientPermissions reports whether body is a Deepgram error
// payload with category == INSUFFICIENT_PERMISSIONS. Tolerates malformed
// JSON — returns false so callers fall back to the generic error path.
func isInsufficientPermissions(body []byte) bool {
	if len(body) == 0 {
		return false
	}

	var payload struct {
		Category string `json:"category"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}

	return payload.Category == "INSUFFICIENT_PERMISSIONS"
}

// doDeepgramGET issues an authenticated GET against endpoint and decodes
// the JSON body into out. Returns an error for non-2xx responses.
func doDeepgramGET(ctx context.Context, endpoint, apiKey string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return fmt.Errorf("deepgram: build request: %w", err)
	}

	req.Header.Set("Authorization", "Token "+apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("deepgram: request failed: %w", err)
	}

	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			_ = closeErr
		}
	}()

	const httpStatusClassOK = 2

	if resp.StatusCode/httpStatusClassReducer != httpStatusClassOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, deepgramErrorBodyLimit))
		if readErr != nil {
			body = nil
		}

		if resp.StatusCode == http.StatusForbidden && isInsufficientPermissions(body) {
			return fmt.Errorf("%w: %s",
				ErrDeepgramInsufficientScope, strings.TrimSpace(string(body)))
		}

		return fmt.Errorf("deepgram: %s returned %s: %s",
			endpoint, resp.Status, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("deepgram: decode response: %w", err)
	}

	return nil
}
