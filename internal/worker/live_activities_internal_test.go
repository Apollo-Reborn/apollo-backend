package worker

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAvatar is the size of a typical base64'd thumbnail (~1.5 KB JPEG).
var fakeAvatar = strings.Repeat("A", 2048)

func decodeContentState(t *testing.T, bb []byte) map[string]interface{} {
	t.Helper()

	var payload struct {
		APS struct {
			ContentState  map[string]interface{} `json:"content-state"`
			Event         string                 `json:"event"`
			Timestamp     int64                  `json:"timestamp"`
			DismissalDate int64                  `json:"dismissal-date"`
		} `json:"aps"`
	}
	require.NoError(t, json.Unmarshal(bb, &payload))
	assert.Equal(t, "update", payload.APS.Event)
	assert.Equal(t, int64(1700000000), payload.APS.Timestamp)
	assert.Equal(t, int64(1700004500), payload.APS.DismissalDate)
	return payload.APS.ContentState
}

func testNotification(body, avatar string) DynamicIslandNotification {
	return DynamicIslandNotification{
		PostCommentCount:    1234,
		PostScore:           5678,
		CommentID:           "abc1234",
		CommentAuthor:       "SomeoneWithALongName",
		CommentBody:         body,
		CommentAge:          1699999990,
		CommentScore:        42,
		CommentAuthorAvatar: avatar,
	}
}

func TestLiveActivityPayload_KeysMatchWidgetContract(t *testing.T) {
	t.Parallel()

	bb, err := liveActivityPayload(testNotification("As she should.", fakeAvatar), "update", 1700004500, 1700000000)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(bb), maxLiveActivityPayloadBytes)

	// These names are decoded by the widget's ContentState (and Apollo's own
	// FollowThreadActivityAttributes); renaming one silently breaks updates.
	cs := decodeContentState(t, bb)
	assert.Equal(t, float64(1234), cs["postTotalComments"])
	assert.Equal(t, float64(5678), cs["postScore"])
	assert.Equal(t, "abc1234", cs["commentId"])
	assert.Equal(t, "SomeoneWithALongName", cs["commentAuthor"])
	assert.Equal(t, "As she should.", cs["commentBody"])
	assert.Equal(t, float64(1699999990), cs["commentAge"])
	assert.Equal(t, float64(42), cs["commentScore"])
	assert.Equal(t, fakeAvatar, cs["commentAuthorAvatar"])
}

func TestLiveActivityPayload_NoAvatarKeyWhenAbsent(t *testing.T) {
	t.Parallel()

	bb, err := liveActivityPayload(testNotification("hi", ""), "update", 1700004500, 1700000000)
	require.NoError(t, err)
	_, present := decodeContentState(t, bb)["commentAuthorAvatar"]
	assert.False(t, present, "omitempty keeps the payload identical for activities without avatars")
}

func TestLiveActivityPayload_CapsLongComments(t *testing.T) {
	t.Parallel()

	// Before the cap, a long comment alone produced a payload APNs refused,
	// and the refusal deleted the activity.
	long := strings.Repeat("x", 10000)
	bb, err := liveActivityPayload(testNotification(long, ""), "update", 1700004500, 1700000000)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(bb), maxLiveActivityPayloadBytes)

	body := decodeContentState(t, bb)["commentBody"].(string)
	assert.Equal(t, strings.Repeat("x", liveActivityCommentRunes)+"…", body)
}

func TestLiveActivityPayload_TrimsTextToKeepAvatar(t *testing.T) {
	t.Parallel()

	// 500 four-byte emoji (2000 bytes) plus a 2 KB avatar is over budget; the
	// text gives way first, since the widget only shows ~3 lines of it.
	emoji := strings.Repeat("😀", 1000)
	bb, err := liveActivityPayload(testNotification(emoji, fakeAvatar), "update", 1700004500, 1700000000)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(bb), maxLiveActivityPayloadBytes)

	cs := decodeContentState(t, bb)
	assert.Equal(t, fakeAvatar, cs["commentAuthorAvatar"])
	body := cs["commentBody"].(string)
	require.True(t, strings.HasSuffix(body, "…"))
	kept := utf8.RuneCountInString(body) - 1 // minus the ellipsis
	assert.GreaterOrEqual(t, kept, liveActivityCommentRunesWithAvatar)
	assert.Less(t, kept, liveActivityCommentRunes)

	// And it's the longest text that fits: one more rune would not.
	over, err := encodeLiveActivityPayload(testNotification(truncateRunes(emoji, kept+1), fakeAvatar), "update", 1700004500, 1700000000)
	require.NoError(t, err)
	assert.Greater(t, len(over), maxLiveActivityPayloadBytes)
}

func TestLiveActivityPayload_DropsAvatarBeforeCuttingTextShort(t *testing.T) {
	t.Parallel()

	// An avatar so large that keeping it would leave < 200 runes of text:
	// the avatar goes, the text stays at the normal cap.
	huge := strings.Repeat("A", 3800)
	text := strings.Repeat("x", 600)
	bb, err := liveActivityPayload(testNotification(text, huge), "update", 1700004500, 1700000000)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(bb), maxLiveActivityPayloadBytes)

	cs := decodeContentState(t, bb)
	_, present := cs["commentAuthorAvatar"]
	assert.False(t, present)
	assert.Equal(t, strings.Repeat("x", liveActivityCommentRunes)+"…", cs["commentBody"])
}

func TestLiveActivityPayload_DoesNotEscapeHTML(t *testing.T) {
	t.Parallel()

	bb, err := liveActivityPayload(testNotification("> quoted & <b>bold</b>", ""), "update", 1700004500, 1700000000)
	require.NoError(t, err)
	assert.Contains(t, string(bb), `"commentBody":"> quoted & <b>bold</b>"`)
	assert.False(t, strings.HasSuffix(string(bb), "\n"))
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "short", truncateRunes("short", 10))
	assert.Equal(t, "exact", truncateRunes("exact", 5))
	assert.Equal(t, "abc…", truncateRunes("abcdef", 3))
	assert.Equal(t, "ab…", truncateRunes("ab cdef", 3), "trailing whitespace is trimmed before the ellipsis")
	assert.Equal(t, "日本…", truncateRunes("日本語のコメント", 2))
	assert.Equal(t, "", truncateRunes("anything", 0))
	assert.Equal(t, "", truncateRunes("", 0))
}
