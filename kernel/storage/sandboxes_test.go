package storage

import (
	"testing"
)

func TestParseDockerPercent(t *testing.T) {
	cases := map[string]float64{
		"12.34%": 12.34,
		"0.00%":  0,
		"150.7%": 150.7,
		"":       0,
	}
	for raw, want := range cases {
		if got := parseDockerPercent(raw); got != want {
			t.Errorf("parseDockerPercent(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestParseDockerBytes(t *testing.T) {
	cases := map[string]int64{
		"0B":       0,
		"512B":     512,
		"1.5kB":    1500,
		"2MiB":     2 << 20,
		"123.4MiB": 129394278, // 123.4 * 2^20 truncated
		"1GiB":     1 << 30,
		"45kB":     45000,
	}
	for raw, want := range cases {
		got, err := parseDockerBytes(raw)
		if err != nil {
			t.Fatalf("parseDockerBytes(%q): %v", raw, err)
		}
		if got != want {
			t.Errorf("parseDockerBytes(%q) = %d, want %d", raw, got, want)
		}
	}
	if _, err := parseDockerBytes("12 bananas"); err == nil {
		t.Error("parseDockerBytes should reject unknown units")
	}
}

func TestParseDockerUsageAndNetIO(t *testing.T) {
	used, limit, ok := parseDockerUsage("123.4MiB / 1.5GiB")
	if !ok || used != 129394278 || limit != 1610612736 {
		t.Errorf("parseDockerUsage = %d/%d ok=%v", used, limit, ok)
	}
	if _, _, ok := parseDockerUsage("garbage"); ok {
		t.Error("parseDockerUsage should reject malformed input")
	}
	in, out, ok := parseDockerNetIO("1.2kB / 45kB")
	if !ok || in != 1200 || out != 45000 {
		t.Errorf("parseDockerNetIO = %d/%d ok=%v", in, out, ok)
	}
}

func TestParseSandboxPsLine(t *testing.T) {
	name, image, labels := parseSandboxPsLine("temporality-sandbox-abc", "temporality.project=omni,temporality.run=untitled-20261016, temporality.actor=ivan", "temporality-sandbox:latest")
	if name != "temporality-sandbox-abc" || image != "temporality-sandbox:latest" {
		t.Fatalf("name/image = %q/%q", name, image)
	}
	if labels["temporality.project"] != "omni" || labels["temporality.run"] != "untitled-20261016" || labels["temporality.actor"] != "ivan" {
		t.Errorf("labels = %v", labels)
	}
	_, _, empty := parseSandboxPsLine("x", "", "")
	if len(empty) != 0 {
		t.Errorf("empty labels should stay empty, got %v", empty)
	}
}
