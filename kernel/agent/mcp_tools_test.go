package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/mcpclient"
)

// PrepareRun must never ship a silently mutilated tool schema: a requested
// MCP server with no discovered tools is healed on demand (MCPReload) and,
// failing that, fails the run start naming the server — the silent gap that
// once stranded a whole session without its mcp__ tools.

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

func TestPrepareRunFailsWhenRequestedMCPServerHasNoTools(t *testing.T) {
	activities := &Activities{MCP: mcpclient.NewRegistry(), Sandbox: &streamingSandbox{root: t.TempDir()}}
	input := &RunInput{RunID: "run-mcp", Project: "proj", MCPServers: []string{"codanna"}}
	err := activities.PrepareRun(input)
	if err == nil || !strings.Contains(err.Error(), "codanna") {
		t.Fatalf("a requested server with no tools must fail the run start naming it, got %v", err)
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
	names := map[string]bool{}
	for _, def := range input.Tools {
		names[def.Name] = true
	}
	if !names["mcp__codanna__read_file"] {
		t.Fatalf("the healed tools must reach the run schema, got %d tools", len(names))
	}
}
