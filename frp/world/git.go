package world

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Minimal read-only Git metadata reader. It intentionally shells out to
// nothing: HEAD, refs, config, the index, and loose objects are parsed in
// pure Go so git.read observations stay deterministic and side-effect free.
// Repositories that rely on packed objects are reported as incomplete rather
// than misread.

var (
	ErrNotAGitRepository = errors.New("not a git repository")
	ErrGitIndexVersion   = errors.New("unsupported git index version")
	ErrPackedObject      = errors.New("git object is packed")
)

type Remote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Commit struct {
	SHA     string   `json:"sha"`
	Tree    string   `json:"tree,omitempty"`
	Parents []string `json:"parents,omitempty"`
	Author  string   `json:"author,omitempty"`
	Summary string   `json:"summary,omitempty"`
}

type Status struct {
	Branch       string   `json:"branch"`
	Detached     bool     `json:"detached"`
	Head         string   `json:"head,omitempty"`
	HeadReadable bool     `json:"head_readable"`
	Remotes      []Remote `json:"remotes"`
	Staged       int      `json:"staged"`
	Untracked    int      `json:"untracked"`
	Incomplete   string   `json:"incomplete,omitempty"`
}

// GitDir resolves the .git directory for a worktree path, including worktrees
// that point at a gitdir file.
func GitDir(root string) (string, error) {
	dotGit := filepath.Join(root, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", ErrNotAGitRepository
	}
	if info.IsDir() {
		return dotGit, nil
	}
	raw, err := os.ReadFile(dotGit)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "gitdir:") {
		return "", ErrNotAGitRepository
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(root, gitdir)
	}
	if _, err = os.Stat(filepath.Join(gitdir, "HEAD")); err != nil {
		return "", ErrNotAGitRepository
	}
	return gitdir, nil
}

// ReadHead resolves HEAD to a branch name and commit SHA.
func ReadHead(gitDir string) (branch string, commit string, detached bool, err error) {
	raw, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", "", false, err
	}
	head := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(head, "ref: ") {
		return "", validateSHA(head), true, nil
	}
	ref := strings.TrimSpace(strings.TrimPrefix(head, "ref: "))
	branch = strings.TrimPrefix(ref, "refs/heads/")
	commit, err = readRef(gitDir, ref)
	if err != nil {
		return branch, "", detached, err
	}
	return branch, commit, false, nil
}

func readRef(gitDir, ref string) (string, error) {
	if raw, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref))); err == nil {
		return strings.TrimSpace(string(raw)), nil
	}
	packed, err := os.ReadFile(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return "", fmt.Errorf("ref %s not found", ref)
	}
	for _, line := range strings.Split(string(packed), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 && parts[1] == ref {
			return parts[0], nil
		}
	}
	return "", fmt.Errorf("ref %s not found", ref)
}

// ReadRemotes parses remote URLs out of the repository config.
func ReadRemotes(gitDir string) ([]Remote, error) {
	raw, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil {
		if os.IsNotExist(err) {
			return []Remote{}, nil
		}
		return nil, err
	}
	remotes := []Remote{}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if strings.HasPrefix(section, "remote ") {
				name := strings.Trim(strings.TrimSpace(strings.TrimPrefix(section, "remote ")), `"`)
				remotes = append(remotes, Remote{Name: name})
			}
			continue
		}
		if strings.HasPrefix(section, "remote ") && len(remotes) > 0 {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			if strings.TrimSpace(parts[0]) == "url" {
				remotes[len(remotes)-1].URL = strings.TrimSpace(parts[1])
			}
		}
	}
	filtered := remotes[:0]
	for _, remote := range remotes {
		if remote.URL != "" {
			filtered = append(filtered, remote)
		}
	}
	return filtered, nil
}

// ReadObject reads a loose object and returns its type and content.
func ReadObject(gitDir, sha string) (string, []byte, error) {
	if len(sha) != 40 {
		return "", nil, fmt.Errorf("invalid object id %q", sha)
	}
	path := filepath.Join(gitDir, "objects", sha[:2], sha[2:])
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil, ErrPackedObject
	}
	reader, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = reader.Close() }()
	content, err := io.ReadAll(reader)
	if err != nil {
		return "", nil, err
	}
	headerEnd := bytes.IndexByte(content, 0)
	if headerEnd < 0 {
		return "", nil, errors.New("malformed git object header")
	}
	header := string(content[:headerEnd])
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return "", nil, errors.New("malformed git object header")
	}
	return parts[0], content[headerEnd+1:], nil
}

// ParseCommit decodes a commit object body.
func ParseCommit(content []byte) (Commit, error) {
	text := string(content)
	head, message, found := strings.Cut(text, "\n\n")
	if !found {
		head = text
	}
	commit := Commit{}
	summary := strings.TrimSpace(message)
	if idx := strings.IndexAny(summary, "\r\n"); idx >= 0 {
		summary = strings.TrimSpace(summary[:idx])
	}
	commit.Summary = summary
	for _, line := range strings.Split(head, "\n") {
		if strings.HasPrefix(line, "tree ") {
			commit.Tree = strings.TrimSpace(strings.TrimPrefix(line, "tree "))
		} else if strings.HasPrefix(line, "parent ") {
			commit.Parents = append(commit.Parents, strings.TrimSpace(strings.TrimPrefix(line, "parent ")))
		} else if strings.HasPrefix(line, "author ") {
			commit.Author = strings.TrimSpace(strings.TrimPrefix(line, "author "))
		}
	}
	return commit, nil
}

