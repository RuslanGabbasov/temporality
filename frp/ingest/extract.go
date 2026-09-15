package ingest

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Extraction is one deterministic proposition derived from committed
// observations. Confidence stays below 1: an extraction reports what was
// observed, not what the substrate has decided to believe.
type Extraction struct {
	Proposition string
	Confidence  float32
	Extractor   string
	Evidence    []string
}

// Extractor ids are versioned; a parser change must ship a new version so
// provenance stays interpretable.
const (
	ExtractorGoMod   = "gomod.v1"
	ExtractorNPM     = "npm.v1"
	ExtractorCargo   = "cargo.v1"
	ExtractorPython  = "python.v1"
	ExtractorReadme  = "readme.v1"
	ExtractorGit     = "git.v1"
	ExtractorHTTP    = "http.v1"
	ExtractorListing = "listing.v1"
)

// Extract runs the deterministic extractors over one run's observations and
// returns deduplicated extractions. The first directory_listing is the walk
// root (breadth-first order guarantees it).
func Extract(resourceID string, observations []observation) []Extraction {
	extractions := make([]Extraction, 0)
	seen := map[string]struct{}{}
	add := func(extraction Extraction) {
		if _, duplicate := seen[extraction.Proposition]; duplicate {
			return
		}
		seen[extraction.Proposition] = struct{}{}
		extractions = append(extractions, extraction)
	}
	rootListed := false
	for _, item := range observations {
		if item.record.EventID == "" {
			continue
		}
		switch item.record.ObservationType {
		case "file_content":
			for _, extraction := range extractFileContent(item) {
				add(extraction)
			}
		case "directory_listing":
			if rootListed {
				continue
			}
			rootListed = true
			if extraction, ok := extractRootListing(resourceID, item); ok {
				add(extraction)
			}
		case "git_status":
			for _, extraction := range extractGitStatus(resourceID, item) {
				add(extraction)
			}
		case "git_log":
			for _, extraction := range extractGitLog(resourceID, item) {
				add(extraction)
			}
		case "http_response":
			for _, extraction := range extractHTTP(item) {
				add(extraction)
			}
		}
	}
	sort.Slice(extractions, func(i, j int) bool { return extractions[i].Proposition < extractions[j].Proposition })
	return extractions
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

// anySlice normalizes slice payloads: the raw adapter produces typed slices
// like []map[string]any, while payloads read back through stores arrive as
// []any. Extractors accept both.
func anySlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []map[string]any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = item
		}
		return out
	default:
		return nil
	}
}

func payloadNumber(payload map[string]any, key string) (float64, bool) {
	switch value := payload[key].(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	default:
		return 0, false
	}
}

func extractRootListing(resourceID string, item observation) (Extraction, bool) {
	entries, ok := payloadNumber(item.payload, "entries")
	if !ok {
		return Extraction{}, false
	}
	return Extraction{
		Proposition: fmt.Sprintf("Workspace resource %q contains %d top-level entries", resourceID, int(entries)),
		Confidence:  0.8,
		Extractor:   ExtractorListing,
		Evidence:    []string{item.record.EventID},
	}, true
}

func extractFileContent(item observation) []Extraction {
	content := payloadString(item.payload, "content")
	if content == "" {
		return nil
	}
	switch filepath.Base(item.relPath) {
	case "go.mod":
		return extractGoMod(content, item)
	case "package.json":
		return extractNPM(content, item)
	case "Cargo.toml":
		return extractCargo(content, item)
	case "pyproject.toml":
		return extractPyProject(content, item)
	case "README.md":
		return extractReadme(content, item)
	default:
		return nil
	}
}

func extractGoMod(content string, item observation) []Extraction {
	module, goVersion := "", ""
	for _, line := range strings.Split(content, "\n") {
		if index := strings.Index(line, "//"); index >= 0 {
			line = line[:index]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "module":
			if module == "" {
				module = fields[1]
			}
		case "go":
			if goVersion == "" {
				goVersion = fields[1]
			}
		}
	}
	extractions := make([]Extraction, 0, 2)
	if module != "" {
		extractions = append(extractions, Extraction{
			Proposition: fmt.Sprintf("Go module %q is declared at %s", module, item.relPath),
			Confidence:  0.9,
			Extractor:   ExtractorGoMod,
			Evidence:    []string{item.record.EventID},
		})
	}
	if goVersion != "" {
		subject := item.relPath
		if module != "" {
			subject = fmt.Sprintf("Go module %q", module)
		}
		extractions = append(extractions, Extraction{
			Proposition: fmt.Sprintf("%s targets Go %s", subject, goVersion),
			Confidence:  0.9,
			Extractor:   ExtractorGoMod,
			Evidence:    []string{item.record.EventID},
		})
	}
	return extractions
}

