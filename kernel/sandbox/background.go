package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// Op marker file suffixes inside the ops dir. The scratch is a bind mount,
// so the markers are host-readable at the runner's scratch directory — the
// property that makes them durable across worker and kernel restarts.
const (
	opPidSuffix  = ".pid"
	opCodeSuffix = ".code"
)

func opPidFile(operationID string) string {
	return fmt.Sprintf("%s/%s%s", opsDir, sanitizeOpToken(operationID), opPidSuffix)
}
func opCodeFile(operationID string) string {
	return fmt.Sprintf("%s/%s%s", opsDir, sanitizeOpToken(operationID), opCodeSuffix)
}

// opHostFile maps a container-side op marker onto the host: /scratch is
// bind-mounted from the run scratch directory, so the same bytes live at
// runScratchDir/.ops/<name> — readable without any container.
func (d *Docker) opHostFile(scratchKey, name string) string {
	return filepath.Join(d.runScratchDir(scratchKey), ".ops", name)
}

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
	// StartedAt is the wrapper's pid-file mtime — when the operation began.
	// The attach path measures the ORIGINAL command budget from it, so a
	// worker restart never stretches the operation's timeout. Zero when the
	// markers are gone.
	StartedAt time.Time
}

// OperationState probes one background operation of the run container keyed
// by scratchKey. The exit code and the start time are host-readable first —
// a FINISHED operation reports its recorded result even when the container
// is gone (a restart or a reaped container must not turn a recorded exit
// code into "unknown"). Only the liveness check needs the container: one
// docker exec per probe — cheap enough for a polling loop.
func (d *Docker) OperationState(scratchKey, operationID string) (OperationState, error) {
	var state OperationState
	if scratchKey == "" {
		return state, fmt.Errorf("background operations need a run container (no scratch key)")
	}
	token := sanitizeOpToken(operationID)
	if info, err := os.Stat(d.opHostFile(scratchKey, token+opPidSuffix)); err == nil {
		state.StartedAt = info.ModTime()
	}
	if raw, err := os.ReadFile(d.opHostFile(scratchKey, token+opCodeSuffix)); err == nil {
		// The file existing is necessary but not sufficient: a truncated or
		// clobbered code file (an interrupted truncate-write, ENOSPC, an
		// external stomp) must never parse as exit 0 — that would turn a
		// corrupted result into a false success. An unreadable code degrades
		// to the liveness probe below: a still-alive process keeps the attach
		// waiting, a vanished one lands in the standard lost/uncertain path.
		code, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if parseErr != nil {
			return d.probeOperationAlive(&state, scratchKey, operationID)
		}
		state.Finished = true
		state.ExitCode = code
		return state, nil
	}
	return d.probeOperationAlive(&state, scratchKey, operationID)
}

// probeOperationAlive fills the state from the container liveness check —
// the fallback when the exit-code file is absent or unreadable. StartedAt
// already carried over in state survives: the pid marker still dates the
// operation even when its result did not land.
func (d *Docker) probeOperationAlive(state *OperationState, scratchKey, operationID string) (OperationState, error) {
	rc := d.lookupRunForHelper(scratchKey)
	if rc == nil {
		return *state, fmt.Errorf("run container not found for %s", scratchKey)
	}
	defer d.releaseRunContainer(rc)
	alive, err := d.opProbeAliveIn(rc.name, operationID)
	if err != nil {
		return *state, err
	}
	if alive {
		state.Running = true
		state.Alive = true
	}
	// pid gone and no (readable) exit code: killed unnaturally, crashed or lost.
	return *state, nil
}

// KillOperation terminates the process tree of one background operation
// inside the run's container: TERM the tree, one beat, KILL the survivors.
// The wrapper pid is verified against the __TEMPORALITY_OP environment
// marker first — a stale pid file must never kill a reused pid. The kill
// records exit 137 when the dying wrapper cannot write its own code file —
// a deliberate kill then reads as a finished (137) operation everywhere,
// never as a lost one.
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
[ -f %[2]s ] || echo 137 > %[2]s
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
// execution: helpers are best-effort diagnostics, not run traffic. A
// registry miss falls back to the container's durable labels — after a
// kernel restart the mapping is recovered before declaring the operation
// unreachable, and an unverifiable container is never attached to.
func (d *Docker) lookupRunForHelper(key string) *runContainer {
	if rc := d.claimRunEntry(key); rc != nil {
		return rc
	}
	if d.recoverRun(key) == nil {
		return nil
	}
	return d.claimRunEntry(key)
}

// unfinishedOps lists the operation tokens whose wrapper started but never
// recorded an exit code — the durable candidates for "still running"
// (docs/agent-async-operations.md). Host-side: the scratch is a bind mount.
func (d *Docker) unfinishedOps(key string) []string {
	entries, err := os.ReadDir(filepath.Join(d.runScratchDir(key), ".ops"))
	if err != nil {
		return nil
	}
	finished := make(map[string]bool, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, opCodeSuffix) {
			finished[strings.TrimSuffix(name, opCodeSuffix)] = true
		}
	}
	var ops []string
	for _, entry := range entries {
		name := entry.Name()
		token := strings.TrimSuffix(name, opPidSuffix)
		if strings.HasSuffix(name, opPidSuffix) && !finished[token] {
			ops = append(ops, token)
		}
	}
	return ops
}

// scratchHasUnfinishedOps reports whether the run's scratch still holds a
// background operation that never recorded its exit code — the durable
// "something may still be running" signal the teardown respects.
func (d *Docker) scratchHasUnfinishedOps(key string) bool {
	return len(d.unfinishedOps(key)) > 0
}

// opProbeAliveIn asks one container whether a background operation's wrapper
// process is still alive (its recorded pid answers a signal). The liveness
// truth only exists inside the container's pid namespace: the host markers
// say what was started, this says what survived.
func (d *Docker) opProbeAliveIn(container, operationID string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	script := fmt.Sprintf(
		`if [ -f %[1]s ] && kill -0 "$(cat %[1]s)" 2>/dev/null; then echo running; else echo gone; fi`,
		opPidFile(operationID),
	)
	request := Request{Command: []string{"/bin/sh", "-c", script}, TimeoutSeconds: 10}
	result, err := d.execCommand(ctx, &runContainer{name: container}, request)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(result.Output) == "running", nil
}

// liveOps reports whether any operation that never recorded an exit code is
// still running inside the container — or cannot be told apart from one.
// A crashed operation with stale markers does not hold its container
// hostage; an answer the probe cannot trust keeps it.
func (d *Docker) liveOps(ref runContainerRef) bool {
	for _, token := range d.unfinishedOps(ref.key) {
		alive, err := d.opProbeAliveIn(ref.name, token)
		if err != nil || alive {
			return true
		}
	}
	return false
}
