package voice

import "github.com/partyzanex/a2text/pkg/hotkey"

//go:generate go run go.uber.org/mock/mockgen@latest -write_package_comment=false -package=voice -destination=voice_mocks_test.go -source=hotkey.go

// Voice-facing aliases for the hotkey library types. The contract
// (consumer-owns-the-interface) is satisfied because pkg/hotkey is a
// generic library: it does not know about voice. Aliases keep the
// existing call sites (voice.HotkeyEvent, voice.HotkeyListener, etc.)
// unchanged.
type (
	HotkeyEvent    = hotkey.Event
	HotkeyListener = hotkey.Listener
	Handler        = hotkey.Handler
)

const (
	HotkeyPress   = hotkey.Press
	HotkeyRelease = hotkey.Release
)
