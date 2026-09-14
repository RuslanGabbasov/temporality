package timetravel_test

import (
	"embed"
	"encoding/json"
	"testing"
)

//go:embed schemas/*.json
var schemaFiles embed.FS

func TestJSONSchemasAreValidJSON(t *testing.T) {
	entries, err := schemaFiles.ReadDir("schemas")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d schemas, want 3", len(entries))
	}
	for _, entry := range entries {
		content, err := schemaFiles.ReadFile("schemas/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]any
		if err := json.Unmarshal(content, &schema); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["$id"] == "" {
			t.Fatalf("%s lacks versioned schema metadata", entry.Name())
		}
	}
}
