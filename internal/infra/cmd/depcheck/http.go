package depcheck

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// maxSanitizedURLLen caps the string produced by sanitizeURL. Long URLs from
// garbage configs can exhaust log line buffers; 200 rune characters cover any
// reasonable service address while still being diagnostic.
const maxSanitizedURLLen = 200

const httpHeadTimeout = 3 * time.Second

// defaultHTTPHead makes an HTTP HEAD request to rawURL and returns the
// status code. A non-nil error means the server could not be reached.
//
// The client is created per-call (depcheck runs once at startup; allocation
// cost is negligible). CheckRedirect returns ErrUseLastResponse so probes
// never follow redirects — a config URL should point directly at the service,
// and following a redirect to an unexpected host is an SSRF-adjacent risk
// when URLs come from user config. Timeout caps the entire round-trip.
//
// This is the production implementation used by [DefaultEnv].
func defaultHTTPHead(ctx context.Context, rawURL string) (int, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return 0, fmt.Errorf("head %s: invalid absolute URL", sanitizeURL(rawURL))
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return 0, fmt.Errorf("head %s: unsupported scheme %q", sanitizeURL(rawURL), parsed.Scheme)
	}

	client := &http.Client{
		Timeout: httpHeadTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("head %s: %w", sanitizeURL(rawURL), err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("head %s: %w", sanitizeURL(rawURL), err)
	}

	// HEAD responses have an empty body; close error is not meaningful for
	// reachability checks — status code was already read from the response line.
	if err := resp.Body.Close(); err != nil {
		_ = err
	}

	return resp.StatusCode, nil
}

// sanitizeURL strips credentials, query params, and fragment from rawURL so
// secrets never appear in log lines or error messages. Returns a safe sentinel
// when the URL is unparseable or lacks scheme/host (opaque or relative URLs
// are not valid config service addresses).
//
// The result is truncated to maxSanitizedURLLen runes to protect log line
// buffers from garbage config values.
func sanitizeURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "[unparseable URL]"
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return "[invalid URL]"
	}

	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.RawFragment = ""

	result := parsed.String()

	runes := []rune(result)
	if len(runes) > maxSanitizedURLLen {
		return string(runes[:maxSanitizedURLLen]) + "…"
	}

	return result
}
