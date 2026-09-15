package world

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/temporality-project/temporality/frp/protocol"
)

// Category classifies what a capability physically allows (M11.1).
// A capability describes what is physically permitted; an affordance is the
// meaningful action exposed to the agent and only references capabilities.
type Category string

const (
	CategoryObserve     Category = "observe"
	CategoryRead        Category = "read"
	CategoryWrite       Category = "write"
	CategoryExecute     Category = "execute"
	CategoryCommunicate Category = "communicate"
	CategoryNavigate    Category = "navigate"
)

// CanonicalCategories returns the closed capability category set.
func CanonicalCategories() []Category {
	return []Category{CategoryObserve, CategoryRead, CategoryWrite, CategoryExecute, CategoryCommunicate, CategoryNavigate}
}

func (c Category) Valid() bool {
	switch c {
	case CategoryObserve, CategoryRead, CategoryWrite, CategoryExecute, CategoryCommunicate, CategoryNavigate:
		return true
	}
	return false
}

// knownCapabilities is the frozen M11 capability registry.
var knownCapabilities = map[string]Category{
	"filesystem.observe": CategoryObserve,
	"filesystem.read":    CategoryRead,
	"filesystem.write":   CategoryWrite,
	"process.observe":    CategoryObserve,
	"process.execute":    CategoryExecute,
	"http.read":          CategoryRead,
	"http.write":         CategoryWrite,
	"git.read":           CategoryRead,
	"git.write":          CategoryWrite,
	"browser.navigate":   CategoryNavigate,
	"browser.read":       CategoryRead,
	"browser.interact":   CategoryWrite,
	"mcp.call":           CategoryCommunicate,
}

