package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/sideshow/apns2/payload"

	"github.com/christianselig/apollo-backend/internal/domain"
)

// barkRequest is the JSON body bark-server accepts on POST (api.day.app or
// self-hosted). `url` is opened on notification tap and supports custom URL
// schemes, which is what deep-links back into Apollo.
type barkRequest struct {
	Title    string `json:"title,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
	Body     string `json:"body"`
	URL      string `json:"url,omitempty"`
	Group    string `json:"group,omitempty"`
	Icon     string `json:"icon,omitempty"`
	Badge    *int   `json:"badge,omitempty"`
	Level    string `json:"level,omitempty"`
	Sound    string `json:"sound,omitempty"`
}

// barkRequestFromPayload translates an APNs payload into a Bark push. The
// apns2 payload builder is the single source of truth for every notification
// this backend produces, so rather than teaching each producer about Bark,
// marshal the payload it built and lift out the alert fields plus the custom
// keys Apollo uses for tap routing. Marshals fresh on every call — the
// subreddit/user workers mutate AlertTitle on a shared payload between sends.
func barkRequestFromPayload(p *payload.Payload) (*barkRequest, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}

	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}

	aps, _ := m["aps"].(map[string]interface{})
	alert, _ := aps["alert"].(map[string]interface{})

	req := &barkRequest{Level: "active"}
	req.Title, _ = alert["title"].(string)
	req.Subtitle, _ = alert["subtitle"].(string)
	req.Body, _ = alert["body"].(string)

	if badge, ok := aps["badge"].(float64); ok {
		b := int(badge)
		req.Badge = &b
	}

	// Group notifications the way APNs would have threaded them.
	if tid, ok := aps["thread-id"].(string); ok && tid != "" {
		req.Group = tid
	} else if cat, ok := aps["category"].(string); ok && cat != "" {
		req.Group = cat
	} else {
		req.Group = "apollo"
	}

	// Everything outside "aps" is a custom key (post_id, subreddit, type, …).
	customs := make(map[string]interface{}, len(m))
	for k, v := range m {
		if k != "aps" {
			customs[k] = v
		}
	}

	// Reddit fills `thumbnail` with sentinels ("self", "default", "nsfw",
	// "spoiler", "image") when a post has no real thumbnail; only a URL is
	// usable as a Bark icon, and leaving Icon empty lets the default-icon
	// fallback in sendBark kick in.
	if thumb, ok := customs["thumbnail"].(string); ok && (strings.HasPrefix(thumb, "https://") || strings.HasPrefix(thumb, "http://")) {
		req.Icon = thumb
	}

	// Carry the payload's sound across, minus the file extension: Apollo's
	// pushes say "traloop.wav", and the matching Bark-side file is
	// assets/bark-sounds/traloop.caf (bark-server appends ".caf" to
	// extensionless values). Plays if the user imported that .caf into the
	// Bark app; iOS falls back to the default alert sound otherwise. Devices
	// whose push URL pins ?sound= (the tweak, mirroring Apollo's in-app
	// sound picker) override this — query beats body on bark-server.
	if sound, ok := aps["sound"].(string); ok && sound != "" && sound != "default" {
		req.Sound = strings.TrimSuffix(sound, filepath.Ext(sound))
	}

	req.URL = clickURL(customs)

	// Bark requires a body; the title alone is better than a dropped push.
	if req.Body == "" {
		req.Body = req.Title
	}
	if req.Body == "" {
		req.Body = "New notification"
	}

	return req, nil
}

// clickURL derives the apollo:// deep link opened when the Bark notification
// is tapped, from the same custom keys Apollo's own notification tap handler
// uses. Private messages have no post to open, so they land on the inbox
// (an Apollo-Reborn tweak deep link). Anything with a post lands on the
// thread — the `apollo://reddit.com/<reddit path>` form Apollo routes
// natively.
//
// When the payload names a comment (replies and mentions), the link anchors
// it with `/_/<comment_id>/?context=1` — byte-for-byte the share-link format
// Apollo itself generates, verified against the link-parser regex in the
// Apollo binary. The slug placeholder must be `_`, NOT `-`: the parser
// captures the comment id as `(\w+)` after an optional `(?:/\w+)?` slug, and
// `-` isn't a \w character, so a `/-/` link silently degrades to opening the
// post unanchored. context=1 shows the parent above the comment, matching
// what a native notification tap does.
func clickURL(customs map[string]interface{}) string {
	if t, _ := customs["type"].(string); t == "private-message" {
		return "apollo://reborn/inbox"
	}

	postID, _ := customs["post_id"].(string)
	subreddit, _ := customs["subreddit"].(string)
	if postID == "" || subreddit == "" {
		return "apollo://reborn/inbox"
	}

	if commentID, _ := customs["comment_id"].(string); commentID != "" {
		return fmt.Sprintf("apollo://reddit.com/r/%s/comments/%s/_/%s/?context=1",
			url.PathEscape(subreddit), url.PathEscape(postID), url.PathEscape(commentID))
	}

	return fmt.Sprintf("apollo://reddit.com/r/%s/comments/%s",
		url.PathEscape(subreddit), url.PathEscape(postID))
}

func (s *Sender) sendBark(ctx context.Context, device domain.Device, p *payload.Payload) (Result, error) {
	req, err := barkRequestFromPayload(p)
	if err != nil {
		return Result{}, err
	}

	// No post thumbnail — show Apollo's icon rather than Bark's. (A ?icon=
	// pinned on the device's push URL overrides either; query parameters
	// beat the JSON body on bark-server.)
	if req.Icon == "" {
		req.Icon = s.barkDefaultIcon
	}

	body, err := json.Marshal(req)
	if err != nil {
		return Result{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, device.TransportEndpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")

	res, err := s.httpClient.Do(httpReq)
	if err != nil {
		return Result{}, err
	}
	defer res.Body.Close()

	// bark-server answers {"code":200,"message":"success"} on delivery; a
	// 200 with a non-200 code (e.g. bad device key) is still a failure.
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	var barkRes struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(respBody, &barkRes)

	if res.StatusCode != http.StatusOK || barkRes.Code != http.StatusOK {
		reason := barkRes.Message
		if reason == "" {
			reason = strings.TrimSpace(string(respBody))
		}
		return Result{Status: res.StatusCode, Reason: reason}, nil
	}

	return Result{Sent: true, Status: res.StatusCode}, nil
}
