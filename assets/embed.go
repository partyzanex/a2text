// Package assets provides static resources embedded in the binary.
package assets

import (
	"embed"
	"fmt"
	"path"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Icons is the embedded filesystem containing all SVG resources:
//   - icons/*.svg          tray state icons (recording / inactive / transcribing)
//   - icons/ui/*.svg       settings-window tab icons (mic, record, export, …)
//
//go:embed icons/*.svg icons/ui/*.svg
var Icons embed.FS

// UIIcon loads a settings-window tab icon by short name (e.g. "mic", "lock")
// and returns it wrapped in a themed resource so Fyne re-tints it on theme
// changes. Panics on a missing name — names are compile-time constants in
// the settings package, so a miss is a wiring bug, not user error.
//
//nolint:ireturn // fyne.Resource is the consumer's only stable contract
func UIIcon(name string) fyne.Resource {
	body, err := Icons.ReadFile(path.Join("icons", "ui", name+".svg"))
	if err != nil {
		panic(fmt.Sprintf("assets: UIIcon %q: %v", name, err))
	}

	return theme.NewThemedResource(fyne.NewStaticResource(name+".svg", body))
}
