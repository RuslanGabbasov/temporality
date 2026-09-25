// Command nb is the nightly box CLI fixture for Experiment 5, run 5+ ("the
// upgrade"). Compared to v2 only the auth mechanism flipped: authentication
// now reads the AUTH_TOKEN environment variable and the token file is gone.
// The report and fetch behavior is unchanged.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: nb <auth|report|fetch> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "auth":
		auth()
	case "report":
		report()
	case "fetch":
		fetch(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

// auth v3: environment authentication. The token-file mechanism was removed.
func auth() {
	if strings.TrimSpace(os.Getenv("AUTH_TOKEN")) == "" {
		fmt.Fprintln(os.Stderr, "auth failed: no token")
		os.Exit(1)
	}
	fmt.Println("auth ok")
}

// report v3: unchanged from v2 — requires an existing out/ directory.
func report() {
	if info, err := os.Stat("out"); err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "report failed: out/ directory missing")
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join("out", "report.txt"), []byte("nightly report\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "report failed:", err)
		os.Exit(1)
	}
	fmt.Println("report written to out/report.txt")
}

// fetch v3: unchanged from v2 — the flag is -limit.
func fetch(args []string) {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	limit := fs.Int("limit", 0, "number of items")
	_ = fs.Parse(args)
	if *limit <= 0 {
		fmt.Fprintln(os.Stderr, "fetch failed: -limit is required")
		os.Exit(1)
	}
	fmt.Printf("fetched %d items\n", *limit)
}
