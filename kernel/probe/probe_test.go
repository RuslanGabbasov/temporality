package probe

import (
	"context"
	"testing"
)

func TestFileProbeDetectsRedirect(t *testing.T) {
	p := &FileProbe{}
	result, err := p.Check(context.Background(), Operation{
		Tool: "run_command",
		Arguments: map[string]any{
			"command": []any{"echo", "ok", ">", "output.txt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Observable {
		t.Fatal("file probe should detect redirect")
	}
	if result.Detail == "" {
		t.Fatal("file probe should provide detail")
	}
	if len(result.Evidence) != 1 {
		t.Fatalf("expected 1 evidence, got %d", len(result.Evidence))
	}
	if result.Evidence[0].Ref != "workspace:output.txt" {
		t.Fatalf("evidence ref = %q", result.Evidence[0].Ref)
	}
}

func TestFileProbeIgnoresNonRunCommand(t *testing.T) {
	p := &FileProbe{}
	result, err := p.Check(context.Background(), Operation{
		Tool:      "mcp__read_file",
		Arguments: map[string]any{"path": "test.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Observable {
		t.Fatal("file probe should ignore non-run_command tools")
	}
}

func TestFileProbeIgnoresNoRedirect(t *testing.T) {
	p := &FileProbe{}
	result, err := p.Check(context.Background(), Operation{
		Tool: "run_command",
		Arguments: map[string]any{
			"command": []any{"go", "test", "./..."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Observable {
		t.Fatal("file probe should ignore commands without redirect")
	}
}

func TestMCPProbeIgnoresNonMCP(t *testing.T) {
	p := &MCPProbe{}
	result, err := p.Check(context.Background(), Operation{
		Tool: "run_command",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Observable {
		t.Fatal("MCP probe should ignore non-MCP tools")
	}
}

func TestProbeEventDerived(t *testing.T) {
	event := ProbeEvent("lighthouse", "run-1", "op-1", Result{
		ProbeType:  "file",
		Observable: true,
		Detail:     "test",
	})
	if event.Type != "operation.probed" {
		t.Fatalf("event type = %q, want operation.probed", event.Type)
	}
	if event.Data["operation_id"] != "op-1" {
		t.Fatalf("operation_id = %v", event.Data["operation_id"])
	}
	if event.Data["observable"] != true {
		t.Fatalf("observable = %v", event.Data["observable"])
	}
	if event.Context.Actor.ID != "system" {
		t.Fatalf("actor = %q", event.Context.Actor.ID)
	}
	if event.Context.Actor.Type != "probe" {
		t.Fatalf("actor type = %q", event.Context.Actor.Type)
	}
	if event.Context.ParentEventID != "" {
		t.Fatalf("parent_event_id should be empty for probe events")
	}
}
