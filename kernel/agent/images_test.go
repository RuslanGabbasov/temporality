package agent

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// encodePNGForTest renders an offscreen image as PNG bytes.
func encodePNGForTest(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func solidImage(width, height int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x7f, A: 0xff})
		}
	}
	return img
}

func TestPrepareImageBlockPassthrough(t *testing.T) {
	original := encodePNGForTest(t, solidImage(100, 50))
	block, meta, err := prepareImageBlock(original)
	if err != nil {
		t.Fatalf("prepareImageBlock: %v", err)
	}
	if meta.resized {
		t.Fatal("a small PNG must pass through without resizing")
	}
	if meta.mime != "image/png" || meta.width != 100 || meta.height != 50 {
		t.Fatalf("unexpected meta: %+v", meta)
	}
	if !bytes.Equal(block.Data, original) {
		t.Fatal("passthrough must preserve the original bytes")
	}
	if block.MIMEType != "image/png" {
		t.Fatalf("unexpected block mime: %s", block.MIMEType)
	}
}

func TestPrepareImageBlockDownscales(t *testing.T) {
	block, meta, err := prepareImageBlock(encodePNGForTest(t, solidImage(3000, 2000)))
	if err != nil {
		t.Fatalf("prepareImageBlock: %v", err)
	}
	if !meta.resized {
		t.Fatal("an oversized image must be marked resized")
	}
	if meta.width > imageMaxSide || meta.height > imageMaxSide {
		t.Fatalf("long side over budget: %dx%d", meta.width, meta.height)
	}
	// 3000x2000 keeps the 3:2 ratio: expect exactly 1568x1045.
	if meta.width != 1568 || meta.height != 1045 {
		t.Fatalf("unexpected scaled dimensions: %dx%d", meta.width, meta.height)
	}
	if meta.bytes != len(block.Data) || meta.bytes > imageEncodedCap {
		t.Fatalf("encoded size %d (meta %d) must fit the cap %d", len(block.Data), meta.bytes, imageEncodedCap)
	}
	// PNG sources stay PNG when the re-encode fits the cap; only an oversized
	// PNG falls through to JPEG (covered by the noise test below).
	if block.MIMEType != "image/png" {
		t.Fatalf("a compressible PNG re-encodes as PNG, got %s", block.MIMEType)
	}
	decoded, format, err := image.Decode(bytes.NewReader(block.Data))
	if err != nil || format != "png" {
		t.Fatalf("re-encoded block must decode as png: %v %s", err, format)
	}
	if decoded.Bounds().Dx() != 1568 {
		t.Fatalf("decoded width %d", decoded.Bounds().Dx())
	}
}

// hybridNoiseImage builds an incompressible-noise strip over a flat field:
// the source PNG stays small, but its re-encode after downscaling tops
// imageEncodedCap, forcing the JPEG fallback in encodeFitting.
func hybridNoiseImage(width, height, noiseRows int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	next := uint32(0x9e3779b9)
	for y := 0; y < noiseRows && y < height; y++ {
		for x := 0; x < width; x++ {
			next ^= next << 13
			next ^= next >> 17
			next ^= next << 5
			offset := img.PixOffset(x, y)
			img.Pix[offset] = byte(next)
			img.Pix[offset+1] = byte(next >> 8)
			img.Pix[offset+2] = byte(next >> 16)
			img.Pix[offset+3] = 0xff
		}
	}
	return img.SubImage(image.Rect(0, 0, width, height))
}

func TestPrepareImageBlockNoiseFallsBackToJPEG(t *testing.T) {
	source := encodePNGForTest(t, hybridNoiseImage(1600, 1100, 300))
	block, meta, err := prepareImageBlock(source)
	if err != nil {
		t.Fatalf("prepareImageBlock(noise): %v (source %d bytes)", err, len(source))
	}
	if meta.width != 1568 || meta.height != 1078 {
		t.Fatalf("unexpected scaled dimensions: %dx%d", meta.width, meta.height)
	}
	if block.MIMEType != "image/jpeg" || meta.mime != "image/jpeg" {
		t.Fatalf("oversized PNG must fall back to JPEG, got block %s meta %s (%d bytes)", block.MIMEType, meta.mime, len(block.Data))
	}
	if len(block.Data) > imageEncodedCap {
		t.Fatalf("jpeg fallback must fit the cap: %d bytes", len(block.Data))
	}
}

