// Package assets provides static resources embedded in the binary.
package assets

import "embed"

// Icons is the embedded filesystem containing tray state icon SVG files.
//
//go:embed icons/*.svg
var Icons embed.FS
