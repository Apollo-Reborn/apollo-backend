package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/christianselig/apollo-backend/internal/domain"
	"github.com/christianselig/apollo-backend/internal/reddit"
)

type accountNotificationsRequest struct {
	InboxNotifications   bool `json:"inbox_notifications"`
	WatcherNotifications bool `json:"watcher_notifications"`
	GlobalMute           bool `json:"global_mute"`
}

func (a *api) notificationsAccountHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	anr := &accountNotificationsRequest{}
	if err := json.NewDecoder(r.Body).Decode(anr); err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	vars := mux.Vars(r)
	apns := vars["apns"]
	rid := vars["redditID"]

	dev, err := a.deviceRepo.GetByAPNSToken(ctx, apns)
	if err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	acct, err := a.accountRepo.GetByRedditID(ctx, rid)
	if err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	if err := a.deviceRepo.SetNotifiable(ctx, &dev, &acct, anr.InboxNotifications, anr.WatcherNotifications, anr.GlobalMute); err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (a *api) getNotificationsAccountHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	vars := mux.Vars(r)
	apns := vars["apns"]
	rid := vars["redditID"]

	dev, err := a.deviceRepo.GetByAPNSToken(ctx, apns)
	if err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	acct, err := a.accountRepo.GetByRedditID(ctx, rid)
	if err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	inbox, watchers, global, err := a.deviceRepo.GetNotifiable(ctx, &dev, &acct)
	if err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	w.WriteHeader(http.StatusOK)

	an := &accountNotificationsRequest{InboxNotifications: inbox, WatcherNotifications: watchers, GlobalMute: global}
	_ = json.NewEncoder(w).Encode(an)
}

func (a *api) disassociateAccountHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	vars := mux.Vars(r)
	apns := vars["apns"]
	rid := vars["redditID"]

	dev, err := a.deviceRepo.GetByAPNSToken(ctx, apns)
	if err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	acct, err := a.accountRepo.GetByRedditID(ctx, rid)
	if err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	if err := a.accountRepo.Disassociate(ctx, &acct, &dev); err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// accountRegistrationRequest is the explicit JSON shape the sideloaded
// Apollo build (e.g. via JeffreyCA's tweak) POSTs at registration time.
// It deliberately differs from domain.Account: counters, message cursors,
// and database IDs are not user-controlled, and the per-account Reddit OAuth
// credentials are mandatory.
type accountRegistrationRequest struct {
	Username     string `json:"username"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"reddit_client_id"`
	ClientSecret string `json:"reddit_client_secret"`
	RedirectURI  string `json:"reddit_redirect_uri"`
	UserAgent    string `json:"reddit_user_agent"`
	Development  bool   `json:"development,omitempty"`
}

