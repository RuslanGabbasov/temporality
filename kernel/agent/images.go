package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif" // register the gif decoder for image.Decode (first-frame extraction)
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"go.temporal.io/sdk/temporal"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the webp decoder with image

	"github.com/temporality-project/temporality/kernel/llm"
)

// Vision tools (docs/web-research.md §3, phase 2). Multimodal models consume
// images as content blocks, but OpenAI-compatible APIs only accept image
// parts in user messages — never in tool results. So the tools return
// metadata as their content plus the payload in ToolResult.Images, and the
// workflow injects the blocks as one follow-up user message after every tool
// reply of the turn landed.

const (
	// imageSourceCap bounds the raw file/download before decoding.
	imageSourceCap = 5 << 20 // 5 MiB
	// imageMaxSide is the long-side budget: providers charge vision input by
	// tiles and reject oversized images. ~1568px matches the common
	// high-fidelity tile limit.
	imageMaxSide = 1568
	// imageEncodedCap bounds the final encoded payload. The block crosses the
	// Temporal activity boundary (result -> workflow history): the cap leaves
	// headroom under the default payload limits after base64 expansion.
	imageEncodedCap = 1500 << 10 // ~1.5 MiB
)

var viewImageTool = llm.ToolDef{
	Name:        "view_image",
	Description: "Attach an image file (PNG, JPEG, WebP, GIF) from the workspace to your context as a vision block, then look at it in the next turn. Use it to actually inspect screenshots, diagrams, charts and rendered files instead of guessing from bytes. Oversized images are downscaled to a 1568px long side; GIF collapses to its first frame.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Workspace-relative path (a leading /workspace/ is accepted)"},
		},
		"required": []string{"path"},
	},
}

var fetchImageTool = llm.ToolDef{
	Name:        "fetch_image",
	Description: "Download an image from an http(s) URL and attach it to your context as a vision block (SSRF-guarded like web_fetch, at most 5 MiB, downscaled to a 1568px long side). For HTML pages use web_fetch instead.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "Absolute http(s) URL of the image"},
		},
		"required": []string{"url"},
	},
}

// imageMeta summarizes what actually reached the model context — the journal
// event carries this, never the blob.
type imageMeta struct {
	mime    string
	width   int
	height  int
	resized bool
	bytes   int
	sum     string
}

func (m imageMeta) summary() string {
	change := ""
	if m.resized {
		change = " (downscaled)"
	}
	return fmt.Sprintf("image attached: %s %dx%d%s, %d bytes, sha256:%s — it is delivered to your next turn as a vision block",
		m.mime, m.width, m.height, change, m.bytes, m.sum)
}

// prepareImageBlock turns raw image bytes into a vision block. Small PNG/
// JPEG/WebP files pass through untouched; anything oversized is downscaled to
// imageMaxSide on the long side and re-encoded (JPEG for photo-ish sources,
// PNG otherwise), and the result must fit imageEncodedCap.
func prepareImageBlock(data []byte) (llm.ImageBlock, imageMeta, error) {
	if len(data) > imageSourceCap {
		return llm.ImageBlock{}, imageMeta{}, fmt.Errorf("image is %d bytes; the cap is %d", len(data), imageSourceCap)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return llm.ImageBlock{}, imageMeta{}, fmt.Errorf("unsupported or corrupt image: %w", err)
	}
	meta := imageMeta{width: config.Width, height: config.Height}
	digest := sha256.Sum256(data)
	meta.sum = hex.EncodeToString(digest[:8])

	mime, mimeOK := imageMIME(format)
	if !mimeOK {
		return llm.ImageBlock{}, imageMeta{}, fmt.Errorf("unsupported image format %q (supported: png, jpeg, webp, gif)", format)
	}
	meta.mime = mime

	fits := config.Width <= imageMaxSide && config.Height <= imageMaxSide
	if fits && format != "gif" {
		// Passthrough: original bytes already fit the budget. GIF is excluded —
		// animated frames are not vision input; the first frame converts to PNG.
		meta.bytes = len(data)
		return llm.ImageBlock{MIMEType: mime, Data: data}, meta, nil
	}

	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return llm.ImageBlock{}, imageMeta{}, fmt.Errorf("decode image: %w", err)
	}
	resized := downscale(decoded, imageMaxSide)
	bounds := resized.Bounds()
	meta.width, meta.height = bounds.Dx(), bounds.Dy()
	// resized stays false when the pixel dimensions did not change (e.g. a
	// small GIF converting to a first-frame PNG): no scaling happened.
	meta.resized = bounds.Dx() != config.Width || bounds.Dy() != config.Height

	// Photo-ish sources re-encode as JPEG (smaller); line art stays PNG.
	pngSource := format == "png" || format == "gif"
	block, err := encodeFitting(resized, pngSource)
	if err != nil {
		return llm.ImageBlock{}, imageMeta{}, err
	}
	meta.mime = block.MIMEType // meta reports what actually reached the context
	meta.bytes = len(block.Data)
	return block, meta, nil
}

