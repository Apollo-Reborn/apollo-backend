package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/DataDog/datadog-go/statsd"
	"github.com/adjust/rmq/v5"
	"github.com/go-redis/redis/v8"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/token"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/christianselig/apollo-backend/internal/domain"
	"github.com/christianselig/apollo-backend/internal/reddit"
	"github.com/christianselig/apollo-backend/internal/repository"
)

var liveActivityTags = []string{"queue:live-activities"}

// DynamicIslandNotification is the ActivityKit content-state Apollo's
// FollowThreadActivityAttributes decodes from liveactivity pushes.
type DynamicIslandNotification struct {
	PostCommentCount int    `json:"postTotalComments"`
	PostScore        int64  `json:"postScore"`
	CommentID        string `json:"commentId,omitempty"`
	CommentAuthor    string `json:"commentAuthor,omitempty"`
	CommentBody      string `json:"commentBody,omitempty"`
	CommentAge       int64  `json:"commentAge,omitempty"`
	CommentScore     int64  `json:"commentScore,omitempty"`
}

type liveActivitiesWorker struct {
	context.Context

	logger *zap.Logger
	tracer trace.Tracer
	statsd statsd.ClientInterface
	db     *pgxpool.Pool
	redis  *redis.Client
	queue  rmq.Connection
	reddit *reddit.Client
	apns   *token.Token
	// liveActivityTopic is "<bundle id>.push-type.liveactivity", derived from
	// APPLE_APNS_TOPIC so rebranded builds get the right topic.
	liveActivityTopic string

	consumers int

	liveActivityRepo domain.LiveActivityRepository
	accountRepo      domain.AccountRepository
}

func NewLiveActivitiesWorker(ctx context.Context, logger *zap.Logger, tracer trace.Tracer, statsd statsd.ClientInterface, db *pgxpool.Pool, redis *redis.Client, queue rmq.Connection, consumers int, apns *token.Token, apnsTopic string) Worker {
	reddit := reddit.NewClient(
		tracer,
		statsd,
		redis,
		consumers,
	)

	return &liveActivitiesWorker{
		ctx,
		logger,
		tracer,
		statsd,
		db,
		redis,
		queue,
		reddit,
		apns,
		apnsTopic + ".push-type.liveactivity",
		consumers,

		repository.NewPostgresLiveActivity(db),
		repository.NewPostgresAccount(db),
	}
}

func (law *liveActivitiesWorker) Start() error {
	queue, err := law.queue.OpenQueue("live-activities")
	if err != nil {
		return err
	}

	law.logger.Info("starting up live activities worker",
		zap.Int("consumers", law.consumers),
		zap.Bool("apns_enabled", law.apns != nil),
	)

	prefetchLimit := int64(law.consumers * 4)

	if err := queue.StartConsuming(prefetchLimit, pollDuration); err != nil {
		return err
	}

	host, _ := os.Hostname()

	for i := 0; i < law.consumers; i++ {
		name := fmt.Sprintf("consumer %s-%d", host, i)

		consumer := NewLiveActivitiesConsumer(law, i)
		if _, err := queue.AddConsumer(name, consumer); err != nil {
			return err
		}
	}

	return nil
}

func (law *liveActivitiesWorker) Stop() {
	<-law.queue.StopAllConsuming() // wait for all Consume() calls to finish
}

type liveActivitiesConsumer struct {
	*liveActivitiesWorker
	tag int

	papns *apns2.Client
	dapns *apns2.Client
}

func NewLiveActivitiesConsumer(law *liveActivitiesWorker, tag int) *liveActivitiesConsumer {
	lac := &liveActivitiesConsumer{
		liveActivitiesWorker: law,
		tag:                  tag,
	}
	// Bark-only mode (nil token): leave the clients nil. Live Activities are
	// APNs-only, so Consume drops jobs instead of pushing.
	if law.apns != nil {
		lac.papns = apns2.NewTokenClient(law.apns).Production()
		lac.dapns = apns2.NewTokenClient(law.apns).Development()
	}
	return lac
}

