// Package landlock compiles a run's frozen filesystem policy (docs/
// filesystem-policy.md §5.3) into Landlock path-beneath rules. Landlock can
// only allow SUBTREES, while the policy language is glob masks — the
// compilation is therefore exact for subtree-shaped masks (`**`, `a/b/**`,
// the shape the ТЗ exemplifies and recommends) and CONSERVATIVE for
// name-pattern masks: existing files still get exact per-file rules, but
// shell creation rights appear only in subtree-uniform writable directories.
// A stricter OS layer never violates §5.1 — denials stay denials everywhere;
// any approximation is reported in Result.Notes so the operator sees the
// divergence instead of guessing (§5.4: one policy, one semantics).
package landlock

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/temporality-project/temporality/workspace"
)

// Access mirrors the Landlock filesystem access bits (ABI 1 unless noted).
type Access uint64

const (
	Execute    Access = 1 << 0
	WriteFile  Access = 1 << 1
	ReadFile   Access = 1 << 2
	ReadDir    Access = 1 << 3
	RemoveDir  Access = 1 << 4
	RemoveFile Access = 1 << 5
	MakeChar   Access = 1 << 6
	MakeDir    Access = 1 << 7
	MakeReg    Access = 1 << 8
	MakeSock   Access = 1 << 9
	MakeFifo   Access = 1 << 10
	MakeBlock  Access = 1 << 11
	MakeSym    Access = 1 << 12
	Refer      Access = 1 << 13 // ABI 3: cross-directory rename/link
	Truncate   Access = 1 << 14 // ABI 2
)

// ReadOps/WriteOps/FullOps are the rule bundles the compiler hands out.
// Execute is deliberately not part of the policy model (§3.2) and stays
// unhandled by the ruleset: tools must run; the policy governs read/write.
const (
	ReadOps  = ReadFile | ReadDir
	WriteOps = WriteFile | MakeReg | MakeDir | MakeChar | MakeFifo | MakeSock |
		MakeBlock | MakeSym | RemoveFile | RemoveDir | Truncate | Refer
	FullOps = ReadOps | WriteOps
)

// Rule is one path-beneath grant: Access is allowed on Path and everything
// beneath it.
type Rule struct {
	Path   string
	Access Access
}

// Result is the compiled ruleset plus the honest account of its limits.
type Result struct {
	Rules []Rule
	// Notes names the policy shapes the OS layer approximated (if any).
	Notes []string
}

// Compile walks workspaceRoot and produces the ruleset for the policy. The
// walk itself is unrestricted — it runs in the launcher BEFORE
// restrict_self — so it can see denied entries and skip them exactly.
func Compile(workspaceRoot string, policy *workspace.EffectiveFilesystem) Result {
	if policy.Unrestricted() {
		return Result{Rules: []Rule{{Path: workspaceRoot, Access: FullOps}}}
	}
	result := Result{Notes: approximatedMasks(policy)}
	var rules []Rule
	var walk func(rel, abs string, readCovered bool)
	walk = func(rel, abs string, readCovered bool) {
		entries, err := os.ReadDir(abs)
		if err != nil {
			return
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			childAbs := filepath.Join(abs, name)
			info, err := os.Lstat(childAbs)
			if err != nil {
				continue
			}
			if info.IsDir() {
				compileDir(policy, childRel, childAbs, readCovered, &rules, walk)
				continue
			}
			// Exact per-file grant: reads only when an ancestor does not
			// already cover them (rules UNION — no need to repeat).
			access := Access(0)
			if !readCovered && policy.AllowRead(childRel) {
				access |= ReadFile
			}
			if policy.AllowWrite(childRel) {
				access |= WriteFile | Truncate
			}
			if access != 0 {
				rules = append(rules, Rule{Path: childAbs, Access: access})
			}
		}
	}
	compileDir(policy, "", workspaceRoot, false, &rules, walk)
	result.Rules = rules
	return result
}

