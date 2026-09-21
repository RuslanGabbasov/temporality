package coding

// Task is one independent coding-agent task on the bleve fixture. Markers are
// the substrings the final answer must contain (case-insensitive) — they are
// facts about the repository, verified against the pinned commit.
type Task struct {
	ID        string
	Subsystem string
	Text      string
	Markers   []string
}

// Tasks3A is the stable-repository task set (10 independent tasks, one per
// subsystem question). Each task requires real discovery (locate code, run the
// right tests) and none of them names the answer files.
func Tasks3A() []Task {
	return []Task{
		{
			ID:        "T1-analysis-freq",
			Subsystem: "analysis",
			Text:      "In the analysis subsystem, find where token frequencies are computed after tokenization: identify the file and the exported function that builds the frequency map. Then run the analysis package tests to confirm they pass. Report: the file path, the function name, and the exact test command you used.",
			Markers:   []string{"analysis/freq.go", "tokenfrequency", "go test ./analysis"},
		},
		{
			ID:        "T2-numeric-float",
			Subsystem: "numeric",
			Text:      "In the numeric subsystem, find how a float64 is converted into a sortable int64 representation for indexing: file and exported conversion functions. Run the numeric package tests. Report: the file path, both function names, and the exact test command.",
			Markers:   []string{"numeric/float.go", "float64toint64", "go test ./numeric"},
		},
		{
			ID:        "T3-mapping-default",
			Subsystem: "mapping",
			Text:      "In the mapping subsystem, find which constant defines the default analyzer for a new index mapping and where it is set. Run the mapping package tests. Report: the file path, the constant name, and the exact test command.",
			Markers:   []string{"mapping/index.go", "defaultanalyzer", "go test ./mapping"},
		},
		{
			ID:        "T4-document-type",
			Subsystem: "document",
			Text:      "In the document subsystem, find the file that defines the Document type and its constructor. Run the document package tests. Report: the file path, the constructor name, and the exact test command.",
			Markers:   []string{"document/document.go", "newdocument", "go test ./document"},
		},
		{
			ID:        "T5-fusion-rrf",
			Subsystem: "fusion",
			Text:      "In the fusion subsystem, find the exported function that combines several ranked result lists reciprocally. Run the fusion package tests. Report: the file path, the function name, and the exact test command.",
			Markers:   []string{"fusion/rrf.go", "reciprocalrankfusion", "go test ./fusion"},
		},
		{
			ID:        "T6-search-scorer",
			Subsystem: "search",
			Text:      "In the search subsystem, find the package that scores query results and name one scorer source file for term queries. Run the search package tests for that scorer's tree (a path with /... keeps it complete). Report: the package directory, one scorer file name, and the exact test command.",
			Markers:   []string{"search/scorer", "go test ./search"},
		},
		{
			ID:        "T7-scorch-rollback",
			Subsystem: "scorch",
			Text:      "Does the scorch index implementation support rollback to earlier snapshots? Find the file and the exported function that lists the available rollback points, then run only the rollback tests in scorch (a -run filter keeps it fast). Report: the file path, the function name, and the exact test command.",
			Markers:   []string{"index/scorch/rollback.go", "rollbackpoints", "go test ./index/scorch"},
		},
		{
			ID:        "T8-upsidedown-type",
			Subsystem: "upsidedown",
			Text:      "Find the legacy index implementation in this repository: the file that defines its main struct type and its constructor. Run that package's tests. Report: the file path, the struct type name, and the exact test command.",
			Markers:   []string{"index/upsidedown/upsidedown.go", "upsidedowncouch", "go test ./index/upsidedown"},
		},
		{
			ID:        "T9-geo-morton",
			Subsystem: "geo",
			Text:      "In the geo subsystem, find how a longitude/latitude pair is packed into a single uint64 for indexing: file and exported function names. Run the geo package tests. Report: the file path, the main function name, and the exact test command.",
			Markers:   []string{"geo/geo.go", "mortonhash", "go test ./geo"},
		},
		{
			ID:        "T10-registry-noteests",
			Subsystem: "registry",
			Text:      "In the registry subsystem, find how index types register themselves: the file and the exported registration function. Then try to run the registry package tests and report exactly what the go tool prints for that package. Report: the file path, the function name, and the exact test outcome line.",
			Markers:   []string{"registry/index_type.go", "registerindextype", "no test files"},
		},
	}
}

// Tasks3B is the post-evolution set: T1 is the flip target (its remembered
// test command now fails), T3 and T10 are unchanged distractors.
func Tasks3B() []Task {
	all := map[string]Task{}
	for _, t := range Tasks3A() {
		all[t.ID] = t
	}
	return []Task{all["T1-analysis-freq"], all["T3-mapping-default"], all["T10-registry-noteests"]}
}

// Task3C is the context-conflict task: same question, two branch conventions.
func Task3C() Task {
	return Tasks3A()[0]
}
