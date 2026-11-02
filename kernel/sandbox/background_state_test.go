package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// A corrupt or empty exit-code file must degrade to an UNDEFINED state, never
// to a finished exit-0: the presence of the .code file alone proves nothing —
// an interrupted truncate-write, ENOSPC or an external stomp can leave it
// unreadable, and parsing it leniently turned corrupted results into false
// successes (docs/agent-async-operations.md).

func writeCodeMarker(t *testing.T, key, operationID, codeContent string) *Docker {
	t.Helper()
	d := &Docker{Root: t.TempDir()}
	ops := filepath.Join(d.runScratchDir(key), ".ops")
	if err := os.MkdirAll(ops, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ops, sanitizeOpToken(operationID)+opPidSuffix), []byte("42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ops, sanitizeOpToken(operationID)+opCodeSuffix), []byte(codeContent), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestOperationStateCorruptCodeFileIsNotAFinishedSuccess(t *testing.T) {
	for name, content := range map[string]string{
		"empty":         "",
		"spaces":        "   \n",
		"garbage":       "x\n",
		"double":        "13\n13\n",
		"trailing_junk": "13 exit\n",
	} {
		d := writeCodeMarker(t, "proj/run-1", "op-1", content)
		state, err := d.OperationState("proj/run-1", "op-1")
		// No container is configured in this test: the liveness fallback
		// reports the missing runner as an error — any such outcome is fine,
		// as long as the state is NOT a finished success.
		if err == nil && state.Finished {
			t.Fatalf("%s: corrupt code file must not read as finished success: %+v", name, state)
		}
		if err == nil && state.ExitCode != 0 {
			t.Fatalf("%s: corrupt code file must not produce an exit code: %+v", name, state)
		}
		if state.StartedAt.IsZero() {
			t.Fatalf("%s: the pid marker must still date the operation: %+v", name, state)
		}
	}
}

func TestOperationStateEmptyCodeFileWithoutContainerIsNotSuccess(t *testing.T) {
	d := &Docker{Root: t.TempDir()}
	key := "proj/run-2"
	ops := filepath.Join(d.runScratchDir(key), ".ops")
	if err := os.MkdirAll(ops, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ops, "op-2"+opPidSuffix), []byte("42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The truncate side of "echo $code > file" without the write yet.
	if err := os.WriteFile(filepath.Join(ops, "op-2"+opCodeSuffix), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := d.OperationState(key, "op-2")
	if err == nil && state.Finished && state.ExitCode == 0 {
		t.Fatalf("an empty code file must not read as a clean exit-0 success: %+v", state)
	}
	if state.StartedAt.IsZero() {
		t.Fatalf("the pid marker must still date the operation: %+v", state)
	}
}

func TestOperationStateValidCodesStillParse(t *testing.T) {
	for name, content := range map[string]string{
		"zero":       "0\n",
		"nonzero":    "137\n",
		"no_newline": "1",
	} {
		d := &Docker{Root: t.TempDir()}
		key := "proj/run-3"
		ops := filepath.Join(d.runScratchDir(key), ".ops")
		if err := os.MkdirAll(ops, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ops, "op-3"+opPidSuffix), []byte("42\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ops, "op-3"+opCodeSuffix), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		state, err := d.OperationState(key, "op-3")
		if err != nil {
			t.Fatalf("%s: a recorded code needs no container: %v", name, err)
		}
		if !state.Finished {
			t.Fatalf("%s: a well-formed code file must read as finished: %+v", name, state)
		}
		if state.ExitCode == 0 && name != "zero" {
			t.Fatalf("%s: parsed the wrong exit code: %+v", name, state)
		}
	}
}
