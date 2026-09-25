// Command relay is the deployment relay CLI fixture for Experiment 6, phase 3
// ("the second upgrade"). Compared to v2 the auth mechanism flipped back:
// authentication reads the API_KEY environment variable again and the
// keys/key.txt mechanism is gone. Push is unchanged. The health probe is
// disabled in this build: it reports an unknown state and exits non-zero,
// which neither confirms nor contradicts earlier health conclusions.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: relay <auth|push|check> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "auth":
		auth(os.Args[2:])
	case "push":
		push(os.Args[2:])
	case "check":
		check()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

// auth v3: environment authentication again — the same mechanism as v1. The
// keys/key.txt mechanism of v2 was removed.
func auth(args []string) {
	fs := flag.NewFlagSet("auth", flag.ExitOnError)
	_ = fs.String("token", "", "auth token")
	_ = fs.Parse(args)
	if strings.TrimSpace(os.Getenv("API_KEY")) == "" {
		fmt.Fprintln(os.Stderr, "auth failed: API_KEY not set")
		os.Exit(1)
	}
	fmt.Println("auth ok")
}

// push v3: unchanged from v1/v2 — requires a non-empty CHANNEL env var.
func push(args []string) {
	fs := flag.NewFlagSet("push", flag.ExitOnError)
	_ = fs.Parse(args)
	channel := strings.TrimSpace(os.Getenv("CHANNEL"))
	if channel == "" {
		fmt.Fprintln(os.Stderr, "push failed: CHANNEL not set")
		os.Exit(1)
	}
	fmt.Printf("pushed to %s\n", channel)
}

// check v3: the health probe is disabled in this build. "unknown" with a
// non-zero exit is deliberately ambiguous: it does not confirm a previously
// green probe and does not contradict it either.
func check() {
	fmt.Println("health: unknown (probe disabled)")
	os.Exit(2)
}
