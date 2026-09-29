package reddit_test

import (
	"io/ioutil"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/valyala/fastjson"

	"github.com/christianselig/apollo-backend/internal/reddit"
)

var pool = &fastjson.ParserPool{}

func NewTestParser(t *testing.T) *fastjson.Parser {
	t.Helper()

	parser := pool.Get()

	t.Cleanup(func() {
		pool.Put(parser)
	})

	return parser
}

func TestMeResponseParsing(t *testing.T) {
	t.Parallel()

	bb, err := ioutil.ReadFile("testdata/me.json")
	assert.NoError(t, err)

	parser := NewTestParser(t)
	val, err := parser.ParseBytes(bb)
	assert.NoError(t, err)

	ret := reddit.NewMeResponse(val)
	me := ret.(*reddit.MeResponse)
	assert.NotNil(t, me)

	assert.Equal(t, "xgeee", me.ID)
	assert.Equal(t, "hugocat", me.Name)
}

func TestRefreshTokenResponseParsing(t *testing.T) {
	t.Parallel()

	bb, err := ioutil.ReadFile("testdata/refresh_token.json")
	assert.NoError(t, err)

	parser := NewTestParser(t)
	val, err := parser.ParseBytes(bb)
	assert.NoError(t, err)

	ret := reddit.NewRefreshTokenResponse(val)
	rtr := ret.(*reddit.RefreshTokenResponse)
	assert.NotNil(t, rtr)

	assert.Equal(t, "xxx", rtr.AccessToken)
	assert.Equal(t, "yyy", rtr.RefreshToken)
	assert.Equal(t, 1*time.Hour, rtr.Expiry)
}

func TestListingResponseParsing(t *testing.T) {
	t.Parallel()

	// Message list
	bb, err := ioutil.ReadFile("testdata/message_inbox.json")
	assert.NoError(t, err)

	parser := NewTestParser(t)
	val, err := parser.ParseBytes(bb)
	assert.NoError(t, err)

	ret := reddit.NewListingResponse(val)
	l := ret.(*reddit.ListingResponse)
	assert.NotNil(t, l)

	assert.Equal(t, 25, l.Count)
	assert.Equal(t, 25, len(l.Children))
	assert.Equal(t, "t1_h470gjv", l.After)
	assert.Equal(t, "", l.Before)

	thing := l.Children[0]
	created := time.Date(2021, time.July, 14, 17, 56, 35, 0, time.UTC)
	assert.Equal(t, "t4", thing.Kind)
	assert.Equal(t, "138z6ke", thing.ID)
	assert.Equal(t, "unknown", thing.Type)
	assert.Equal(t, "iamthatis", thing.Author)
	assert.Equal(t, "how goes it", thing.Subject)
	assert.Equal(t, "how are you today", thing.Body)
	assert.Equal(t, created, thing.CreatedAt)
	assert.Equal(t, "hugocat", thing.Destination)
	assert.Equal(t, "t4_138z6ke", thing.FullName())

	thing = l.Children[6]
	assert.Equal(t, "/r/calicosummer/comments/ngcapc/hello_i_am_a_cat/h4q5j98/?context=3", thing.Context)
	assert.Equal(t, "t1_h46tec3", thing.ParentID)
	assert.Equal(t, "hello i am a cat", thing.LinkTitle)
	assert.Equal(t, "calicosummer", thing.Subreddit)

	// Post list
	bb, err = ioutil.ReadFile("testdata/subreddit_new.json")
	assert.NoError(t, err)

	val, err = parser.ParseBytes(bb)
	assert.NoError(t, err)

	ret = reddit.NewListingResponse(val)
	l = ret.(*reddit.ListingResponse)
	assert.NotNil(t, l)

	assert.Equal(t, 100, l.Count)

	thing = l.Children[1]
	assert.Equal(t, "Riven boss", thing.Title)
	assert.Equal(t, "Question", thing.Flair)
	assert.Contains(t, thing.SelfText, "never done riven")
	assert.Equal(t, int64(1), thing.Score)
}

func TestSubredditResponseParsing(t *testing.T) {
	t.Parallel()

	bb, err := ioutil.ReadFile("testdata/subreddit_about.json")
	assert.NoError(t, err)

	parser := NewTestParser(t)
	val, err := parser.ParseBytes(bb)
	assert.NoError(t, err)

	ret := reddit.NewSubredditResponse(val)
	s := ret.(*reddit.SubredditResponse)
	assert.NotNil(t, s)

	assert.Equal(t, "t5", s.Kind)
	assert.Equal(t, "2vq0w", s.ID)
	assert.Equal(t, "DestinyTheGame", s.Name)
	assert.Equal(t, false, s.Quarantined)
	assert.Equal(t, true, s.Public)
}