func TestPrepareImageBlockGifBecomesFirstFramePNG(t *testing.T) {
	palette := color.Palette{color.White, color.Black}
	frameA := image.NewPaletted(image.Rect(0, 0, 40, 30), palette)
	for i := range frameA.Pix {
		frameA.Pix[i] = 1 // solid black
	}
	frameB := image.NewPaletted(image.Rect(0, 0, 40, 30), palette)
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{Image: []*image.Paletted{frameA, frameB}, Delay: []int{10, 10}, Config: image.Config{ColorModel: palette, Width: 40, Height: 30}}); err != nil {
		t.Fatal(err)
	}
	block, meta, err := prepareImageBlock(buf.Bytes())
	if err != nil {
		t.Fatalf("prepareImageBlock(gif): %v", err)
	}
	// A small GIF still converts: animated frames are not vision input.
	if meta.resized {
		t.Fatal("a small GIF converts to PNG without scaling")
	}
	if meta.mime != "image/png" || block.MIMEType != "image/png" {
		t.Fatalf("gif must convert to png, got meta %s block %s", meta.mime, block.MIMEType)
	}
	if _, format, err := image.Decode(bytes.NewReader(block.Data)); err != nil || format != "png" {
		t.Fatalf("converted block must decode as png: %v %s", err, format)
	}
}

func TestPrepareImageBlockRejectsGarbage(t *testing.T) {
	if _, _, err := prepareImageBlock([]byte("definitely not an image")); err == nil {
		t.Fatal("garbage input must fail")
	}
}

func TestViewImageAttachesWorkspaceFile(t *testing.T) {
	activities, workspace := newFileToolActivities(t)
	if err := os.WriteFile(filepath.Join(workspace, "shot.png"), encodePNGForTest(t, solidImage(64, 64)), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := activities.RunTool(context.Background(), fileToolRequest("view_image", workspace, false, map[string]any{"path": "shot.png"}))
	if err != nil {
		t.Fatalf("view_image: %v", err)
	}
	if !strings.Contains(result.Content, "image/png 64x64") {
		t.Fatalf("content must summarize the image meta:\n%s", result.Content)
	}
	if len(result.Images) != 1 || result.Images[0].MIMEType != "image/png" {
		t.Fatalf("expected one png vision block, got %+v", result.Images)
	}
}

func TestViewImageRejectsEscapeAndNonImage(t *testing.T) {
	activities, workspace := newFileToolActivities(t)
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := activities.RunTool(context.Background(), fileToolRequest("view_image", workspace, false, map[string]any{"path": "../../etc/passwd"}))
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("path escape must be rejected, got %v", err)
	}
	_, err = activities.RunTool(context.Background(), fileToolRequest("view_image", workspace, false, map[string]any{"path": "notes.txt"}))
	kind, _ := nonRetryableType(t, err)
	if kind != "InvalidToolArguments" {
		t.Fatalf("non-image must fail non-retryably, got %s", kind)
	}
}

func TestFetchImageDownloadsAndAttaches(t *testing.T) {
	payload := encodePNGForTest(t, solidImage(32, 32))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(payload)
	}))
	defer server.Close()

	a := &Activities{NetworkAccess: true, WebFetchAllowPrivate: true}
	result, err := a.handleFetchImage(context.Background(), ToolRequest{Arguments: map[string]any{"url": server.URL + "/pic.png"}})
	if err != nil {
		t.Fatalf("handleFetchImage: %v", err)
	}
	if len(result.Images) != 1 || result.Images[0].MIMEType != "image/png" {
		t.Fatalf("expected one png block, got %+v", result.Images)
	}
	if !bytes.Equal(result.Images[0].Data, payload) {
		t.Fatal("small png must pass through byte-identical")
	}
	if !strings.Contains(result.Content, "32x32") {
		t.Fatalf("content must carry dimensions:\n%s", result.Content)
	}
}

