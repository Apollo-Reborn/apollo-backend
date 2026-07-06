package push

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sideshow/apns2/payload"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/christianselig/apollo-backend/internal/domain"
)

// The sample payloads mirror the per-category test notifications in
// internal/api/notifications.go, which themselves mirror what the workers
// build in production.

func TestBarkRequestFromPayload_CommentReply(t *testing.T) {
	t.Parallel()

	p := payload.NewPayload().
		MutableContent().
		Sound("traloop.wav").
		AlertTitle("Equinox_Shift in Protests set to disrupt Ottawa's downtown for 3rd straight weekend").
		AlertBody("They don't even go here.").
		Category("inbox-comment-reply").
		Custom("account_id", "1ia22").
		Custom("author", "Equinox_Shift").
		Custom("comment_id", "hwp66zg").
		Custom("post_id", "sqqk29").
		Custom("subreddit", "ottawa").
		Custom("type", "comment").
		ThreadID("comment")

	req, err := barkRequestFromPayload(p)
	require.NoError(t, err)

	assert.Equal(t, "Equinox_Shift in Protests set to disrupt Ottawa's downtown for 3rd straight weekend", req.Title)
	assert.Equal(t, "They don't even go here.", req.Body)
	assert.Equal(t, "apollo://reddit.com/r/ottawa/comments/sqqk29/_/hwp66zg/?context=1", req.URL)
	assert.Equal(t, "comment", req.Group)
	// "traloop.wav" minus the extension: matches assets/bark-sounds/
	// traloop.caf once bark-server appends ".caf".
	assert.Equal(t, "traloop", req.Sound)
}

func TestBarkRequestFromPayload_NoSound(t *testing.T) {
	t.Parallel()

	req, err := barkRequestFromPayload(payload.NewPayload().AlertTitle("Quiet"))
	require.NoError(t, err)
	assert.Empty(t, req.Sound)

	req, err = barkRequestFromPayload(payload.NewPayload().AlertTitle("Stock").Sound("default"))
	require.NoError(t, err)
	assert.Empty(t, req.Sound)
}

func TestBarkRequestFromPayload_PrivateMessage(t *testing.T) {
	t.Parallel()

	p := payload.NewPayload().
		MutableContent().
		Sound("traloop.wav").
		AlertTitle("Message from welcomebot").
		AlertSubtitle("Welcome to r/GriefSupport!").
		AlertBody("**Welcome to r/GriefSupport!**").
		Category("inbox-private-message").
		Custom("account_id", "1ia22").
		Custom("author", "welcomebot").
		Custom("comment_id", "1d2oouy").
		Custom("subreddit", "").
		Custom("type", "private-message")

	req, err := barkRequestFromPayload(p)
	require.NoError(t, err)

	assert.Equal(t, "Message from welcomebot", req.Title)
	assert.Equal(t, "Welcome to r/GriefSupport!", req.Subtitle)
	assert.Equal(t, "apollo://reborn/inbox", req.URL)
	// No thread-id set; category is the grouping fallback.
	assert.Equal(t, "inbox-private-message", req.Group)
}

func TestBarkRequestFromPayload_SubredditWatcher(t *testing.T) {
	t.Parallel()

	p := payload.NewPayload().
		MutableContent().
		Sound("traloop.wav").
		AlertTitle("📣 “bug pics” Watcher").
		AlertBody("r/pics: “A Goliath Stick Insect.”").
		AlertSummaryArg("pics").
		Category("subreddit-watcher").
		Custom("author", "befarked247").
		Custom("post_age", 1651409659.0).
		Custom("post_id", "ufzaml").
		Custom("post_title", "A Goliath Stick Insect.").
		Custom("subreddit", "pics").
		Custom("thumbnail", "https://a.thumbs.redditmedia.com/Lr4b.jpg").
		ThreadID("subreddit-watcher")

	req, err := barkRequestFromPayload(p)
	require.NoError(t, err)

	assert.Equal(t, "📣 “bug pics” Watcher", req.Title)
	assert.Equal(t, "apollo://reddit.com/r/pics/comments/ufzaml", req.URL)
	assert.Equal(t, "subreddit-watcher", req.Group)
	assert.Equal(t, "https://a.thumbs.redditmedia.com/Lr4b.jpg", req.Icon)
}

