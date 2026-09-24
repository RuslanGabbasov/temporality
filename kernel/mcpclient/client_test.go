package mcpclient

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/temporality-project/temporality/aml/llm"
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
