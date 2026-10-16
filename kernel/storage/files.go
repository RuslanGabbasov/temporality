package storage

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// File-level access to one project's storage areas (the browser behind
// docs/sandbox-storage.md §7.1). The tree snapshot answers "how big is it";
// these helpers answer "what is in here" — directory listings, file reads,
// uploads and deletes — under the same strict path rules.

// Area roots inside the sandbox storage root (must match ProjectTreeSnapshot).
const (
	AreaWorkspace = "workspace"
	AreaCache     = "cache"
)

// ValidArea reports whether the area selector is one of the two known roots.
func ValidArea(area string) error {
	if area != AreaWorkspace && area != AreaCache {
		return fmt.Errorf("area must be %q or %q", AreaWorkspace, AreaCache)
	}
	return nil
}

// ResolveAreaPath maps a slash-separated relative path inside an area to an
// absolute host path, refusing everything that could escape: absolute paths,
// backslashes, "..", empty components, NUL bytes. An empty rel is the area
// root itself. The project id is validated first — it is a path component
// here, not just an identifier.
func ResolveAreaPath(root, project, area, rel string) (string, error) {
	if err := ValidProjectID(project); err != nil {
		return "", err
	}
	if err := ValidArea(area); err != nil {
		return "", err
	}
	clean := path.Clean(strings.TrimPrefix(filepath.ToSlash(rel), "/"))
	if clean == "." {
		clean = ""
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "\x00") {
		return "", fmt.Errorf("path %q escapes the %s area", rel, area)
	}
	// Absolute-looking paths and backslashes are refused outright, not
	// silently re-rooted — the browser should never guess what the caller meant.
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return "", fmt.Errorf("path %q is not a relative area path", rel)
	}
	base := filepath.Join(root, "projects", project)
	if area == AreaCache {
		base = filepath.Join(root, "cache", project)
	}
	return filepath.Join(base, filepath.FromSlash(clean)), nil
}

// FileEntry is one directory row: what the browser lists.
type FileEntry struct {
	Name    string     `json:"name"`
	Path    string     `json:"path"` // slash-separated, relative to the area root
	Dir     bool       `json:"dir"`
	Bytes   int64      `json:"bytes"`
	ModAt   *time.Time `json:"mod_at,omitempty"`
	Entries int        `json:"entries,omitempty"` // directories: child count
}

// ListDir lists one directory inside an area, directories first, both groups
// alphabetical — the shape a Finder-style tree expects for lazy loading.
func ListDir(root, project, area, rel string) ([]FileEntry, error) {
	dir, err := ResolveAreaPath(root, project, area, rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	result := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		childRel := strings.TrimPrefix(path.Join(cleanRel(rel), name), "/")
		info, err := entry.Info()
		if err != nil {
			continue // raced with a delete — skip instead of failing the listing
		}
		item := FileEntry{Name: name, Path: childRel, Dir: entry.IsDir(), ModAt: timePtr(info.ModTime())}
		if entry.IsDir() {
			if children, err := os.ReadDir(filepath.Join(dir, name)); err == nil {
				item.Entries = len(children)
			}
		} else {
			item.Bytes = info.Size()
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Dir != result[j].Dir {
			return result[i].Dir
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// StatFile resolves one file (or directory) for the preview/download/delete
// surface. exists=false reports a clean miss, other failures return errors.
func StatFile(root, project, area, rel string) (abs string, size int64, modAt time.Time, isDir bool, err error) {
	abs, err = ResolveAreaPath(root, project, area, rel)
	if err != nil {
		return "", 0, time.Time{}, false, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", 0, time.Time{}, false, err
	}
	return abs, info.Size(), info.ModTime(), info.IsDir(), nil
}

// DeletePath removes one file or directory inside an area. Refuses the area
// root itself — "wipe the whole workspace" is the sweeper's job with its own
// rules, not a browser misclick.
func DeletePath(root, project, area, rel string) error {
	if strings.TrimSpace(rel) == "" || path.Clean(filepath.ToSlash(rel)) == "." {
		return fmt.Errorf("deleting the whole %s area is not allowed from the browser", area)
	}
	abs, err := ResolveAreaPath(root, project, area, rel)
	if err != nil {
		return err
	}
	if abs == filepath.Clean(root) {
		return fmt.Errorf("refusing to delete the storage root")
	}
	return os.RemoveAll(abs)
}

// MaxPreviewBytes caps inline text preview reads; bigger files must be
// downloaded, not piped through JSON.
const MaxPreviewBytes = 512 << 10

// ReadPreview returns up to MaxPreviewBytes of a file as text-ish bytes plus
// whether the content was truncated.
func ReadPreview(abs string, size int64) ([]byte, bool, error) {
	limit := int64(MaxPreviewBytes)
	truncated := size > limit
	if truncated {
		size = limit
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data := make([]byte, size)
	read, err := f.Read(data)
	if err != nil && read == 0 {
		return nil, truncated, err
	}
	return data[:read], truncated, nil
}

// StoreFile writes an uploaded file under rel (parent directories are
// created), refusing to clobber nothing silently: overwrite is explicit via
// the caller passing overwrite=true.
func StoreFile(root, project, area, rel string, data []byte, overwrite bool) error {
	if strings.TrimSpace(rel) == "" {
		return fmt.Errorf("file path is required")
	}
	abs, err := ResolveAreaPath(root, project, area, rel)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err == nil && !overwrite {
		return fmt.Errorf("%s already exists", rel)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, data, 0o644)
}

func cleanRel(rel string) string {
	clean := path.Clean(strings.TrimPrefix(filepath.ToSlash(rel), "/"))
	if clean == "." {
		return ""
	}
	return clean
}

func timePtr(t time.Time) *time.Time {
	value := t
	return &value
}