// UnmarshalJSON accepts both the snake_case keys our struct documents and
// the camelCase keys Apollo's iOS client actually emits (accessToken /
// refreshToken). Snake_case wins when both appear so the tweak's body
// augmentation can still override.
func (r *accountRegistrationRequest) UnmarshalJSON(data []byte) error {
	type alias accountRegistrationRequest
	aux := struct {
		AccessTokenCamel  string `json:"accessToken"`
		RefreshTokenCamel string `json:"refreshToken"`
		*alias
	}{alias: (*alias)(r)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if r.AccessToken == "" {
		r.AccessToken = aux.AccessTokenCamel
	}
	if r.RefreshToken == "" {
		r.RefreshToken = aux.RefreshTokenCamel
	}
	return nil
}

// fillRedditCredsFromHeader backfills any unset Reddit OAuth fields from the
// X-Apollo-Reddit-* request headers. The tweak sets these (its global-default
// credentials) on every account upsert because its NSData-based body
// augmentation can't reach bodies attached to upload/data tasks via
// `fromData:` — the same reason /v1/device's transport rides in headers.
// Precedence: body fields (per-account overrides) win over headers, headers
// win over the env fallback below.
func (r *accountRegistrationRequest) fillRedditCredsFromHeader(h http.Header) {
	if r.ClientID == "" {
		r.ClientID = h.Get("X-Apollo-Reddit-Client-Id")
	}
	if r.ClientSecret == "" {
		r.ClientSecret = h.Get("X-Apollo-Reddit-Client-Secret")
	}
	if r.RedirectURI == "" {
		r.RedirectURI = h.Get("X-Apollo-Reddit-Redirect-Uri")
	}
	if r.UserAgent == "" {
		r.UserAgent = h.Get("X-Apollo-Reddit-User-Agent")
	}
}

// fillRedditCredsFromEnv backfills any unset Reddit OAuth fields with the
// process-level REDDIT_CLIENT_ID / REDDIT_CLIENT_SECRET / REDDIT_REDIRECT_URI
// / REDDIT_USER_AGENT env vars. Last-resort fallback after body fields and
// the X-Apollo-Reddit-* headers (see fillRedditCredsFromHeader); also lets
// simple single-key deployments skip per-account configuration entirely.
func (r *accountRegistrationRequest) fillRedditCredsFromEnv() {
	if r.ClientID == "" {
		r.ClientID = os.Getenv("REDDIT_CLIENT_ID")
	}
	if r.ClientSecret == "" {
		r.ClientSecret = os.Getenv("REDDIT_CLIENT_SECRET")
	}
	if r.RedirectURI == "" {
		r.RedirectURI = os.Getenv("REDDIT_REDIRECT_URI")
	}
	if r.UserAgent == "" {
		r.UserAgent = os.Getenv("REDDIT_USER_AGENT")
	}
}

func (req *accountRegistrationRequest) toAccount() domain.Account {
	return domain.Account{
		Username:           req.Username,
		AccessToken:        req.AccessToken,
		RefreshToken:       req.RefreshToken,
		Development:        req.Development,
		RedditClientID:     req.ClientID,
		RedditClientSecret: req.ClientSecret,
		RedditRedirectURI:  req.RedirectURI,
		RedditUserAgent:    req.UserAgent,
	}
}

// registerAccount validates the supplied credentials by performing the same
// refresh-and-me dance the original handler did, persists the account, and
// associates it with the device. Shared by both registration handlers.
func (a *api) registerAccount(ctx context.Context, req accountRegistrationRequest, dev *domain.Device) (int, error) {
	acct := req.toAccount()

	creds := reddit.AuthCredentials{
		RedditID:     reddit.SkipRateLimiting,
		RefreshToken: acct.RefreshToken,
		AccessToken:  acct.AccessToken,
		ClientID:     acct.RedditClientID,
		ClientSecret: acct.RedditClientSecret,
		UserAgent:    acct.RedditUserAgent,
	}

	rac := a.reddit.NewAuthenticatedClient(creds)
	tokens, err := rac.RefreshTokens(ctx)
	if err != nil {
		return 422, fmt.Errorf("failed to refresh tokens: %w", err)
	}

	acct.TokenExpiresAt = time.Now().Add(tokens.Expiry)
	acct.RefreshToken = tokens.RefreshToken
	acct.AccessToken = tokens.AccessToken

	creds.RefreshToken = tokens.RefreshToken
	creds.AccessToken = tokens.AccessToken
	rac = a.reddit.NewAuthenticatedClient(creds)

	me, err := rac.Me(ctx)
	if err != nil {
		return 500, fmt.Errorf("failed to fetch user info: %w", err)
	}

	if me.NormalizedUsername() != acct.NormalizedUsername() {
		return 401, fmt.Errorf("wrong user: expected %s, got %s", me.NormalizedUsername(), acct.NormalizedUsername())
	}

	acct.AccountID = me.ID

	mi, err := rac.MessageInbox(ctx, reddit.WithQuery("limit", "1"))
	if err != nil {
		return 500, err
	}

	if mi.Count > 0 {
		acct.LastMessageID = mi.Children[0].FullName()
		acct.CheckCount = 1
	}

	if err := a.accountRepo.CreateOrUpdate(ctx, &acct); err != nil {
		return 422, err
	}

	if err := a.accountRepo.Associate(ctx, &acct, dev); err != nil {
		return 422, err
	}

	return http.StatusOK, nil
}

func (a *api) upsertAccountsHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	vars := mux.Vars(r)
	apns := vars["apns"]

	dev, err := a.deviceRepo.GetByAPNSToken(ctx, apns)
	if err != nil {
		a.errorResponse(w, r, 422, err)
		return
	}

	laccs, err := a.accountRepo.GetByAPNSToken(ctx, apns)
	if err != nil {
		a.errorResponse(w, r, 422, err)
		return
	}

	accsMap := map[string]domain.Account{}
	for _, acc := range laccs {
		accsMap[acc.NormalizedUsername()] = acc
	}

	var reqs []accountRegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&reqs); err != nil {
		a.errorResponse(w, r, 422, err)
		return
	}

	for i, req := range reqs {
		delete(accsMap, strings.ToLower(req.Username))
		req.fillRedditCredsFromHeader(r.Header)
		req.fillRedditCredsFromEnv()

		// Defensive: registerAccount → NewAuthenticatedClient panics on empty
		// tokens. Return a clean 422 with diagnostics if anything's still
		// missing after the header/env backfills.
		if req.AccessToken == "" || req.RefreshToken == "" || req.ClientID == "" {
			a.logger.Error("upsertAccounts missing credentials",
				zap.Int("index", i),
				zap.String("username", req.Username),
				zap.Bool("has_access_token", req.AccessToken != ""),
				zap.Bool("has_refresh_token", req.RefreshToken != ""),
				zap.Bool("has_client_id", req.ClientID != ""),
			)
			a.errorResponse(w, r, 422, fmt.Errorf("account %d (%q) missing required credentials in request body", i, req.Username))
			return
		}

		if status, err := a.registerAccount(ctx, req, &dev); err != nil {
			a.errorResponse(w, r, status, err)
			return
		}
	}

	for _, acc := range accsMap {
		_ = a.accountRepo.Disassociate(ctx, &acc, &dev)
	}

	w.WriteHeader(http.StatusOK)
}

func (a *api) upsertAccountHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	vars := mux.Vars(r)

	var req accountRegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		a.logger.Error("failed to parse request json", zap.Error(err))
		a.errorResponse(w, r, 422, err)
		return
	}

	dev, err := a.deviceRepo.GetByAPNSToken(ctx, vars["apns"])
	if err != nil {
		a.logger.Error("failed to fetch device from database", zap.Error(err))
		a.errorResponse(w, r, 500, err)
		return
	}

	req.fillRedditCredsFromHeader(r.Header)
	req.fillRedditCredsFromEnv()

	// Defensive: registerAccount → NewAuthenticatedClient panics on empty
	// tokens, same as the bulk handler above.
	if req.AccessToken == "" || req.RefreshToken == "" || req.ClientID == "" {
		a.logger.Error("upsertAccount missing credentials",
			zap.String("username", req.Username),
			zap.Bool("has_access_token", req.AccessToken != ""),
			zap.Bool("has_refresh_token", req.RefreshToken != ""),
			zap.Bool("has_client_id", req.ClientID != ""),
		)
		a.errorResponse(w, r, 422, fmt.Errorf("account %q missing required credentials in request body", req.Username))
		return
	}

	if status, err := a.registerAccount(ctx, req, &dev); err != nil {
		a.logger.Error("failed to register account", zap.Error(err))
		a.errorResponse(w, r, status, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}
