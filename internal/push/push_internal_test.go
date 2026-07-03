package push

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sideshow/apns2/payload"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/christianselig/apollo-backend/internal/domain"
)

// Bark-only mode: NewSender must tolerate a nil APNs token (cmdutil.LoadAPNS
// returns one when no APPLE_* vars are set) without panicking, and APNs sends
// must fail with a plain error — critically, NOT ShouldUnregister, which
// would make the notifications worker delete the device row.

func TestNewSender_NilTokenBarkOnly(t *testing.T) {
	t.Parallel()

	s := NewSender(zap.NewNop(), nil, "")
	require.NotNil(t, s)
	assert.Nil(t, s.apnsProd)
	assert.Nil(t, s.apnsSandbox)
}

func TestSendAPNS_NilClientDoesNotUnregister(t *testing.T) {
	t.Parallel()

	s := NewSender(zap.NewNop(), nil, "")

	for _, sandbox := range []bool{false, true} {
		d := domain.Device{Transport: domain.DeviceTransportAPNS, APNSToken: "abc", Sandbox: sandbox}

		res, err := s.Send(t.Context(), d, payload.NewPayload().AlertTitle("hi"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "APNs not configured")
		assert.False(t, res.Sent)
		assert.False(t, res.ShouldUnregister)
	}
}

func TestSend_BarkStillWorksWithNilToken(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"message":"success"}`))
	}))
	defer srv.Close()

	s := NewSender(zap.NewNop(), nil, "")
	s.httpClient = srv.Client()
	d := domain.Device{Transport: domain.DeviceTransportBark, TransportEndpoint: srv.URL}

	res, err := s.Send(t.Context(), d, payload.NewPayload().AlertTitle("hi"))
	require.NoError(t, err)
	assert.True(t, res.Sent)
}
