package main

import "testing"

// The nightly gate runs `go test ./...`; this suite pins the basics so the
// gate has something real to check.
func TestVersionConstant(t *testing.T) {
	if lighthouseVersion == "" {
		t.Fatal("lighthouseVersion must not be empty")
	}
}

func TestCommandNameFallback(t *testing.T) {
	if got := commandName(10); got != "lh" {
		t.Fatalf("commandName fallback = %q, want lh", got)
	}
}
