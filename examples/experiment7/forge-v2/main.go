// Command forge is the release-pipeline CLI fixture for Experiment 7
// (reinforcement cycles, parallel competing hypotheses, scale). Five
// independent mechanisms, all partially mis-documented in the README, so every
// behavior must be verified empirically.
//
// v2 behavior (the upgrade):
//
//	forge login    unchanged: reads the FORGE_TOKEN environment variable
//	forge build    honors CACHE_DIR when set (cache layer added in v2);
//	                 without CACHE_DIR it is still a full rebuild
//	forge pack     writes dist.zip (the v1 tarball is gone)
//	forge sign     reads the SIGNING_KEY environment variable (SIGN_KEY was
//	                 renamed in v2; --key is still not implemented)
//	forge publish  unchanged: requires a non-empty CHANNEL environment variable
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const forgeVersion = "2"

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

// login v2: unchanged from v1 — environment authentication. The README's
// --token flag is still not implemented.
func login(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	_ = fs.String("token", "", "auth token")
	_ = fs.Parse(args)
	_ = requireEnv("FORGE_TOKEN")
	fmt.Println("login ok")
}

// build v2: the cache layer landed. A non-empty CACHE_DIR enables caching
// (a marker artifact is written there and reused); without it every build is
// still a full rebuild.
func build(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	_ = fs.Parse(args)
	if dir := strings.TrimSpace(os.Getenv("CACHE_DIR")); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "build failed: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(filepath.Join(dir, "forge-cache.txt"), []byte("cache v"+forgeVersion+"\n"), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "build failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("build ok (cached via CACHE_DIR=%s)\n", dir)
		return
	}
	fmt.Println("build ok (full rebuild)")
}

// pack v2: the artifact format moved to zip, matching what the README always
// claimed.
func pack(args []string) {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	_ = fs.Parse(args)
	if err := os.WriteFile("dist.zip", []byte("forge-artifact v"+forgeVersion+"\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "pack failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("packed dist.zip")
}

// sign v2: the signing secret was renamed to SIGNING_KEY. SIGN_KEY is no
// longer honored; the --key flag is still not implemented.
func sign(args []string) {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	_ = fs.String("key", "", "signing key")
	_ = fs.Parse(args)
	_ = requireEnv("SIGNING_KEY")
	fmt.Println("sign ok")
}

// publish v2: unchanged from v1 — publishing requires a non-empty CHANNEL
// environment variable.
func publish(args []string) {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	_ = fs.Parse(args)
	channel := requireEnv("CHANNEL")
	fmt.Printf("published to %s\n", channel)
}
