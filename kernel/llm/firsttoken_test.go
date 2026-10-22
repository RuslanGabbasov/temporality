package llm

import (
	"strings"
	"testing"
	"time"
)

func TestTextPayloadBytesDiscountsBase64Images(t *testing.T) {
	megabyte := strings.Repeat("a", 1<<20)
	// 3MB of base64 image bytes inside the payload.
	image := `{"url":"data:image/png;base64,` + megabyte + `"}` + `{"url":"data:image/jpeg;base64,` + megabyte + `"}` + `{"url":"data:image/png;base64,` + megabyte + `"}`
	payload := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{` + image + `}},{"type":"text","text":"describe"}]}]}`)

	text := textPayloadBytes(payload)
	if text > 3*len(";base64,")+4096 {
		t.Fatalf("base64 blobs must be discounted: text=%d raw=%d", text, len(payload))
	}
	if text < 100 {
		t.Fatalf("surrounding JSON must still count: %d", text)
	}
}

func TestTextPayloadBytesHandlesPlainPayload(t *testing.T) {
	payload := []byte(`{"model":"m","messages":[{"content":"hello world"}]}`)
	if got := textPayloadBytes(payload); got != len(payload) {
		t.Fatalf("plain payload must count as-is: %d != %d", got, len(payload))
	}
	// Unterminated data URI: the tail counts (defensive overcount).
	broken := []byte(`{"url":"data:image/png;base64,AAAA`)
	if got := textPayloadBytes(broken); got <= len(`{"url":"data:image/png;base64,`) {
		t.Fatalf("broken data URI must count its tail: %d", got)
	}
}

func TestFirstTokenPrefillBonusCapped(t *testing.T) {
	// ~4MB of text ≈ 1M estimated tokens × 2.5ms ≈ 42m — the cap holds it at
	// 10 minutes.
	text4MB := []byte(strings.Repeat("a", 4<<20))
	if bonus := firstTokenPrefillBonus(text4MB); bonus != 10*time.Minute {
		t.Fatalf("4MB text bonus = %s, want the 10m cap", bonus)
	}
	// 40MB of raw base64 images: the raw-length formula would give +40m;
	// discounted the images contribute nearly nothing — that is the fix.
	images := []byte(`{"u":"data:image/png;base64,` + strings.Repeat("a", 20<<20) + `","v":"data:image/jpeg;base64,` + strings.Repeat("b", 20<<20) + `"}`)
	if bonus := firstTokenPrefillBonus(images); bonus > 30*time.Second {
		t.Fatalf("image-heavy payload bonus = %s, want the base64 discount", bonus)
	}
	// A 150KB text payload (~37k tokens) buys ~90s of prefill patience on top
	// of the base — an uncached big-context prefill at a few hundred tokens/s
	// must survive instead of being killed mid-work.
	payload150KB := []byte(strings.Repeat("x", 150_000))
	if bonus := firstTokenPrefillBonus(payload150KB); bonus < 90*time.Second || bonus > 95*time.Second {
		t.Fatalf("150KB text bonus = %s, want ~93s of token-scaled patience", bonus)
	}
	// No payload: just the base 2-minute budget.
	client := &Client{cfg: Config{StreamFirstTokenTimeout: 2 * time.Minute}}
	if budget := client.firstTokenBudget(nil); budget != 2*time.Minute {
		t.Fatalf("base budget = %s", budget)
	}
	// Explicitly disabled (negative) stays disabled.
	off := &Client{cfg: Config{StreamFirstTokenTimeout: -1}}
	if budget := off.firstTokenBudget([]byte("hello")); budget != 0 {
		t.Fatalf("disabled budget = %s", budget)
	}
}
