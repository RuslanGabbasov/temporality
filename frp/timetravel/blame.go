package timetravel

import (
	"errors"
	"fmt"
	"sort"
)

type BlameNodeType string

const (
	BlameNodeEvent      BlameNodeType = "event"
	BlameNodeSnapshot   BlameNodeType = "snapshot"
	BlameNodeState      BlameNodeType = "state"
	BlameNodeDecision   BlameNodeType = "decision"
	BlameNodeSideEffect BlameNodeType = "side_effect"
)

type BlameEdgeType string

const (
	BlameEdgeCausedBy    BlameEdgeType = "caused_by"
	BlameEdgeDerivedFrom BlameEdgeType = "derived_from"
	BlameEdgeReadFrom    BlameEdgeType = "read_from"
	BlameEdgeProduced    BlameEdgeType = "produced"
)

type BlameNode struct {
	ID   string        `json:"id"`
	Type BlameNodeType `json:"type"`
}

// BlameEdge is directed from cause to effect.
type BlameEdge struct {
	From string        `json:"from"`
	To   string        `json:"to"`
	Type BlameEdgeType `json:"type"`
}

type BlameGraph struct {
	RootID string      `json:"root_id"`
	Nodes  []BlameNode `json:"nodes"`
	Edges  []BlameEdge `json:"edges"`
}

// BuildBlameGraph traverses incoming edges breadth-first. A node is emitted at
// its shortest depth; visited nodes make cycles safe. Output order is stable.
func BuildBlameGraph(rootID string, nodes []BlameNode, edges []BlameEdge, maxDepth int) (BlameGraph, error) {
	if rootID == "" {
		return BlameGraph{}, errors.New("root_id is required")
	}
	if maxDepth < 0 {
		return BlameGraph{}, errors.New("max_depth cannot be negative")
	}
	byID := make(map[string]BlameNode, len(nodes))
	for _, node := range nodes {
		if node.ID == "" || !validNodeType(node.Type) {
			return BlameGraph{}, fmt.Errorf("invalid blame node %q", node.ID)
		}
		if _, exists := byID[node.ID]; exists {
			return BlameGraph{}, fmt.Errorf("duplicate blame node %q", node.ID)
		}
		byID[node.ID] = node
	}
	if _, exists := byID[rootID]; !exists {
		return BlameGraph{}, fmt.Errorf("root node %q not found", rootID)
	}
	incoming := make(map[string][]BlameEdge)
	for _, edge := range edges {
		if _, exists := byID[edge.From]; !exists {
			return BlameGraph{}, fmt.Errorf("edge references missing node %q", edge.From)
		}
		if _, exists := byID[edge.To]; !exists {
			return BlameGraph{}, fmt.Errorf("edge references missing node %q", edge.To)
		}
		if !validEdgeType(edge.Type) {
			return BlameGraph{}, fmt.Errorf("invalid blame edge type %q", edge.Type)
		}
		incoming[edge.To] = append(incoming[edge.To], edge)
	}
	for id := range incoming {
		sort.Slice(incoming[id], func(i, j int) bool { return lessEdge(incoming[id][i], incoming[id][j]) })
	}

	type visit struct {
		id    string
		depth int
	}
	queue := []visit{{id: rootID}}
	visited := map[string]bool{rootID: true}
	graph := BlameGraph{RootID: rootID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		graph.Nodes = append(graph.Nodes, byID[current.id])
		if current.depth == maxDepth {
			continue
		}
		for _, edge := range incoming[current.id] {
			graph.Edges = append(graph.Edges, edge)
			if !visited[edge.From] {
				visited[edge.From] = true
				queue = append(queue, visit{id: edge.From, depth: current.depth + 1})
			}
		}
	}
	return graph, nil
}

func validNodeType(value BlameNodeType) bool {
	switch value {
	case BlameNodeEvent, BlameNodeSnapshot, BlameNodeState, BlameNodeDecision, BlameNodeSideEffect:
		return true
	default:
		return false
	}
}

func validEdgeType(value BlameEdgeType) bool {
	switch value {
	case BlameEdgeCausedBy, BlameEdgeDerivedFrom, BlameEdgeReadFrom, BlameEdgeProduced:
		return true
	default:
		return false
	}
}

func lessEdge(a, b BlameEdge) bool {
	if a.From != b.From {
		return a.From < b.From
	}
	if a.To != b.To {
		return a.To < b.To
	}
	return a.Type < b.Type
}
