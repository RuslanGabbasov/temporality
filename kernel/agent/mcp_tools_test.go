package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/mcpclient"
)

// PrepareRun must never ship a silently mutilated tool schema — and must
// never let one dead MCP server block the healthy ones: a requested server
// with no discovered tools is healed on demand (MCPReload), and whatever
// stays missing is named to the model while the rest of the schema stands.

func testMCPServerConfig(t *testing.T, id string) mcpclient.ServerConfig {
	t.Helper()
	root := t.TempDir()
	t.Setenv("KERNEL_MCP_TEST_ROOT", root)
	args, _ := json.Marshal([]string{"run", "../../examples/test-mcp"})
	var argv []string
	_ = json.Unmarshal(args, &argv)
	return mcpclient.ServerConfig{
		ID: id, Name: id, Type: mcpclient.TypeStdio,
		Command: "go", Args: argv,
		Allow:   []string{"read_file", "search", "create_issue"},
		Enabled: true,
	}
}

func TestPrepareRunDegradesPerServerNotAllOrNothing(t *testing.T) {
	// One server is healthy, the other has no tools (down or undiscovered):
	// the run keeps the healthy tools and names the gap — the dead server
	// costs its own tools, never its neighbours'.
	registry := mcpclient.NewRegistry()
	healthy := testMCPServerConfig(t, "graphmap")
	if err := registry.Apply(context.Background(), healthy); err != nil {
		t.Fatalf("register healthy server: %v", err)
	}
	activities := &Activities{MCP: registry, Sandbox: &streamingSandbox{root: t.TempDir()}}
	input := &RunInput{RunID: "run-mcp", Project: "proj", MCPServers: []string{"graphmap", "codanna"}}
	if err := activities.PrepareRun(input); err != nil {
		t.Fatalf("one dead server must not fail the run start: %v", err)
	}
	names := map[string]bool{}
	for _, def := range input.Tools {
		names[def.Name] = true
	}
	if !names["mcp__graphmap__read_file"] {
		t.Fatalf("the healthy server's tools must survive, got %d tools", len(names))
	}
	if names["mcp__codanna__read_file"] {
		t.Fatal("the dead server's tools cannot be advertised")
	}
	if len(input.MCPUnavailable) != 1 || input.MCPUnavailable[0] != "codanna" {
		t.Fatalf("the gap must be named for the model, got %v", input.MCPUnavailable)
	}
}

func TestPrepareRunHealsThroughMCPReload(t *testing.T) {
	registry := mcpclient.NewRegistry()
	cfg := testMCPServerConfig(t, "codanna")
	reloads := 0
	activities := &Activities{
		MCP:     registry,
		Sandbox: &streamingSandbox{root: t.TempDir()},
		MCPReload: func() {
			reloads++
			if err := registry.Apply(context.Background(), cfg); err != nil {
				t.Errorf("reload apply: %v", err)
			}
		},
	}
	input := &RunInput{RunID: "run-mcp", Project: "proj", MCPServers: []string{"codanna"}}
	if err := activities.PrepareRun(input); err != nil {
		t.Fatalf("PrepareRun must heal through MCPReload: %v", err)
	}
	if reloads != 1 {
		t.Fatalf("reload calls = %d, want exactly one heal attempt", reloads)
	}
	if len(input.MCPUnavailable) != 0 {
		t.Fatalf("a healed server leaves no gap, got %v", input.MCPUnavailable)
	}
	names := map[string]bool{}
	for _, def := range input.Tools {
		names[def.Name] = true
	}
	if !names["mcp__codanna__read_file"] {
		t.Fatalf("the healed tools must reach the run schema, got %d tools", len(names))
	}
}

func TestMCPUnavailableNoticeNamesTheGap(t *testing.T) {
	notice := mcpUnavailableNotice([]string{"codanna", "penpot"})
	if !strings.Contains(notice, "codanna") || !strings.Contains(notice, "penpot") {
		t.Fatalf("the notice must name every missing server: %q", notice)
	}
	if !strings.Contains(notice, "Do not attempt to call them") {
		t.Fatalf("the notice must steer the model away from phantom calls: %q", notice)
	}
}
