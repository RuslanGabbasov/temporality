// Skill import from the Agent Skills open format (https://agentskills.io):
// a skill directory with SKILL.md (YAML frontmatter: name, description,
// optional license / allowed-tools / metadata) plus optional resource files.
// The UI offers two entry points — an archive (.zip / .tar.gz) or a folder
// picked in the browser (multipart entries with relative paths) — both land
// in POST /v1/workspace/skills/import. Parsing happens server-side: the
// kernel already owns YAML tooling for manifests, and client-side unzip
// would duplicate limits (zip bombs, path traversal) in a second language.
//
// The endpoint returns a draft (same shape as skills/draft), never persists
// anything: the user reviews the result in the skill editor and saves as usual.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/temporality-project/temporality/controlplane"
	"gopkg.in/yaml.v3"
)

const (
	skillImportMaxRequest  = 16 << 20  // multipart body cap (archives stay compressed in transit)
	skillImportMaxFiles    = 200       // entries after unpacking
	skillImportMaxFile     = 1 << 20   // per unpacked file
	skillImportMaxTotal    = 8 << 20   // unpacked total
	skillImportInlineMax   = 64 << 10  // a text file at most this big is inlined into the markdown appendix
	skillImportAppendixCap = 256 << 10 // total appendix budget for inlined files
)

// skillImportFile is one unpacked entry: slash-separated relative path + bytes.
type skillImportFile struct {
	Path string
	Data []byte
}

// skillFrontmatter is the subset of the agentskills.io SKILL.md header the
// importer understands. Unknown fields are reported as warnings, not errors —
// the format is open and producers add vendor fields.
type skillFrontmatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Version      string   `yaml:"version"`
	License      string   `yaml:"license"`
	AllowedTools []string `yaml:"allowed-tools"`
}

type skillImportDraft struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Version     string          `json:"version,omitempty"`
	Markdown    string          `json:"markdown"`
	Manifest    json.RawMessage `json:"manifest"`
	Warnings    []string        `json:"warnings,omitempty"`
}

// RegisterSkillImportRoutes mounts the agentskills.io import endpoint.
func RegisterSkillImportRoutes(mux *http.ServeMux, gate *controlplane.Gate) {
	mux.HandleFunc("POST /v1/workspace/skills/import", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, skillImportMaxRequest)
		files, err := skillImportReadMultipart(r)
		if err != nil {
			writeError(w, skillImportStatus(err), err)
			return
		}
		draft, err := skillImportParse(files)
		if err != nil {
			writeError(w, skillImportStatus(err), err)
			return
		}
		writeJSON(w, 200, draft)
	})
}

// skillImportStatus keeps transport errors (413/422) apart from parse errors
// (422) and internal ones (500) without importing controlplane here.
func skillImportStatus(err error) int {
	switch {
	case errors.Is(err, errSkillImportNoFile):
		return 422
	case errors.Is(err, errSkillImportLimits):
		return 413
	case errors.Is(err, errSkillImportFormat):
		return 422
	default:
		return 500
	}
}

var (
	errSkillImportLimits = errors.New("import exceeds limits")
	errSkillImportFormat = errors.New("invalid skill package")
	errSkillImportNoFile = errors.New("multipart field 'files' is required")
)

// skillImportReadMultipart collects `files` entries. An entry that is an
// archive by extension is unpacked in place; everything else keeps its
// filename as the entry path — unless the client sent a parallel `paths`
// field: Go's multipart reader applies filepath.Base to filenames, which
// strips the relative paths of folder uploads (webkitRelativePath), so the
// browser sends them separately and they win when the counts match.
func skillImportReadMultipart(r *http.Request) ([]skillImportFile, error) {
	if err := r.ParseMultipartForm(skillImportMaxRequest); err != nil {
		return nil, fmt.Errorf("%w: %s", errSkillImportLimits, err)
	}
	headers := r.MultipartForm.File["files"]
	if len(headers) == 0 {
		return nil, fmt.Errorf("%w", errSkillImportNoFile)
	}
	paths := r.MultipartForm.Value["paths"]
	out := make([]skillImportFile, 0, len(headers))
	total := 0
	for i, header := range headers {
		if len(out) >= skillImportMaxFiles {
			return nil, fmt.Errorf("%w: more than %d files", errSkillImportLimits, skillImportMaxFiles)
		}
		name := header.Filename
		if i < len(paths) && strings.TrimSpace(paths[i]) != "" {
			name = paths[i]
		}
		file, err := header.Open()
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(file, skillImportMaxRequest+1))
		file.Close()
		if err != nil {
			return nil, err
		}
		if len(raw) > skillImportMaxRequest {
			return nil, fmt.Errorf("%w: uploaded entry too large", errSkillImportLimits)
		}
		var unpacked []skillImportFile
		switch {
		case isZipName(name):
			unpacked, err = unzipSkillArchive(raw)
		case isTarName(name):
			unpacked, err = untarSkillArchive(raw)
		default:
			unpacked = []skillImportFile{{Path: cleanRelPath(name), Data: raw}}
		}
		if err != nil {
			return nil, err
		}
		for _, f := range unpacked {
			if len(f.Data) > skillImportMaxFile {
				return nil, fmt.Errorf("%w: %s larger than %d bytes", errSkillImportLimits, f.Path, skillImportMaxFile)
			}
			total += len(f.Data)
			if total > skillImportMaxTotal {
				return nil, fmt.Errorf("%w: unpacked total exceeds %d bytes", errSkillImportLimits, skillImportMaxTotal)
			}
			out = append(out, f)
			if len(out) >= skillImportMaxFiles {
				return nil, fmt.Errorf("%w: more than %d files", errSkillImportLimits, skillImportMaxFiles)
			}
		}
	}
	return out, nil
}