func TestBarkRequestFromPayload_UsernameMention(t *testing.T) {
	t.Parallel()

	p := payload.NewPayload().
		AlertTitle("Mention in “testimg”").
		AlertBody("yo u/changelog what's good").
		Category("inbox-username-mention-no-context").
		Custom("comment_id", "i6xobpa").
		Custom("post_id", "u02338").
		Custom("subreddit", "calicosummer").
		Custom("type", "username")

	req, err := barkRequestFromPayload(p)
	require.NoError(t, err)

	assert.Equal(t, "apollo://reddit.com/r/calicosummer/comments/u02338/_/i6xobpa/?context=1", req.URL)
}

func TestBarkRequestFromPayload_Badge(t *testing.T) {
	t.Parallel()

	p := payload.NewPayload().
		AlertTitle("Message from someone").
		AlertBody("hi").
		Badge(3).
		Custom("type", "private-message")

	req, err := barkRequestFromPayload(p)
	require.NoError(t, err)

	require.NotNil(t, req.Badge)
	assert.Equal(t, 3, *req.Badge)
}

func TestBarkRequestFromPayload_TestBlastFallsBackToInbox(t *testing.T) {
	t.Parallel()

	// The api's testDeviceHandler payload has no post_id/subreddit customs.
	p := payload.NewPayload().
		Category("test-notification").
		Custom("test_accounts", "changelog").
		AlertTitle("📣 Hello, is this thing on?").
		AlertBody("Active usernames are: changelog. Tap me for more info!").
		MutableContent().
		Sound("traloop.wav")

	req, err := barkRequestFromPayload(p)
	require.NoError(t, err)

	assert.Equal(t, "apollo://reborn/inbox", req.URL)
	assert.Equal(t, "test-notification", req.Group)
}

func TestClickURL_EscapesPathComponents(t *testing.T) {
	t.Parallel()

	got := clickURL(map[string]interface{}{
		"post_id":   "abc123",
		"subreddit": "r weird/name",
	})
	assert.Equal(t, "apollo://reddit.com/r/r%20weird%2Fname/comments/abc123", got)
}

// The slug placeholder must be "_" — Apollo's link parser captures the
// comment id as (\w+) after an optional (?:/\w+)? slug segment, and "-" is
// not a \w character, so a /-/ link silently opens the post unanchored.
func TestClickURL_CommentAnchorUsesUnderscoreSlug(t *testing.T) {
	t.Parallel()

	got := clickURL(map[string]interface{}{
		"post_id":    "1um41tv",
		"subreddit":  "ApolloReborn",
		"comment_id": "ov9d35z",
	})
	assert.Equal(t, "apollo://reddit.com/r/ApolloReborn/comments/1um41tv/_/ov9d35z/?context=1", got)
	assert.NotContains(t, got, "/-/")
}

func TestBarkRequestFromPayload_EmptyBodyFallsBackToTitle(t *testing.T) {
	t.Parallel()

	p := payload.NewPayload().AlertTitle("Only a title")

	req, err := barkRequestFromPayload(p)
	require.NoError(t, err)

	assert.Equal(t, "Only a title", req.Body)
}

// sendBark fills in the default icon only when the payload carried no post
// thumbnail — PMs and comment replies get Apollo's icon instead of Bark's,
// while thumbnail-bearing watcher pushes keep the post image.
func TestSendBark_DefaultIconFallback(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		payload  *payload.Payload
		wantIcon string
	}{
		{
			name:     "no thumbnail gets the default icon",
			payload:  payload.NewPayload().AlertTitle("New message").Custom("type", "private-message"),
			wantIcon: "https://example.com/apollo.png",
		},
		{
			name:     "thumbnail wins over the default icon",
			payload:  payload.NewPayload().AlertTitle("New post").Custom("thumbnail", "https://a.thumbs.redditmedia.com/Lr4b.jpg"),
			wantIcon: "https://a.thumbs.redditmedia.com/Lr4b.jpg",
		},
		{
			// Reddit sends "self" (and "default"/"nsfw"/"spoiler"/"image")
			// instead of a URL for posts without a thumbnail.
			name:     "sentinel thumbnail gets the default icon",
			payload:  payload.NewPayload().AlertTitle("New post").Custom("thumbnail", "self"),
			wantIcon: "https://example.com/apollo.png",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got barkRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
				_, _ = w.Write([]byte(`{"code":200,"message":"success"}`))
			}))
			defer srv.Close()

			s := &Sender{
				httpClient:      srv.Client(),
				barkDefaultIcon: "https://example.com/apollo.png",
			}
			d := domain.Device{Transport: domain.DeviceTransportBark, TransportEndpoint: srv.URL}

			res, err := s.sendBark(t.Context(), d, tc.payload)
			require.NoError(t, err)
			assert.True(t, res.Sent)
			assert.Equal(t, tc.wantIcon, got.Icon)
		})
	}
}
