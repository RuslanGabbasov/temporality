package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/workspace"
)

func TestUserMetricsHandlerDoesNotHang(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a local postgres")
	}
	ctx := context.Background()
	store, err := workspace.Open(ctx, "postgres://temporality:x@localhost:55432/temporality?sslmode=disable")
	if err != nil {
		t.Skipf("local repro db unavailable: %v", err)
	}
	gate, err := controlplane.NewGate("")
	if err != nil {
		t.Skipf("gate: %v", err)
	}
	mux := http.NewServeMux()
	registerUserMetricsRoutes(mux, gate, store, cost.DefaultHumanFactors)
	server := httptest.NewServer(gate.Authenticate(mux))
	defer server.Close()
	done := make(chan int, 1)
	go func() {
		resp, err := server.Client().Get(server.URL + "/v1/workspace/users/metrics")
		if err != nil {
			t.Logf("request error: %v", err)
			done <- -1
			return
		}
		defer resp.Body.Close()
		done <- resp.StatusCode
	}()
	select {
	case code := <-done:
		if code != 200 {
			t.Fatalf("status %d", code)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("handler hangs > 8s")
	}
}
