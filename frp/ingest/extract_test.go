package ingest

import (
	"fmt"
	"testing"
)

func observationOf(observationType, eventID, relPath string, payload map[string]any) observation {
	return observation{record: ObservationRecord{EventID: eventID, ObservationType: observationType}, payload: payload, relPath: relPath}
}

func TestExtractGoMod(t *testing.T) {
	content := "module example.com/app // trailing comment\n\ngo 1.24\n\nrequire (\n\tgithub.com/x/y v1.0.0\n)\n"
	extractions := Extract("repo", []observation{observationOf("file_content", "e1", "go.mod", map[string]any{"content": content})})
	propositions := propositionsOf(extractions)
	want := []string{
		`Go module "example.com/app" is declared at go.mod`,
		`Go module "example.com/app" targets Go 1.24`,
	}
	for _, expected := range want {
		if _, ok := propositions[expected]; !ok {
			t.Fatalf("missing %q in %v", expected, propositions)
		}
	}
}

func TestExtractNPMPartial(t *testing.T) {
	// Invalid JSON yields no claims rather than wrong ones.
	extractions := Extract("repo", []observation{observationOf("file_content", "e1", "package.json", map[string]any{"content": "{oops"})})
	if len(extractions) != 0 {
		t.Fatalf("extractions = %+v", extractions)
	}
	valid := Extract("repo", []observation{observationOf("file_content", "e2", "web/package.json", map[string]any{"content": `{"name":"web","dependencies":{"a":"1","b":"2"}}`})})
	propositions := propositionsOf(valid)
	if _, ok := propositions[`Project at web/package.json is npm package "web"`]; !ok {
		t.Fatalf("missing identity claim in %v", propositions)
	}
	if _, ok := propositions[`Npm package "web" at web/package.json declares 2 direct dependencies`]; !ok {
		t.Fatalf("missing dependency claim in %v", propositions)
	}
}

func TestExtractCargoAndPython(t *testing.T) {
	cargo := "[package]\nname = \"solver\"\nversion = \"0.3.1\"\n\n[dependencies]\nserde = \"1\"\n"
	extractions := Extract("repo", []observation{observationOf("file_content", "e1", "Cargo.toml", map[string]any{"content": cargo})})
	if len(extractions) != 1 || extractions[0].Proposition != `Project at Cargo.toml is Rust crate "solver" version 0.3.1` {
		t.Fatalf("cargo extractions = %+v", extractions)
	}
	python := "[build-system]\nrequires = [\"setuptools\"]\n\n[project]\nname = \"demo\"\nversion = \"2.1.0\"\n"
	extractions = Extract("repo", []observation{observationOf("file_content", "e2", "pyproject.toml", map[string]any{"content": python})})
	if len(extractions) != 1 || extractions[0].Proposition != `Project at pyproject.toml is Python project "demo" version 2.1.0` {
		t.Fatalf("python extractions = %+v", extractions)
	}
	// A [project] section without a name must not leak the [build-system] one.
	unnamed := "[build-system]\nname = \"ignored\"\n"
	extractions = Extract("repo", []observation{observationOf("file_content", "e3", "pyproject.toml", map[string]any{"content": unnamed})})
	if len(extractions) != 0 {
		t.Fatalf("unnamed python extractions = %+v", extractions)
	}
}

func TestExtractReadme(t *testing.T) {
	content := "[![badge](https://img)](x)\n\n# Real Title\n\n## Section\n"
	extractions := Extract("repo", []observation{observationOf("file_content", "e1", "README.md", map[string]any{"content": content})})
	if len(extractions) != 1 || extractions[0].Proposition != `Project documentation at README.md is titled "Real Title"` {
		t.Fatalf("readme extractions = %+v", extractions)
	}
}

func TestExtractGit(t *testing.T) {
	status := observationOf("git_status", "e1", ".", map[string]any{"status": map[string]any{"branch": "main", "detached": false, "staged": 2, "untracked": 1}})
	log := observationOf("git_log", "e2", ".", map[string]any{"commits": []any{map[string]any{"sha": "0123456789abcdef", "author": "Ada", "summary": "initial commit"}, map[string]any{"sha": "ffffffff", "author": "Bob", "summary": "second"}}})
	extractions := Extract("repo", []observation{status, log})
	propositions := propositionsOf(extractions)
	if _, ok := propositions[`Repository "repo" is on branch "main"`]; !ok {
		t.Fatalf("missing branch claim in %v", propositions)
	}
	if _, ok := propositions[`Repository "repo" has 3 uncommitted file changes`]; !ok {
		t.Fatalf("missing changes claim in %v", propositions)
	}
	if _, ok := propositions[`Latest commit in repository "repo" is 0123456 "initial commit" by Ada`]; !ok {
		t.Fatalf("missing log claim in %v", propositions)
	}
}

func TestExtractHTTP(t *testing.T) {
	extractions := Extract("repo", []observation{observationOf("http_response", "e1", "", map[string]any{"url": "https://api.example.com/health", "status": 200.0, "content_type": "application/json"})})
	if len(extractions) != 1 || extractions[0].Proposition != `HTTP endpoint https://api.example.com/health responded 200 with content type "application/json"` {
		t.Fatalf("http extractions = %+v", extractions)
	}
	failing := Extract("repo", []observation{observationOf("http_response", "e2", "", map[string]any{"url": "https://api.example.com/x", "status": 503.0})})
	if len(failing) != 0 {
		t.Fatalf("failing http extractions = %+v", failing)
	}
}

func TestExtractDeduplicatesPropositions(t *testing.T) {
	item := observationOf("file_content", "e1", "go.mod", map[string]any{"content": "module example.com/dup\n"})
	duplicate := observationOf("file_content", "e2", "go.mod", map[string]any{"content": "module example.com/dup\n"})
	extractions := Extract("repo", []observation{item, duplicate})
	if len(extractions) != 1 {
		t.Fatalf("extractions = %+v", extractions)
	}
	if extractions[0].Evidence[0] != "e1" {
		t.Fatalf("dedup must keep the first evidence, got %v", extractions[0].Evidence)
	}
}

func TestExtractListingOnlyRoot(t *testing.T) {
	root := observationOf("directory_listing", "e1", ".", map[string]any{"entries": 12})
	nested := observationOf("directory_listing", "e2", "sub", map[string]any{"entries": 3})
	extractions := Extract("repo", []observation{root, nested})
	if len(extractions) != 1 || extractions[0].Proposition != `Workspace resource "repo" contains 12 top-level entries` {
		t.Fatalf("listing extractions = %+v", extractions)
	}
	if fmt.Sprint(extractions[0].Evidence) != "[e1]" {
		t.Fatalf("evidence = %v", extractions[0].Evidence)
	}
}

func propositionsOf(extractions []Extraction) map[string]Extraction {
	out := map[string]Extraction{}
	for _, extraction := range extractions {
		out[extraction.Proposition] = extraction
	}
	return out
}