func (lac *liveActivitiesConsumer) Consume(delivery rmq.Delivery) {
	ctx, cancel := context.WithCancel(lac)
	defer cancel()

	now := time.Now()
	defer func() {
		elapsed := time.Now().Sub(now).Milliseconds()
		_ = lac.statsd.Histogram("apollo.consumer.runtime", float64(elapsed), liveActivityTags, 0.1)
	}()

	at := delivery.Payload()
	logger := lac.logger.With(zap.String("live_activity#apns_token", at))
	key := fmt.Sprintf("locks:live-activities:%s", at)

	defer func() {
		if err := delivery.Ack(); err != nil {
			logger.Error("failed to acknowledge message", zap.Error(err))
		}
	}()

	// Measure queue latency
	ttl := lac.redis.PTTL(ctx, key).Val()
	if ttl == 0 {
		logger.Debug("job is too old, skipping")
		return
	}
	age := (domain.NotificationCheckTimeout - ttl)
	_ = lac.statsd.Histogram("apollo.dequeue.latency", float64(age.Milliseconds()), liveActivityTags, 0.1)

	defer func() {
		if err := lac.redis.Del(ctx, key).Err(); err != nil {
			logger.Error("failed to remove live activity lock", zap.Error(err), zap.String("key", key))
		}
	}()

	logger.Debug("starting job")

	// Live Activities require APNs; a Bark-only backend can never deliver
	// them. Delete the row so the scheduler stops re-enqueueing it every
	// batch (registration is also rejected with a 422 in this mode — this
	// catches rows that predate the mode switch).
	if lac.apns == nil {
		logger.Warn("APNs disabled (Bark-only mode); dropping live activity")
		_ = lac.liveActivityRepo.Delete(ctx, at)
		return
	}

	la, err := lac.liveActivityRepo.Get(ctx, at)
	if err != nil {
		logger.Error("failed to get live activity", zap.Error(err))
		return
	}

	account, err := lac.accountRepo.GetByRedditID(ctx, la.RedditAccountID)
	if err != nil {
		logger.Info("live activity has no registered account, deleting",
			zap.Error(err),
			zap.String("account#reddit_account_id", la.RedditAccountID),
		)
		_ = lac.liveActivityRepo.Delete(ctx, at)
		return
	}

	rac := lac.reddit.NewAuthenticatedClient(reddit.AuthCredentials{RedditID: account.AccountID, RefreshToken: account.RefreshToken, AccessToken: account.AccessToken, ClientID: account.RedditClientID, ClientSecret: account.RedditClientSecret, UserAgent: account.RedditUserAgent})
	logger = logger.With(
		zap.String("account#reddit_account_id", la.RedditAccountID),
		zap.String("account#access_token", rac.ObfuscatedAccessToken()),
		zap.String("account#refresh_token", rac.ObfuscatedRefreshToken()),
	)
	if account.TokenExpiresAt.Before(now.Add(5 * time.Minute)) {
		logger.Debug("refreshing reddit token")

		tokens, err := rac.RefreshTokens(ctx)
		if err != nil {
			logger.Error("failed to refresh reddit tokens", zap.Error(err))
			if err == reddit.ErrOauthRevoked {
				_ = lac.liveActivityRepo.Delete(ctx, at)
			}
			return
		}

		// Update account. The notifications consumer may race this write for
		// the same account; Reddit returns a stable refresh token for
		// permanent grants, so last-writer-wins is benign.
		account.AccessToken = tokens.AccessToken
		account.RefreshToken = tokens.RefreshToken
		account.TokenExpiresAt = now.Add(tokens.Expiry)
		_ = lac.accountRepo.Update(ctx, &account)

		// Refresh client
		rac = lac.reddit.NewAuthenticatedClient(reddit.AuthCredentials{RedditID: account.AccountID, RefreshToken: tokens.RefreshToken, AccessToken: tokens.AccessToken, ClientID: account.RedditClientID, ClientSecret: account.RedditClientSecret, UserAgent: account.RedditUserAgent})
	}

	logger.Debug("fetching latest comments")

	tr, err := rac.TopLevelComments(ctx, la.Subreddit, la.ThreadID)
	if err != nil {
		logger.Error("failed to fetch latest comments", zap.Error(err))
		if err == reddit.ErrOauthRevoked {
			_ = lac.liveActivityRepo.Delete(ctx, at)
		}
		return
	}

	if len(tr.Children) == 0 && la.ExpiresAt.After(now) {
		logger.Debug("no comments found")
		return
	}

	// Look for fresh comments, widening the window until something turns up.
	candidates := make([]*reddit.Thing, 0)
	cutoffs := []time.Time{
		now.Add(-domain.LiveActivityCheckInterval),
		now.Add(-domain.LiveActivityCheckInterval * 2),
		now.Add(-domain.LiveActivityCheckInterval * 4),
	}

	for _, cutoff := range cutoffs {
		for _, t := range tr.Children {
			if t.CreatedAt.After(cutoff) {
				candidates = append(candidates, t)
			}
		}

		if len(candidates) > 0 {
			break
		}
	}

	if len(candidates) == 0 && la.ExpiresAt.After(now) {
		logger.Debug("no new comments found")
		return
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	din := DynamicIslandNotification{
		PostCommentCount: tr.Post.NumComments,
		PostScore:        tr.Post.Score,
	}

	if len(candidates) > 0 {
		comment := candidates[0]

		din.CommentID = comment.ID
		din.CommentAuthor = comment.Author
		din.CommentBody = comment.Body
		din.CommentAge = comment.CreatedAt.Unix()
		din.CommentScore = comment.Score
	}

	ev := "update"
	if la.ExpiresAt.Before(now) {
		ev = "end"
	}

	bb, _ := json.Marshal(map[string]interface{}{
		"aps": map[string]interface{}{
			"content-state":  din,
			"dismissal-date": la.ExpiresAt.Unix(),
			"event":          ev,
			"timestamp":      now.Unix(),
		},
	})

	notification := &apns2.Notification{
		DeviceToken: la.APNSToken,
		Topic:       lac.liveActivityTopic,
		PushType:    "liveactivity",
		Payload:     bb,
	}

	client := lac.papns
	if la.Development {
		client = lac.dapns
	}

	res, err := client.PushWithContext(ctx, notification)
	if err != nil {
		_ = lac.statsd.Incr("apns.live_activities.errors", []string{}, 1)
		logger.Error("failed to send notification",
			zap.Error(err),
			zap.Bool("live_activity#development", la.Development),
			zap.String("notification#type", ev),
		)

		_ = lac.liveActivityRepo.Delete(ctx, at)
	} else if !res.Sent() {
		_ = lac.statsd.Incr("apns.live_activities.errors", []string{}, 1)
		logger.Error("notification not sent",
			zap.Bool("live_activity#development", la.Development),
			zap.String("notification#type", ev),
			zap.Int("response#status", res.StatusCode),
			zap.String("response#reason", res.Reason),
		)

		_ = lac.liveActivityRepo.Delete(ctx, at)
	} else {
		_ = lac.statsd.Incr("apns.notification.sent", []string{}, 1)
		logger.Debug("sent notification",
			zap.Bool("live_activity#development", la.Development),
			zap.String("notification#type", ev),
		)
	}

	if la.ExpiresAt.Before(now) {
		logger.Debug("live activity expired, deleting")
		_ = lac.liveActivityRepo.Delete(ctx, at)
	}

	logger.Debug("finishing job")
}
