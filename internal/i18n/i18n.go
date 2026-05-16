// Package i18n provides the a2text translation layer. Backed by
// nicksnyder/go-i18n/v2; message catalogues are embedded so the binary is
// self-contained and works offline.
//
// Two surfaces are exposed:
//
//   - The Translator struct — preferred for tests and any caller that
//     needs an isolated instance (multiple UI windows in different
//     locales, parallel tests, etc.).
//   - Package-level Init/T — a thin facade over a lazily-constructed
//     default Translator. Kept for compatibility with the ~120 existing
//     call sites that pre-date the struct.
//
// Usage (facade):
//
//	i18n.Init("ru")             // once at startup, from cfg.UILanguage
//	label := i18n.T("label.url")
//
// Usage (struct):
//
//	tr := i18n.NewTranslator()
//	_ = tr.Init("ru")
//	label := tr.T("label.url")
package i18n

import (
	"embed"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

// DefaultLanguage is the locale used when Init is not called or the
// requested language is not bundled.
const DefaultLanguage = "ru"

// SupportedLanguages enumerates locales shipped with the binary. UI code
// uses this to populate the "UI language" select; adding a new locale
// means adding a messages/<tag>.toml file AND extending this slice.
//
//nolint:gochecknoglobals // immutable lookup; one of two equivalent ways to surface a const slice
var SupportedLanguages = []string{"ru", "en"}

//go:generate go run ./cmd/gen-keys

//go:embed messages/*.toml
var messageFS embed.FS

// Translator is an isolated i18n instance. Constructed via NewTranslator;
// callers Init once (or per language change) and call T from any
// goroutine. The internal RW mutex makes Init/T safe to interleave.
type Translator struct {
	mu        sync.RWMutex
	bundle    *goi18n.Bundle
	localizer *goi18n.Localizer
}

// NewTranslator returns an uninitialised Translator. Init must be called
// before T, otherwise T returns the message id as a broken-translation
// marker.
func NewTranslator() *Translator {
	return &Translator{}
}

// Init configures the active locale on this Translator. Returns an error
// only on a programming bug (corrupt embedded TOML). An unknown lang
// silently falls back to DefaultLanguage so a stale config never blocks
// startup. An empty lang triggers $LANG/$LC_ALL detection; if that also
// resolves to nothing supported, DefaultLanguage is used.
func (t *Translator) Init(lang string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.bundle == nil {
		t.bundle = goi18n.NewBundle(language.MustParse(DefaultLanguage))
		t.bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)

		entries, err := messageFS.ReadDir("messages")
		if err != nil {
			return fmt.Errorf("i18n: read embedded messages dir: %w", err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			path := "messages/" + entry.Name()

			if _, err := t.bundle.LoadMessageFileFS(messageFS, path); err != nil {
				return fmt.Errorf("i18n: load %s: %w", path, err)
			}
		}
	}

	lang = resolveLang(lang)

	t.localizer = goi18n.NewLocalizer(t.bundle, lang, DefaultLanguage)

	return nil
}

// T returns the localised string for messageID. If the Translator has
// not been initialised, or the id is missing, T returns the id itself —
// visible in the UI as a broken-translation marker so the developer
// notices immediately, rather than rendering an empty widget.
func (t *Translator) T(messageID string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.localizer == nil {
		return messageID
	}

	out, err := t.localizer.Localize(&goi18n.LocalizeConfig{MessageID: messageID})
	if err != nil || out == "" {
		return messageID
	}

	return out
}

// --- package-level facade ---

//nolint:gochecknoglobals // process-wide default Translator, mutated only via Init
var defaultTranslator = NewTranslator()

// Init configures the default Translator. See Translator.Init.
func Init(lang string) error {
	return defaultTranslator.Init(lang)
}

// T returns the localised string for messageID against the default
// Translator. See Translator.T.
func T(messageID string) string {
	return defaultTranslator.T(messageID)
}

// resolveLang turns the caller's lang into a concrete tag. Empty lang
// falls through to $LC_ALL / $LANG / $LC_MESSAGES (POSIX precedence);
// anything not in SupportedLanguages becomes DefaultLanguage. The OS
// env values look like "ru_RU.UTF-8" — we keep only the base tag.
func resolveLang(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		lang = detectFromEnv()
	}

	lang = baseTag(lang)
	if lang == "" {
		return DefaultLanguage
	}

	if slices.Contains(SupportedLanguages, lang) {
		return lang
	}

	return DefaultLanguage
}

// detectFromEnv reads POSIX locale envs in priority order and returns
// the first non-empty value. Returns "" if none are set.
func detectFromEnv() string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}

	return ""
}

// baseTag strips region/encoding modifiers from a POSIX locale string:
// "ru_RU.UTF-8" -> "ru", "en_US@something" -> "en", "C" -> "" (treated
// as unset so we fall through to DefaultLanguage).
func baseTag(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "C" || raw == "POSIX" {
		return ""
	}

	// Strip @modifier
	if idx := strings.Index(raw, "@"); idx >= 0 {
		raw = raw[:idx]
	}

	// Strip .encoding
	if idx := strings.Index(raw, "."); idx >= 0 {
		raw = raw[:idx]
	}

	// Take part before _region
	if idx := strings.Index(raw, "_"); idx >= 0 {
		raw = raw[:idx]
	}

	return strings.ToLower(raw)
}
