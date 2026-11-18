package main

import (
	"context"
	"log/slog"
	"time"
)

// Background-loop guard (the 2026-11-18 fold stall): org loops run passes
// synchronously on a context without a deadline, over http.DefaultClient
// without a timeout. One hung journal read parked the fold goroutine
// forever — no error, no warning, the projection just went silent while the
// journal kept ingesting. Every background pass now runs with its own
// deadline and a panic guard: a hung call costs the loop one tick, the next
// tick starts on a fresh connection.
const bgPassTimeout = 2 * time.Minute

// runPassGuarded runs one background pass under a per-pass deadline and a
// panic guard. The inner goroutine is abandoned on timeout — its request is
// cancelled through the pass context, so it does not leak; a genuine panic
// is logged instead of taking the kernel down.
func runPassGuarded(log *slog.Logger, name string, ctx context.Context, fn func(context.Context)) {
	pctx, cancel := context.WithTimeout(ctx, bgPassTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				log.Error("background loop panic", "loop", name, "panic", r)
			}
		}()
		fn(pctx)
	}()
	select {
	case <-done:
	case <-pctx.Done():
		log.Error("background loop pass timed out — starting fresh on the next tick", "loop", name, "timeout", bgPassTimeout.String())
	}
}
