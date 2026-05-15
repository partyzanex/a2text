// Package i18n provides the a2text translation layer. Backed by
// nicksnyder/go-i18n/v2; message catalogues are embedded so the binary is
// self-contained and works offline.
//
// Usage:
//
//	i18n.Init("ru")          // once at startup, from cfg.UILanguage
//	label := i18n.T("label.url")
//
// Init is safe to call multiple times; the latest call wins. Concurrent T
// calls during an Init are guarded by a RW mutex — the typical pattern is
// to Init at startup and at every UI-language change, then T from any
// goroutine.
package i18n

import (
	"embed"
	"fmt"
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

//go:embed messages/*.toml
var messageFS embed.FS

var (
	bundle    *goi18n.Bundle    //nolint:gochecknoglobals // process-wide singleton
	localizer *goi18n.Localizer //nolint:gochecknoglobals // ditto, behind mu
	mu        sync.RWMutex      //nolint:gochecknoglobals // guards bundle/localizer
)

// Init configures the active locale. Returns an error only on a
// programming bug (corrupt embedded TOML). An unknown lang silently
// falls back to DefaultLanguage so a stale config never blocks startup.
func Init(lang string) error {
	mu.Lock()
	defer mu.Unlock()

	if bundle == nil {
		bundle = goi18n.NewBundle(language.MustParse(DefaultLanguage))
		bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)

		entries, err := messageFS.ReadDir("messages")
		if err != nil {
			return fmt.Errorf("i18n: read embedded messages dir: %w", err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			path := "messages/" + entry.Name()

			if _, err := bundle.LoadMessageFileFS(messageFS, path); err != nil {
				return fmt.Errorf("i18n: load %s: %w", path, err)
			}
		}
	}

	if lang == "" {
		lang = DefaultLanguage
	}

	localizer = goi18n.NewLocalizer(bundle, lang, DefaultLanguage)

	return nil
}

// T returns the localised string for messageID. If the bundle has not
// been initialised, or the id is missing, T returns the id itself —
// visible in the UI as a broken-translation marker so the developer
// notices immediately, rather than rendering an empty widget.
func T(messageID string) string {
	mu.RLock()
	defer mu.RUnlock()

	if localizer == nil {
		return messageID
	}

	out, err := localizer.Localize(&goi18n.LocalizeConfig{MessageID: messageID})
	if err != nil || out == "" {
		return messageID
	}

	return out
}
