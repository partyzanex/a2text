package tray

import (
	"bytes"
	"fmt"
	"image"
	"image/png"

	"github.com/partyzanex/a2text/assets"
	"github.com/partyzanex/a2text/internal/domain"
)

type iconEntry struct {
	file  string
	state domain.State
}

func iconEntries() []iconEntry {
	return []iconEntry{
		{"icons/a2t-state-inactive.svg", domain.StateIdle},
		{"icons/a2t-state-recording.svg", domain.StateRecording},
		{"icons/a2t-state-transcribing.svg", domain.StateTranscribing},
	}
}

// loadIcons rasterises the embedded SVGs into PNG byte slices the tray
// library hands to the StatusNotifierItem D-Bus interface. The cache
// lives for the daemon's lifetime — the embedded source bytes cannot
// change at runtime, so a one-shot rasterise is enough. A daemon
// restart (after rebuilding with updated SVGs) drops and rebuilds it.
//
// The actual SVG-to-PNG conversion is delegated to assets.StateIconPNG
// so the settings window's title-bar icon (assets.AppIcon) and the tray
// indicator stay visually identical.
func (tr *Tray) loadIcons() {
	tr.iconOnce.Do(func() {
		tr.icons = make(map[domain.State][]byte)

		entries := iconEntries()
		for i := range entries {
			data, err := assets.Icons.ReadFile(entries[i].file)
			if err != nil {
				continue
			}

			tr.icons[entries[i].state] = assets.StateIconPNG(data)
		}
	})
}

// iconFor returns the PNG icon for state. SVG icons are used when available;
// a programmatic colour circle is returned for states without a dedicated SVG.
func (tr *Tray) iconFor(state domain.State) []byte {
	tr.loadIcons()

	if data, ok := tr.icons[state]; ok {
		return data
	}

	rr, gg, bb := colorForState(state)

	return circleIcon(rr, gg, bb)
}

// encodePNG is the shared PNG encoder for the fallback circleIcon path.
// SVG-backed icons go through assets.StateIconPNG which encodes there.
func encodePNG(img *image.NRGBA) []byte {
	var buf bytes.Buffer

	if err := png.Encode(&buf, img); err != nil {
		panic(fmt.Sprintf("tray: encodePNG: png.Encode: %v", err))
	}

	return buf.Bytes()
}
