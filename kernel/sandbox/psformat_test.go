package sandbox

import (
	"strings"
	"testing"
	"text/template"
)

// Regression: the docker CLI parses the --format template at command time —
// one missing closing brace made every docker ps fail, and listRunContainers'
// callers (the sandbox janitor, the orphan reaper, run recovery) all degraded
// to silent warn-and-return. The docker integration tests skip without a
// daemon (CI included), so nothing exercised the template until the stand's
// containers stopped being reaped. Parse it here instead: the constructs used
// ({{.Field}}, {{.Label "name"}}) are plain text/template syntax, so a parse
// here catches the malformation the integration path would.
func TestRunContainersFormatParses(t *testing.T) {
	if _, err := template.New("ps").Parse(runContainersFormat); err != nil {
		t.Fatalf("docker ps format template does not parse: %v\nformat: %q", err, runContainersFormat)
	}
	for _, label := range []string{ScratchKeyLabel, FingerprintLabel, RunLabel, ProjectLabel} {
		want := `{{.Label "` + label + `"}}`
		if !strings.Contains(runContainersFormat, want) {
			t.Fatalf("template lost the well-formed %s lookup:\nwant %q\nhave %q", label, want, runContainersFormat)
		}
	}
}
