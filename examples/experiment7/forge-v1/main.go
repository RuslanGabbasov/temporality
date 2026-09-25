// Command forge is the release-pipeline CLI fixture for Experiment 7
// (reinforcement cycles, parallel competing hypotheses, scale). Five
// independent mechanisms, all partially mis-documented in the README, so every
// behavior must be verified empirically.
//
// v1 behavior:
//
//	forge login    reads the FORGE_TOKEN environment variable (--token is NOT implemented)
//	forge build    always full rebuild; CACHE_DIR is ignored in this build
//	forge pack     writes dist.tar.gz
//	forge sign     reads the SIGN_KEY environment variable (--key is NOT implemented)
//	forge publish  requires a non-empty CHANNEL environment variable
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

const forgeVersion = "1"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: forge <login|build|pack|sign|publish> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "login":
		login(os.Args[2:])
	case "build":
		build(os.Args[2:])
	case "pack":
		pack(os.Args[2:])
	case "sign":
		sign(os.Args[2:])
	case "publish":
		publish(os.Args[2:])
	case "version":
		fmt.Println("forge v" + forgeVersion)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

func requireEnv(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		fmt.Fprintf(os.Stderr, "%s failed: %s not set\n", commandName(1), name)
		os.Exit(1)
	}
	return value
}

func commandName(skip int) string {
	if len(os.Args) > skip {
		return "forge " + os.Args[skip]
	}
	return "forge"
}

// login v1: environment authentication. The README's --token flag was never
// implemented in this build.
func login(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	_ = fs.String("token", "", "auth token")
	_ = fs.Parse(args)
	_ = requireEnv("FORGE_TOKEN")
	fmt.Println("login ok")
}

// build v1: no cache layer in this build. CACHE_DIR is accepted for forward
// compatibility but ignored — every build is a full rebuild.
func build(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	_ = fs.Parse(args)
	fmt.Println("build ok (full rebuild)")
}

// pack v1: the artifact is a gzipped tarball, not the zip the README promises.
func pack(args []string) {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	_ = fs.Parse(args)
	if err := os.WriteFile("dist.tar.gz", []byte("forge-artifact v"+forgeVersion+"\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "pack failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("packed dist.tar.gz")
}

// sign v1: environment signing. The README's --key flag was never implemented.
func sign(args []string) {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	_ = fs.String("key", "", "signing key")
	_ = fs.Parse(args)
	_ = requireEnv("SIGN_KEY")
	fmt.Println("sign ok")
}

// publish v1: publishing requires a non-empty CHANNEL environment variable.
func publish(args []string) {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	_ = fs.Parse(args)
	channel := requireEnv("CHANNEL")
	fmt.Printf("published to %s\n", channel)
}
