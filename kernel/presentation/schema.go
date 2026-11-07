package presentation

import (
	_ "embed"
	"net/http"
)

// The formal JSON Schema of Presentation IR v1 (docs/presentation-engine.md
// §4.1: "Формальная схема валидации"). The file is the single source of truth
// for external consumers (agents, UI tooling, channel integrations); the Go
// validator enforces the same contract plus the parts JSON Schema cannot
// express (resource limits, URL policy, dependency cycles).

//go:embed presentation-ir-v1.schema.json
var irSchemaJSON []byte

// IRSchema returns the embedded JSON Schema bytes.
func IRSchema() []byte {
	return irSchemaJSON
}

// ServeSchema writes the JSON Schema as an HTTP response — the kernel exposes
// it on the capabilities endpoint so tooling never hardcodes a copy.
func ServeSchema(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/schema+json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(irSchemaJSON)
}