func isZipName(name string) bool {
	return strings.EqualFold(path.Ext(name), ".zip")
}

func isTarName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") || strings.HasSuffix(lower, ".tar")
}

func unzipSkillArchive(raw []byte) ([]skillImportFile, error) {
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("%w: bad zip archive: %s", errSkillImportFormat, err)
	}
	out := make([]skillImportFile, 0, len(reader.File))
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: bad zip entry %s: %s", errSkillImportFormat, entry.Name, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, skillImportMaxFile+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%w: bad zip entry %s: %s", errSkillImportFormat, entry.Name, err)
		}
		if len(data) > skillImportMaxFile {
			return nil, fmt.Errorf("%w: %s larger than %d bytes", errSkillImportLimits, entry.Name, skillImportMaxFile)
		}
		out = append(out, skillImportFile{Path: cleanRelPath(entry.Name), Data: data})
	}
	return out, nil
}

func untarSkillArchive(raw []byte) ([]skillImportFile, error) {
	var tarReader *tar.Reader
	// .tar.gz/.tgz arrive gzip-compressed (sniffed by magic, the plain .tar
	// case has no gzip header and reads as-is).
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("%w: bad gzip stream: %s", errSkillImportFormat, err)
		}
		tarReader = tar.NewReader(gz)
	} else {
		tarReader = tar.NewReader(bytes.NewReader(raw))
	}
	out := []skillImportFile{}
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: bad tar archive: %s", errSkillImportFormat, err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if header.Size > skillImportMaxFile {
			return nil, fmt.Errorf("%w: %s larger than %d bytes", errSkillImportLimits, header.Name, skillImportMaxFile)
		}
		data, err := io.ReadAll(io.LimitReader(tarReader, skillImportMaxFile+1))
		if err != nil {
			return nil, fmt.Errorf("%w: bad tar entry %s: %s", errSkillImportFormat, header.Name, err)
		}
		if len(data) > skillImportMaxFile {
			return nil, fmt.Errorf("%w: %s larger than %d bytes", errSkillImportLimits, header.Name, skillImportMaxFile)
		}
		out = append(out, skillImportFile{Path: cleanRelPath(header.Name), Data: data})
	}
	return out, nil
}

// cleanRelPath normalizes an entry path from an archive or folder upload to a
// slash-separated relative path. Absolute paths and `..` hops (zip-slip) are
// rejected — entries never touch disk, but a normalized namespace keeps the
// SKILL.md lookup and the appendix honest.
func cleanRelPath(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimLeft(name, "/")
	parts := make([]string, 0, 8)
	for _, part := range strings.Split(name, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "/")
}

