// Package ipc carries the unix-socket protocol between the a2text CLI
// (short-lived toggle invocation) and the long-running daemon. See I.6 in
// docs/TODO.md for the protocol shape.
package ipc

// ProtocolVersion is the wire format version this build speaks.
//
// Bump rules:
//   - additive change to Request/Response (new optional fields): same major,
//     bump nothing — JSON ignores unknown fields.
//   - breaking change (rename/remove/retype): bump ProtocolVersion AND raise
//     MinSupportedVersion to drop the old format from the daemon's accept
//     range.
const ProtocolVersion = 1

// MinSupportedVersion is the oldest client version this daemon will speak
// to. Equal to ProtocolVersion at v=1 — there's nothing to be backwards
// compatible with yet.
const MinSupportedVersion = 1
