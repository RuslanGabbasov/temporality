package agent

import (
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/presentation"
)

// The presentation IR catalog embedded in the present tool description is the
// model's ONLY schema source (the sandbox has no repo access and the schema
// endpoint is auth-walled): every registered component type must be named in
// the catalog, or the model will invent field shapes for the missing one and
// burn turns probing the validator.
func TestPresentationCatalogCoversAllRegisteredTypes(t *testing.T) {
	for _, componentType := range presentation.Default().Types() {
		if !strings.Contains(presentationIRCatalog, componentType) {
			t.Errorf("catalog in the present tool description does not mention component type %q — the model will guess its shape", componentType)
		}
	}
	// The envelope fields and the error-path hint the model relies on.
	for _, needle := range []string{"version:'1.0'", "content:[", "items", "data", "props", "children", "content[2]"} {
		if !strings.Contains(presentationIRCatalog, needle) {
			t.Errorf("catalog missing %q", needle)
		}
	}
}
