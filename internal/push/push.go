// Package push delivers notification payloads to devices over their
// registered transport. APNs is the historical path; devices registered by
// builds signed without the aps-environment entitlement (free-account
// sideloads) carry a synthetic token and receive notifications as an HTTP
// POST to a Bark push URL instead, with an apollo:// deep link that opens
// Apollo when the Bark notification is tapped.
package push

import (
	"context"
	"net/http"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/payload"
	"github.com/sideshow/apns2/token"
	"go.uber.org/zap"

	"github.com/christianselig/apollo-backend/internal/domain"
)

type Sender struct {
	logger      *zap.Logger
	apnsProd    *apns2.Client
	apnsSandbox *apns2.Client
	topic       string
	httpClient  *http.Client
}

// Result describes one delivery attempt. Sent mirrors apns2's res.Sent();
// Status/Reason carry the rejection details when Sent is false but the
// transport itself worked (the caller decides whether that's log-worthy or a
// 422). ShouldUnregister is true only for APNs failures, preserving the
// historical "failed push means the device is gone or notifications were
// disabled" cleanup. Bark failures never set it: a mistyped key, a rotated
// key, or an unreachable bark-server must not destroy the device row and its
// account/watcher graph.
type Result struct {
	Sent             bool
	ShouldUnregister bool
	Status           int
	Reason           string
}

func NewSender(logger *zap.Logger, key *token.Token, topic string) *Sender {
	return &Sender{
		logger:      logger,
		apnsProd:    apns2.NewTokenClient(key).Production(),
		apnsSandbox: apns2.NewTokenClient(key).Development(),
		topic:       topic,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Send routes the payload to the device's transport. The returned error is
// transport-level (couldn't talk to the gateway); a reachable gateway that
// refused the notification comes back as (Result{Sent: false, ...}, nil),
// matching how apns2 separates Push errors from unsent responses.
func (s *Sender) Send(ctx context.Context, device domain.Device, p *payload.Payload) (Result, error) {
	if device.IsBark() {
		return s.sendBark(ctx, device, p)
	}
	return s.sendAPNS(ctx, device, p)
}

func (s *Sender) sendAPNS(ctx context.Context, device domain.Device, p *payload.Payload) (Result, error) {
	client := s.apnsProd
	if device.Sandbox {
		client = s.apnsSandbox
	}

	notification := &apns2.Notification{
		DeviceToken: device.APNSToken,
		Topic:       s.topic,
		Payload:     p,
	}

	res, err := client.PushWithContext(ctx, notification)
	if err != nil {
		return Result{ShouldUnregister: true}, err
	}
	if !res.Sent() {
		return Result{ShouldUnregister: true, Status: res.StatusCode, Reason: res.Reason}, nil
	}
	return Result{Sent: true, Status: res.StatusCode}, nil
}

// SentMetric/ErrorsMetric name the statsd counters per transport so
// self-hosters can watch Bark delivery health separately from APNs.
func SentMetric(device domain.Device) string {
	if device.IsBark() {
		return "bark.notification.sent"
	}
	return "apns.notification.sent"
}

func ErrorsMetric(device domain.Device) string {
	if device.IsBark() {
		return "bark.notification.errors"
	}
	return "apns.notification.errors"
}
