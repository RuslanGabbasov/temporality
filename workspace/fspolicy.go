package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Filesystem policy model (docs/filesystem-policy.md §3): glob rules over
// normalized workspace-relative paths, restrictive merge across org-policy
// levels, one frozen snapshot per run.
//
// Semantics fixed here (docs/filesystem-policy-implementation.md §1):
//   - a nil dimension restricts nothing (§3.5 — pre-filesystem installs keep
//     their behavior byte-for-byte);
//   - an empty non-nil list allows nothing (the PolicyAllowsList nil/[] split);
//     store normalization maps a submitted empty list back to nil;
//   - within one policy row a dimension is OR over its masks;
//   - across rows the dimension INTERSECTS semantically — represented as ANDed
//     layers (one OR-layer per contributing row), because glob sets are not
//     closed under intersection and «простого пересечения строковых значений
//     недостаточно» (§3.4);
//   - deny is absolute: union across levels, checked before any allow, never
//     un-deniable by a child policy.

// FilesystemRules is one policy row's filesystem rules. Masks are normalized
// relative paths with '/' separators: `**` matches any number of segments
// (including none), `*`/`?`/`[...]` match within one segment (path.Match).
type FilesystemRules struct {
	Read  []string `json:"read,omitempty"`
	Write []string `json:"write,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// EffectiveFilesystem is the run's frozen filesystem policy (§6.1). Read and
// Write are ANDed layers — a path must match every layer (each layer matches
// if any of its masks matches). Deny is the ORed union of every level.
// The zero value allows everything: an install with no filesystem rules
// behaves exactly like the pre-filesystem model.
type EffectiveFilesystem struct {
	Read  [][]string `json:"read,omitempty"`
	Write [][]string `json:"write,omitempty"`
	Deny  []string   `json:"deny,omitempty"`
}

// Unrestricted reports whether the policy constrains nothing — useful for
// diagnostics and for skipping checks on the hot path.
func (e *EffectiveFilesystem) Unrestricted() bool {
	return e == nil || (len(e.Read) == 0 && len(e.Write) == 0 && len(e.Deny) == 0)
}

// NormalizeFSPath renders a model/tool-supplied path as the canonical
// workspace-relative form ("a/b/c"). It strips the /workspace prefix the
// tools accept, cleans "./" and "//", and rejects absolute paths, ".."
// segments and empty results — the same discipline the file tools apply
// before containment checks (kernel/agent/filetools.go).
func NormalizeFSPath(p string) (string, error) {
	rel := strings.TrimSpace(p)
	if rel == "" {
		return "", errors.New("path is required")
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(rel, "/workspace/") {
		rel = strings.TrimPrefix(rel, "/workspace/")
	} else if rel == "/workspace" {
		return "", errors.New("path is required")
	} else if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("path %q is absolute; use workspace-relative paths", p)
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q escapes the workspace", p)
	}
	return clean, nil
}

// MatchFSPath reports whether a normalized path matches one mask. `**` is
// special only as a whole segment and spans zero or more segments —
// `src/**` matches `src` itself and everything below it, `**/x` matches `x`
// at any depth. Every other segment goes through path.Match.
func MatchFSPath(mask, cleanPath string) bool {
	return matchSegments(strings.Split(mask, "/"), strings.Split(cleanPath, "/"))
}

func matchSegments(mask, segs []string) bool {
	if len(mask) == 0 {
		return len(segs) == 0
	}
	if mask[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegments(mask[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	if ok, err := path.Match(mask[0], segs[0]); err != nil || !ok {
		return false
	}
	return matchSegments(mask[1:], segs[1:])
}

// ValidateFilesystemMask rejects masks the matcher cannot honor: empty,
// absolute, backslashed or dotted-through ("..", "./"), with a per-segment
// pattern check so a bad glob fails at policy-save time, not mid-run.
func ValidateFilesystemMask(mask string) error {
	if strings.TrimSpace(mask) == "" {
		return errors.New("filesystem mask must not be empty")
	}
	if mask != strings.TrimSpace(mask) {
		return fmt.Errorf("filesystem mask %q has surrounding whitespace", mask)
	}
	if strings.Contains(mask, "\\") {
		return fmt.Errorf("filesystem mask %q must use '/' separators", mask)
	}
	if strings.HasPrefix(mask, "/") {
		return fmt.Errorf("filesystem mask %q must be workspace-relative", mask)
	}
	for _, seg := range strings.Split(mask, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("filesystem mask %q has an empty or dotted segment", mask)
		}
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("filesystem mask %q: %w", mask, err)
		}
	}
	return nil
}

// ValidateFilesystemRules checks every mask of every dimension.
func ValidateFilesystemRules(r *FilesystemRules) error {
	if r == nil {
		return nil
	}
	for dimension, masks := range map[string][]string{"read": r.Read, "write": r.Write, "deny": r.Deny} {
		for _, mask := range masks {
			if err := ValidateFilesystemMask(mask); err != nil {
				return fmt.Errorf("filesystem %s: %w", dimension, err)
			}
		}
	}
	return nil
}

// normalizeFilesystemRules trims and dedupes masks; a dimension that came in
// empty reads back as nil ("not set"), matching normalizePolicyLists.
func normalizeFilesystemRules(r *FilesystemRules) {
	if r == nil {
		return
	}
	r.Read = normalizeMasks(r.Read)
	r.Write = normalizeMasks(r.Write)
	r.Deny = normalizeMasks(r.Deny)
}

func normalizeMasks(masks []string) []string {
	if len(masks) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(masks))
	for _, m := range masks {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MergeFilesystem folds the filesystem rules of the contributing policy rows
// (already filtered to enabled ones, in effective-policy order) into the
// layered effective policy. A row with no rules contributes nothing —
// absence of restriction is not restriction (§3.5). Layers that match
// everything (a mask of `**`) narrow nothing and are dropped; deny masks
// are never dropped, `**` included.
func MergeFilesystem(rows []Policy) EffectiveFilesystem {
	var effective EffectiveFilesystem
	for _, row := range rows {
		if !row.Enabled || row.Filesystem == nil {
			continue
		}
		fs := row.Filesystem
		if layer := concreteMasks(fs.Read); len(layer) > 0 {
			effective.Read = append(effective.Read, layer)
		}
		if layer := concreteMasks(fs.Write); len(layer) > 0 {
			effective.Write = append(effective.Write, layer)
		}
		for _, mask := range fs.Deny {
			effective.Deny = append(effective.Deny, mask)
		}
	}
	effective.Deny = normalizeMasks(effective.Deny)
	return effective
}

// concreteMasks drops layers that cannot narrow anything: a layer matches a
// path when ANY mask matches, so a layer containing a bare `**` is a no-op
// for the intersection. Returns nil for such layers.
func concreteMasks(masks []string) []string {
	out := make([]string, 0, len(masks))
	for _, m := range masks {
		if m == "**" {
			return nil
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AllowRead reports whether a read operation on the normalized path is
// permitted: deny wins, then every read layer must match. A nil/empty policy
// allows (§3.5).
func (e *EffectiveFilesystem) AllowRead(cleanPath string) bool {
	return e.allow(cleanPath, e.Read)
}

// AllowWrite reports whether a mutation (create, overwrite, truncate,
// rename, delete, mkdir — delete is write, §3.2) on the normalized path is
// permitted. write does not imply read: the two are separate operations.
func (e *EffectiveFilesystem) AllowWrite(cleanPath string) bool {
	return e.allow(cleanPath, e.Write)
}

func (e *EffectiveFilesystem) allow(cleanPath string, layers [][]string) bool {
	if e == nil {
		return true
	}
	for _, mask := range e.Deny {
		if MatchFSPath(mask, cleanPath) {
			return false
		}
	}
	for _, layer := range layers {
		matched := false
		for _, mask := range layer {
			if MatchFSPath(mask, cleanPath) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// Version fingerprints the policy snapshot a run was authorized with
// (§6.3): stable across mask order/duplicates, sensitive to any rule change.
func (e *EffectiveFilesystem) Version() string {
	canonical := EffectiveFilesystem{
		Read:  canonicalLayers(e.Read),
		Write: canonicalLayers(e.Write),
		Deny:  canonicalMasks(e.Deny),
	}
	encoded, _ := json.Marshal(canonical)
	sum := sha256.Sum256(encoded)
	return "fs-" + hex.EncodeToString(sum[:6])
}

func canonicalLayers(layers [][]string) [][]string {
	if len(layers) == 0 {
		return nil
	}
	out := make([][]string, 0, len(layers))
	for _, layer := range layers {
		out = append(out, canonicalMasks(layer))
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Join(out[i], "\x00") < strings.Join(out[j], "\x00")
	})
	return out
}

func canonicalMasks(masks []string) []string {
	if len(masks) == 0 {
		return nil
	}
	out := append([]string{}, masks...)
	sort.Strings(out)
	return out
}
