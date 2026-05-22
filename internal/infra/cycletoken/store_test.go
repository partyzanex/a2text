package cycletoken_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/internal/infra/cycletoken"
)

// fakeClock returns a controllable now() for the store. Tests mutate
// it via clock.current = ... to make TTL behaviour deterministic.
type fakeClock struct {
	current time.Time
}

func (c *fakeClock) now() time.Time {
	return c.current
}

func newClock() *fakeClock {
	return &fakeClock{current: time.Unix(1_700_000_000, 0)}
}

func TestStore_IssueReturnsTokenAndExpire(t *testing.T) {
	t.Parallel()

	clock := newClock()
	ttl := 30 * time.Second
	store := cycletoken.New(ttl, clock.now)

	tok, expire, err := store.Issue()
	require.NoError(t, err)

	assert.NotEmpty(t, tok)
	assert.Equal(t, clock.now().Add(ttl), expire)
	assert.True(t, store.Active())
}

func TestStore_IssueRefusesSecondLiveToken(t *testing.T) {
	t.Parallel()

	store := cycletoken.New(time.Minute, newClock().now)

	_, _, err := store.Issue()
	require.NoError(t, err)

	_, _, err = store.Issue()
	assert.ErrorIs(t, err, cycletoken.ErrAlreadyActive)
}

func TestStore_IssueReplacesExpiredToken(t *testing.T) {
	t.Parallel()

	clock := newClock()
	store := cycletoken.New(time.Second, clock.now)

	first, _, err := store.Issue()
	require.NoError(t, err)

	clock.current = clock.current.Add(2 * time.Second)

	second, _, err := store.Issue()
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
	require.NoError(t, store.Validate(second))
	require.ErrorIs(t, store.Validate(first), cycletoken.ErrNotFound)
}

func TestStore_IssueReplacesConsumedToken(t *testing.T) {
	t.Parallel()

	store := cycletoken.New(time.Minute, newClock().now)

	first, _, err := store.Issue()
	require.NoError(t, err)
	require.NoError(t, store.Consume(first))

	second, _, err := store.Issue()
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
	assert.NoError(t, store.Validate(second))
}

func TestStore_ValidateUnknownToken(t *testing.T) {
	t.Parallel()

	store := cycletoken.New(time.Minute, newClock().now)

	err := store.Validate("never-issued")
	assert.ErrorIs(t, err, cycletoken.ErrNotFound)
}

func TestStore_ValidateMismatchedToken(t *testing.T) {
	t.Parallel()

	store := cycletoken.New(time.Minute, newClock().now)

	_, _, err := store.Issue()
	require.NoError(t, err)

	err = store.Validate("other-token")
	assert.ErrorIs(t, err, cycletoken.ErrNotFound)
}

func TestStore_ValidateActiveTokenIsOK(t *testing.T) {
	t.Parallel()

	store := cycletoken.New(time.Minute, newClock().now)

	tok, _, err := store.Issue()
	require.NoError(t, err)

	assert.NoError(t, store.Validate(tok))
}

func TestStore_ValidateExpired(t *testing.T) {
	t.Parallel()

	clock := newClock()
	store := cycletoken.New(time.Second, clock.now)

	tok, _, err := store.Issue()
	require.NoError(t, err)

	clock.current = clock.current.Add(2 * time.Second)

	assert.ErrorIs(t, store.Validate(tok), cycletoken.ErrExpired)
}

func TestStore_ConsumeFirstTime(t *testing.T) {
	t.Parallel()

	store := cycletoken.New(time.Minute, newClock().now)

	tok, _, err := store.Issue()
	require.NoError(t, err)

	assert.NoError(t, store.Consume(tok))
}

func TestStore_ConsumeTwiceReturnsErrConsumed(t *testing.T) {
	t.Parallel()

	store := cycletoken.New(time.Minute, newClock().now)

	tok, _, err := store.Issue()
	require.NoError(t, err)

	require.NoError(t, store.Consume(tok))
	assert.ErrorIs(t, store.Consume(tok), cycletoken.ErrConsumed)
}

func TestStore_ValidateAfterConsume(t *testing.T) {
	t.Parallel()

	store := cycletoken.New(time.Minute, newClock().now)

	tok, _, err := store.Issue()
	require.NoError(t, err)

	require.NoError(t, store.Consume(tok))
	assert.ErrorIs(t, store.Validate(tok), cycletoken.ErrConsumed)
}

func TestStore_ConsumeExpired(t *testing.T) {
	t.Parallel()

	clock := newClock()
	store := cycletoken.New(time.Second, clock.now)

	tok, _, err := store.Issue()
	require.NoError(t, err)

	clock.current = clock.current.Add(2 * time.Second)

	assert.ErrorIs(t, store.Consume(tok), cycletoken.ErrExpired)
}

func TestStore_ConsumeUnknown(t *testing.T) {
	t.Parallel()

	store := cycletoken.New(time.Minute, newClock().now)

	assert.ErrorIs(t, store.Consume("never-issued"), cycletoken.ErrNotFound)
}

func TestStore_ActiveBeforeIssue(t *testing.T) {
	t.Parallel()

	store := cycletoken.New(time.Minute, newClock().now)

	assert.False(t, store.Active())
}

func TestStore_ActiveAfterConsume(t *testing.T) {
	t.Parallel()

	store := cycletoken.New(time.Minute, newClock().now)

	tok, _, err := store.Issue()
	require.NoError(t, err)
	require.NoError(t, store.Consume(tok))

	assert.False(t, store.Active())
}

func TestStore_NilClockUsesTimeNow(t *testing.T) {
	t.Parallel()

	store := cycletoken.New(time.Minute, nil)

	tok, expire, err := store.Issue()
	require.NoError(t, err)

	assert.NotEmpty(t, tok)
	assert.True(t, expire.After(time.Now()), "expire must be in the future")
}

func TestStore_SentinelErrorsAreDistinct(t *testing.T) {
	t.Parallel()

	require.NotErrorIs(t, cycletoken.ErrNotFound, cycletoken.ErrExpired)
	require.NotErrorIs(t, cycletoken.ErrExpired, cycletoken.ErrConsumed)
	require.NotErrorIs(t, cycletoken.ErrConsumed, cycletoken.ErrAlreadyActive)
	require.NotErrorIs(t, cycletoken.ErrAlreadyActive, cycletoken.ErrNotFound)
}
