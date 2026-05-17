// Command genappicon writes the app icon PNG to stdout. Used by the
// Makefile install-desktop target to materialise an icon file for the
// freedesktop hicolor theme so GNOME/KDE pick it up for the taskbar
// entry. Keeping it as a tiny program (instead of a build-time asset)
// guarantees the installed icon and the in-app icon are byte-identical
// — both come from assets.StateIconPNG with the same SVG source.
package main

import (
	"fmt"
	"os"

	"github.com/partyzanex/a2text/assets"
)

func main() {
	if _, err := os.Stdout.Write(assets.AppIcon().Content()); err != nil {
		fmt.Fprintf(os.Stderr, "genappicon: write: %v\n", err)
		os.Exit(1)
	}
}
