// Package secretstore is the file-backed implementation of the
// adapter's SecretRepository contract. It persists provider API
// keys and other long-lived secrets the daemon itself needs at
// runtime.
//
// Storage protection is by access control, not by application-level
// encryption. The on-disk file is written with mode 0600 and is
// expected to live under a directory owned by the daemon user
// (typically /var/lib/a2textd/ for system installs); combined with
// systemd hardening on the unit this keeps the file unreadable to
// every non-root process on the machine. Disk-level encryption
// (LUKS) is the recommended OS-level mitigation against cold-disk
// theft.
//
// On-disk format is JSON with values base64-encoded so binary
// secrets (PEM keys, raw bytes, OAuth refresh tokens with control
// characters) round-trip safely. Writes are atomic: the new content
// is written to a sibling .tmp file and renamed over the canonical
// path so a crash mid-write never leaves a truncated store.
package secretstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/partyzanex/a2text/internal/adapters/grpc/server"
)

// fileMode is the permission applied to the secrets file. 0600 keeps
// the contents unreadable to any other UID even on a multi-user
// machine.
const fileMode fs.FileMode = 0o600

// currentVersion stamps the on-disk format so future format changes
// can detect and migrate old files. Version 1 = JSON + base64 values.
const currentVersion = 1

// ErrInvalidKey is returned by Set when the supplied key is empty.
// Adapters typically catch this earlier and surface it as
// INVALID_ARGUMENT.
var ErrInvalidKey = errors.New("secretstore: invalid key")

// Store is the in-memory cache + persistent file for the daemon's
// secrets. Safe for concurrent use. Constructors return concrete
// types per the project's architecture rules — consumers declare
// their own interface against this struct's methods.
type Store struct {
	path  string
	clock func() time.Time

	mu      sync.Mutex
	entries map[string]storedEntry
}

// storedEntry is the in-memory representation of one secret slot.
type storedEntry struct {
	value     []byte
	storeTime time.Time
}

// fileFormat is the on-disk JSON shape. Kept internal so the
// in-memory model can evolve without changing the file schema.
type fileFormat struct {
	Version int                  `json:"version"`
	Entries map[string]fileEntry `json:"entries"`
}

// fileEntry is the on-disk shape of one secret slot.
type fileEntry struct {
	ValueB64  string    `json:"value_b64"`
	StoreTime time.Time `json:"store_time"`
}

// New constructs a Store backed by path. If the file exists it is
// loaded and the cache is populated; otherwise an empty store is
// returned. Caller is responsible for ensuring the parent directory
// exists with appropriate permissions.
func New(path string, clock func() time.Time) (*Store, error) {
	if clock == nil {
		clock = time.Now
	}

	store := &Store{
		path:    path,
		clock:   clock,
		entries: make(map[string]storedEntry),
	}

	if err := store.load(); err != nil {
		return nil, fmt.Errorf("secretstore: load %q: %w", path, err)
	}

	return store, nil
}

// Set writes (or overwrites) the value bound to key and persists
// the store. Returns the wall-clock time recorded for the entry.
func (s *Store) Set(_ context.Context, key string, value []byte) (time.Time, error) {
	if key == "" {
		return time.Time{}, ErrInvalidKey
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()

	stored := make([]byte, len(value))
	copy(stored, value)

	s.entries[key] = storedEntry{
		value:     stored,
		storeTime: now,
	}

	if err := s.persistLocked(); err != nil {
		return time.Time{}, fmt.Errorf("secretstore: persist: %w", err)
	}

	return now, nil
}

// List returns metadata for every key currently present in the
// store. Values are never returned — callers receive key names and
// last-write timestamps only. Order is implementation-defined.
func (s *Store) List(_ context.Context) ([]server.SecretRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]server.SecretRecord, 0, len(s.entries))

	for key := range s.entries {
		entry := s.entries[key]
		out = append(out, server.SecretRecord{
			Key:       key,
			StoreTime: entry.storeTime,
		})
	}

	return out, nil
}

// Get returns the value bound to key together with the moment it
// was last written. Used by daemon-internal consumers (STT
// providers etc.) that need the plaintext; intentionally NOT
// exposed through the gRPC SecretRepository interface so a
// compromised IPC caller cannot read keys back.
func (s *Store) Get(key string) ([]byte, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok {
		return nil, time.Time{}, false
	}

	out := make([]byte, len(entry.value))
	copy(out, entry.value)

	return out, entry.storeTime, true
}

// load reads the on-disk file (if present) into the in-memory
// cache. A missing file is not an error — it just means an empty
// store.
func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	if len(data) == 0 {
		return nil
	}

	var parsed fileFormat
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	for key := range parsed.Entries {
		entry := parsed.Entries[key]

		raw, err := base64.StdEncoding.DecodeString(entry.ValueB64)
		if err != nil {
			return fmt.Errorf("decode %q: %w", key, err)
		}

		s.entries[key] = storedEntry{
			value:     raw,
			storeTime: entry.StoreTime,
		}
	}

	return nil
}

// persistLocked writes the in-memory cache to disk atomically:
// marshal → write to .tmp → rename. Caller must hold s.mu.
func (s *Store) persistLocked() error {
	parsed := fileFormat{
		Version: currentVersion,
		Entries: make(map[string]fileEntry, len(s.entries)),
	}

	for key := range s.entries {
		entry := s.entries[key]
		parsed.Entries[key] = fileEntry{
			ValueB64:  base64.StdEncoding.EncodeToString(entry.value),
			StoreTime: entry.storeTime,
		}
	}

	data, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := writeAtomic(tmp, s.path, data); err != nil {
		return fmt.Errorf("write %q: %w", s.path, err)
	}

	return nil
}

// writeAtomic writes data to tmp with the secrets mode and renames
// it over final. Rename is atomic on the same filesystem so a crash
// between write and rename never leaves the canonical file
// truncated. fsync is intentionally skipped — single-user laptops
// do not gain enough from it to justify the extra IO, and a daemon
// crash without rsync just falls back to the pre-write state.
func writeAtomic(tmp, final string, data []byte) error {
	if err := os.WriteFile(tmp, data, fileMode); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}

	if err := os.Rename(tmp, final); err != nil {
		if removeErr := os.Remove(tmp); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			return fmt.Errorf("rename failed and tmp cleanup failed: %w", errors.Join(err, removeErr))
		}

		return fmt.Errorf("rename: %w", err)
	}

	return nil
}
