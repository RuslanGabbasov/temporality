package sandbox

// FilesystemPolicy is the runner-side wire mirror of workspace.
// EffectiveFilesystem (docs/filesystem-policy.md §6.1): the same JSON shape,
// zero conversion at the launcher boundary. The runner deliberately does not
// import the storage package — policy SEMANTICS live in workspace (glob
// matching, restrictive merge); the runner only carries the snapshot to the
// execution boundary and hands it to the landlock launcher.
type FilesystemPolicy struct {
	Read  [][]string `json:"read,omitempty"`
	Write [][]string `json:"write,omitempty"`
	Deny  []string   `json:"deny,omitempty"`
}

// Unrestricted reports whether the snapshot constrains anything — the same
// contract as workspace.EffectiveFilesystem.Unrestricted.
func (p *FilesystemPolicy) Unrestricted() bool {
	return p == nil || (len(p.Read) == 0 && len(p.Write) == 0 && len(p.Deny) == 0)
}
