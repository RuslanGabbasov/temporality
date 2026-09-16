// Package entity defines the M14 knowledge-graph model. Entities and typed
// relations are a rebuildable projection over Claims: a claim that carries a
// subject/predicate/object triple asserts a relation between two entities, and
// entity ids are content-derived so rebuilding is deterministic and idempotent.
// The package has no dependencies outside the standard library so cognition
// can validate claim triples without import cycles.
package entity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrEntityNotFound is returned by stores when an entity id is unknown.
var ErrEntityNotFound = errors.New("entity not found")

// ProjectorVersion namespaces entity id derivation; changing the projection
// semantics requires a new version so ids stay interpretable.
const ProjectorVersion = "entity-claims.v1"

// Canonical entity types (frozen registry).
const (
	TypeWorkspace  = "workspace"
	TypeRepository = "repository"
	TypeBranch     = "branch"
	TypeCommit     = "commit"
	TypePerson     = "person"
	TypeProject    = "project"
	TypeModule     = "module"
	TypePackage    = "package"
	TypeLibrary    = "library"
	TypeToolchain  = "toolchain"
	TypeFile       = "file"
	TypeDirectory  = "directory"
	TypeEndpoint   = "endpoint"
	TypeService    = "service"
)

var entityTypes = map[string]struct{}{
	TypeWorkspace: {}, TypeRepository: {}, TypeBranch: {}, TypeCommit: {},
	TypePerson: {}, TypeProject: {}, TypeModule: {}, TypePackage: {},
	TypeLibrary: {}, TypeToolchain: {}, TypeFile: {}, TypeDirectory: {},
	TypeEndpoint: {}, TypeService: {},
}

// CanonicalEntityTypes returns the frozen entity type names in stable order.
func CanonicalEntityTypes() []string {
	names := make([]string, 0, len(entityTypes))
	for name := range entityTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidateType reports whether the entity type is registered.
func ValidateType(name string) error {
	if _, ok := entityTypes[name]; !ok {
		return fmt.Errorf("unknown entity type %q", name)
	}
	return nil
}

// Canonical relation predicates (frozen registry). Predicates type the edges
// of the knowledge graph.
const (
	PredicateContains   = "contains"
	PredicateDeclaredIn = "declared_in"
	PredicateDependsOn  = "depends_on"
	PredicateTargets    = "targets"
	PredicateOnBranch   = "on_branch"
	PredicateUses       = "uses"
	PredicateIntegrates = "integrates"
	PredicateAuthoredBy = "authored_by"
	PredicateDocuments  = "documents"
	PredicateServes     = "serves"
)

var predicates = map[string]struct{}{
	PredicateContains: {}, PredicateDeclaredIn: {}, PredicateDependsOn: {},
	PredicateTargets: {}, PredicateOnBranch: {}, PredicateUses: {},
	PredicateIntegrates: {}, PredicateAuthoredBy: {}, PredicateDocuments: {},
	PredicateServes: {},
}

// functionalPredicates are relations where one subject holds at most one
// object at a time. Two active claims asserting different objects for the
// same subject+functional predicate contradict each other; for one-to-many
// relations (contains, depends_on, uses, …) different objects are normal and
// carry no tension. The set is deliberately conservative: a false
// "contradiction" is noise the model would have to argue with.
var functionalPredicates = map[string]struct{}{
	PredicateDeclaredIn: {},
	PredicateOnBranch:   {},
	PredicateAuthoredBy: {},
}

// FunctionalPredicate reports whether the predicate is functional: subject
// and predicate together determine at most one object.
func FunctionalPredicate(name string) bool {
	_, ok := functionalPredicates[name]
	return ok
}

// CanonicalPredicates returns the frozen predicate names in stable order.
func CanonicalPredicates() []string {
	names := make([]string, 0, len(predicates))
	for name := range predicates {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidatePredicate reports whether the predicate is registered.
func ValidatePredicate(name string) error {
	if _, ok := predicates[name]; !ok {
		return fmt.Errorf("unknown entity predicate %q", name)
	}
	return nil
}

// Ref identifies an entity by type and name, serialized as "type:name". The
// name may itself contain colons (URLs, module paths); parsing splits on the
// first colon only.
type Ref struct {
	Type string
	Name string
}

func ParseRef(raw string) (Ref, error) {
	typeName, name, ok := strings.Cut(raw, ":")
	if !ok {
		return Ref{}, fmt.Errorf("entity ref %q must be \"type:name\"", raw)
	}
	ref := Ref{Type: typeName, Name: name}
	if err := ref.Validate(); err != nil {
		return Ref{}, err
	}
	return ref, nil
}

func (r Ref) Validate() error {
	if err := ValidateType(r.Type); err != nil {
		return err
	}
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("entity name must not be empty")
	}
	if len(r.Name) > 512 {
		return errors.New("entity name must not exceed 512 characters")
	}
	return nil
}

func (r Ref) String() string { return r.Type + ":" + r.Name }

// EntityID derives a deterministic UUID-shaped id from the entity identity.
func EntityID(typeName, name string) string {
	sum := sha256.Sum256([]byte(ProjectorVersion + "\x00" + typeName + "\x00" + name))
	raw := append([]byte(nil), sum[:16]...)
	raw[6] = (raw[6] & 0x0f) | 0x50
	raw[8] = (raw[8] & 0x3f) | 0x80
	h := hex.EncodeToString(raw)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// ID is the content-derived entity id for the ref.
func (r Ref) ID() string { return EntityID(r.Type, r.Name) }

// Entity is one node of the knowledge graph, projected from claims.
type Entity struct {
	EntityID          string  `json:"entity_id"`
	Type              string  `json:"type"`
	Name              string  `json:"name"`
	MentionCount      int     `json:"mention_count"`
	Confidence        float32 `json:"confidence"`
	ProjectionVersion string  `json:"projection_version"`
}

// EntityRelation is one typed edge asserted by one claim.
type EntityRelation struct {
	SourceID          string  `json:"source_id"`
	TargetID          string  `json:"target_id"`
	Predicate         string  `json:"predicate"`
	ClaimID           string  `json:"claim_id"`
	Confidence        float32 `json:"confidence"`
	ProjectionVersion string  `json:"projection_version"`
}

// Filter scopes entity listing.
type Filter struct {
	Type string
	Name string
}

// RelationFilter scopes relation listing; EntityID matches source or target.
type RelationFilter struct {
	EntityID string
}

// Store persists the entity projection. Entities are global world knowledge:
// rebuilding replaces the whole projection and is idempotent thanks to
// content-derived ids.
type Store interface {
	ReplaceAllEntities(context.Context, []Entity, []EntityRelation) error
	ListEntities(context.Context, Filter) ([]Entity, error)
	GetEntity(context.Context, string) (Entity, error)
	ListEntityRelations(context.Context, RelationFilter) ([]EntityRelation, error)
}