func TestFetchImageRejectsHTMLAndNetworkOffAndPrivateBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html><body>no</body></html>"))
	}))
	defer server.Close()

	// HTML page → UnsupportedContent, non-retryable (use web_fetch instead).
	a := &Activities{NetworkAccess: true, WebFetchAllowPrivate: true}
	_, err := a.handleFetchImage(context.Background(), ToolRequest{Arguments: map[string]any{"url": server.URL}})
	kind, message := nonRetryableType(t, err)
	if kind != "UnsupportedContent" || !strings.Contains(message, "web_fetch") {
		t.Fatalf("html must point at web_fetch, got %s: %s", kind, message)
	}

	// Network disabled → NetworkDisabled.
	_, err = (&Activities{NetworkAccess: false}).handleFetchImage(context.Background(), ToolRequest{Arguments: map[string]any{"url": "https://example.com/x.png"}})
	kind, _ = nonRetryableType(t, err)
	if kind != "NetworkDisabled" {
		t.Fatalf("expected NetworkDisabled, got %s", kind)
	}

	// Private loopback blocked by the SSRF guard unless explicitly allowed.
	_, err = (&Activities{NetworkAccess: true}).handleFetchImage(context.Background(), ToolRequest{Arguments: map[string]any{"url": server.URL}})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked-target error, got %v", err)
	}
}

func TestWorkflowInjectsImageBlocksAsUserMessage(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(AgentRun, workflow.RegisterOptions{Name: "AgentRun"})
	registerExtractionChild(env)
	env.RegisterActivityWithOptions(func(_ context.Context, event observation.Event) error { return nil }, activity.RegisterOptions{Name: ActivityRecordEvent})
	env.RegisterActivityWithOptions(func(context.Context, HintRequest) ([]Hint, error) { return nil, nil }, activity.RegisterOptions{Name: ActivityKnowledgeHints})
	env.RegisterActivityWithOptions(func(context.Context, KnowledgeLookupQuery) (KnowledgeLookupResult, error) {
		return KnowledgeLookupResult{}, nil
	}, activity.RegisterOptions{Name: ActivityKnowledgeLookup})
	var requests []ModelRequest
	env.RegisterActivityWithOptions(func(_ context.Context, request ModelRequest) (llm.Completion, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return llm.Completion{ToolCalls: []llm.ToolCall{{ID: "img-call", Name: "view_image", Args: map[string]any{"path": "shot.png"}}}}, nil
		}
		return llm.Completion{Content: "the screenshot shows a green square"}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	images := 0
	env.RegisterActivityWithOptions(func(context.Context, ToolRequest) (ToolResult, error) {
		images++
		return ToolResult{
			Content: "image attached: image/png 32x32, 120 bytes",
			Images:  []llm.ImageBlock{{MIMEType: "image/png", Data: []byte("png-bytes")}},
		}, nil
	}, activity.RegisterOptions{Name: ActivityRunTool})
	registerExtraction(env, KnowledgeExtractResult{}, nil)
	env.ExecuteWorkflow("AgentRun", RunInput{RunID: "vision-run", Project: "repo", Prompt: "look at the screenshot", WorkspacePath: "/workspace/task"})
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 1, images)
	require.Len(t, requests, 2)
	second := requests[1].Messages
	toolAt := -1
	for i, message := range second {
		if message.Role == "tool" && message.ToolCallID == "img-call" {
			toolAt = i
		}
	}
	require.GreaterOrEqual(t, toolAt, 0, "tool reply missing from second model request")
	require.Less(t, toolAt+1, len(second), "image injection message missing")
	followUp := second[toolAt+1]
	require.Equal(t, "user", followUp.Role, "image blocks must ride a user message right after the tool reply")
	require.Len(t, followUp.Images, 1)
	require.Equal(t, "image/png", followUp.Images[0].MIMEType)
	require.Equal(t, []byte("png-bytes"), followUp.Images[0].Data)
}
