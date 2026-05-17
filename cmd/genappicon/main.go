// Command genappicon writes the app icon PNG to stdout at the requested
// size. Invoked by the Makefile's `build-icons` target to materialise
// the freedesktop hicolor variants (64/128/256) shipped by the .deb
// and by `make install`. Keeping it as a tiny program (instead of a
// build-time asset) guarantees every installed icon size is byte-stable
// and derived from the same SVG + gobold rendering as the in-app icon.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/partyzanex/a2text/assets"
)

const (
	defaultSize = 64
	stateSVGKey = "icons/a2t-state-inactive.svg"
	exitUsage   = 2
)

func main() {
	size := flag.Int("size", defaultSize, "icon edge size in pixels (64, 128, 256, ...)")

	flag.Parse()

	if *size <= 0 {
		fmt.Fprintf(os.Stderr, "genappicon: -size must be positive, got %d\n", *size)
		os.Exit(exitUsage)
	}

	svg, err := assets.Icons.ReadFile(stateSVGKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "genappicon: read %s: %v\n", stateSVGKey, err)
		os.Exit(1)
	}

	png := assets.StateIconPNGSized(svg, *size)

	if _, err := os.Stdout.Write(png); err != nil {
		fmt.Fprintf(os.Stderr, "genappicon: write: %v\n", err)
		os.Exit(1)
	}
}
