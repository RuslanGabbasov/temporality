// test-mcp is a small stdio MCP server used by Agent Kernel integration tests.
// Its filesystem access is deliberately confined to KERNEL_MCP_TEST_ROOT.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fileArgs struct {
	Path string `json:"path"`
}
type searchArgs struct {
	Query string `json:"query"`
}
type issueArgs struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func main() {
	root := os.Getenv("KERNEL_MCP_TEST_ROOT")
	if root == "" {
		panic("KERNEL_MCP_TEST_ROOT is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		panic(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		panic(err)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "temporality-test-mcp", Version: "1.0.0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "read_file", Description: "Read a workspace file", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}}, func(_ context.Context, _ *mcp.CallToolRequest, a fileArgs) (*mcp.CallToolResult, any, error) {
		path, err := safePath(root, a.Path)
		if err != nil {
			return nil, nil, err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		return mcpText(string(b)), nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "search", Description: "Search workspace text", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}}}, func(_ context.Context, _ *mcp.CallToolRequest, a searchArgs) (*mcp.CallToolResult, any, error) {
		if a.Query == "" {
			return nil, nil, errors.New("query is required")
		}
		var matches []string
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			b, e := os.ReadFile(path)
			if e == nil && strings.Contains(string(b), a.Query) {
				rel, _ := filepath.Rel(root, path)
				matches = append(matches, rel)
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		return mcpText(strings.Join(matches, "\n")), nil, nil
	})
	// The server persists issues and idempotency keys under the test root, so
	// the contract survives process restarts — the kernel spawns a fresh stdio
	// server per call, which is exactly the retry window this simulates.
	issueLog := filepath.Join(root, ".test-issues.log")
	keyLog := filepath.Join(root, ".test-idempotency.log")
	var issueMu sync.Mutex
	loadKeys := func() (map[string]int, int) {
		keys := map[string]int{}
		highest := 0
		data, err := os.ReadFile(keyLog)
		if err != nil {
			return keys, highest
		}
		for _, line := range strings.Split(string(data), "\n") {
			key, number, ok := strings.Cut(line, " ")
			if !ok || key == "" {
				continue
			}
			n, err := strconv.Atoi(number)
			if err != nil {
				continue
			}
			keys[key] = n
			if n > highest {
				highest = n
			}
		}
		return keys, highest
	}
	mcp.AddTool(s, &mcp.Tool{Name: "create_issue", Description: "Create a test issue", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}, "body": map[string]any{"type": "string"}}, "required": []string{"title", "body"}}}, func(_ context.Context, req *mcp.CallToolRequest, a issueArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(a.Title) == "" {
			return nil, nil, errors.New("title is required")
		}
		// The server honors the client's idempotency key: a retry of the same
		// operation returns the same issue instead of creating a duplicate.
		key, _ := req.Params.Meta["idempotency_key"].(string)
		issueMu.Lock()
		defer issueMu.Unlock()
		keys, seq := loadKeys()
		if number, ok := keys[key]; key != "" && ok {
			return mcpText(fmt.Sprintf("issue #%d already created (idempotent retry)", number)), nil, nil
		}
		seq++
		number := seq
		f, err := os.OpenFile(issueLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return nil, nil, err
		}
		if _, err := f.WriteString(fmt.Sprintf("#%d %s\n%s\n---\n", number, a.Title, a.Body)); err != nil {
			f.Close()
			return nil, nil, err
		}
		f.Close()
		if key != "" {
			kf, err := os.OpenFile(keyLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				return nil, nil, err
			}
			if _, err := kf.WriteString(key + " " + strconv.Itoa(number) + "\n"); err != nil {
				kf.Close()
				return nil, nil, err
			}
			kf.Close()
		}
		return mcpText(fmt.Sprintf("issue #%d created", number)), nil, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		panic(err)
	}
}

func safePath(root, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", errors.New("absolute paths are forbidden")
	}
	path := filepath.Join(root, filepath.Clean(name))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err = filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("symlink escapes workspace")
	}
	return resolved, nil
}
func mcpText(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}
