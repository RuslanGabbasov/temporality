package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrInvalidProject rejects project ids that could escape
// <root>/projects/<id>: separators, dots and empty names.
var ErrInvalidProject = errors.New("invalid project id")

// TreeNode is one entry of a project area tree with the aggregated subtree
// size; children appear only while the depth and node budgets allow.
type TreeNode struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"` // relative to the area root, slash-separated
	Bytes    int64       `json:"bytes"`
	Files    int64       `json:"files"`
	ModAt    *time.Time  `json:"mod_at,omitempty"`
	Children []*TreeNode `json:"children,omitempty"`
}

// ProjectTree is the drill-down view of one project's areas.
type ProjectTree struct {
	Project   string
	Workspace *TreeNode
	Cache     *TreeNode
	TopFiles  []TopPath
	Truncated bool
}

// TreeOptions bounds the tree walk: Depth limits rendered children (sizes are
// still counted in full below the cut), MaxNodes caps the JSON size, TopFiles
// caps the largest-file list, Budget bounds wall time.
type TreeOptions struct {
	Depth    int
	MaxNodes int
	TopFiles int
	Budget   time.Duration
}

func (o TreeOptions) withDefaults() TreeOptions {
	if o.Depth <= 0 {
		o.Depth = 4
	}
	if o.MaxNodes <= 0 {
		o.MaxNodes = 5000
	}
	if o.TopFiles <= 0 {
		o.TopFiles = 10
	}
	if o.Budget <= 0 {
		o.Budget = 15 * time.Second
	}
	return o
}

// ProjectTreeSnapshot builds the depth-limited tree of a project's workspace
// and cache areas with full subtree aggregation. Only the node budget and the
// depth cut rendering — every byte below is still counted, so parent sizes
// stay truthful even in huge directories.
func ProjectTreeSnapshot(ctx context.Context, root, project string, opts TreeOptions) (*ProjectTree, error) {
	if err := ValidProjectID(project); err != nil {
		return nil, err
	}
	opts = opts.withDefaults()
	base, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(base); err != nil {
		return nil, err
	}
	tree := &ProjectTree{Project: project}
	builder := &treeBuilder{
		deadline:  time.Now().Add(opts.Budget),
		nodeLeft:  opts.MaxNodes,
		topDepth:  opts.Depth,
		truncated: &tree.Truncated,
	}
	tree.Workspace = builder.node(ctx, filepath.Join(base, "projects", project), "", 1)
	// Cache top-files get a "cache/" prefix so the largest-file list never
	// mixes the two areas under identical-looking relative paths.
	builder.prefix = "cache/"
	tree.Cache = builder.node(ctx, filepath.Join(base, "cache", project), "", 1)
	sort.Slice(builder.topFiles, func(i, j int) bool {
		if builder.topFiles[i].Bytes != builder.topFiles[j].Bytes {
			return builder.topFiles[i].Bytes > builder.topFiles[j].Bytes
		}
		return builder.topFiles[i].Path < builder.topFiles[j].Path
	})
	if len(builder.topFiles) > opts.TopFiles {
		builder.topFiles = builder.topFiles[:opts.TopFiles]
	}
	tree.TopFiles = builder.topFiles
	return tree, nil
}

// ValidProjectID accepts plain slug-like names only: no separators, no dot
// walking, no emptiness.
func ValidProjectID(project string) error {
	if project == "" || project == "." || project == ".." || strings.ContainsAny(project, `/\`) {
		return fmt.Errorf("%w: %q", ErrInvalidProject, project)
	}
	if strings.Contains(project, "\x00") {
		return fmt.Errorf("%w: %q", ErrInvalidProject, project)
	}
	return nil
}

type treeBuilder struct {
	deadline  time.Time
	nodeLeft  int
	topDepth  int
	prefix    string
	truncated *bool
	topFiles  []TopPath
}

// node aggregates one directory. Recursion always descends to keep sizes
// correct; children are only attached while the depth and node budgets last.
func (b *treeBuilder) node(ctx context.Context, dir, rel string, depth int) *TreeNode {
	node := &TreeNode{Name: filepath.Base(dir), Path: rel}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A missing or unreadable area is an empty node, not a failure.
		return node
	}
	if rel == "" {
		node.Name = ""
	}
	for _, entry := range entries {
		if ctx.Err() != nil || time.Now().After(b.deadline) {
			*b.truncated = true
			break
		}
		childRel := entry.Name()
		if rel != "" {
			childRel = rel + "/" + entry.Name()
		}
		if entry.IsDir() {
			child := b.node(ctx, filepath.Join(dir, entry.Name()), childRel, depth+1)
			node.Bytes += child.Bytes
			node.Files += child.Files
			if node.ModAt == nil || (child.ModAt != nil && child.ModAt.After(*node.ModAt)) {
				node.ModAt = child.ModAt
			}
			if depth <= b.topDepth {
				if b.nodeLeft > 0 {
					b.nodeLeft--
					node.Children = append(node.Children, child)
				} else {
					*b.truncated = true
				}
			}
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		node.Bytes += info.Size()
		node.Files++
		mod := info.ModTime()
		if node.ModAt == nil || mod.After(*node.ModAt) {
			m := mod
			node.ModAt = &m
		}
		b.topFiles = append(b.topFiles, TopPath{Path: b.prefix + childRel, Bytes: info.Size()})
	}
	sort.Slice(node.Children, func(i, j int) bool { return node.Children[i].Bytes > node.Children[j].Bytes })
	return node
}