func TestUserResponseParsing(t *testing.T) {
	t.Parallel()

	bb, err := ioutil.ReadFile("testdata/user_about.json")
	assert.NoError(t, err)

	parser := NewTestParser(t)
	val, err := parser.ParseBytes(bb)
	assert.NoError(t, err)

	ret := reddit.NewUserResponse(val)
	u := ret.(*reddit.UserResponse)
	assert.NotNil(t, u)

	assert.Equal(t, "t2", u.Kind)
	assert.Equal(t, "1ia22", u.ID)
	assert.Equal(t, "changelog", u.Name)
	assert.Equal(t, true, u.AcceptFollowers)

	// Default avatar: the profile subreddit's icon is the only candidate
	// (snoovatar_img is empty, community_icon is null).
	assert.Equal(t, "https://www.redditstatic.com/avatars/defaults/v2/avatar_default_2.png", u.ProfileIconURL)
	assert.Equal(t, "", u.SnoovatarURL)
	assert.Equal(t, "", u.CommunityIcon)
	assert.Equal(t, "https://www.redditstatic.com/avatars/defaults/v2/avatar_default_2.png", u.AvatarURL())
}

func TestUserResponseAvatarURL(t *testing.T) {
	t.Parallel()

	bb, err := ioutil.ReadFile("testdata/user_about_snoovatar.json")
	assert.NoError(t, err)

	parser := NewTestParser(t)
	val, err := parser.ParseBytes(bb)
	assert.NoError(t, err)

	u := reddit.NewUserResponse(val).(*reddit.UserResponse)

	// A snoovatar user: the profile icon is the headshot, which wins over the
	// full-body snoovatar image.
	headshot := "https://styles.redditmedia.com/t5_9zz9z/styles/profileIcon_snoo0f1e2d3c-4b5a-4968-8778-a6b5c4d3e2f1-headshot.png?width=256&height=256&crop=256:256,smart&s=0123456789abcdef0123456789abcdef01234567"
	assert.Equal(t, headshot, u.ProfileIconURL)
	assert.Equal(t, "https://i.redd.it/snoovatar/avatars/0f1e2d3c-4b5a-4968-8778-a6b5c4d3e2f1.png", u.SnoovatarURL)
	assert.Equal(t, headshot, u.AvatarURL())

	// Fallback order when earlier candidates are missing.
	u.ProfileIconURL = ""
	assert.Equal(t, "https://i.redd.it/snoovatar/avatars/0f1e2d3c-4b5a-4968-8778-a6b5c4d3e2f1.png", u.AvatarURL())
	u.SnoovatarURL = ""
	assert.Equal(t, headshot, u.AvatarURL(), "falls back to data.icon_img")
	u.IconURL = ""
	assert.Equal(t, "", u.AvatarURL())
}

func TestUserPostsParsing(t *testing.T) {
	t.Parallel()

	bb, err := ioutil.ReadFile("testdata/user_posts.json")
	assert.NoError(t, err)

	parser := NewTestParser(t)
	val, err := parser.ParseBytes(bb)
	assert.NoError(t, err)

	ret := reddit.NewListingResponse(val)
	ps := ret.(*reddit.ListingResponse)
	assert.NotNil(t, ps)

	post := ps.Children[0]

	assert.Equal(t, "public", post.SubredditType)
}

func TestThreadResponseParsing(t *testing.T) {
	t.Parallel()

	bb, err := ioutil.ReadFile("testdata/thread.json")
	assert.NoError(t, err)

	parser := NewTestParser(t)
	val, err := parser.ParseBytes(bb)
	assert.NoError(t, err)

	ret := reddit.NewThreadResponse(val)
	tr := ret.(*reddit.ThreadResponse)
	assert.NotNil(t, tr)

	assert.Equal(t, "When you buy $400 machine to run games that you can run using $15 RPi", tr.Post.Title)
	assert.Equal(t, 20, len(tr.Children))

	assert.Equal(t, "The Deck is a lot more portable than the Pi though.", tr.Children[0].Body)
	assert.Equal(t, "PhonicUK", tr.Children[1].Author)
}

func TestEmptyThreadResponseParsing(t *testing.T) {
	t.Parallel()

	bb, err := ioutil.ReadFile("testdata/thread_empty.json")
	assert.NoError(t, err)

	parser := NewTestParser(t)
	val, err := parser.ParseBytes(bb)
	assert.NoError(t, err)

	ret := reddit.NewThreadResponse(val)
	tr := ret.(*reddit.ThreadResponse)
	assert.NotNil(t, tr)

	assert.Equal(t, "So many knives… so little time.", tr.Post.Title)
	assert.Equal(t, 0, len(tr.Children))
}
