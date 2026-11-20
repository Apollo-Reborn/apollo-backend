package domain

import (
	"context"
	"time"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	LiveActivityDuration      = 75 * time.Minute // how long an activity stays live before the final "end" push
	LiveActivityCheckInterval = 30 * time.Second // time between Reddit polls for an active activity
)

// LiveActivity tracks a Dynamic Island / Lock Screen activity the app started
// for a Reddit thread. Reddit OAuth tokens are not stored here; the worker
// resolves them through the registered account (RedditAccountID), which also
// carries the per-account Reddit client credentials.
type LiveActivity struct {
	ID int64

	APNSToken   string
	Development bool

	RedditAccountID string

	ThreadID    string
	Subreddit   string
	NextCheckAt time.Time
	ExpiresAt   time.Time

	// ShowAvatars asks the worker to embed the newest comment author's
	// profile picture in each push. The tweak opts in at registration when
	// the user has Show User Profile Pictures on
	// (X-Apollo-Live-Activity-Avatars).
	ShowAvatars bool
}

func (la *LiveActivity) Validate() error {
	return validation.ValidateStruct(la,
		validation.Field(&la.APNSToken, validation.Required, validation.Length(64, 0)),
		validation.Field(&la.RedditAccountID, validation.Required),
		validation.Field(&la.ThreadID, validation.Required),
		validation.Field(&la.Subreddit, validation.Required),
	)
}

// LiveActivityRepository represents the live activity's repository contract
type LiveActivityRepository interface {
	Get(ctx context.Context, apnsToken string) (LiveActivity, error)

	Create(ctx context.Context, la *LiveActivity) error

	RemoveStale(ctx context.Context, before time.Time) error
	Delete(ctx context.Context, apnsToken string) error
}
