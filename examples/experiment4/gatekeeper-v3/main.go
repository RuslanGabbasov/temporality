package main

import (
	"fmt"
	"os"
	"strings"
)

// gatekeeper v3 — environment authentication.
// The token-file mechanism was removed in v3.

func main() {
	token := os.Getenv("AUTH_TOKEN")
	if strings.TrimSpace(token) == "" {
		fmt.Fprintln(os.Stderr, "access denied: no token")
		os.Exit(1)
	}
	fmt.Println("access granted")
}
