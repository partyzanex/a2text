package server

// Compile-time interface assertions. If a proto regeneration adds a
// new RPC to one of the wire services, or a seam interface evolves
// to a signature the usecase implementation no longer matches, these
// declarations break the build right here instead of failing far
// away at gRPC registration or at bootstrap-time dependency wiring.

import (
	"github.com/partyzanex/a2text/internal/usecases/hotkey"
	"github.com/partyzanex/a2text/internal/usecases/inject"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

// Adapter ↔ proto wire interfaces. These ensure the local Keyboard /
// Secret structs still implement the contract their gRPC services
// expose.
var (
	_ a2textv1.KeyboardServiceServer = (*KeyboardService)(nil)
	_ a2textv1.SecretServiceServer   = (*SecretService)(nil)
)

// Adapter ↔ usecase seams. These ensure the concrete usecase types
// still satisfy the dependency interfaces this package declares.
// The hotkey.Hub satisfies BOTH HotkeySource (observers subscribe
// for events) and CycleTrigger (the gRPC adapter and the evdev
// reader call Start to begin a cycle).
var (
	_ HotkeySource = (*hotkey.Hub)(nil)
	_ Injector     = (*inject.Service)(nil)
	_ CycleTrigger = (*hotkey.Hub)(nil)
)
