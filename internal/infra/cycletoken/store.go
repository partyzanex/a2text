// Package cycletoken is the in-memory single-slot repository for the
// recording-cycle authorisation token exchanged between a2textd and
// its UI clients. It belongs to the infrastructure layer because it
// is pure storage and concurrency mechanics with no business rules.
//
// Design: the daemon serves at most one recording cycle at a time —
// the user is one person on a single machine — so the store holds
// exactly one slot: the currently active token (if any), its expiry
// time, and whether it has been consumed. Issue refuses to mint a
// new token while the slot is still live; Consume marks the token
// used; Validate reports the slot state without mutating it. There
// is no map; there is no Sweep.
//
// Storage is in-memory and per-process. There is no persistence: a
// daemon restart invalidates the slot, which is exactly the desired
// behaviour for short-lived authorisation material.
package cycletoken

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

// tokenByteLen is the number of random bytes a fresh token is
// generated from. 16 bytes → 22-char base64 string, which gives
// 128 bits of entropy — collision-resistant for any realistic
// daemon lifetime.
const tokenByteLen = 16

// Token is the opaque value the daemon hands to UI clients. Treat it
// as a black box on every layer above this package.
type Token string

// Sentinel errors returned by Store. Adapters compare via errors.Is.
var (
	// ErrNotFound — the token does not match the currently active
	// slot (either the slot is empty or holds a different token).
	ErrNotFound = errors.New("cycletoken: not found")

	// ErrExpired — the active slot's TTL has elapsed. The adapter
	// should reject the call with PERMISSION_DENIED.
	ErrExpired = errors.New("cycletoken: expired")

	// ErrConsumed — the active token has already been consumed. The
	// adapter uses this to decide whether to serve a cached response
	// (idempotent retry).
	ErrConsumed = errors.New("cycletoken: already consumed")

	// ErrAlreadyActive — Issue was called while the slot still holds
	// a live, unconsumed token. The adapter should surface this as
	// FAILED_PRECONDITION ("cycle already in flight").
	ErrAlreadyActive = errors.New("cycletoken: a token is already active")
)

// Store is the in-memory single-slot token ledger. Safe for
// concurrent use.
//
// Constructors return concrete types per the project's architecture
// rules — consumers declare their own interface against this struct's
// methods when they need mocking at the seam.
type Store struct {
	ttl time.Duration
	now func() time.Time

	mu       sync.Mutex
	active   Token
	expireAt time.Time
	consumed bool
}

// New constructs a Store with the given TTL. If clock is nil the
// store uses time.Now — pass a fake clock from tests to make timing
// deterministic.
func New(ttl time.Duration, clock func() time.Time) *Store {
	if clock == nil {
		clock = time.Now
	}

	return &Store{
		ttl: ttl,
		now: clock,
	}
}

// Issue mints a fresh token and stores it in the slot. Returns the
// token together with its absolute expiry time. Fails with
// ErrAlreadyActive when the slot still holds a live, unconsumed
// token. Issue silently replaces a token that is either expired or
// already consumed.
func (s *Store) Issue() (Token, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	if s.slotLiveLocked(now) {
		return "", time.Time{}, ErrAlreadyActive
	}

	raw := make([]byte, tokenByteLen)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("cycletoken: random: %w", err)
	}

	tok := Token(base64.RawURLEncoding.EncodeToString(raw))

	s.active = tok
	s.expireAt = now.Add(s.ttl)
	s.consumed = false

	return tok, s.expireAt, nil
}

// Validate checks that tok matches the active slot and that the slot
// is neither expired nor consumed. Returns nil on success or a
// sentinel error matching the failure mode.
func (s *Store) Validate(tok Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.checkLocked(tok)
}

// Consume atomically marks the slot as consumed and returns nil on
// the first call. Subsequent calls with the same token return
// ErrConsumed — adapters use that as the signal to serve the cached
// response. ErrNotFound / ErrExpired are returned unchanged when the
// slot does not match or has lapsed.
func (s *Store) Consume(tok Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.checkLocked(tok); err != nil {
		return err
	}

	s.consumed = true

	return nil
}

// Active reports whether the slot currently holds a live unconsumed
// token. Useful for the StartCycle adapter to decide before minting.
func (s *Store) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.slotLiveLocked(s.now())
}

// slotLiveLocked answers "is the slot holding a token that has not
// yet expired and has not yet been consumed?" Caller must hold s.mu.
func (s *Store) slotLiveLocked(now time.Time) bool {
	return s.active != "" && !s.consumed && !now.After(s.expireAt)
}

// checkLocked performs the not-found / expired / consumed checks
// against the single slot. Caller must hold s.mu.
func (s *Store) checkLocked(tok Token) error {
	if s.active == "" || s.active != tok {
		return ErrNotFound
	}

	if s.now().After(s.expireAt) {
		return ErrExpired
	}

	if s.consumed {
		return ErrConsumed
	}

	return nil
}
