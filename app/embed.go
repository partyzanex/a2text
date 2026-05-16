// Package app holds runtime-adjacent assets that ship inside the
// binary. Currently exposes the YAML config template that the daemon
// drops into the user's per-user config dir on first launch.
//
// The template lives next to this file (app/config.yaml) so the
// project's repo root carries a single readable copy. //go:embed
// directives are scoped to the directory the source file is in, so
// keeping the embed declaration here lets us avoid duplicating the
// YAML inside internal/infra/config/.
package app

import _ "embed"

// DefaultConfigYAML is the verbatim contents of app/config.yaml at
// build time. Consumers (internal/infra/config.EnsureUserConfig) write
// this bytes payload into the per-user config file on first launch.
//
// Note: the embed is a build-time snapshot. If you keep local API
// keys in app/config.yaml on your development machine, those keys end
// up baked into every binary you produce locally — use
// `git update-index --skip-worktree app/config.yaml` to hide local
// edits, and ensure release builds happen from a clean checkout.
//
//go:embed config.yaml
var DefaultConfigYAML []byte
