package repository

import (
	"context"
	"time"

	"github.com/christianselig/apollo-backend/internal/domain"
)

type postgresLiveActivityRepository struct {
	conn Connection
}

func NewPostgresLiveActivity(conn Connection) domain.LiveActivityRepository {
	return &postgresLiveActivityRepository{conn: conn}
}

func (p *postgresLiveActivityRepository) fetch(ctx context.Context, query string, args ...interface{}) ([]domain.LiveActivity, error) {
	rows, err := p.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var las []domain.LiveActivity
	for rows.Next() {
		var la domain.LiveActivity
		if err := rows.Scan(
			&la.ID,
			&la.APNSToken,
			&la.RedditAccountID,
			&la.ThreadID,
			&la.Subreddit,
			&la.NextCheckAt,
			&la.ExpiresAt,
			&la.Development,
			&la.ShowAvatars,
		); err != nil {
			return nil, err
		}
		las = append(las, la)
	}
	return las, nil
}

func (p *postgresLiveActivityRepository) Get(ctx context.Context, apnsToken string) (domain.LiveActivity, error) {
	query := `
		SELECT id, apns_token, reddit_account_id, thread_id, subreddit, next_check_at, expires_at, development, show_avatars
		FROM live_activities
		WHERE apns_token = $1`

	las, err := p.fetch(ctx, query, apnsToken)

	if err != nil {
		return domain.LiveActivity{}, err
	}
	if len(las) == 0 {
		return domain.LiveActivity{}, domain.ErrNotFound
	}
	return las[0], nil
}

// Create upserts on apns_token: re-registering an existing activity extends
// its lifetime instead of erroring, so app retries are harmless. The avatar
// opt-in follows the newest registration, so it tracks the app's setting.
func (p *postgresLiveActivityRepository) Create(ctx context.Context, la *domain.LiveActivity) error {
	now := time.Now()
	la.NextCheckAt = now
	la.ExpiresAt = now.Add(domain.LiveActivityDuration)

	query := `
		INSERT INTO live_activities (apns_token, reddit_account_id, thread_id, subreddit, next_check_at, expires_at, development, show_avatars)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (apns_token) DO UPDATE SET expires_at = EXCLUDED.expires_at, next_check_at = EXCLUDED.next_check_at, show_avatars = EXCLUDED.show_avatars
		RETURNING id`

	return p.conn.QueryRow(ctx, query,
		la.APNSToken,
		la.RedditAccountID,
		la.ThreadID,
		la.Subreddit,
		la.NextCheckAt,
		la.ExpiresAt,
		la.Development,
		la.ShowAvatars,
	).Scan(&la.ID)
}

func (p *postgresLiveActivityRepository) RemoveStale(ctx context.Context, before time.Time) error {
	query := `DELETE FROM live_activities WHERE expires_at < $1`

	_, err := p.conn.Exec(ctx, query, before)
	return err
}

func (p *postgresLiveActivityRepository) Delete(ctx context.Context, apnsToken string) error {
	query := `DELETE FROM live_activities WHERE apns_token = $1`

	_, err := p.conn.Exec(ctx, query, apnsToken)
	return err
}