// CanonicalCapabilities returns the frozen capability names in stable order.
func CanonicalCapabilities() []string {
	names := make([]string, 0, len(knownCapabilities))
	for name := range knownCapabilities {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// CapabilityCategory reports the category of a registered capability.
func CapabilityCategory(name string) (Category, bool) {
	c, ok := knownCapabilities[name]
	return c, ok
}

func ValidateCapability(name string) error {
	if _, ok := knownCapabilities[name]; !ok {
		return fmt.Errorf("unknown capability %q", name)
	}
	return nil
}

// Resource types describe physical world resources referenced by capabilities.
const (
	ResourceFilesystem    = "filesystem"
	ResourceGitRepository = "git_repository"
	ResourceHTTPEndpoint  = "http_endpoint"
	ResourceBrowser       = "browser"
	ResourceMCPServer     = "mcp_server"
)

var resourceTypes = map[string]struct{}{
	ResourceFilesystem:    {},
	ResourceGitRepository: {},
	ResourceHTTPEndpoint:  {},
	ResourceBrowser:       {},
	ResourceMCPServer:     {},
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// Resource is a physical addressable part of the world (M11.2).
type Resource struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Path     string `json:"path,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

func (r Resource) Validate() error {
	if !idPattern.MatchString(r.ID) {
		return fmt.Errorf("resource id %q must match %s", r.ID, idPattern.String())
	}
	if _, ok := resourceTypes[r.Type]; !ok {
		return fmt.Errorf("resource %q has unknown type %q", r.ID, r.Type)
	}
	switch r.Type {
	case ResourceFilesystem, ResourceGitRepository:
		if strings.TrimSpace(r.Path) == "" {
			return fmt.Errorf("resource %q of type %s requires path", r.ID, r.Type)
		}
	case ResourceHTTPEndpoint, ResourceBrowser, ResourceMCPServer:
		if strings.TrimSpace(r.Endpoint) == "" {
			return fmt.Errorf("resource %q of type %s requires endpoint", r.ID, r.Type)
		}
		if err := validateEndpoint(r.Endpoint); err != nil {
			return fmt.Errorf("resource %q: %w", r.ID, err)
		}
	}
	return nil
}

func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("endpoint %q must use http or https", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("endpoint %q requires a host", raw)
	}
	return nil
}

// Identity names an actor in the world; it never carries secret material.
type Identity struct {
	ID   string `json:"id"`
	Kind string `json:"kind,omitempty"`
}

// Credential references a secret by name only (env var or secret store ref).
// The secret value itself is never part of the world description.
type Credential struct {
	ID         string `json:"id"`
	IdentityID string `json:"identity_id,omitempty"`
	Kind       string `json:"kind"`
	Ref        string `json:"ref"`
}

// Limits bound every physical observation or effect the world permits.
type Limits struct {
	MaxReadBytes int64 `json:"max_read_bytes"`
	MaxEntries   int   `json:"max_entries"`
	TimeoutSec   int   `json:"timeout_sec"`
}

const (
	DefaultMaxReadBytes int64 = 65536
	DefaultMaxEntries         = 1000
	DefaultTimeoutSec         = 30
	MaxAllowedReadBytes int64 = 1048576
	MaxAllowedEntries         = 10000
)

func (l *Limits) ApplyDefaults() {
	if l.MaxReadBytes <= 0 {
		l.MaxReadBytes = DefaultMaxReadBytes
	}
	if l.MaxEntries <= 0 {
		l.MaxEntries = DefaultMaxEntries
	}
	if l.TimeoutSec <= 0 {
		l.TimeoutSec = DefaultTimeoutSec
	}
}

func (l Limits) Validate() error {
	if l.MaxReadBytes <= 0 || l.MaxEntries <= 0 || l.TimeoutSec <= 0 {
		return errors.New("world limits must be positive")
	}
	if l.MaxReadBytes > MaxAllowedReadBytes {
		return fmt.Errorf("max_read_bytes cannot exceed %d", MaxAllowedReadBytes)
	}
	if l.MaxEntries > MaxAllowedEntries {
		return fmt.Errorf("max_entries cannot exceed %d", MaxAllowedEntries)
	}
	return nil
}

// PolicyEffect determines whether a policy allows or denies a capability.
type PolicyEffect string

const (
	PolicyAllow PolicyEffect = "allow"
	PolicyDeny  PolicyEffect = "deny"
)

// Policy is an explicit allow/deny rule scoped to a capability and optionally
// to a single resource.
type Policy struct {
	Effect     PolicyEffect `json:"effect"`
	Capability string       `json:"capability"`
	ResourceID string       `json:"resource_id,omitempty"`
}

func (p Policy) Validate() error {
	if p.Effect != PolicyAllow && p.Effect != PolicyDeny {
		return fmt.Errorf("policy effect must be %q or %q", PolicyAllow, PolicyDeny)
	}
	return ValidateCapability(p.Capability)
}

var (
	ErrWorldNotFound      = errors.New("world not found")
	ErrWorldStateStale    = errors.New("world state version is not newer than the stored state")
	ErrCapabilityDenied   = errors.New("capability is not granted by world")
	ErrResourceNotFound   = errors.New("no world resource matches the request")
	ErrObservationInvalid = errors.New("world observation is invalid")
)

// World is the versioned description of the environment an agent acts in
// (M11.2). Worlds are mutable but strictly versioned so replay can attribute
// every effect to the exact environment state it happened in.
type World struct {
	Protocol     string       `json:"protocol"`
	Version      string       `json:"version"`
	WorldID      string       `json:"world_id"`
	StateVersion int          `json:"state_version"`
	Resources    []Resource   `json:"resources"`
	Capabilities []string     `json:"capabilities"`
	Identities   []Identity   `json:"identities"`
	Credentials  []Credential `json:"credentials"`
	Limits       Limits       `json:"limits"`
	Policies     []Policy     `json:"policies"`
}

func (w *World) ApplyDefaults() {
	if w.Protocol == "" {
		w.Protocol = protocol.Name
	}
	if w.Version == "" {
		w.Version = protocol.Version
	}
	if w.Resources == nil {
		w.Resources = []Resource{}
	}
	if w.Capabilities == nil {
		w.Capabilities = []string{}
	}
	if w.Identities == nil {
		w.Identities = []Identity{}
	}
	if w.Credentials == nil {
		w.Credentials = []Credential{}
	}
	if w.Policies == nil {
		w.Policies = []Policy{}
	}
	w.Limits.ApplyDefaults()
}

func (w World) Validate() error {
	if w.Protocol != protocol.Name || w.Version != protocol.Version {
		return fmt.Errorf("unsupported protocol version %q/%q", w.Protocol, w.Version)
	}
	if !idPattern.MatchString(w.WorldID) {
		return fmt.Errorf("world_id %q must match %s", w.WorldID, idPattern.String())
	}
	if w.StateVersion < 1 {
		return errors.New("state_version must be >= 1")
	}
	resources := make(map[string]struct{}, len(w.Resources))
	for _, resource := range w.Resources {
		if err := resource.Validate(); err != nil {
			return err
		}
		if _, ok := resources[resource.ID]; ok {
			return fmt.Errorf("duplicate resource %q", resource.ID)
		}
		resources[resource.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(w.Capabilities))
	for _, capability := range w.Capabilities {
		if err := ValidateCapability(capability); err != nil {
			return err
		}
		if _, ok := seen[capability]; ok {
			return fmt.Errorf("duplicate capability %q", capability)
		}
		seen[capability] = struct{}{}
	}
	identities := make(map[string]struct{}, len(w.Identities))
	for _, identity := range w.Identities {
		if !idPattern.MatchString(identity.ID) {
			return fmt.Errorf("identity id %q must match %s", identity.ID, idPattern.String())
		}
		if _, ok := identities[identity.ID]; ok {
			return fmt.Errorf("duplicate identity %q", identity.ID)
		}
		identities[identity.ID] = struct{}{}
	}
	for _, credential := range w.Credentials {
		if !idPattern.MatchString(credential.ID) {
			return fmt.Errorf("credential id %q must match %s", credential.ID, idPattern.String())
		}
		if strings.TrimSpace(credential.Kind) == "" || strings.TrimSpace(credential.Ref) == "" {
			return errors.New("credential kind and ref are required")
		}
		if credential.IdentityID != "" {
			if _, ok := identities[credential.IdentityID]; !ok {
				return fmt.Errorf("credential %q references unknown identity %q", credential.ID, credential.IdentityID)
			}
		}
	}
	for _, policy := range w.Policies {
		if err := policy.Validate(); err != nil {
			return err
		}
		if _, ok := seen[policy.Capability]; !ok {
			return fmt.Errorf("policy references capability %q not granted by the world", policy.Capability)
		}
		if policy.ResourceID != "" {
			if _, ok := resources[policy.ResourceID]; !ok {
				return fmt.Errorf("policy references unknown resource %q", policy.ResourceID)
			}
		}
	}
	return w.Limits.Validate()
}

// Grants reports whether the world declares the capability.
func (w World) Grants(capability string) bool {
	for _, granted := range w.Capabilities {
		if granted == capability {
			return true
		}
	}
	return false
}

// Authorize enforces the M11 effect boundary at intent and effect time: the
// capability must be granted and no deny policy may match it.
func (w World) Authorize(capability string) error {
	if err := ValidateCapability(capability); err != nil {
		return err
	}
	if !w.Grants(capability) {
		return fmt.Errorf("%w: %s", ErrCapabilityDenied, capability)
	}
	for _, policy := range w.Policies {
		if policy.Effect == PolicyDeny && policy.Capability == capability {
			return fmt.Errorf("%w: %s denied by policy", ErrCapabilityDenied, capability)
		}
	}
	return nil
}

// AuthorizeAll checks a full capability set.
func (w World) AuthorizeAll(capabilities []string) error {
	for _, capability := range capabilities {
		if err := w.Authorize(capability); err != nil {
			return err
		}
	}
	return nil
}

// Resource returns the declared resource by id.
func (w World) Resource(id string) (Resource, bool) {
	for _, resource := range w.Resources {
		if resource.ID == id {
			return resource, true
		}
	}
	return Resource{}, false
}

// Roots lists filesystem-backed resources (filesystem and git repositories).
func (w World) Roots() []Resource {
	roots := make([]Resource, 0, len(w.Resources))
	for _, resource := range w.Resources {
		if resource.Type == ResourceFilesystem || resource.Type == ResourceGitRepository {
			roots = append(roots, resource)
		}
	}
	return roots
}

// HTTPEndpoints lists declared HTTP endpoint resources.
func (w World) HTTPEndpoints() []Resource {
	endpoints := make([]Resource, 0, len(w.Resources))
	for _, resource := range w.Resources {
		if resource.Type == ResourceHTTPEndpoint {
			endpoints = append(endpoints, resource)
		}
	}
	return endpoints
}
