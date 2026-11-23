package repository_test

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/christianselig/apollo-backend/internal/domain"
	"github.com/christianselig/apollo-backend/internal/repository"
	"github.com/christianselig/apollo-backend/internal/testhelper"
)

func NewTestPostgresLiveActivity(t *testing.T) domain.LiveActivityRepository {
	t.Helper()

	ctx := t.Context()
	conn := testhelper.NewTestPgxConn(t)

	tx, err := conn.Begin(ctx)
	require.NoError(t, err)

	repo := repository.NewPostgresLiveActivity(tx)

	t.Cleanup(func() {
		_ = tx.Rollback(ctx)
	})

	return repo
}

func newTestLiveActivityToken(t *testing.T) string {
	t.Helper()

	// ActivityKit push tokens are ~80 bytes (160 hex chars).
	b := make([]byte, 80)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func TestPostgresLiveActivity_CreateGetDelete(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	repo := NewTestPostgresLiveActivity(t)

	tok := newTestLiveActivityToken(t)
	la := &domain.LiveActivity{
		APNSToken:       tok,
		RedditAccountID: "xyz123",
		ThreadID:        "abc987",
		Subreddit:       "apolloapp",
	}

	require.NoError(t, repo.Create(ctx, la))
	assert.NotEqual(t, int64(0), la.ID)
	assert.WithinDuration(t, time.Now().Add(domain.LiveActivityDuration), la.ExpiresAt, time.Minute)

	got, err := repo.Get(ctx, tok)
	require.NoError(t, err)
	assert.Equal(t, la.ID, got.ID)
	assert.Equal(t, "abc987", got.ThreadID)
	assert.Equal(t, "apolloapp", got.Subreddit)

	require.NoError(t, repo.Delete(ctx, tok))

	_, err = repo.Get(ctx, tok)
	assert.Equal(t, domain.ErrNotFound, err)
}

func TestPostgresLiveActivity_CreateIsUpsert(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	repo := NewTestPostgresLiveActivity(t)

	tok := newTestLiveActivityToken(t)
	la := &domain.LiveActivity{
		APNSToken:       tok,
		RedditAccountID: "xyz123",
		ThreadID:        "abc987",
		Subreddit:       "apolloapp",
	}
	require.NoError(t, repo.Create(ctx, la))
	firstID := la.ID

	again := &domain.LiveActivity{
		APNSToken:       tok,
		RedditAccountID: "xyz123",
		ThreadID:        "abc987",
		Subreddit:       "apolloapp",
	}
	require.NoError(t, repo.Create(ctx, again))
	assert.Equal(t, firstID, again.ID)
}

func TestPostgresLiveActivity_ShowAvatarsFollowsLatestRegistration(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	repo := NewTestPostgresLiveActivity(t)

	tok := newTestLiveActivityToken(t)
	la := &domain.LiveActivity{
		APNSToken:       tok,
		RedditAccountID: "xyz123",
		ThreadID:        "abc987",
		Subreddit:       "apolloapp",
	}
	require.NoError(t, repo.Create(ctx, la))

	got, err := repo.Get(ctx, tok)
	require.NoError(t, err)
	assert.False(t, got.ShowAvatars, "avatars are opt-in")

	// Re-registering with the setting turned on updates the row in place.
	la.ShowAvatars = true
	require.NoError(t, repo.Create(ctx, la))

	got, err = repo.Get(ctx, tok)
	require.NoError(t, err)
	assert.True(t, got.ShowAvatars)
}

func TestPostgresLiveActivity_RemoveStale(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	repo := NewTestPostgresLiveActivity(t)

	tok := newTestLiveActivityToken(t)
	la := &domain.LiveActivity{
		APNSToken:       tok,
		RedditAccountID: "xyz123",
		ThreadID:        "abc987",
		Subreddit:       "apolloapp",
	}
	require.NoError(t, repo.Create(ctx, la))

	// Cutoff in the past relative to expires_at: row survives.
	require.NoError(t, repo.RemoveStale(ctx, time.Now()))
	_, err := repo.Get(ctx, tok)
	require.NoError(t, err)

	// Cutoff after expires_at: row is reaped.
	require.NoError(t, repo.RemoveStale(ctx, la.ExpiresAt.Add(time.Minute)))
	_, err = repo.Get(ctx, tok)
	assert.Equal(t, domain.ErrNotFound, err)
}
