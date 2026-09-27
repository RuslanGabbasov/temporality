package agent

import (
	"testing"
)

func TestDedupSignatureCollapsesShellWrappers(t *testing.T) {
	direct := map[string]any{"command": []any{"go", "test", "./..."}}
	wrapper := map[string]any{"command": []any{"sh", "-c", "go test ./..."}}
	d1 := dedupSignature("run_command", direct)
	d2 := dedupSignature("run_command", wrapper)
	if d1 != d2 {
		t.Fatalf("direct and wrapper should collapse:\n  %s\n  %s", d1, d2)
	}
}

func TestDedupSignatureCollapsesSemicolonVariations(t *testing.T) {
	bare := map[string]any{"command": []any{"go", "test", "./..."}}
	echo := map[string]any{"command": []any{"sh", "-c", "echo running; go test ./..."}}
	d1 := dedupSignature("run_command", bare)
	d2 := dedupSignature("run_command", echo)
	if d1 != d2 {
		t.Fatalf("bare and echo-prefixed should collapse:\n  %s\n  %s", d1, d2)
	}
}

func TestDedupSignatureKeepsDifferentCommandsSeparate(t *testing.T) {
	a := map[string]any{"command": []any{"go", "test", "./..."}}
	b := map[string]any{"command": []any{"go", "build", "./..."}}
	d1 := dedupSignature("run_command", a)
	d2 := dedupSignature("run_command", b)
	if d1 == d2 {
		t.Fatalf("different commands should not collide: %s", d1)
	}
}

func TestDedupSignatureNonRunCommandUsesExactArgs(t *testing.T) {
	args := map[string]any{"text": "hello"}
	d := dedupSignature("echo", args)
	if d == "" {
		t.Fatal("non-run_command should produce a key")
	}
}

func TestEffectiveCommandPreservesDirectArgv(t *testing.T) {
	in := []string{"go", "test", "./..."}
	out := effectiveCommand(in)
	if len(out) != 3 || out[0] != "go" || out[1] != "test" || out[2] != "./..." {
		t.Fatalf("direct argv should pass through: %v", out)
	}
}

func TestRawArgumentsHashFormat(t *testing.T) {
	h := rawArgumentsHash(`not json`)
	if h[:7] != "sha256:" {
		t.Fatalf("expected sha256: prefix, got %s", h[:7])
	}
}