// skillImportParse turns the file set into a draft: SKILL.md frontmatter
// becomes name/description (allowed-tools → manifest.tools), the body becomes
// the instructions, and resource files land in a size-capped appendix.
func skillImportParse(files []skillImportFile) (skillImportDraft, error) {
	var warnings []string
	// Shallowest SKILL.md wins: archives typically wrap one skill directory,
	// and folder uploads repeat the selected folder as the first segment.
	skillPath := ""
	skillIndex := -1
	for i, f := range files {
		base := path.Base(strings.ToLower(f.Path))
		if base == "skill.md" {
			if skillPath == "" || strings.Count(f.Path, "/") < strings.Count(skillPath, "/") {
				skillPath, skillIndex = f.Path, i
			}
		}
	}
	if skillIndex < 0 {
		return skillImportDraft{}, fmt.Errorf("%w: no SKILL.md found in the upload", errSkillImportFormat)
	}
	front, body, err := parseSkillFrontmatter(string(files[skillIndex].Data))
	if err != nil {
		return skillImportDraft{}, fmt.Errorf("%w: %s", errSkillImportFormat, err)
	}
	if strings.TrimSpace(front.Name) == "" || strings.TrimSpace(front.Description) == "" {
		return skillImportDraft{}, fmt.Errorf("%w: SKILL.md frontmatter requires both name and description", errSkillImportFormat)
	}
	if front.License != "" {
		warnings = append(warnings, fmt.Sprintf("license %q is not stored — Temporality tracks skill versions in its own registry", front.License))
	}

	manifest := map[string]any{}
	if len(front.AllowedTools) > 0 {
		manifest["tools"] = front.AllowedTools
		warnings = append(warnings, "allowed-tools mapped to manifest tools — verify the names exist in this workspace (builtin or MCP)")
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		return skillImportDraft{}, err
	}

	// Resource files: same directory or below the SKILL.md folder only — a
	// multi-skill bundle must not leak sibling skills into the appendix.
	skillDir := path.Dir(skillPath)
	if skillDir == "." {
		skillDir = ""
	}
	var inline []skillImportFile
	var listed []string
	for i, f := range files {
		if i == skillIndex {
			continue
		}
		if skillDir != "" && !strings.HasPrefix(f.Path+"/", skillDir+"/") {
			continue
		}
		if isTextResourceName(f.Path) && len(f.Data) <= skillImportInlineMax {
			inline = append(inline, f)
		} else {
			listed = append(listed, fmt.Sprintf("%s (%d bytes)", f.Path, len(f.Data)))
		}
	}
	markdown := strings.TrimSpace(body)
	if len(inline) > 0 || len(listed) > 0 {
		markdown += "\n\n## Bundled files\n"
		if skillDir != "" {
			markdown += fmt.Sprintf("\nImported from `%s/`; paths below are relative to the skill.\n", skillDir)
		}
		used := 0
		for _, f := range inline {
			rel := f.Path
			if skillDir != "" {
				rel = strings.TrimPrefix(f.Path, skillDir+"/")
			}
			used += len(f.Data)
			if used > skillImportAppendixCap {
				listed = append(listed, fmt.Sprintf("%s (%d bytes)", rel, len(f.Data)))
				continue
			}
			lang := resourceCodeLang(rel)
			markdown += fmt.Sprintf("\n### %s\n\n```%s\n%s\n```\n", rel, lang, strings.TrimRight(string(f.Data), "\n"))
		}
		if len(listed) > 0 {
			markdown += "\nNot inlined (binary or over the size cap):\n\n"
			for _, item := range listed {
				markdown += fmt.Sprintf("- `%s`\n", item)
			}
		}
	}

	version := strings.TrimSpace(front.Version)
	if version == "" {
		version = "1.0.0"
	}
	return skillImportDraft{
		Name:        strings.TrimSpace(front.Name),
		Description: strings.TrimSpace(front.Description),
		Version:     version,
		Markdown:    markdown,
		Manifest:    manifestRaw,
		Warnings:    warnings,
	}, nil
}

// parseSkillFrontmatter splits `---\nyaml\n---` headers from the markdown body.
func parseSkillFrontmatter(raw string) (skillFrontmatter, string, error) {
	var front skillFrontmatter
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	trimmed := strings.TrimLeft(normalized, "\n\uFEFF ")
	if !strings.HasPrefix(trimmed, "---") {
		return front, raw, errors.New("SKILL.md must start with a `---` YAML frontmatter block")
	}
	rest := trimmed[3:]
	if len(rest) > 0 && rest[0] != '\n' && rest[0] != ' ' {
		return front, raw, errors.New("SKILL.md must start with a `---` YAML frontmatter block")
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return front, raw, errors.New("SKILL.md frontmatter is not closed with a `---` line")
	}
	header := rest[:end]
	if err := yaml.Unmarshal([]byte(header), &front); err != nil {
		return front, raw, fmt.Errorf("frontmatter YAML: %s", err)
	}
	body := strings.TrimLeft(rest[end+4:], "\n")
	return front, body, nil
}

// isTextResourceName decides whether a bundled file can be inlined as text.
func isTextResourceName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".csv", ".sh", ".bash", ".js", ".ts", ".py", ".go", ".sql", ".html", ".css", ".xml", ".env", ".ini", ".cfg", ".conf":
		return true
	default:
		return strings.ToLower(path.Base(name)) == "dockerfile" || strings.ToLower(path.Base(name)) == "makefile"
	}
}

func resourceCodeLang(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".md":
		return "markdown"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".sh", ".bash", ".env":
		return "bash"
	case ".js":
		return "javascript"
	case ".ts":
		return "typescript"
	case ".py":
		return "python"
	case ".go":
		return "go"
	case ".sql":
		return "sql"
	case ".html":
		return "html"
	case ".css":
		return "css"
	case ".xml":
		return "xml"
	default:
		return ""
	}
}
