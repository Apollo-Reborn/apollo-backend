package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"go.uber.org/zap"

	"github.com/christianselig/apollo-backend/internal/domain"
)

// liveActivityRequest is the payload Apollo posts when it starts a Live
// Activity for a thread. The OAuth tokens it carries are accepted but unused:
// the worker polls Reddit through the registered account row (which also
// holds the per-account Reddit client credentials), so the account is the
// single source of truth for tokens.
type liveActivityRequest struct {
	APNSToken       string `json:"apns_token"`
	RedditAccountID string `json:"reddit_account_id"`
	AccessToken     string `json:"access_token"`
	RefreshToken    string `json:"refresh_token"`
	ThreadID        string `json:"thread_id"`
	Subreddit       string `json:"subreddit"`
	Development     bool   `json:"development"`
	SandboxReceipt  string `json:"sandboxReceipt"`
}

// UnmarshalJSON accepts both snake_case and the camelCase token keys Apollo's
// iOS client emits elsewhere, mirroring accountRegistrationRequest.
func (r *liveActivityRequest) UnmarshalJSON(data []byte) error {
	type alias liveActivityRequest
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

func (a *api) createLiveActivityHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	req := &liveActivityRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		a.errorResponse(w, r, 400, err)
		return
	}

	// Live Activity pushes go straight to APNs (no Bark path exists), so a
	// Bark-only backend rejects the registration up front — this is what
	// keeps live_activities rows from piling up in that mode.
	if a.apns == nil {
		a.errorResponse(w, r, 422, fmt.Errorf("Live Activities require APNs, which this backend has not configured (Bark-only mode)"))
		return
	}

	// Apollo stores accounts by bare Reddit ID (me.ID); tolerate a t2_
	// fullname in case the client sends one.
	rid := strings.TrimPrefix(req.RedditAccountID, "t2_")

	if _, err := a.accountRepo.GetByRedditID(ctx, rid); err != nil {
		a.logger.Info("live activity registration for unknown account",
			zap.String("account#reddit_account_id", req.RedditAccountID),
			zap.Error(err),
		)
		a.errorResponse(w, r, 422, fmt.Errorf("account not registered with this backend; open Apollo with notifications configured first"))
		return
	}

	// Same gateway-selection logic as device registration: the client's flag
	// is a hint, APPLE_APNS_SANDBOX pins it to match the build's signing.
	dev := req.Development || req.SandboxReceipt != ""
	if v := os.Getenv("APPLE_APNS_SANDBOX"); v != "" {
		dev = v == "1" || strings.EqualFold(v, "true")
	}

	la := &domain.LiveActivity{
		APNSToken:       req.APNSToken,
		Development:     dev,
		RedditAccountID: rid,
		ThreadID:        req.ThreadID,
		Subreddit:       req.Subreddit,
	}

	if err := la.Validate(); err != nil {
		a.errorResponse(w, r, 422, err)
		return
	}

	if err := a.liveActivityRepo.Create(ctx, la); err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}
