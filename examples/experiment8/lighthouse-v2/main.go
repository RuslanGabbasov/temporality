// Command lighthouse (lh) is the long-horizon evolution fixture for
// Experiment 8: three environment generations where one mechanism reverts
// (login), one migrates once (deploy), one changes only in the last
// generation (report), and one never changes (build). Every mechanism is
// partially mis-documented in the README, so each behavior must be verified
// empirically.
//
// v2 behavior (the first upgrade):
//
//	lh login    reads the token from the .lh-token file in the working
//	              directory (LH_TOKEN is no longer honored; the README's
//	              --token flag is still not implemented)
//	lh build    unchanged: plain build, no requirements
//	lh deploy   the --env flag is gone; deploy targets the LH_STAGE
//	              environment variable
//	lh report   unchanged: writes report.txt
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

const lighthouseVersion = "2"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lh <login|build|deploy|report|version> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "login":
		login(os.Args[2:])
	case "build":
		build(os.Args[2:])
	case "deploy":
		deploy(os.Args[2:])
	case "report":
		report(os.Args[2:])
	case "version":
		fmt.Println("lighthouse v" + lighthouseVersion)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

func commandName(skip int) string {
	if len(os.Args) > skip {
		return "lh " + os.Args[skip]
	}
	return "lh"
}

func requireEnv(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		fmt.Fprintf(os.Stderr, "%s failed: %s not set\n", commandName(1), name)
		os.Exit(1)
	}
	return value
}

// login v2: file-based authentication. The token must live in .lh-token in
// the working directory; the environment variable and the README's --token
// flag are no longer honored.
func login(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	_ = fs.String("token", "", "auth token")
	_ = fs.Parse(args)
	token, err := os.ReadFile(".lh-token")
	if err != nil || strings.TrimSpace(string(token)) == "" {
		fmt.Fprintf(os.Stderr, "%s failed: .lh-token file missing\n", commandName(1))
		os.Exit(1)
	}
	fmt.Println("login ok")
}

// build v2: unchanged — plain build with no requirements; the stable
// mechanism across every generation.
func build(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	_ = fs.Parse(args)
	fmt.Println("build ok")
}

// deploy v2: the --env flag is gone. Deployment targets the LH_STAGE
// environment variable instead.
func deploy(args []string) {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	_ = fs.String("env", "", "ignored since v2")
	_ = fs.Parse(args)
	stage := requireEnv("LH_STAGE")
	fmt.Printf("deployed to %s\n", stage)
}

// report v2: unchanged — writes a plain-text report artifact.
func report(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	_ = fs.Parse(args)
	if err := os.WriteFile("report.txt", []byte("lighthouse report v"+lighthouseVersion+"\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", commandName(1), err)
		os.Exit(1)
	}
	fmt.Println("wrote report.txt")
}
