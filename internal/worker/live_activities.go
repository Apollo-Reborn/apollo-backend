package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DataDog/datadog-go/statsd"
	"github.com/adjust/rmq/v5"
	"github.com/go-redis/redis/v8"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/token"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/christianselig/apollo-backend/internal/avatar"
	"github.com/christianselig/apollo-backend/internal/domain"
	"github.com/christianselig/apollo-backend/internal/reddit"
	"github.com/christianselig/apollo-backend/internal/repository"
)

var liveActivityTags = []string{"queue:live-activities"}

const (
	// maxLiveActivityPayloadBytes is APNs' size limit for a liveactivity
	// push. A larger payload is refused (413), and a refused push deletes the
	// activity (see Consume), so every payload is fitted under it.
	maxLiveActivityPayloadBytes = 4096

	// liveActivityCommentRunes caps the comment text. The Lock Screen shows
	// at most three lines of it and the Dynamic Island one, so the cap never
	// shows, but an uncapped long comment alone can exceed the APNs limit.
	liveActivityCommentRunes = 500

	// liveActivityCommentRunesWithAvatar is how far the comment may be
	// trimmed to keep an avatar in the payload before the avatar is dropped
	// instead (still more text than the Lock Screen shows).
	liveActivityCommentRunesWithAvatar = 200

	// Avatar cache lifetimes. Authors of fresh comments are mostly new each
	// poll, but the top candidate often repeats for several polls in a row.
	liveActivityAvatarTTL      = 24 * time.Hour
	liveActivityAvatarNoneTTL  = 6 * time.Hour    // user has no usable picture
	liveActivityAvatarErrorTTL = 10 * time.Minute // lookup failed; retry later
	liveActivityAvatarNone     = "-"

	// liveActivityAvatarTimeout bounds the whole lookup (profile + image) so
	// a slow Reddit never holds up the push; the push goes out without it.
	liveActivityAvatarTimeout = 5 * time.Second
)

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
	// CommentAuthorAvatar is the comment author's profile picture as a
	// base64-encoded 48x48 JPEG (internal/avatar), present only when the
	// activity opted in. The bytes ride in the push because a Live Activity
	// has no network access to fetch an image URL itself. Apps and widgets
	// that don't know the key ignore it.
	CommentAuthorAvatar string `json:"commentAuthorAvatar,omitempty"`
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

	avatarClient *http.Client
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

		avatar.NewClient(),
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

		if la.ShowAvatars {
			din.CommentAuthorAvatar = lac.commentAuthorAvatar(ctx, rac, comment.Author, logger)
		}
	}

	ev := "update"
	if la.ExpiresAt.Before(now) {
		ev = "end"
	}

	bb, err := liveActivityPayload(din, ev, la.ExpiresAt.Unix(), now.Unix())
	if err != nil {
		logger.Error("failed to encode live activity payload", zap.Error(err))
		return
	}

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

// commentAuthorAvatar returns the author's profile picture as the base64 JPEG
// DynamicIslandNotification carries, or "" when there is none. Results,
// including misses, are cached in Redis so a comment that stays on top for
// several polls costs one lookup. Failures never block the push.
func (lac *liveActivitiesConsumer) commentAuthorAvatar(ctx context.Context, rac *reddit.AuthenticatedClient, author string, logger *zap.Logger) string {
	if author == "" || author == "[deleted]" {
		return ""
	}

	key := fmt.Sprintf("live-activities:avatars:%s", strings.ToLower(author))
	if cached, err := lac.redis.Get(ctx, key).Result(); err == nil {
		_ = lac.statsd.Incr("apollo.live_activities.avatars", []string{"result:cached"}, 0.1)
		if cached == liveActivityAvatarNone {
			return ""
		}
		return cached
	}

	encoded, err := lac.fetchCommentAuthorAvatar(ctx, rac, author)

	value, ttl, result := encoded, liveActivityAvatarTTL, "fetched"
	switch {
	case err != nil:
		logger.Debug("failed to fetch comment author avatar", zap.String("author", author), zap.Error(err))
		value, ttl, result = liveActivityAvatarNone, liveActivityAvatarErrorTTL, "error"
	case encoded == "":
		value, ttl, result = liveActivityAvatarNone, liveActivityAvatarNoneTTL, "none"
	}
	_ = lac.statsd.Incr("apollo.live_activities.avatars", []string{"result:" + result}, 0.1)

	if err := lac.redis.Set(ctx, key, value, ttl).Err(); err != nil {
		logger.Debug("failed to cache comment author avatar", zap.Error(err))
	}
	return encoded
}

