package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataDog/datadog-go/statsd"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/christianselig/apollo-backend/internal/domain"
)

// Apollo sends account-scoped requests with an empty Reddit ID
// (`/account//notifications`) when its local account record has none; after
// path cleaning that used to hit the DELETE-only `/account/{redditID}` route
// and 405. These stubs record which Reddit ID the handlers end up using.

type stubAccountRepo struct {
	domain.AccountRepository
	accounts []domain.Account
}

func (s *stubAccountRepo) GetByAPNSToken(context.Context, string) ([]domain.Account, error) {
	return s.accounts, nil
}

func (s *stubAccountRepo) GetByRedditID(_ context.Context, id string) (domain.Account, error) {
	for _, acc := range s.accounts {
		if acc.AccountID == id {
			return acc, nil
		}
	}
	return domain.Account{}, domain.ErrNotFound
}

type stubDeviceRepo struct {
	domain.DeviceRepository
	setFor string
}

func (s *stubDeviceRepo) GetByAPNSToken(_ context.Context, token string) (domain.Device, error) {
	return domain.Device{APNSToken: token}, nil
}

func (s *stubDeviceRepo) GetNotifiable(context.Context, *domain.Device, *domain.Account) (bool, bool, bool, error) {
	return true, true, false, nil
}

func (s *stubDeviceRepo) SetNotifiable(_ context.Context, _ *domain.Device, acct *domain.Account, _, _, _ bool) error {
	s.setFor = acct.AccountID
	return nil
}

type stubWatcherRepo struct {
	domain.WatcherRepository
	listedFor string
}

func (s *stubWatcherRepo) GetByDeviceAPNSTokenAndAccountRedditID(_ context.Context, _ string, rid string) ([]domain.Watcher, error) {
	s.listedFor = rid
	return nil, nil
}

func newRoutesTestAPI(accounts ...domain.Account) (*api, *stubDeviceRepo, *stubWatcherRepo) {
	devs := &stubDeviceRepo{}
	watchers := &stubWatcherRepo{}
	return &api{
		logger:      zap.NewNop(),
		statsd:      &statsd.NoOpClient{},
		accountRepo: &stubAccountRepo{accounts: accounts},
		deviceRepo:  devs,
		watcherRepo: watchers,
	}, devs, watchers
}

func serve(a *api, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	a.Routes().ServeHTTP(rec, req)
	return rec
}

func TestRoutes_MissingRedditIDFallsBackToSoleAccount(t *testing.T) {
	t.Parallel()

	a, devs, watchers := newRoutesTestAPI(domain.Account{AccountID: "646p2"})

	rec := serve(a, http.MethodGet, "/v1/device/abc/account/notifications", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"inbox_notifications":true,"watcher_notifications":true,"global_mute":false}`, rec.Body.String())

	rec = serve(a, http.MethodPatch, "/v1/device/abc/account/notifications", `{"inbox_notifications":true}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "646p2", devs.setFor)

	rec = serve(a, http.MethodGet, "/v1/device/abc/account/watchers", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "646p2", watchers.listedFor)
}

func TestRoutes_MissingRedditIDIsAmbiguousWithoutExactlyOneAccount(t *testing.T) {
	t.Parallel()

	for name, accounts := range map[string][]domain.Account{
		"none": nil,
		"two":  {{AccountID: "646p2"}, {AccountID: "abc12"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			a, _, _ := newRoutesTestAPI(accounts...)

			rec := serve(a, http.MethodGet, "/v1/device/abc/account/notifications", "")
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestRoutes_ExplicitRedditIDStillWins(t *testing.T) {
	t.Parallel()

	a, _, watchers := newRoutesTestAPI(domain.Account{AccountID: "646p2"}, domain.Account{AccountID: "abc12"})

	rec := serve(a, http.MethodGet, "/v1/device/abc/account/abc12/watchers", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "abc12", watchers.listedFor)
}

func TestRoutes_MethodMismatchStill405s(t *testing.T) {
	t.Parallel()

	a, _, _ := newRoutesTestAPI(domain.Account{AccountID: "646p2"})

	rec := serve(a, http.MethodPut, "/v1/device/abc/account/notifications", "")
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
