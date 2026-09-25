package mcpclient

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/temporality-project/temporality/kernel/llm"
)

func TestToolAllowlistAndApprovalPolicy(t *testing.T) {
	c := &Client{
		tools:   map[string]string{"mcp__read_file": "read_file", "mcp__write_file": "write_file"},
		defs:    []llm.ToolDef{{Name: "mcp__read_file"}, {Name: "mcp__write_file"}},
		approve: map[string]bool{"mcp__write_file": true},
	}
	if !c.HasTool("mcp__read_file") || c.HasTool("mcp__exec") {
		t.Fatal("only advertised allowlisted MCP tools should be callable")
	}
	if c.RequiresApproval("mcp__read_file") || !c.RequiresApproval("mcp__write_file") {
		t.Fatal("approval policy did not distinguish read and write tools")
	}
	if got := c.ApprovalTools(); len(got) != 1 || got[0] != "mcp__write_file" {
		t.Fatalf("ApprovalTools() = %v", got)
	}
}

func TestStdioMCPServerDiscoveryAndCalls(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "calculator.go"), []byte("func Add(a,b int) int { return a+b }"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KERNEL_MCP_TEST_ROOT", root)
	t.Setenv("KERNEL_MCP_COMMAND", "go")
	args, _ := json.Marshal([]string{"run", "../../examples/test-mcp"})
	t.Setenv("KERNEL_MCP_ARGS", string(args))
	t.Setenv("KERNEL_MCP_ALLOW", "read_file,search,create_issue")
	t.Setenv("KERNEL_MCP_APPROVAL", "create_issue")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := FromEnv(ctx)
	if err != nil {
		t.Fatalf("discover stdio MCP server: %v", err)
	}
	if len(c.ToolDefs()) != 3 || !c.RequiresApproval("mcp__create_issue") {
		t.Fatalf("unexpected discovered tools/policy: %#v", c.ToolDefs())
	}
	got, err := c.Call(ctx, "mcp__read_file", map[string]any{"path": "calculator.go"})
	if err != nil || !strings.Contains(got, "a+b") {
		t.Fatalf("read_file = %q, %v", got, err)
	}
	got, err = c.Call(ctx, "mcp__search", map[string]any{"query": "func Add"})
	if err != nil || got != "calculator.go" {
		t.Fatalf("search = %q, %v", got, err)
	}
	if _, err := c.Call(ctx, "mcp__read_file", map[string]any{"path": "../outside"}); err == nil {
		t.Fatal("workspace escape was accepted")
	}
	if _, err := c.Call(ctx, "mcp__not_allowlisted", nil); err == nil {
		t.Fatal("non-allowlisted tool was callable")
	}
	if _, err := c.Call(ctx, "mcp__create_issue", map[string]any{"title": "bug", "body": "repro"}); err != nil {
		t.Fatalf("create_issue call: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".test-issues.log")); err != nil {
		t.Fatalf("create_issue side effect missing: %v", err)
	}
}

func TestApprovalNamesMustBeAllowlisted(t *testing.T) {
	if err := validateApprovalNames([]string{"read_file", "write_file"}, []string{"write_file"}); err != nil {
		t.Fatalf("valid approval policy rejected: %v", err)
	}
	if err := validateApprovalNames([]string{"read_file"}, []string{"write_file"}); err == nil {
		t.Fatal("approval-required tool outside allowlist was accepted")
	}
}

func TestToolResultIsBounded(t *testing.T) {
	got := contentText([]mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", maxResultBytes+20)}})
	if len(got) > maxResultBytes+40 || !strings.Contains(got, "truncated") {
		t.Fatalf("MCP result was not bounded: length=%d", len(got))
	}
}
