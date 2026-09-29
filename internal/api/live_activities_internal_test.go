package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLiveActivityAvatarsRequested(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		body   bool
		want   bool
	}{
		{name: "no header, no body flag", want: false},
		{name: "body flag only", body: true, want: true},
		{name: "header on", header: "1", want: true},
		{name: "header true", header: "TRUE", want: true},
		{name: "header off wins over body", header: "0", body: true, want: false},
		{name: "header garbage is off", header: "yes please", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequest("POST", "/v1/live_activities", strings.NewReader("{}"))
			if tc.header != "" {
				r.Header.Set(liveActivityAvatarsHeader, tc.header)
			}
			req := &liveActivityRequest{ShowAvatars: tc.body}
			assert.Equal(t, tc.want, liveActivityAvatarsRequested(r, req))
		})
	}
}
