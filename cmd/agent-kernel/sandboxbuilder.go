package main

// Sandbox profile wizard (docs/sandbox-profiles.md): plain-language task
// description → sandbox profile draft, same UX pattern as the team/agent
// builders. The draft lands in the existing Sandbox form; the operator edits
// and saves, the ordinary build endpoint assembles the image. The model is
// the supervisor-scope wizard client (env gateway or the workspace registry).

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
)

// RegisterSandboxBuilderRoutes mounts POST /v1/workspace/sandbox-profiles/builder.
func RegisterSandboxBuilderRoutes(mux *http.ServeMux, gate *controlplane.Gate, modelClient func(w http.ResponseWriter, r *http.Request) *llm.Client) {
	mux.HandleFunc("POST /v1/workspace/sandbox-profiles/builder", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var req struct {
			Description string `json:"description"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.Description) == "" {
			writeError(w, 422, errors.New("description is required"))
			return
		}
		// Known bases: the closed set the platform can actually assemble —
		// the builtin profiles plus the embedded minimal base.
		bases := []agent.KnownSandboxBase{
			{ID: "node", Name: "Node 22", Descript: "Node.js 22 + git/curl/jq/bash/ripgrep — frontend and JS tasks"},
			{ID: "python", Name: "Python 3.12", Descript: "Python 3.12 + git/curl/jq/bash/ripgrep — scripts, data, tooling"},
			{ID: "browser-e2e", Name: "Playwright (browsers)", Descript: "Playwright + browsers on debian — web e2e test suites"},
			{ID: "base", Name: "Alpine (minimal)", Descript: "Minimal alpine + the shared CLI layer — shell-only tasks"},
		}
		client := modelClient(w, r)
		if client == nil {
			return // the wizard client already wrote the 502
		}
		draft, err := agent.BuildSandboxDraft(r.Context(), client, req.Description, bases)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		writeJSON(w, 200, draft)
	})
}
