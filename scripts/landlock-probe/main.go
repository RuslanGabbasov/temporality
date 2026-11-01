//go:build !linux

// Command landlock-probe verifies Landlock availability and enforcement for
// the Temporality sandbox (docs/filesystem-policy.md §5.3). This stub keeps
// the package buildable everywhere; the real probe (probe_linux.go) runs
// inside a Linux sandbox container — Landlock is a Linux LSM.
package main

import "fmt"

func main() {
	fmt.Println("landlock-probe: Landlock is a Linux facility — run this inside the sandbox container")
	fmt.Println("(docs/filesystem-policy-implementation.md §4): GOOS=linux go build ./scripts/landlock-probe")
}
