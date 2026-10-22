package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
)

// ── Live console of run_command (GET /v1/agent/console/…) ────────────────────
//
// The backend of the trace-rework special scenario (docs/specs/
// trassirovka-vkladka-zapuski/v2-consolidated.md §6): the Runs tab's
// expanded run_command row watches stdout/stderr while the command runs and
// reads the tail afterwards. The bytes live only in the in-memory registry
// (agent.Console) inside the kernel process — §11.1 defers persistent
// storage by customer decision, so nothing here writes to the journal, the
// database or files. Authorization mirrors GET /v1/agent/runs: the project
// query parameter is mandatory and the control-plane gate checks the reader
// role plus the principal's project scope (AllowQuery's exact checks for a
// non-empty project).

type consoleDeps struct {
	console *agent.Console
	gate    *controlplane.Gate
}

// registerConsoleRoutes mounts:
//   - GET /v1/agent/console/{operationID}?project=…   — JSON tail snapshot;
//   - GET /v1/agent/console/{operationID}/stream?project=…&tail=…
//     — SSE frames event: stdout|stderr|done, data: {"chunk": …}.
func registerConsoleRoutes(mux *http.ServeMux, deps consoleDeps) {
	mux.HandleFunc("GET /v1/agent/console/{operationID}", func(w http.ResponseWriter, r *http.Request) {
		project, ok := consoleProject(w, r, deps.gate)
		if !ok {
			return
		}
		state, found := deps.console.Snapshot(project, r.PathValue("operationID"))
		if !found {
			writeError(w, 404, fmt.Errorf("no console for operation %q in project %q", r.PathValue("operationID"), project))
			return
		}
		writeJSON(w, 200, state)
	})
	mux.HandleFunc("GET /v1/agent/console/{operationID}/stream", func(w http.ResponseWriter, r *http.Request) {
		project, ok := consoleProject(w, r, deps.gate)
		if !ok {
			return
		}
		operationID := r.PathValue("operationID")
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, 500, errors.New("streaming not supported"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		// R2 exemption (kernel-wedge-2026-10-21), same as /stream and
		// /tokens: the server-wide WriteTimeout would sever this stream once
		// its budget runs out; a command may legitimately outlive 120s and
		// the stream lives as long as the subscriber wants.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		// tail accepts "true"/"1" — the stream client sends ?tail=1.
		tailOnly, _ := strconv.ParseBool(r.URL.Query().Get("tail"))
		sub, found := deps.console.Subscribe(project, operationID, tailOnly)
		if !found {
			// Only a nil registry lands here; SSE headers are already out,
			// so the miss travels as a framed error event rather than a
			// status code.
			writeSSE(w, flusher, "error", map[string]string{"error": fmt.Sprintf("no console for operation %q in project %q", operationID, project)})
			return
		}
		defer sub.Cancel()
		for chunk := range sub.Events {
			writeSSE(w, flusher, chunk.Stream, map[string]string{"chunk": chunk.Chunk})
		}
		// The registry closed the channel — the command finished. The done
		// frame carries the exit code so the UI can render the final status
		// without one extra GET.
		state, _ := deps.console.Snapshot(project, operationID)
		donePayload := map[string]any{}
		if state.ExitCode != nil {
			donePayload["exit_code"] = *state.ExitCode
		}
		writeSSE(w, flusher, "done", donePayload)
	})
}

// consoleProject authorizes one console request the way GET /v1/agent/runs
// does: the project query parameter is required and the gate enforces both
// the reader role and the principal's project scope.
func consoleProject(w http.ResponseWriter, r *http.Request, gate *controlplane.Gate) (string, bool) {
	project := r.URL.Query().Get("project")
	if project == "" {
		writeError(w, 400, errors.New("project query parameter is required"))
		return "", false
	}
	if !gate.Allow(w, r, controlplane.RoleReader, project) {
		return "", false
	}
	return project, true
}

// writeSSE emits one SSE frame; the payload is JSON-encoded so chunks with
// newlines or quotes cannot break the framing. Marshal of these payload
// shapes cannot fail, and a dropped frame beats a stalled stream.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	flusher.Flush()
}