func extractNPM(content string, item observation) []Extraction {
	var manifest struct {
		Name         string         `json:"name"`
		Version      string         `json:"version"`
		Dependencies map[string]any `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(content), &manifest); err != nil {
		return nil
	}
	extractions := make([]Extraction, 0, 2)
	if manifest.Name == "" {
		return extractions
	}
	identity := fmt.Sprintf("Project at %s is npm package %q", item.relPath, manifest.Name)
	if manifest.Version != "" {
		identity += fmt.Sprintf(" version %s", manifest.Version)
	}
	extractions = append(extractions, Extraction{
		Proposition: identity,
		Confidence:  0.9,
		Extractor:   ExtractorNPM,
		Evidence:    []string{item.record.EventID},
	})
	if len(manifest.Dependencies) > 0 {
		extractions = append(extractions, Extraction{
			Proposition: fmt.Sprintf("Npm package %q at %s declares %d direct dependencies", manifest.Name, item.relPath, len(manifest.Dependencies)),
			Confidence:  0.85,
			Extractor:   ExtractorNPM,
			Evidence:    []string{item.record.EventID},
		})
	}
	return extractions
}

// tomlSectionStrings scans one [section] for `key = "value"` pairs. This is a
// deliberately naive parser covering well-formed descriptors; anything subtle
// simply yields no extraction rather than a wrong claim.
func tomlSectionStrings(content, section string) map[string]string {
	values := map[string]string{}
	active := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			active = trimmed == "["+section+"]"
			continue
		}
		if !active {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
			values[key] = strings.Trim(value, "\"")
		}
	}
	return values
}

func extractCargo(content string, item observation) []Extraction {
	values := tomlSectionStrings(content, "package")
	if values["name"] == "" {
		return nil
	}
	proposition := fmt.Sprintf("Project at %s is Rust crate %q", item.relPath, values["name"])
	if values["version"] != "" {
		proposition += fmt.Sprintf(" version %s", values["version"])
	}
	return []Extraction{{Proposition: proposition, Confidence: 0.85, Extractor: ExtractorCargo, Evidence: []string{item.record.EventID}}}
}

func extractPyProject(content string, item observation) []Extraction {
	values := tomlSectionStrings(content, "project")
	if values["name"] == "" {
		return nil
	}
	proposition := fmt.Sprintf("Project at %s is Python project %q", item.relPath, values["name"])
	if values["version"] != "" {
		proposition += fmt.Sprintf(" version %s", values["version"])
	}
	return []Extraction{{Proposition: proposition, Confidence: 0.85, Extractor: ExtractorPython, Evidence: []string{item.record.EventID}}}
}

func extractReadme(content string, item observation) []Extraction {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			title := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
			if title == "" {
				return nil
			}
			return []Extraction{{Proposition: fmt.Sprintf("Project documentation at %s is titled %q", item.relPath, title), Confidence: 0.8, Extractor: ExtractorReadme, Evidence: []string{item.record.EventID}}}
		}
	}
	return nil
}

func extractGitStatus(resourceID string, item observation) []Extraction {
	status, _ := item.payload["status"].(map[string]any)
	if status == nil {
		return nil
	}
	extractions := make([]Extraction, 0, 2)
	if branch, _ := status["branch"].(string); branch != "" {
		if detached, _ := status["detached"].(bool); !detached {
			extractions = append(extractions, Extraction{
				Proposition: fmt.Sprintf("Repository %q is on branch %q", resourceID, branch),
				Confidence:  0.9,
				Extractor:   ExtractorGit,
				Evidence:    []string{item.record.EventID},
			})
		}
	}
	staged, _ := payloadNumber(status, "staged")
	untracked, _ := payloadNumber(status, "untracked")
	if changes := int(staged + untracked); changes > 0 {
		extractions = append(extractions, Extraction{
			Proposition: fmt.Sprintf("Repository %q has %d uncommitted file changes", resourceID, changes),
			Confidence:  0.85,
			Extractor:   ExtractorGit,
			Evidence:    []string{item.record.EventID},
		})
	}
	return extractions
}

func extractGitLog(resourceID string, item observation) []Extraction {
	commits := anySlice(item.payload["commits"])
	if len(commits) == 0 {
		return nil
	}
	commit, _ := commits[0].(map[string]any)
	if commit == nil {
		return nil
	}
	sha, _ := commit["sha"].(string)
	if len(sha) > 7 {
		sha = sha[:7]
	}
	author, _ := commit["author"].(string)
	summary, _ := commit["summary"].(string)
	return []Extraction{{
		Proposition: fmt.Sprintf("Latest commit in repository %q is %s %q by %s", resourceID, sha, summary, author),
		Confidence:  0.9,
		Extractor:   ExtractorGit,
		Evidence:    []string{item.record.EventID},
	}}
}

func extractHTTP(item observation) []Extraction {
	status, ok := payloadNumber(item.payload, "status")
	if !ok || status >= 400 {
		return nil
	}
	url := payloadString(item.payload, "url")
	if url == "" {
		return nil
	}
	contentType := payloadString(item.payload, "content_type")
	proposition := fmt.Sprintf("HTTP endpoint %s responded %d", url, int(status))
	if contentType != "" {
		proposition += fmt.Sprintf(" with content type %q", contentType)
	}
	return []Extraction{{Proposition: proposition, Confidence: 0.85, Extractor: ExtractorHTTP, Evidence: []string{item.record.EventID}}}
}
