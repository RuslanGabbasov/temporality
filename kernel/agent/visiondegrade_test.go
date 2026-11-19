package agent

import (
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/llm"
)

// stripImagesForTextModel backs the no-vision-model degradation path: an
// image-bearing payload sent to a text-only endpoint is a guaranteed provider
// 400 (z.ai code 1210), so the images are dropped and the message carries an
// honest note instead — the model must know it cannot see what was shown.
func TestStripImagesForTextModel(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "You are a coding agent."},
		{Role: "user", Content: "Что на скриншоте?", Images: []llm.ImageBlock{{MIMEType: "image/png", Data: []byte("png-bytes")}}},
		{Role: "assistant", Content: "Промежуточный ответ"},
		{Role: "user", Content: "Ещё две картинки", Images: []llm.ImageBlock{
			{MIMEType: "image/png", Data: []byte("a")},
			{MIMEType: "image/jpeg", Data: []byte("b")},
		}},
	}

	out := stripImagesForTextModel(messages)

	// Originals untouched: the caller's request stays intact for journaling.
	if len(messages[1].Images) != 1 {
		t.Fatalf("input messages must not be mutated: images = %d", len(messages[1].Images))
	}
	for i := range out {
		if len(out[i].Images) != 0 {
			t.Fatalf("message %d still carries images", i)
		}
	}
	if !strings.Contains(out[1].Content, "image not delivered") {
		t.Fatalf("the degraded note must tell the model what happened: %q", out[1].Content)
	}
	if !strings.Contains(out[1].Content, "Что на скриншоте?") {
		t.Fatalf("the original text must survive: %q", out[1].Content)
	}
	if !strings.Contains(out[3].Content, "2 images omitted") {
		t.Fatalf("the note must count the omitted images: %q", out[3].Content)
	}
	// Clean messages keep their content byte-identical (the prompt-cache
	// prefix must not shift).
	if out[0].Content != "You are a coding agent." || out[2].Content != "Промежуточный ответ" {
		t.Fatalf("clean messages must be untouched: %q %q", out[0].Content, out[2].Content)
	}
}