// compileDir decides between the subtree fast path (one rule for the whole
// directory) and per-entry refinement. uniformWrite already implies
// uniformRead (write ⊆ read), so a write-uniform directory gets FullOps.
// Landlock rules are pure allows — they cannot subtract a denied subarea —
// so creation rights appear only on subtrees with no deny beneath: a tainted
// parent keeps ReadDir while its clean children get FullOps. That is the
// maximal expressiveness of the mechanism, not a shortcut.
func compileDir(policy *workspace.EffectiveFilesystem, rel, abs string, readCovered bool, rules *[]Rule, walk func(rel, abs string, readCovered bool)) {
	switch {
	case uniformWrite(policy, rel):
		*rules = append(*rules, Rule{Path: abs, Access: FullOps})
	case readCovered || uniformRead(policy, rel):
		// Reads beneath are covered — by this subtree's own ReadOps rule or by
		// an ancestor's; the walk only places write refinements below.
		if !readCovered {
			*rules = append(*rules, Rule{Path: abs, Access: ReadOps})
		}
		walk(rel, abs, true)
	default:
		// Partially readable (or denied) subtree: listing only when the policy
		// itself allows reading this path (listing is a read of structure,
		// §3.2), exact per-entry rules below.
		if policy.AllowRead(rel) {
			*rules = append(*rules, Rule{Path: abs, Access: ReadDir})
		}
		walk(rel, abs, false)
	}
}

// uniformRead reports whether EVERY hypothetical path beneath rel (rel
// itself included) is readable under the policy. Syntactic: some read layer
// covers the subtree wholesale with a `prefix/**` mask, and no deny mask can
// bite anywhere beneath.
func uniformRead(policy *workspace.EffectiveFilesystem, rel string) bool {
	return layersCover(policy.Read, rel) && !denyActive(policy.Deny, rel)
}

// uniformWrite is uniformRead plus wholesale write coverage — the only
// directories where the OS layer may hand out creation rights: a name-pattern
// mask cannot stop `> dir/evil.sh`, so creation appears solely where ALL
// possible names are writable (conservative by design, see the package doc).
func uniformWrite(policy *workspace.EffectiveFilesystem, rel string) bool {
	return uniformRead(policy, rel) && layersCover(policy.Write, rel)
}

// layersCover reports whether every layer has a subtree mask covering rel
// wholesale (`**` or an ancestor-or-equal `prefix/**`).
func layersCover(layers [][]string, rel string) bool {
	for _, layer := range layers {
		covered := false
		for _, mask := range layer {
			if coversBeneath(mask, rel) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// coversBeneath reports whether the mask matches EVERY path beneath rel.
// Only subtree masks can: `**` and `prefix/**` with a literal prefix that is
// rel itself or one of its ancestors.
func coversBeneath(mask, rel string) bool {
	prefix, ok := subtreePrefix(mask)
	if !ok {
		return false
	}
	if prefix == "" {
		return true // `**`
	}
	return rel == prefix || strings.HasPrefix(rel, prefix+"/")
}

// subtreePrefix extracts the literal directory prefix of a subtree mask
// (`**` or `a/b/**`); ok=false for any other shape.
func subtreePrefix(mask string) (string, bool) {
	if mask == "**" {
		return "", true
	}
	prefix, ok := strings.CutSuffix(mask, "/**")
	if !ok {
		return "", false
	}
	for _, seg := range strings.Split(prefix, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
		if strings.ContainsAny(seg, "*?[") {
			return "", false
		}
	}
	return prefix, true
}

// denyActive reports whether some deny mask could hit ANY hypothetical path
// beneath rel — subtree denies decide by prefix overlap (rel="" is the root,
// where everything is active), name-pattern denies are conservatively always
// active (they could match a future name).
func denyActive(deny []string, rel string) bool {
	for _, mask := range deny {
		if prefix, ok := subtreePrefix(mask); ok {
			if rel == "" || prefix == "" || rel == prefix ||
				strings.HasPrefix(rel, prefix+"/") || strings.HasPrefix(prefix, rel+"/") {
				return true
			}
			continue
		}
		return true // name-pattern deny: could match a name beneath anywhere
	}
	return false
}

// approximatedMasks reports the policy masks the OS layer cannot express
// exactly, so the launcher can log the divergence once per run.
func approximatedMasks(policy *workspace.EffectiveFilesystem) []string {
	seen := map[string]bool{}
	var notes []string
	add := func(kind, mask string) {
		if _, ok := subtreePrefix(mask); ok {
			return
		}
		if seen[kind+mask] {
			return
		}
		seen[kind+mask] = true
		notes = append(notes, kind+" mask "+mask+" is not a subtree area: the OS layer enforces existing files exactly and withholds shell creation outside subtree-shaped write areas")
	}
	for _, layers := range [][][]string{policy.Read, policy.Write} {
		for _, layer := range layers {
			for _, mask := range layer {
				add("allow", mask)
			}
		}
	}
	for _, mask := range policy.Deny {
		add("deny", mask)
	}
	sort.Strings(notes)
	return notes
}
