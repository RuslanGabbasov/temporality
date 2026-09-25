package main

import (
	"fmt"
	"os"
	"strings"
)

// gatekeeper v2 — token-file authentication.
// The AUTH_TOKEN environment variable was removed in v2.

func main() {
	data, err := os.ReadFile(".token-file")
	if err != nil || strings.TrimSpace(string(data)) == "" {
		fmt.Fprintln(os.Stderr, "access denied: no token file")
		os.Exit(1)
	}
	fmt.Println("access granted")
}
