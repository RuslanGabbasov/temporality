package sandbox

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Background operations (docs/agent-async-operations.md): a run_command with
// background=true detaches into the run's keep-alive container and reports
// its outcome later. The bookkeeping lives in the run-scoped scratch so it
// survives kernel restarts:
//
//	/scratch/.ops/<operation>.pid   — the wrapper shell's pid, written at start
//	/scratch/.ops/<operation>.code  — the command's exit code, written at end
//
// The wrapper also runs with __TEMPORALITY_OP=<operation> in its environment;
// the kill path verifies the marker before signalling, so a reused pid can
// never take an innocent process down.

const opsDir = "/scratch/.ops"

func opPidFile(operationID string) string  { return fmt.Sprintf("%s/%s.pid", opsDir, sanitizeOpToken(operationID)) }
func opCodeFile(operationID string) string { return fmt.Sprintf("%s/%s.code", opsDir, sanitizeOpToken(operationID)) }

// OpMarkerEnv is the environment entry the wrapper runs with; the kill path
// matches it against the live process before killing anything.
func OpMarkerEnv(operationID string) string {
	return "__TEMPORALITY_OP=" + sanitizeOpToken(operationID)
}

// sanitizeOpToken keeps the operation id safe as a path component; operation
// ids are kernel-generated and already tame, but the command line is written
// by model-adjacent data — belt over suspenders.
func sanitizeOpToken(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		b.WriteString("op")
	}
	return b.String()
}

// OpShellWrapper wraps the command argv so a background command records its
// pid and exit code into the run scratch without changing how output streams
// (stdout/stderr still flow through the docker exec pipe to the console).
// The original argv rides as the wrapper's parameters and is invoked via
// "$@" — no re-parsing, no quoting hazards.
func OpShellWrapper(operationID string, command []string) []string {
	wrapper := fmt.Sprintf(
		"mkdir -p %[1]s; echo $$ > %[2]s; \"$@\"; code=$?; echo $code > %[3]s; exit $code",
		opsDir, opPidFile(operationID), opCodeFile(operationID),
	)
	argv := make([]string, 0, len(command)+3)
	argv = append(argv, "/bin/sh", "-c", wrapper, "sh")
	argv = append(argv, command...)
	return argv
}

// OperationState reports what a background operation is doing right now.
type OperationState struct {
	Running  bool
	Finished bool // the exit-code file exists
	ExitCode int
	Alive    bool // the pid file exists and the process answers a signal
}

// OperationState probes one background operation of the run container keyed
// by scratchKey. One docker exec per probe — cheap enough for a 2-second
// polling loop.
func (d *Docker) OperationState(scratchKey, operationID string) (OperationState, error) {
	var state OperationState
	if scratchKey == "" {
		return state, fmt.Errorf("background operations need a run container (no scratch key)")
	}
	rc := d.lookupRunForHelper(scratchKey)
	if rc == nil {
		return state, fmt.Errorf("run container not found for %s", scratchKey)
	}
	defer d.releaseRunContainer(rc)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	script := fmt.Sprintf(
		`if [ -f %[1]s ]; then echo "done $(cat %[1]s)"; elif [ -f %[2]s ] && kill -0 "$(cat %[2]s)" 2>/dev/null; then echo running; else echo gone; fi`,
		opCodeFile(operationID), opPidFile(operationID),
	)
	request := Request{Command: []string{"/bin/sh", "-c", script}, TimeoutSeconds: 10}
	result, err := d.execCommand(ctx, rc, request)
	if err != nil {
		return state, err
	}
	out := strings.TrimSpace(result.Output)
	switch {
	case strings.HasPrefix(out, "done"):
		state.Finished = true
		state.Running = false
		fmt.Sscanf(out, "done %d", &state.ExitCode)
	case out == "running":
		state.Running = true
		state.Alive = true
	default:
		// pid gone and no exit code: killed (or the wrapper died unnaturally).
		state.Finished = false
		state.Running = false
	}
	return state, nil
}

// KillOperation terminates the process tree of one background operation
// inside the run's container: TERM the tree, one beat, KILL the survivors.
// The wrapper pid is verified against the __TEMPORALITY_OP environment
// marker first — a stale pid file must never kill a reused pid.
func (d *Docker) KillOperation(scratchKey, operationID string) (bool, error) {
	if scratchKey == "" {
		return false, fmt.Errorf("background operations need a run container (no scratch key)")
	}
	rc := d.lookupRunForHelper(scratchKey)
	if rc == nil {
		return false, fmt.Errorf("run container not found for %s", scratchKey)
	}
	defer d.releaseRunContainer(rc)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	marker := OpMarkerEnv(operationID)
	script := fmt.Sprintf(`
pid=$(cat %[1]s 2>/dev/null)
[ -z "$pid" ] && echo absent && exit 0
tr '\0' '\n' < /proc/$pid/environ 2>/dev/null | grep -Fx %[3]q >/dev/null || { echo foreign; exit 0; }
list=$pid
changed=1
while [ "$changed" = 1 ]; do
  changed=0
  for f in /proc/[0-9]*/status; do
    p=${f%%/status}; p=${p##*/}
    case " $list " in *" $p "*) continue ;; esac
    ppid=$(awk '/^PPid:/{print $2}' "$f" 2>/dev/null)
    case " $list " in *" $ppid "*) list="$list $p"; changed=1 ;; esac
  done
done
for p in $list; do kill -TERM $p 2>/dev/null; done
sleep 1
for p in $list; do kill -KILL $p 2>/dev/null; done
rm -f %[1]s
echo killed
`, opPidFile(operationID), opCodeFile(operationID), marker)
	request := Request{Command: []string{"/bin/sh", "-c", script}, TimeoutSeconds: 25}
	result, err := d.execCommand(ctx, rc, request)
	if err != nil {
		return false, err
	}
	return strings.Contains(result.Output, "killed"), nil
}

// lookupRunForHelper borrows the run container for a helper exec (kill /
// state probe) without disturbing the fingerprint discipline of command
// execution: helpers are best-effort diagnostics, not run traffic.
func (d *Docker) lookupRunForHelper(key string) *runContainer {
	d.runMu.Lock()
	defer d.runMu.Unlock()
	rc := d.runs[key]
	if rc == nil {
		return nil
	}
	rc.busy++
	rc.lastUsed = time.Now()
	return rc
}
