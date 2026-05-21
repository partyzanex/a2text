package secret_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/internal/infra/secret"
)

// StoreSuite covers the file-backed Store: Set / List / Get
// roundtrips, persistence across reopen, the binary-safe value
// path, mode-0600 enforcement, and atomic-rename behaviour.
type StoreSuite struct {
	suite.Suite

	dir   string
	path  string
	clock fakeClock
	store *secret.Store
}

// fakeClock returns a deterministic now() so storeTime assertions
// are exact.
type fakeClock struct {
	current time.Time
}

func (c *fakeClock) now() time.Time {
	return c.current
}

// SetupTest builds a fresh store rooted at a per-test temp dir.
func (s *StoreSuite) SetupTest() {
	s.dir = s.T().TempDir()
	s.path = filepath.Join(s.dir, "secrets.json")
	s.clock = fakeClock{current: time.Unix(1_700_000_000, 0).UTC()}

	store, err := secret.New(s.path, s.clock.now)
	s.Require().NoError(err)
	s.store = store
}

// TestSet_PersistsAndRoundtrips verifies a stored value is
// retrievable via Get with the same bytes and the same store time.
func (s *StoreSuite) TestSet_PersistsAndRoundtrips() {
	value := []byte("sk-test-12345")

	stored, err := s.store.Set(context.Background(), "openai", value)
	s.Require().NoError(err)
	s.Equal(s.clock.current, stored)

	got, ts, ok := s.store.Get("openai")
	s.Require().True(ok)
	s.Equal(value, got)
	s.Equal(s.clock.current, ts)
}

// TestSet_BinarySafe verifies bytes that are not valid UTF-8
// round-trip unchanged — the proto contract picked `bytes` for this
// exact reason.
func (s *StoreSuite) TestSet_BinarySafe() {
	value := []byte{0x00, 0xff, 0xfe, 0xfd, 0x10}

	_, err := s.store.Set(context.Background(), "raw", value)
	s.Require().NoError(err)

	got, _, ok := s.store.Get("raw")
	s.Require().True(ok)
	s.Equal(value, got)
}

// TestSet_EmptyKeyRejected verifies an empty key is refused at the
// store layer.
func (s *StoreSuite) TestSet_EmptyKeyRejected() {
	_, err := s.store.Set(context.Background(), "", []byte("v"))
	s.Require().ErrorIs(err, secret.ErrInvalidKey)
}

// TestSet_OverwriteUpdatesTime verifies a second Set on the same
// key replaces the value and refreshes the store time.
func (s *StoreSuite) TestSet_OverwriteUpdatesTime() {
	_, err := s.store.Set(context.Background(), "k", []byte("first"))
	s.Require().NoError(err)

	s.clock.current = s.clock.current.Add(time.Hour)

	_, err = s.store.Set(context.Background(), "k", []byte("second"))
	s.Require().NoError(err)

	got, ts, ok := s.store.Get("k")
	s.Require().True(ok)
	s.Equal([]byte("second"), got)
	s.Equal(s.clock.current, ts)
}

// TestList_ReturnsAllKeysWithoutValues verifies List exposes key
// names and timestamps but never the raw value.
func (s *StoreSuite) TestList_ReturnsAllKeysWithoutValues() {
	_, err := s.store.Set(context.Background(), "openai", []byte("a"))
	s.Require().NoError(err)

	_, err = s.store.Set(context.Background(), "deepgram", []byte("b"))
	s.Require().NoError(err)

	records, err := s.store.List(context.Background())
	s.Require().NoError(err)
	s.Require().Len(records, 2)

	keys := map[string]bool{}
	for _, r := range records {
		keys[r.Key] = true
		s.Equal(s.clock.current, r.StoreTime)
	}

	s.True(keys["openai"])
	s.True(keys["deepgram"])
}

// TestList_EmptyStore verifies List on a fresh store yields an
// empty slice without error.
func (s *StoreSuite) TestList_EmptyStore() {
	records, err := s.store.List(context.Background())
	s.Require().NoError(err)
	s.Empty(records)
}

// TestPersistence_ReopenSeesPreviousWrites verifies the on-disk file
// is loaded on construction, so a fresh Store instance pointed at
// the same path sees every prior Set.
func (s *StoreSuite) TestPersistence_ReopenSeesPreviousWrites() {
	_, err := s.store.Set(context.Background(), "openai", []byte("sk-A"))
	s.Require().NoError(err)

	reopened, err := secret.New(s.path, s.clock.now)
	s.Require().NoError(err)

	got, _, ok := reopened.Get("openai")
	s.Require().True(ok)
	s.Equal([]byte("sk-A"), got)
}

// TestPersistence_FileHasModeSixZeroZero verifies the on-disk
// secrets file is written with mode 0600 so other users cannot
// read it. Filesystem permissions are the only protection we
// promise for the store.
func (s *StoreSuite) TestPersistence_FileHasModeSixZeroZero() {
	_, err := s.store.Set(context.Background(), "k", []byte("v"))
	s.Require().NoError(err)

	info, err := os.Stat(s.path)
	s.Require().NoError(err)
	s.Equal(os.FileMode(0o600), info.Mode().Perm())
}

// TestPersistence_FileFormatIsJSONWithBase64Values verifies the
// on-disk file uses the documented JSON shape and base64-encodes
// values. Stable format guards against accidental schema drift.
func (s *StoreSuite) TestPersistence_FileFormatIsJSONWithBase64Values() {
	_, err := s.store.Set(context.Background(), "k", []byte("hello"))
	s.Require().NoError(err)

	raw, err := os.ReadFile(s.path)
	s.Require().NoError(err)

	var decoded map[string]any
	s.Require().NoError(json.Unmarshal(raw, &decoded))

	s.InEpsilon(float64(1), decoded["version"], 0.0001)

	entries, ok := decoded["entries"].(map[string]any)
	s.Require().True(ok)

	entry, ok := entries["k"].(map[string]any)
	s.Require().True(ok)
	// base64("hello") = "aGVsbG8="
	s.Equal("aGVsbG8=", entry["value_b64"])
}

// TestNew_MissingFileIsEmptyStore verifies pointing New at a
// non-existent path yields an empty store and not an error.
func (s *StoreSuite) TestNew_MissingFileIsEmptyStore() {
	store, err := secret.New(filepath.Join(s.dir, "absent.json"), s.clock.now)
	s.Require().NoError(err)

	records, err := store.List(context.Background())
	s.Require().NoError(err)
	s.Empty(records)
}

// TestNew_CorruptFileSurfacesError verifies a garbage file surfaces
// the unmarshal failure so the daemon refuses to start on a
// corrupt store rather than silently losing data.
func (s *StoreSuite) TestNew_CorruptFileSurfacesError() {
	s.Require().NoError(os.WriteFile(s.path, []byte("{not json"), 0o600))

	_, err := secret.New(s.path, s.clock.now)
	s.Require().Error(err)
}

// TestGet_MissingKeyReturnsFalse verifies Get of an unknown key
// returns the not-found signal cleanly.
func (s *StoreSuite) TestGet_MissingKeyReturnsFalse() {
	_, _, ok := s.store.Get("never-set")
	s.False(ok)
}

// TestStoreSuite is the standard testify entry point.
func TestStoreSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(StoreSuite))
}
