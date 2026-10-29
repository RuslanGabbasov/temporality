package agent

import (
	"testing"
)

// The trace-analysis surface for agents: run_search and trace_read are the
// org-scoped window into recent runs — a regression that drops them from the
// advertised catalog silently blinds every agent that reads the room.
func TestKernelToolsExposeTraceAnalysis(t *testing.T) {
	want := map[string]bool{"run_search": false, "trace_read": false}
	for _, def := range KernelTools() {
		if _, ok := want[def.Name]; ok {
			want[def.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("KernelTools must advertise %q", name)
		}
	}
}
