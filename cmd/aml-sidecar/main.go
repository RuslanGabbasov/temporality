// Command aml-sidecar runs the adaptive memory layer as an HTTP sidecar over
// the experiment-3 integration contract. The agent harness keeps its own loop
// and talks to this process only through /task/start, /tool/before,
// /tool/after and /task/end.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/temporality-project/temporality/aml/coding"
	"github.com/temporality-project/temporality/aml/memory"
	"github.com/temporality-project/temporality/aml/sidecar"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18190", "listen address")
	dsn := flag.String("dsn", os.Getenv("AML_SIDECAR_DSN"), "postgres DSN")
	world := flag.String("world", "coding", "synthetic|coding")
	flag.Parse()

	if *dsn == "" {
		fatal("dsn required (flag or AML_SIDECAR_DSN)")
	}
	var w sidecar.World
	switch *world {
	case "coding":
		w = sidecar.WorldCoding
	case "synthetic":
		w = sidecar.WorldSynthetic
	default:
		fatal("world must be coding or synthetic")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := memory.OpenPg(ctx, *dsn)
	if err != nil {
		fatal("open db: " + err.Error())
	}
	if err := memory.ApplyMigrations(ctx, store.Pool()); err != nil {
		fatal("migrate: " + err.Error())
	}

	server := sidecar.NewServer(store, w, coding.Subsystems, slog.Default())
	httpServer := &http.Server{Addr: *addr, Handler: server.Handler()}
	fmt.Printf("aml-sidecar listening on %s (world=%s)\n", *addr, *world)
	if err := httpServer.ListenAndServe(); err != nil {
		fatal(err.Error())
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "aml-sidecar: "+msg)
	os.Exit(1)
}
