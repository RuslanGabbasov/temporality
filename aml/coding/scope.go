// Package coding adapts the adaptive memory layer to a real coding-agent
// trajectory: a Go repository explored with shell/read/grep tools (experiment
// 3). The world is honest — commands run for real, failures are real toolchain
// failures, and the repository evolves between phases.
package coding

import "strings"

// Subsystems is the scope lexicon for the bleve fixture, ordered
// specific-first so "index/scorch" resolves to scorch, not index.
var Subsystems = []string{
	"geov2", "mergeplan", "upsidedown", "scorch",
	"analysis", "document", "fusion", "geo", "mapping",
	"numeric", "registry", "search", "scorer", "util", "index",
}

// InferScope resolves the subsystem a piece of text (command, path, task)
// refers to. First lexicon hit wins; "" when nothing matches.
func InferScope(text string, lexicon []string) string {
	lowered := strings.ToLower(text)
	for _, svc := range lexicon {
		if strings.Contains(lowered, svc) {
			return svc
		}
	}
	return ""
}
