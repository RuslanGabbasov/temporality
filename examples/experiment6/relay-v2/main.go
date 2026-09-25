// Command relay is the deployment relay CLI fixture for Experiment 6, phase 2
// ("the upgrade"). Compared to v1 only the auth mechanism flipped:
// authentication now reads keys/key.txt and the API_KEY environment variable
// is no longer read. Push and check behavior is unchanged.
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

// auth v2: file-based authentication. The API_KEY environment variable was
// the v1 mechanism and is no longer read.
func auth(args []string) {
	fs := flag.NewFlagSet("auth", flag.ExitOnError)
	_ = fs.String("token", "", "auth token")
	_ = fs.Parse(args)
	data, err := os.ReadFile("keys/key.txt")
	if err != nil || len(data) == 0 {
		fmt.Fprintln(os.Stderr, "auth failed: no key file (keys/key.txt)")
		os.Exit(1)
	}
	fmt.Println("auth ok")
}

// push v2: unchanged from v1 — requires a non-empty CHANNEL env var.
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

// check v2: unchanged from v1 — prints "health: green" to stdout.
func check() {
	fmt.Println("health: green")
}
