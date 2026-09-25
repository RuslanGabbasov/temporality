// Command nb is the nightly box CLI fixture for Experiment 5 (long-horizon
// memory). Three independent quirks, all partially mis-documented in the
// README, so every mechanism must be verified empirically.
//
// v2 behavior:
//
//	nb auth           reads .token-file only (AUTH_TOKEN is NOT supported)
//	nb report         requires an existing out/ directory, writes out/report.txt
//	nb fetch -limit N uses -limit (--count is NOT supported)
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
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

// auth v2: token-file authentication. The AUTH_TOKEN environment variable
// was the v1 mechanism and is no longer read.
func auth() {
	data, err := os.ReadFile(".token-file")
	if err != nil || len(data) == 0 {
		fmt.Fprintln(os.Stderr, "auth failed: no token file")
		os.Exit(1)
	}
	fmt.Println("auth ok")
}

// report v2: writes out/report.txt. The out/ directory must already exist;
// the CLI does not create it.
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

// fetch v2: -limit is the page size flag. --count was the old spelling and
// is rejected by the flag parser.
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
