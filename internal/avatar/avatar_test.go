package avatar_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/christianselig/apollo-backend/internal/avatar"
)

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func decodeThumbnail(t *testing.T, data []byte) image.Image {
	t.Helper()

	assert.LessOrEqual(t, len(data), avatar.MaxJPEGBytes)
	img, err := jpeg.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, image.Rect(0, 0, avatar.Size, avatar.Size), img.Bounds())
	return img
}

// near reports whether c is within tol of want on every channel (JPEG is lossy).
func near(c color.Color, want color.RGBA, tol int) bool {
	r, g, b, _ := c.RGBA()
	diff := func(got uint32, want uint8) bool {
		d := int(got>>8) - int(want)
		return d >= -tol && d <= tol
	}
	return diff(r, want.R) && diff(g, want.G) && diff(b, want.B)
}

func TestThumbnail_OpaqueAvatar(t *testing.T) {
	t.Parallel()

	src := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			src.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x80, A: 0xFF})
		}
	}

	out, err := avatar.Thumbnail(encodePNG(t, src))
	require.NoError(t, err)
	img := decodeThumbnail(t, out)

	// Box-filtered corners keep the gradient's corner colors.
	assert.True(t, near(img.At(0, 0), color.RGBA{R: 2, G: 2, B: 0x80}, 12))
	assert.True(t, near(img.At(avatar.Size-1, avatar.Size-1), color.RGBA{R: 253, G: 253, B: 0x80}, 12))
}

func TestThumbnail_TransparencyGetsBackdrop(t *testing.T) {
	t.Parallel()

	// A snoovatar headshot is a transparent PNG; JPEG has no alpha, so the
	// see-through parts become the light neutral backdrop.
	src := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	out, err := avatar.Thumbnail(encodePNG(t, src))
	require.NoError(t, err)
	img := decodeThumbnail(t, out)

	assert.True(t, near(img.At(avatar.Size/2, avatar.Size/2), color.RGBA{R: 0xD7, G: 0xDD, B: 0xE3}, 4))
}

func TestThumbnail_CropsNonSquareToCenter(t *testing.T) {
	t.Parallel()

	// 300x100: red side bands, green center square. Only the center survives.
	src := image.NewRGBA(image.Rect(0, 0, 300, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 300; x++ {
			c := color.RGBA{R: 0xFF, A: 0xFF}
			if x >= 100 && x < 200 {
				c = color.RGBA{G: 0xFF, A: 0xFF}
			}
			src.SetRGBA(x, y, c)
		}
	}

	out, err := avatar.Thumbnail(encodePNG(t, src))
	require.NoError(t, err)
	img := decodeThumbnail(t, out)

	for _, x := range []int{2, avatar.Size / 2, avatar.Size - 3} {
		assert.True(t, near(img.At(x, avatar.Size/2), color.RGBA{G: 0xFF}, 40), "x=%d", x)
	}
}

func TestThumbnail_UpscalesSmallSources(t *testing.T) {
	t.Parallel()

	src := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for i := range src.Pix {
		src.Pix[i] = 0xFF
	}

	out, err := avatar.Thumbnail(encodePNG(t, src))
	require.NoError(t, err)
	img := decodeThumbnail(t, out)
	assert.True(t, near(img.At(10, 10), color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF}, 4))
}

func TestThumbnail_NoiseStaysInBudget(t *testing.T) {
	t.Parallel()

	// Worst case for JPEG: the quality steps down until it fits (or the
	// thumbnail is refused, never oversized).
	rng := rand.New(rand.NewSource(1))
	src := image.NewRGBA(image.Rect(0, 0, 256, 256))
	rng.Read(src.Pix)
	for i := 3; i < len(src.Pix); i += 4 {
		src.Pix[i] = 0xFF
	}

	out, err := avatar.Thumbnail(encodePNG(t, src))
	if errors.Is(err, avatar.ErrDoesNotFit) {
		return
	}
	require.NoError(t, err)
	decodeThumbnail(t, out)
}

