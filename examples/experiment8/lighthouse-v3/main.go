// Command lighthouse (lh) is the long-horizon evolution fixture for
// Experiment 8: three environment generations where one mechanism reverts
// (login), one migrates once (deploy), one changes only in the last
// generation (report), and one never changes (build). Every mechanism is
// partially mis-documented in the README, so each behavior must be verified
// empirically.
//
// v3 behavior (the second upgrade — a partial revert):
//
//	lh login    reads the LH_TOKEN environment variable again (the v2
//	              token file is no longer honored; the README's --token
//	              flag is still not implemented)
//	lh build    unchanged: plain build, no requirements
//	lh deploy   unchanged from v2: the LH_STAGE environment variable
//	lh report   the --format flag is now required (e.g. --format json);
//	              the artifact is report.json
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

const lighthouseVersion = "3"

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

// login v3: the revert. Environment authentication is back exactly as it
// was in v1; the v2 token file is no longer honored.
func login(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	_ = fs.String("token", "", "auth token")
	_ = fs.Parse(args)
	_ = requireEnv("LH_TOKEN")
	fmt.Println("login ok")
}

// build v3: unchanged — plain build with no requirements; the stable
// mechanism across every generation.
func build(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	_ = fs.Parse(args)
	fmt.Println("build ok")
}

// deploy v3: unchanged from v2 — deployment targets the LH_STAGE
// environment variable.
func deploy(args []string) {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	_ = fs.String("env", "", "ignored since v2")
	_ = fs.Parse(args)
	stage := requireEnv("LH_STAGE")
	fmt.Printf("deployed to %s\n", stage)
}

// report v3: the late change. The --format flag is now required and the
// artifact is report.json; plain `lh report` no longer works.
func report(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	format := fs.String("format", "", "artifact format")
	_ = fs.Parse(args)
	if strings.TrimSpace(*format) == "" {
		fmt.Fprintf(os.Stderr, "%s failed: --format is required (e.g. --format json)\n", commandName(1))
		os.Exit(1)
	}
	if err := os.WriteFile("report."+*format, []byte("lighthouse report v"+lighthouseVersion+"\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", commandName(1), err)
		os.Exit(1)
	}
	fmt.Printf("wrote report.%s\n", *format)
}
