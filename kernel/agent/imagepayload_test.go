package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A tiny real PNG: the drivers must classify real image bytes, not just
// extensions (a misnamed payload stays a file attachment).
func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// TestMatrixSendFileImageIsInline: chart/screenshot deliveries must render
// inline — image payloads go out as m.image with the mimetype and pixel
// dimensions in the info block, not as m.file download chips
// (docs/presentation-engine.md §9.3).
func TestMatrixSendFileImageIsInline(t *testing.T) {
	png := testPNG(t)
	var uploadContentType string
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/_matrix/media/v3/upload"):
			uploadContentType = r.Header.Get("Content-Type")
			_, _ = w.Write([]byte(`{"content_uri": "mxc://hs/abc123"}`))
		case strings.Contains(r.URL.Path, "/send/m.room.message/"):
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			_, _ = w.Write([]byte(`{"event_id": "$img"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	driver := matrixDriver{}
	err := driver.SendFile(context.Background(), server.Client(),
		map[string]string{"homeserver": server.URL, "access_token": "tok"},
		"!r:hs", OutboundFile{Name: "latency-chart.png", Note: "Latency", Payload: png})
	require.NoError(t, err)

	require.Equal(t, "m.image", payload["msgtype"], "images must ride m.image so Element renders them inline")
	require.Equal(t, "image/png", uploadContentType)
	info, _ := payload["info"].(map[string]any)
	require.Equal(t, "image/png", info["mimetype"])
	require.InDelta(t, 4.0, info["w"], 0)
	require.InDelta(t, 3.0, info["h"], 0)
	require.Equal(t, "mxc://hs/abc123", payload["url"])
	require.Equal(t, "Latency", payload["body"])
}

// TestMatrixSendFileDocumentStaysFile: non-image payloads keep the m.file
// delivery — a PDF or archive must not masquerade as a picture.
func TestMatrixSendFileDocumentStaysFile(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/_matrix/media/v3/upload"):
			_, _ = w.Write([]byte(`{"content_uri": "mxc://hs/doc"}`))
		case strings.Contains(r.URL.Path, "/send/m.room.message/"):
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			_, _ = w.Write([]byte(`{"event_id": "$doc"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	driver := matrixDriver{}
	err := driver.SendFile(context.Background(), server.Client(),
		map[string]string{"homeserver": server.URL, "access_token": "tok"},
		"!r:hs", OutboundFile{Name: "report.pdf", Note: "Report", Payload: []byte("%PDF-1.4 fake")})
	require.NoError(t, err)

	require.Equal(t, "m.file", payload["msgtype"])
}

// TestMatrixSendFileSvgStaysFile: SVG is deliberately not inline — Element
// does not render SVG images, so an SVG payload must arrive as a file.
func TestMatrixSendFileSvgStaysFile(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/_matrix/media/v3/upload"):
			_, _ = w.Write([]byte(`{"content_uri": "mxc://hs/svg"}`))
		case strings.Contains(r.URL.Path, "/send/m.room.message/"):
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			_, _ = w.Write([]byte(`{"event_id": "$svg"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	driver := matrixDriver{}
	err := driver.SendFile(context.Background(), server.Client(),
		map[string]string{"homeserver": server.URL, "access_token": "tok"},
		"!r:hs", OutboundFile{Name: "chart.svg", Payload: []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"/>")})
	require.NoError(t, err)

	require.Equal(t, "m.file", payload["msgtype"], "SVG must not claim m.image: Element refuses to render it")
}

// TestMatrixSendFileMisnamedStaysFile: a .png name over non-PNG bytes stays
// a file — the magic-byte check guards against broken inline previews.
func TestMatrixSendFileMisnamedStaysFile(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/_matrix/media/v3/upload"):
			_, _ = w.Write([]byte(`{"content_uri": "mxc://hs/fake"}`))
		case strings.Contains(r.URL.Path, "/send/m.room.message/"):
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			_, _ = w.Write([]byte(`{"event_id": "$fake"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	driver := matrixDriver{}
	err := driver.SendFile(context.Background(), server.Client(),
		map[string]string{"homeserver": server.URL, "access_token": "tok"},
		"!r:hs", OutboundFile{Name: "chart.png", Payload: []byte("definitely not a png")})
	require.NoError(t, err)

	require.Equal(t, "m.file", payload["msgtype"])
}

// TestTelegramSendFileImageUsesSendPhoto: image payloads go through
// sendPhoto (inline picture), documents stay on sendDocument. The driver
// hardwires api.telegram.org, so requests route to the test server through
// the roundTripperTo transport (the established pattern of the form test).
func TestTelegramSendFileImageUsesSendPhoto(t *testing.T) {
	png := testPNG(t)
	var gotPath, gotField string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.NoError(t, r.ParseMultipartForm(1<<20))
		if _, header, err := r.FormFile("photo"); err == nil {
			gotField = header.Filename
		} else if _, header, err := r.FormFile("document"); err == nil {
			gotField = header.Filename
		}
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	client := &http.Client{Transport: roundTripperTo(strings.TrimPrefix(server.URL, "http://"))}
	driver := telegramDriver{}
	err := driver.SendFile(context.Background(), client,
		map[string]string{"bot_token": "tok"},
		"@chat", OutboundFile{Name: "chart.png", Note: "Latency", Payload: png})
	require.NoError(t, err)

	require.True(t, strings.HasSuffix(gotPath, "/sendPhoto"), "image payloads must use sendPhoto, got %s", gotPath)
	require.Equal(t, "chart.png", gotField)

	// A document keeps sendDocument.
	err = driver.SendFile(context.Background(), client,
		map[string]string{"bot_token": "tok"},
		"@chat", OutboundFile{Name: "report.pdf", Payload: []byte("%PDF-1.4 fake")})
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(gotPath, "/sendDocument"), "non-image payloads must use sendDocument, got %s", gotPath)
}

// TestImageDimensionsGuard: the shared detector separates inline-renderable
// images from everything else, extension AND magic bytes must agree.
func TestImageDimensionsGuard(t *testing.T) {
	png := testPNG(t)
	w, h, ok := imageDimensions("chart.png", png)
	require.True(t, ok)
	require.Equal(t, 4, w)
	require.Equal(t, 3, h)

	// Wrong extension: not inline.
	_, _, ok = imageDimensions("chart.txt", png)
	require.False(t, ok)

	// Wrong bytes: not inline.
	_, _, ok = imageDimensions("chart.png", []byte("not an image"))
	require.False(t, ok)
}