func TestThumbnail_RejectsGarbage(t *testing.T) {
	t.Parallel()

	_, err := avatar.Thumbnail([]byte("<html>not an image</html>"))
	assert.Error(t, err)
}

func TestThumbnail_RejectsHugeDimensionsBeforeDecoding(t *testing.T) {
	t.Parallel()

	// A 1x1 PNG whose header claims 20000x20000: refused from the header
	// alone, without allocating 1.6 GB.
	data := encodePNG(t, image.NewGray(image.Rect(0, 0, 1, 1)))
	ihdr := bytes.Index(data, []byte("IHDR"))
	require.Positive(t, ihdr)
	binary.BigEndian.PutUint32(data[ihdr+4:], 20000)
	binary.BigEndian.PutUint32(data[ihdr+8:], 20000)
	binary.BigEndian.PutUint32(data[ihdr+17:], crc32.ChecksumIEEE(data[ihdr:ihdr+17]))

	_, err := avatar.Thumbnail(data)
	assert.ErrorIs(t, err, avatar.ErrTooLarge)
}

func TestAllowedURL(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"https://styles.redditmedia.com/t5_2/styles/profileIcon_x.png?width=256&height=256&crop=256:256,smart&s=abc",
		"https://i.redd.it/snoovatar/avatars/0f1e2d3c-headshot.png",
		"https://www.redditstatic.com/avatars/defaults/v2/avatar_default_2.png",
		"https://b.thumbs.redditmedia.com/x.png",
		"https://www.redditstatic.com:443/avatars/avatar_default_07_545452.png",
	}
	for _, raw := range allowed {
		_, ok := avatar.AllowedURL(raw)
		assert.True(t, ok, raw)
	}

	refused := []string{
		"",
		"http://i.redd.it/x.png",                 // not https
		"https://example.com/x.png",              // not Reddit
		"https://redd.it.example.com/x.png",      // suffix trick
		"https://evilredd.it/x.png",              // no dot boundary
		"https://127.0.0.1/x.png",                // IP literal
		"https://i.redd.it:8443/x.png",           // odd port
		"https://user:pass@i.redd.it/x.png",      // credentials
		"file:///etc/passwd",                     // scheme
		"https://[::1]/x.png",                    // IPv6 literal
		"https://metadata.google.internal/x.png", // internal name
	}
	for _, raw := range refused {
		_, ok := avatar.AllowedURL(raw)
		assert.False(t, ok, raw)
	}
}

// redirectTo sends every request to the test server, standing in for
// Reddit's image CDN (the allowlist keeps the real URL on a Reddit host).
func redirectTo(srv *httptest.Server) *http.Client {
	addr := srv.Listener.Addr().(*net.TCPAddr).String()
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme = "http"
		r.URL.Host = addr
		return http.DefaultTransport.RoundTrip(r)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetch(t *testing.T) {
	t.Parallel()

	png := encodePNG(t, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	var sawAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			sawAccept = r.Header.Get("Accept")
			_, _ = w.Write(png)
		case "/huge.png":
			_, _ = w.Write(make([]byte, 3<<20))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	client := redirectTo(srv)
	ctx := t.Context()

	got, err := avatar.Fetch(ctx, client, "https://i.redd.it/ok.png")
	require.NoError(t, err)
	assert.Equal(t, png, got)
	assert.NotContains(t, sawAccept, "webp", "WebP isn't decodable with the standard library")

	_, err = avatar.Fetch(ctx, client, "https://i.redd.it/missing.png")
	assert.Error(t, err)

	_, err = avatar.Fetch(ctx, client, "https://i.redd.it/huge.png")
	assert.ErrorIs(t, err, avatar.ErrTooLarge)

	_, err = avatar.Fetch(ctx, client, "https://example.com/ok.png")
	assert.ErrorIs(t, err, avatar.ErrUnsupportedURL)
}
