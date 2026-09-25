package main

import "testing"

// The nightly gate runs `go test ./...`; this suite pins the basics so the
// gate has something real to check.
func TestVersionConstant(t *testing.T) {
	if forgeVersion == "" {
		t.Fatal("forgeVersion must not be empty")
	}
}

func TestCommandNameFallback(t *testing.T) {
	if got := commandName(10); got != "forge" {
		t.Fatalf("commandName fallback = %q, want forge", got)
	}
}
