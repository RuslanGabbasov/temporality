package presentation

import (
	"fmt"
	"strings"
)

// Static renders for the extended diagram catalog (docs/presentation-engine.md
// §6.4, P3): mindmap, state-diagram, er-diagram, class-diagram and the
// dependency graph all convert into the flow layout — one layered graph
// engine serves every node-edge diagram, so the multi-format contract stays
// cheap to hold (§7.1).

func (l *layout) drawMindmapBlock(c Component, x, y, width float64) float64 {
	var data MindmapData
	_ = decodeStrict(c.Data, &data)
	flow, _ := mindmapToFlow(data.Root)
	frag := layoutFlow(flow, "LR", width, l.theme)
	return l.drawFragmentWithOptionalTitle("", frag, x, y)
}

// mindmapToFlow flattens the mindmap tree into a flow graph: every node
// becomes a rounded box, every parent-child link a plain edge, laid out
// left-to-right (the reading direction of a mind map).
func mindmapToFlow(root MindNode) (FlowData, string) {
	data := FlowData{Nodes: []FlowNode{}, Edges: []FlowEdge{}}
	counter := 0
	var walk func(node MindNode, parent string) string
	walk = func(node MindNode, parent string) string {
		id := fmt.Sprintf("m%d", counter)
		counter++
		label := node.Label
		if node.Note != "" {
			label += "\n" + node.Note
		}
		data.Nodes = append(data.Nodes, FlowNode{ID: id, Label: label, Shape: "round"})
		if parent != "" {
			data.Edges = append(data.Edges, FlowEdge{From: parent, To: id})
		}
		for _, child := range node.Children {
			walk(child, id)
		}
		return id
	}
	walk(root, "")
	return data, "LR"
}

func (l *layout) drawStateBlock(c Component, x, y, width float64) float64 {
	var data StateData
	_ = decodeStrict(c.Data, &data)
	var props FlowProps
	_ = decodeStrict(c.Props, &props)
	// States draw as rounded boxes; the initial state gains an entry edge
	// from a start marker (the flow layout has no pseudo-node concept, so the
	// start marker is a regular node with a fixed label).
	flow := FlowData{Nodes: make([]FlowNode, 0, len(data.States)+1), Edges: make([]FlowEdge, 0, len(data.Transitions)+1)}
	if data.Initial != "" {
		flow.Nodes = append(flow.Nodes, FlowNode{ID: "__start__", Label: "●", Shape: "round"})
		flow.Edges = append(flow.Edges, FlowEdge{From: "__start__", To: data.Initial, Label: "start"})
	}
	for _, state := range data.States {
		flow.Nodes = append(flow.Nodes, FlowNode{ID: state.ID, Label: state.Label, Shape: "round", Status: state.Status})
	}
	for _, transition := range data.Transitions {
		flow.Edges = append(flow.Edges, FlowEdge{From: transition.From, To: transition.To, Label: transition.Label, Dashed: transition.Dashed})
	}
	frag := layoutFlow(flow, labelOr(props.Direction, "LR"), width, l.theme)
	return l.drawFragmentWithOptionalTitle(props.Title, frag, x, y)
}

func (l *layout) drawERBlock(c Component, x, y, width float64) float64 {
	var data ERData
	_ = decodeStrict(c.Data, &data)
	var props struct {
		Title string `json:"title,omitempty"`
	}
	_ = decodeStrict(c.Props, &props)
	flow := FlowData{Nodes: make([]FlowNode, 0, len(data.Entities)), Edges: make([]FlowEdge, 0, len(data.Relations))}
	for _, entity := range data.Entities {
		label := entity.Name
		if len(entity.Attributes) > 0 {
			label += "\n" + joinDashed(entity.Attributes)
		}
		flow.Nodes = append(flow.Nodes, FlowNode{ID: entity.Name, Label: label})
	}
	for _, relation := range data.Relations {
		label := relation.Cardinality
		if relation.Label != "" {
			if label != "" {
				label += " "
			}
			label += relation.Label
		}
		flow.Edges = append(flow.Edges, FlowEdge{From: relation.From, To: relation.To, Label: label})
	}
	frag := layoutFlow(flow, "LR", width, l.theme)
	return l.drawFragmentWithOptionalTitle(props.Title, frag, x, y)
}

func (l *layout) drawClassBlock(c Component, x, y, width float64) float64 {
	var data ClassData
	_ = decodeStrict(c.Data, &data)
	var props struct {
		Title string `json:"title,omitempty"`
	}
	_ = decodeStrict(c.Props, &props)
	flow := FlowData{Nodes: make([]FlowNode, 0, len(data.Classes)), Edges: make([]FlowEdge, 0, len(data.Relations))}
	for _, class := range data.Classes {
		label := class.Name
		if len(class.Attributes) > 0 {
			label += "\n" + joinPrefixed(class.Attributes)
		}
		if len(class.Methods) > 0 {
			if len(class.Attributes) == 0 {
				label += "\n"
			}
			label += "\n" + joinPrefixed(class.Methods)
		}
		flow.Nodes = append(flow.Nodes, FlowNode{ID: class.Name, Label: label})
	}
	for _, relation := range data.Relations {
		label := relation.Kind
		if relation.Label != "" {
			if label != "" {
				label += ": "
			}
			label += relation.Label
		}
		flow.Edges = append(flow.Edges, FlowEdge{From: relation.From, To: relation.To, Label: label, Dashed: relation.Kind == "inheritance"})
	}
	frag := layoutFlow(flow, "TB", width, l.theme)
	return l.drawFragmentWithOptionalTitle(props.Title, frag, x, y)
}

// joinDashed / joinPrefixed shape attribute lists into compact box lines.
func joinDashed(values []string) string {
	return strings.Join(values, " · ")
}

func joinPrefixed(values []string) string {
	for i, value := range values {
		if !strings.HasPrefix(value, "+") && !strings.HasPrefix(value, "-") && !strings.HasPrefix(value, "#") {
			values[i] = "+ " + value
		}
	}
	return strings.Join(values, "\n")
}

func (l *layout) drawDependencyGraphBlock(c Component, x, y, width float64) float64 {
	// The dependency graph shares the flowchart data shape and engine.
	return l.drawFlowBlock(c, x, y, width)
}
