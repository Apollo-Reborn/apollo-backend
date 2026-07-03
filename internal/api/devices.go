package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/dustin/go-humanize/english"
	"github.com/gorilla/mux"
	"github.com/sideshow/apns2/payload"
	"go.uber.org/zap"

	"github.com/christianselig/apollo-backend/internal/domain"
)

const notificationTitle = "📣 Hello, is this thing on?"

func (a *api) upsertDeviceHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	d := &domain.Device{}
	if err := json.NewDecoder(r.Body).Decode(d); err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	// Apollo's release-signed App Store binary sent sandbox=false because it
	// always used production APNs. A sideloaded build signed under a paid
	// developer account, in contrast, gets sandbox APNs tokens — and
	// `apns2.NewTokenClient` defaults to sandbox unless told otherwise,
	// matched against this flag. APPLE_APNS_SANDBOX overrides whatever the
	// client sent so a self-host can pin the gateway to match its signing.
	if v := os.Getenv("APPLE_APNS_SANDBOX"); v != "" {
		d.Sandbox = v == "1" || strings.EqualFold(v, "true")
	}

	// The Apollo-Reborn tweak sends the transport in headers as well as the
	// body: Apollo posts /v1/device as an NSURLSession upload task whose body
	// is attached outside the request object, so the tweak's body rewrite
	// doesn't always reach the wire — headers reliably do. Headers win when
	// present.
	if t := r.Header.Get("X-Apollo-Transport"); t != "" {
		d.Transport = t
		d.TransportEndpoint = r.Header.Get("X-Apollo-Transport-Endpoint")
	}

	// Older clients send no transport field — treat them as plain APNs.
	// CreateOrUpdate historically skipped Validate(), so run it here: it
	// rejects unknown transports and bark registrations without a usable
	// http(s) push endpoint.
	if d.Transport == "" {
		d.Transport = domain.DeviceTransportAPNS
	}
	if err := d.Validate(); err != nil {
		a.errorResponse(w, r, 422, err)
		return
	}

	// A Bark-only backend (no APPLE_* config) can never deliver to an APNs
	// device; reject at registration so the misconfiguration is visible in
	// the app instead of as silent per-send failures in the worker logs.
	if !d.IsBark() && a.apns == nil {
		a.errorResponse(w, r, 422, fmt.Errorf("this backend runs in Bark-only mode (APNs not configured); set a Bark push URL in Apollo's settings and re-register"))
		return
	}

	if err := a.deviceRepo.CreateOrUpdate(ctx, d); err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (a *api) testDeviceHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	vars := mux.Vars(r)
	tok := vars["apns"]

	d, err := a.deviceRepo.GetByAPNSToken(ctx, tok)
	if err != nil {
		a.logger.Error("failed to fetch device from database", zap.Error(err))
		a.errorResponse(w, r, 500, err)
		return
	}

	accs, err := a.accountRepo.GetByAPNSToken(ctx, tok)
	if err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	users := make([]string, len(accs))
	for i := range accs {
		users[i] = accs[i].Username
	}

	body := fmt.Sprintf("Active usernames are: %s. Tap me for more info!", english.OxfordWordSeries(users, "and"))
	p := payload.
		NewPayload().
		Category("test-notification").
		Custom("test_accounts", strings.Join(users, ",")).
		AlertTitle(notificationTitle).
		AlertBody(body).
		MutableContent().
		Sound("traloop.wav")

	res, err := a.sender.Send(ctx, d, p)
	if err != nil {
		a.logger.Info("failed to send test notification", zap.Error(err))
		a.errorResponse(w, r, 500, err)
	} else if !res.Sent {
		a.errorResponse(w, r, 422, fmt.Errorf("errror sending notification: %d: %s", res.Status, res.Reason))
	} else {
		w.WriteHeader(http.StatusOK)
	}
}

func (a *api) deleteDeviceHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	vars := mux.Vars(r)

	dev, err := a.deviceRepo.GetByAPNSToken(ctx, vars["apns"])
	if err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	accs, err := a.accountRepo.GetByAPNSToken(ctx, vars["apns"])
	if err != nil {
		a.errorResponse(w, r, 500, err)
		return
	}

	for _, acc := range accs {
		_ = a.accountRepo.Disassociate(ctx, &acc, &dev)
	}

	_ = a.deviceRepo.Delete(ctx, vars["apns"])

	w.WriteHeader(http.StatusOK)
}
