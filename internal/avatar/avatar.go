// Package avatar turns a Reddit profile picture into the tiny inline
// thumbnail a Live Activity push carries.
//
// A Live Activity can't reach the network (Apple sandboxes it), so the widget
// can only draw an avatar whose bytes arrive inside the push itself, and the
// whole APNs payload has to stay under 4 KB. Thumbnail therefore produces a
// 48x48 baseline JPEG of about 1-1.5 KB: big enough for a ~16pt avatar at 3x,
// small enough to leave room for the comment text next to it.
package avatar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // registers the GIF decoder for image.Decode
	"image/jpeg"
	_ "image/png" // registers the PNG decoder for image.Decode
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// Size is the thumbnail's edge length in pixels.
	Size = 48

	// MaxJPEGBytes caps the encoded thumbnail (about 2 KB once base64'd).
	// Thumbnail lowers the JPEG quality until the image fits.
	MaxJPEGBytes = 1536

	// maxSourceBytes caps how much of the source image is read. Reddit
	// profile icons are 256x256 and 5-70 KB; anything far larger is not an
	// avatar.
	maxSourceBytes = 2 << 20

	// maxSourcePixels rejects decompression bombs before decoding: a
	// 4096x4096 source already allocates 64 MB.
	maxSourcePixels = 4096 * 4096
)

var (
	ErrUnsupportedURL = errors.New("avatar: not a Reddit image URL")
	ErrTooLarge       = errors.New("avatar: source image too large")
	ErrDoesNotFit     = errors.New("avatar: thumbnail does not fit the size budget")
)

// backdrop fills transparent pixels before JPEG encoding (JPEG has no alpha).
// Snoovatar headshots are transparent PNGs; Reddit draws them on a light
// neutral circle too, and the widget clips the square to a circle.
var backdrop = color.RGBA{R: 0xD7, G: 0xDD, B: 0xE3, A: 0xFF}

// jpegQualities is tried in order until the encoded thumbnail fits in
// MaxJPEGBytes. Typical avatars fit at the first step.
var jpegQualities = []int{75, 60, 45, 30}

// allowedHostSuffixes are the hosts Reddit serves profile pictures from:
// styles.redditmedia.com (custom icons), i.redd.it (snoovatar headshots) and
// www.redditstatic.com (default avatars). Fetching anything else would let a
// crafted profile URL make the worker request arbitrary hosts.
var allowedHostSuffixes = []string{"redditmedia.com", "redd.it", "redditstatic.com"}

// AllowedURL parses raw and reports whether it is an https URL on one of
// Reddit's image hosts.
func AllowedURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil {
		return nil, false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || net.ParseIP(host) != nil {
		return nil, false
	}
	if u.Port() != "" && u.Port() != "443" {
		return nil, false
	}
	for _, suffix := range allowedHostSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return u, true
		}
	}
	return nil, false
}

// NewClient returns the HTTP client Fetch expects: a short timeout, and
// redirects followed only while they stay on Reddit's image hosts.
func NewClient() *http.Client {
	return &http.Client{
		Timeout: 4 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("avatar: too many redirects")
			}
			if _, ok := AllowedURL(req.URL.String()); !ok {
				return ErrUnsupportedURL
			}
			return nil
		},
	}
}

// Fetch downloads the image at rawURL, which must pass AllowedURL.
func Fetch(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	u, ok := AllowedURL(rawURL)
	if !ok {
		return nil, ErrUnsupportedURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	// Ask for formats the standard library decodes. Reddit's image CDN only
	// serves WebP to clients that advertise it.
	req.Header.Set("Accept", "image/png,image/jpeg,image/gif;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("avatar: fetch returned HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSourceBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

// Thumbnail decodes a PNG, JPEG or GIF, center-crops it to a square,
// box-filters it down to Size x Size over the backdrop, and returns a JPEG no
// larger than MaxJPEGBytes.
func Thumbnail(data []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxSourcePixels {
		return nil, ErrTooLarge
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	thumb := downscale(src, Size)

	var buf bytes.Buffer
	for _, quality := range jpegQualities {
		buf.Reset()
		if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		if buf.Len() <= MaxJPEGBytes {
			return buf.Bytes(), nil
		}
	}
	return nil, ErrDoesNotFit
}

// downscale area-averages the centered square crop of src into a size x size
// opaque image, compositing any transparency over the backdrop. Sources
// smaller than size are upscaled by the same arithmetic (each output pixel
// samples a fraction of one source pixel).
func downscale(src image.Image, size int) *image.RGBA {
	b := src.Bounds()
	side := b.Dx()
	if b.Dy() < side {
		side = b.Dy()
	}

	// Normalize to premultiplied RGBA so the box filter weights alpha right.
	crop := image.NewRGBA(image.Rect(0, 0, side, side))
	origin := image.Pt(b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2)
	draw.Draw(crop, crop.Bounds(), src, origin, draw.Src)

	out := image.NewRGBA(image.Rect(0, 0, size, size))
	scale := float64(side) / float64(size)
	for oy := 0; oy < size; oy++ {
		y0, y1 := float64(oy)*scale, float64(oy+1)*scale
		for ox := 0; ox < size; ox++ {
			x0, x1 := float64(ox)*scale, float64(ox+1)*scale

			var r, g, bl, a, total float64
			for sy := int(y0); sy < side && float64(sy) < y1; sy++ {
				wy := min(y1, float64(sy+1)) - max(y0, float64(sy))
				if wy <= 0 {
					continue
				}
				for sx := int(x0); sx < side && float64(sx) < x1; sx++ {
					wx := min(x1, float64(sx+1)) - max(x0, float64(sx))
					if wx <= 0 {
						continue
					}
					w := wx * wy
					c := crop.RGBAAt(sx, sy)
					r += float64(c.R) * w
					g += float64(c.G) * w
					bl += float64(c.B) * w
					a += float64(c.A) * w
					total += w
				}
			}
			if total == 0 {
				out.SetRGBA(ox, oy, backdrop)
				continue
			}
			r, g, bl, a = r/total, g/total, bl/total, a/total

			// Premultiplied source over the opaque backdrop.
			rest := 1 - a/0xFF
			out.SetRGBA(ox, oy, color.RGBA{
				R: channel(r + float64(backdrop.R)*rest),
				G: channel(g + float64(backdrop.G)*rest),
				B: channel(bl + float64(backdrop.B)*rest),
				A: 0xFF,
			})
		}
	}
	return out
}

func channel(v float64) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 0xFF:
		return 0xFF
	default:
		return uint8(v + 0.5)
	}
}
