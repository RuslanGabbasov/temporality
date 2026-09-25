// Command lighthouse (lh) is the long-horizon evolution fixture for
// Experiment 8: three environment generations where one mechanism reverts
// (login), one migrates once (deploy), one changes only in the last
// generation (report), and one never changes (build). Every mechanism is
// partially mis-documented in the README, so each behavior must be verified
// empirically.
//
// v1 behavior (the original):
//
//	lh login    reads the LH_TOKEN environment variable (the README's
//	              --token flag is not implemented)
//	lh build    plain build, no requirements
//	lh deploy   requires a non-empty --env flag (e.g. --env prod)
//	lh report   writes report.txt in the working directory
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

const lighthouseVersion = "1"

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

// login v1: environment authentication. The README's --token flag is not
// implemented.
func login(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	_ = fs.String("token", "", "auth token")
	_ = fs.Parse(args)
	_ = requireEnv("LH_TOKEN")
	fmt.Println("login ok")
}

// build v1: plain build with no requirements; the stable mechanism across
// every generation.
func build(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	_ = fs.Parse(args)
	fmt.Println("build ok")
}

// deploy v1: deploys to the environment named by the --env flag.
func deploy(args []string) {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	env := fs.String("env", "", "target environment")
	_ = fs.Parse(args)
	if strings.TrimSpace(*env) == "" {
		fmt.Fprintf(os.Stderr, "%s failed: --env is required (e.g. --env prod)\n", commandName(1))
		os.Exit(1)
	}
	fmt.Printf("deployed to %s\n", *env)
}

// report v1: writes a plain-text report artifact.
func report(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	_ = fs.Parse(args)
	if err := os.WriteFile("report.txt", []byte("lighthouse report v"+lighthouseVersion+"\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", commandName(1), err)
		os.Exit(1)
	}
	fmt.Println("wrote report.txt")
}
