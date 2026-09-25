// Command relay is the deployment relay CLI fixture for Experiment 6
// (competing experiences across two environment flips). Three independent
// mechanisms, all partially mis-documented in the README, so every behavior
// must be verified empirically.
//
// v1 behavior:
//
//	relay auth    reads the API_KEY environment variable (--token is NOT implemented)
//	relay push    requires a non-empty CHANNEL environment variable
//	relay check   prints "health: green" to stdout
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

// auth v1: environment authentication. The README's --token flag was never
// implemented in this build.
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

// push v1: publishing requires a non-empty CHANNEL environment variable.
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

// check v1: the health probe prints to stdout (not stderr as documented).
func check() {
	fmt.Println("health: green")
}
