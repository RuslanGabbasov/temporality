// Package sidecar exposes the adaptive memory layer over the experiment-3
// integration contract (POST /task/start, /tool/before, /tool/after, /task/end).
// The agent harness keeps ownership of its loop; the sidecar only observes and
// returns small memory hints.
package sidecar

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/temporality-project/temporality/aml/coding"
	"github.com/temporality-project/temporality/aml/harness"
	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/aml/memory"
)

// World selects the parser/extractor pair the sidecar installs per layer.
type World string

const (
	WorldSynthetic World = "synthetic"
	WorldCoding    World = "coding"
)

// taskState is one active task's layer.
type taskState struct {
	layer *memory.Layer
}

// Server is the HTTP memory sidecar.
type Server struct {
	store    memory.Store
	cfg      memory.Config
	world    World
	services []string

	mu       sync.Mutex
	tasks    map[string]*taskState
	sessions map[string]int // sessionID → 1-based index, in arrival order
}

// NewServer builds the server. World-specific parsing/extraction is installed
// per layer so the same process can serve any fixture.
func NewServer(store memory.Store, world World, services []string, logger *slog.Logger) *Server {
	return &Server{
		store:    store,
		cfg:      memory.Config{Arm: "D", InjectTaskStart: true, TemporalRanking: true, PreAction: true, Guardrail: true, Feedback: true},
		world:    world,
		services: services,
		tasks:    map[string]*taskState{},
		sessions: map[string]int{},
	}
}

// TaskStartRequest mirrors the §4 contract.
type TaskStartRequest struct {
	TaskID      string `json:"task_id"`
	AgentID     string `json:"agent_id"`
	SessionID   string `json:"session_id"`
	Task        string `json:"task"`
	Repository  string `json:"repository"`
	Branch      string `json:"branch"`
	Environment string `json:"environment"`
}

// MemoryHint is one returned memory.
type MemoryHint struct {
	ID         string  `json:"id"`
	Hint       string  `json:"hint"`
	Confidence float64 `json:"confidence"`
}

// MemoriesResponse carries hints (empty is a normal result).
type MemoriesResponse struct {
	Memories  []MemoryHint `json:"memories"`
	Guardrail *string      `json:"guardrail"`
}

// ToolBeforeRequest is the pre-action probe.
type ToolBeforeRequest struct {
	TaskID    string         `json:"task_id"`
	SessionID string         `json:"session_id"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

// ToolAfterRequest is the post-observation record.
type ToolAfterRequest struct {
	TaskID    string         `json:"task_id"`
	SessionID string         `json:"session_id"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
	Result    struct {
		OK     bool   `json:"ok"`
		Status int    `json:"status"`
		Text   string `json:"text"`
		Cause  string `json:"cause"`
	} `json:"result"`
}

// TaskEndRequest closes a task.
type TaskEndRequest struct {
	TaskID    string `json:"task_id"`
	SessionID string `json:"session_id"`
	Success   bool   `json:"success"`
	Answer    string `json:"answer"`
}

// Routes wires the HTTP handlers.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /task/start", s.handleTaskStart)
	mux.HandleFunc("POST /tool/before", s.handleToolBefore)
	mux.HandleFunc("POST /tool/after", s.handleToolAfter)
	mux.HandleFunc("POST /task/end", s.handleTaskEnd)
	mux.HandleFunc("GET /stats", s.handleStats)
}

// Handler builds a ready-to-use handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.Routes(mux)
	return mux
}

func (s *Server) handleTaskStart(w http.ResponseWriter, r *http.Request) {
	var req TaskStartRequest
	if !decode(w, r, &req) {
		return
	}
	if req.TaskID == "" || req.SessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "task_id and session_id are required"})
		return
	}
	ctx := r.Context()

	s.mu.Lock()
	layer := memory.NewLayer(s.store, s.cfg, s.services, nil)
	switch s.world {
	case WorldCoding:
		layer.SetParser(codingParser())
		layer.SetExtractor(codingExtractor())
	}
	idx := s.sessionIndex(req.SessionID)
	state := &taskState{layer: layer}
	s.tasks[req.TaskID] = state
	s.mu.Unlock()

	layer.SetSession(req.SessionID, idx)
	env := req.Environment
	if env == "" {
		env = req.Branch
	}
	if env == "" {
		env = "default"
	}
	layer.SetTaskContext(env, "")

	hints := layer.TaskStart(ctx, req.SessionID, req.TaskID, req.Task)
	out := MemoriesResponse{}
	for _, h := range hints {
		out.Memories = append(out.Memories, MemoryHint{ID: "hint", Hint: h.Text})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleToolBefore(w http.ResponseWriter, r *http.Request) {
	var req ToolBeforeRequest
	if !decode(w, r, &req) {
		return
	}
	state := s.task(req.TaskID)
	if state == nil {
		writeJSON(w, http.StatusOK, MemoriesResponse{})
		return
	}
	call := llm.ToolCall{ID: "", Name: req.Tool, Args: req.Arguments}
	hints := state.layer.BeforeToolCall(r.Context(), req.SessionID, call)
	out := MemoriesResponse{}
	for _, h := range hints {
		out.Memories = append(out.Memories, MemoryHint{Hint: h.Text})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleToolAfter(w http.ResponseWriter, r *http.Request) {
	var req ToolAfterRequest
	if !decode(w, r, &req) {
		return
	}
	state := s.task(req.TaskID)
	if state == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	call := llm.ToolCall{ID: "", Name: req.Tool, Args: req.Arguments}
	result := harness.ToolResult{OK: req.Result.OK, Status: req.Result.Status, Text: req.Result.Text, Cause: req.Result.Cause}
	state.layer.AfterToolCall(r.Context(), req.SessionID, call, result)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTaskEnd(w http.ResponseWriter, r *http.Request) {
	var req TaskEndRequest
	if !decode(w, r, &req) {
		return
	}
	state := s.task(req.TaskID)
	if state == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	state.layer.TaskEnd(r.Context(), req.SessionID, req.TaskID, req.Success, req.Answer)
	stats := state.layer.Stats()
	s.mu.Lock()
	delete(s.tasks, req.TaskID)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stats": stats})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	state := s.task(taskID)
	if state == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown task"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": state.layer.Stats()})
}

func (s *Server) task(taskID string) *taskState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasks[taskID]
}

func (s *Server) sessionIndex(sessionID string) int {
	if idx, ok := s.sessions[sessionID]; ok {
		return idx
	}
	idx := len(s.sessions) + 1
	s.sessions[sessionID] = idx
	return idx
}

func codingParser() memory.CallParser {
	return coding.Parser(coding.Subsystems)
}

func codingExtractor() memory.ExtractFunc {
	return coding.Extractor(coding.RepoName, coding.Subsystems)
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("decode: %v", err)})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// StatsResponse is what /stats returns after task end (kept for the driver).
type StatsResponse struct {
	Stats memory.Stats `json:"stats"`
}
