package presentation

import (
	"os"
	"path/filepath"
	"runtime"
)

// schemaOnDisk reads the schemas/ copy of the IR schema when the checkout
// layout allows; tests skip gracefully in module-cache-only builds.
func schemaOnDisk() (string, error) {
	_, thisFile, _, _ := runtime.Caller(0)
	// kernel/presentation -> repo root is two levels up.
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	raw, err := os.ReadFile(filepath.Join(root, "schemas", "temporality.presentation.v1.schema.json"))
	return string(raw), err
}
