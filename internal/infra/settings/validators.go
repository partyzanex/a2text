package settings

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2/widget"

	"github.com/partyzanex/a2text/internal/i18n"
)

// --- Validators ---
//
// All validators tolerate an empty string and return nil — empty means
// "use the default" and propagates through normalizeVoiceConfig. That
// avoids forcing the user to clear a partial value before retyping.

// validateDuration accepts any string that time.ParseDuration accepts:
// "30s", "1m30s", "10m", "500ms". Empty string is OK (treated as zero).
func validateDuration(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	if _, err := time.ParseDuration(value); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("validation.duration_invalid"), err)
	}

	return nil
}

// validatePositiveInt accepts only strings that strconv.Atoi accepts and
// that produce a value strictly > 0. Empty string is OK.
func validatePositiveInt(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("validation.int_invalid"), err)
	}

	if parsed <= 0 {
		return errors.New(i18n.T("validation.int_positive"))
	}

	return nil
}

// validateNonNegativeInt is validatePositiveInt that accepts zero.
func validateNonNegativeInt(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("validation.int_invalid"), err)
	}

	if parsed < 0 {
		return errors.New(i18n.T("validation.int_non_negative"))
	}

	return nil
}

// validateHTTPURL accepts absolute http/https URLs. Empty string is OK
// so the cloud_base_url field (optional) does not light up red when left
// blank. Use validateRequiredHTTPURL for must-be-present URLs.
func validateHTTPURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("validation.url_invalid"), err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New(i18n.T("validation.url_scheme"))
	}

	if parsed.Host == "" {
		return errors.New(i18n.T("validation.url_host_missing"))
	}

	return nil
}

// validateRequiredHTTPURL is validateHTTPURL that rejects empty input.
func validateRequiredHTTPURL(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New(i18n.T("validation.url_required"))
	}

	return validateHTTPURL(value)
}

// validateNonPositiveFloat accepts decimal numbers that are zero or
// negative. Used for dBFS values: full-scale audio is exactly 0 dBFS,
// nothing is louder, so a positive threshold can never be correct.
// Empty string is OK (treated as 0).
func validateNonPositiveFloat(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("validation.float_invalid"), err)
	}

	if parsed > 0 {
		return errors.New(i18n.T("validation.float_non_positive"))
	}

	return nil
}

// --- Entry helpers ---

func entryWithText(text, placeholder string) *widget.Entry {
	ee := widget.NewEntry()
	ee.SetText(text)
	ee.SetPlaceHolder(placeholder)

	return ee
}

// formatDuration formats a duration as a string; returns empty string for zero.
func formatDuration(dd time.Duration) string {
	if dd == 0 {
		return ""
	}

	return dd.String()
}

// parseDuration parses a duration string; returns zero on empty or parse error.
func parseDuration(ss string) time.Duration {
	if ss == "" {
		return 0
	}

	dd, err := time.ParseDuration(ss)
	if err != nil {
		return 0
	}

	return dd
}

// parseIntEntry parses an integer string; returns zero on empty or parse error.
func parseIntEntry(ss string) int {
	if ss == "" {
		return 0
	}

	nn, err := strconv.Atoi(ss)
	if err != nil {
		return 0
	}

	return nn
}

// intOrEmpty converts an int to its string representation; returns empty string for zero.
func intOrEmpty(nn int) string {
	if nn == 0 {
		return ""
	}

	return strconv.Itoa(nn)
}

// parseFloatEntry parses a float string; returns zero on empty or parse error.
// Matches the parseIntEntry policy: invalid input degrades to zero rather
// than blocking the save, because validateNonPositiveFloat already shows
// the user a red inline error before this is ever reached.
func parseFloatEntry(ss string) float64 {
	ss = strings.TrimSpace(ss)
	if ss == "" {
		return 0
	}

	val, err := strconv.ParseFloat(ss, 64)
	if err != nil {
		return 0
	}

	return val
}

// floatOrEmpty converts a float to its string representation; returns
// empty string for zero so a freshly-installed (default-only) config does
// not show 0.0 in the placeholder slot.
func floatOrEmpty(value float64) string {
	if value == 0 {
		return ""
	}

	return strconv.FormatFloat(value, 'f', -1, 64)
}
