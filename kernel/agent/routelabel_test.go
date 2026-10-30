package agent

import "testing"

func TestRouteLabelPrefersName(t *testing.T) {
	if got := routeLabel("prov-465a", "GPUStack prod"); got != "GPUStack prod" {
		t.Fatalf("routeLabel = %q, want the human name", got)
	}
	if got := routeLabel("prov-465a", ""); got != "prov-465a" {
		t.Fatalf("routeLabel without a name = %q, want the id fallback", got)
	}
}
