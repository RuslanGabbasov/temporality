package sandbox

import (
	"os"
	"strings"
	"testing"
)

// The embedded Dockerfile must stay byte-identical to sandbox/Dockerfile:
// sandbox/build.sh builds from the file, the onboarding build endpoint builds
// from the constant. When they drift, one of the two paths ships a different
// sandbox than the other with no visible failure.
func TestEmbeddedDockerfileMatchesFile(t *testing.T) {
	if os.Getenv("KERNEL_SANDBOX_SKIP_FILE_SYNC") != "" {
		t.Skip("file sync check disabled")
	}
	raw, err := os.ReadFile("../../sandbox/Dockerfile")
	if err != nil {
		t.Skipf("sandbox/Dockerfile not available: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != strings.TrimSpace(EmbeddedDockerfile) {
		t.Fatalf("sandbox/Dockerfile and kernel/sandbox EmbeddedDockerfile drifted:\n--- file ---\n%s\n--- embedded ---\n%s", got, EmbeddedDockerfile)
	}
}