// ReadCommit loads and decodes a single commit by SHA.
func ReadCommit(gitDir, sha string) (Commit, error) {
	kind, content, err := ReadObject(gitDir, sha)
	if err != nil {
		return Commit{}, err
	}
	if kind != "commit" {
		return Commit{}, fmt.Errorf("object %s is a %s, not a commit", sha, kind)
	}
	commit, err := ParseCommit(content)
	if err != nil {
		return Commit{}, err
	}
	commit.SHA = sha
	return commit, nil
}

// WalkCommits walks first-parent history over loose objects up to limit.
// The walk stops silently at missing (packed) objects and reports how far it
// got so callers can mark the observation incomplete.
func WalkCommits(gitDir, head string, limit int) ([]Commit, bool, error) {
	commits := make([]Commit, 0, limit)
	complete := true
	current := head
	for len(commits) < limit {
		commit, err := ReadCommit(gitDir, current)
		if err != nil {
			if errors.Is(err, ErrPackedObject) {
				complete = false
				break
			}
			return commits, complete, err
		}
		commits = append(commits, commit)
		if len(commit.Parents) == 0 {
			break
		}
		current = commit.Parents[0]
	}
	return commits, complete, nil
}

// IndexEntry is one staged path from .git/index.
type IndexEntry struct {
	Path string `json:"path"`
	SHA  string `json:"sha"`
}

// ReadIndex parses .git/index (version 2) into staged entries.
func ReadIndex(gitDir string) ([]IndexEntry, error) {
	raw, err := os.ReadFile(filepath.Join(gitDir, "index"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(raw) < 12 || string(raw[:4]) != "DIRC" {
		return nil, errors.New("malformed git index header")
	}
	version := int(binary.BigEndian.Uint32(raw[4:8]))
	if version != 2 {
		return nil, fmt.Errorf("%w: %d", ErrGitIndexVersion, version)
	}
	count := int(binary.BigEndian.Uint32(raw[8:12]))
	entries := make([]IndexEntry, 0, count)
	offset := 12
	for i := 0; i < count; i++ {
		if offset+62 > len(raw) {
			return nil, errors.New("truncated git index entry")
		}
		entryStart := offset
		sha := hex.EncodeToString(raw[entryStart+40 : entryStart+60])
		flags := binary.BigEndian.Uint16(raw[entryStart+60 : entryStart+62])
		nameLen := int(flags & 0x0fff)
		offset += 62
		var path []byte
		if nameLen < 0x0fff {
			if offset+nameLen > len(raw) {
				return nil, errors.New("truncated git index path")
			}
			path = raw[offset : offset+nameLen]
			offset += nameLen + 1
		} else {
			end := bytes.IndexByte(raw[offset:], 0)
			if end < 0 {
				return nil, errors.New("truncated git index path")
			}
			path = raw[offset : offset+end]
			offset += end + 1
		}
		entryLen := offset - entryStart
		if pad := (8 - entryLen%8) % 8; pad != 0 {
			offset += pad
		}
		entries = append(entries, IndexEntry{Path: string(path), SHA: sha})
	}
	return entries, nil
}

// CountUntracked walks the worktree and counts files absent from the index,
// bounded by limit; truncated reports whether the walk hit the bound.
func CountUntracked(root string, indexed map[string]struct{}, limit int) (count int, truncated bool) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if count >= limit {
			truncated = true
			return filepath.SkipAll
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if _, ok := indexed[rel]; !ok {
			count++
		}
		return nil
	})
	return count, truncated
}

// InspectRepository assembles the git_status observation for a worktree root.
func InspectRepository(root string, maxEntries int) (Status, error) {
	gitDir, err := GitDir(root)
	if err != nil {
		return Status{}, err
	}
	status := Status{Remotes: []Remote{}}
	branch, head, detached, err := ReadHead(gitDir)
	if err != nil {
		return Status{}, err
	}
	status.Branch = branch
	status.Detached = detached
	status.Head = head
	if head != "" {
		if _, _, readErr := ReadObject(gitDir, head); readErr == nil {
			status.HeadReadable = true
		}
	}
	status.Remotes, err = ReadRemotes(gitDir)
	if err != nil {
		return Status{}, err
	}
	sort.Slice(status.Remotes, func(i, j int) bool { return status.Remotes[i].Name < status.Remotes[j].Name })
	entries, err := ReadIndex(gitDir)
	if err != nil && !errors.Is(err, ErrGitIndexVersion) {
		return Status{}, err
	}
	if errors.Is(err, ErrGitIndexVersion) {
		status.Incomplete = err.Error()
	}
	indexed := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		indexed[entry.Path] = struct{}{}
	}
	status.Staged = len(entries)
	untracked, truncated := CountUntracked(root, indexed, maxEntries)
	status.Untracked = untracked
	if truncated {
		status.Incomplete = joinIncomplete(status.Incomplete, "untracked walk hit entry limit")
	}
	return status, nil
}

func joinIncomplete(left, right string) string {
	if left == "" {
		return right
	}
	return left + "; " + right
}

func validateSHA(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if len(candidate) != 40 {
		return ""
	}
	if _, err := hex.DecodeString(candidate); err != nil {
		return ""
	}
	return candidate
}