func imageMIME(format string) (string, bool) {
	switch format {
	case "png":
		return "image/png", true
	case "jpeg":
		return "image/jpeg", true
	case "webp":
		return "image/webp", true
	case "gif":
		return "image/gif", true
	default:
		return "", false
	}
}

// downscale shrinks the long side to maxSide with Catmull-Rom resampling.
func downscale(img image.Image, maxSide int) image.Image {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= maxSide && height <= maxSide {
		return img
	}
	scale := float64(maxSide) / float64(max(width, height))
	target := image.Rect(0, 0, max(1, int(float64(width)*scale+0.5)), max(1, int(float64(height)*scale+0.5)))
	dst := image.NewRGBA(target)
	draw.CatmullRom.Scale(dst, target, img, bounds, draw.Src, nil)
	return dst
}

// encodeFitting encodes under imageEncodedCap, stepping JPEG quality down
// before giving up; a PNG over the cap retries as JPEG.
func encodeFitting(img image.Image, preferPNG bool) (llm.ImageBlock, error) {
	if preferPNG {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err == nil && buf.Len() <= imageEncodedCap {
			return llm.ImageBlock{MIMEType: "image/png", Data: buf.Bytes()}, nil
		}
	}
	for _, quality := range []int{85, 70, 55, 40} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return llm.ImageBlock{}, fmt.Errorf("encode image: %w", err)
		}
		if buf.Len() <= imageEncodedCap {
			return llm.ImageBlock{MIMEType: "image/jpeg", Data: buf.Bytes()}, nil
		}
	}
	return llm.ImageBlock{}, fmt.Errorf("image still over %d bytes after downscaling to %dpx — attach a smaller one", imageEncodedCap, imageMaxSide)
}

func (a *Activities) handleViewImage(request ToolRequest) (ToolResult, error) {
	pathArg, _ := request.Arguments["path"].(string)
	if strings.TrimSpace(pathArg) == "" {
		return ToolResult{}, invalidToolArguments(fmt.Errorf("view_image requires path"))
	}
	abs, err := a.resolveToolTarget(request, pathArg)
	if err != nil {
		return ToolResult{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return ToolResult{}, invalidToolArguments(fmt.Errorf("view %q: %w", cleanPath(pathArg), err))
	}
	if info.IsDir() {
		return ToolResult{}, invalidToolArguments(fmt.Errorf("%q is a directory", cleanPath(pathArg)))
	}
	if info.Size() > imageSourceCap {
		return ToolResult{}, invalidToolArguments(fmt.Errorf("%q is %d bytes; the cap is %d", cleanPath(pathArg), info.Size(), imageSourceCap))
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return ToolResult{}, fileToolFailure(fmt.Errorf("view %q: %w", cleanPath(pathArg), err))
	}
	block, meta, err := prepareImageBlock(data)
	if err != nil {
		return ToolResult{}, invalidToolArguments(fmt.Errorf("%q: %w", cleanPath(pathArg), err))
	}
	return ToolResult{Content: meta.summary(), Images: []llm.ImageBlock{block}}, nil
}

func (a *Activities) handleFetchImage(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if !a.NetworkAccess {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("network access is disabled for this agent", "NetworkDisabled", nil)
	}
	rawURL, _ := request.Arguments["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("url is required", "InvalidToolArguments", nil)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ToolResult{}, temporal.NewNonRetryableApplicationError("url must be an absolute http(s) URL", "InvalidToolArguments", nil)
	}

	client := webHTTPClient
	if a.WebFetchAllowPrivate {
		client = webHTTPPrivateClient
	}
	ctx, cancel := context.WithTimeout(ctx, webFetchTimeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ToolResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidToolArguments", nil)
	}
	httpRequest.Header.Set("User-Agent", "Mozilla/5.0 (compatible; TemporalityAgent/1.0; +fetch_image)")
	response, err := client.Do(httpRequest)
	if err != nil {
		return ToolResult{}, fmt.Errorf("fetch_image: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("fetch_image: HTTP %d from %s", response.StatusCode, response.Request.URL.String()), "HTTPStatus", nil)
	}
	if mediaType := response.Header.Get("Content-Type"); strings.HasPrefix(strings.ToLower(mediaType), "text/html") {
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("fetch_image: %q is an HTML page — use web_fetch for pages", mediaType), "UnsupportedContent", nil)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, imageSourceCap+1))
	if err != nil {
		return ToolResult{}, fmt.Errorf("fetch_image: read body: %w", err)
	}
	if len(data) > imageSourceCap {
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("fetch_image: image exceeds %d bytes", imageSourceCap), "TooLarge", nil)
	}
	// Magic bytes decide: a mislabeled content-type must not break decoding,
	// and a binary that is not an image must fail clearly.
	block, meta, err := prepareImageBlock(data)
	if err != nil {
		return ToolResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("fetch_image: %s: %v — for pages use web_fetch, for other binaries use run_command with curl", response.Request.URL.String(), err), "UnsupportedContent", nil)
	}
	return ToolResult{Content: meta.summary(), Images: []llm.ImageBlock{block}}, nil
}