func (lac *liveActivitiesConsumer) fetchCommentAuthorAvatar(ctx context.Context, rac *reddit.AuthenticatedClient, author string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, liveActivityAvatarTimeout)
	defer cancel()

	// No retries: a missing or suspended user 404s, and the default backoff
	// schedule would hold the push for seconds.
	user, err := rac.UserAbout(ctx, author, reddit.WithRetry(false), reddit.WithTags([]string{"url:/user/about"}))
	if err != nil {
		return "", err
	}

	src := user.AvatarURL()
	if src == "" {
		return "", nil
	}
	if _, ok := avatar.AllowedURL(src); !ok {
		// Not on Reddit's image hosts; treat as no picture rather than an error.
		return "", nil
	}

	data, err := avatar.Fetch(ctx, lac.avatarClient, src)
	if err != nil {
		return "", err
	}
	thumb, err := avatar.Thumbnail(data)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(thumb), nil
}

// liveActivityPayload encodes the APNs liveactivity payload and keeps it
// under maxLiveActivityPayloadBytes. The comment text is capped first; if the
// payload is still too big, the text is trimmed further while keeping the
// avatar, down to liveActivityCommentRunesWithAvatar; past that the avatar is
// dropped (the words matter more than the picture) and the text is trimmed
// only as far as it has to be.
func liveActivityPayload(din DynamicIslandNotification, event string, dismissal, timestamp int64) ([]byte, error) {
	body := din.CommentBody

	encodeWith := func(runes int) ([]byte, error) {
		d := din
		d.CommentBody = truncateRunes(body, runes)
		return encodeLiveActivityPayload(d, event, dismissal, timestamp)
	}

	bb, err := encodeWith(liveActivityCommentRunes)
	if err != nil || len(bb) <= maxLiveActivityPayloadBytes {
		return bb, err
	}

	if din.CommentAuthorAvatar != "" {
		if bb, ok, err := longestFittingPayload(encodeWith, liveActivityCommentRunesWithAvatar, liveActivityCommentRunes); err != nil || ok {
			return bb, err
		}
		din.CommentAuthorAvatar = ""
	}

	bb, ok, err := longestFittingPayload(encodeWith, 0, liveActivityCommentRunes)
	if err != nil || ok {
		return bb, err
	}
	// Even an empty comment doesn't fit (it can't with a Reddit username);
	// send the smallest payload and let APNs report it.
	return encodeWith(0)
}

// longestFittingPayload binary-searches the largest comment length in
// [lo, hi] whose payload fits, returning ok=false when even lo doesn't.
func longestFittingPayload(encodeWith func(int) ([]byte, error), lo, hi int) ([]byte, bool, error) {
	var best []byte
	for lo <= hi {
		mid := lo + (hi-lo)/2
		bb, err := encodeWith(mid)
		if err != nil {
			return nil, false, err
		}
		if len(bb) <= maxLiveActivityPayloadBytes {
			best = bb
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best, best != nil, nil
}

func encodeLiveActivityPayload(din DynamicIslandNotification, event string, dismissal, timestamp int64) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Comment text is shown as-is by the widget; \u003c-style escapes for
	// <, > and & would only spend payload bytes (6 each instead of 1).
	enc.SetEscapeHTML(false)
	err := enc.Encode(map[string]interface{}{
		"aps": map[string]interface{}{
			"content-state":  din,
			"dismissal-date": dismissal,
			"event":          event,
			"timestamp":      timestamp,
		},
	})
	if err != nil {
		return nil, err
	}
	if buf.Len() == 0 {
		return nil, errors.New("empty live activity payload")
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// truncateRunes shortens s to at most n runes, ending with an ellipsis when
// anything was cut.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	cut := 0
	for i := range s {
		if cut == n {
			return strings.TrimRight(s[:i], " \t\r\n") + "…"
		}
		cut++
	}
	return s
}
