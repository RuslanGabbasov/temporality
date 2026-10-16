package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
)

func newSkillImportServer(t *testing.T) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\nwritertoken:writer:writer:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterSkillImportRoutes(mux, gate)
	return httptest.NewServer(gate.Authenticate(mux))
}

const importSampleSKILL = `---
name: pdf-report
description: Generate polished PDF reports from markdown drafts
license: MIT
allowed-tools:
  - run_command
  - artifact_read
version: 0.3.1
---

# PDF report generation

1. Read the draft.
2. Render the PDF.
3. Verify page breaks.
`

// postSkillImport sends a multipart body with the given parts (name → content).
// withPaths=true also sends the parallel `paths` field the browser folder
// picker needs (Go's multipart reader strips directories from filenames).
func postSkillImport(t *testing.T, server *httptest.Server, token string, parts map[string][]byte) (int, map[string]any) {
	return postSkillImportOrdered(t, server, token, parts, nil)
}

func postSkillImportOrdered(t *testing.T, server *httptest.Server, token string, parts map[string][]byte, paths []string) (int, map[string]any) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	// Map iteration order is random: sort the names so the optional paths
	// array lines up with the files array deterministically.
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if paths != nil {
			require.NoError(t, writer.WriteField("paths", name))
		}
		part, err := writer.CreateFormFile("files", name)
		require.NoError(t, err)
		_, err = part.Write(parts[name])
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/skills/import", body)
	require.NoError(t, err)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	parsed := map[string]any{}
	if strings.Contains(string(raw), "{") {
		require.NoError(t, json.Unmarshal(raw, &parsed))
	}
	return response.StatusCode, parsed
}

// zipBytes builds an in-memory zip archive.
func zipBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	writer := zip.NewWriter(buf)
	for name, content := range files {
		entry, err := writer.Create(name)
		require.NoError(t, err)
		_, err = entry.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return buf.Bytes()
}

// tarGzBytes builds an in-memory tar.gz archive.
func tarGzBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	gz := gzip.NewWriter(buf)
	tarWriter := tar.NewWriter(gz)
	for name, content := range files {
		require.NoError(t, tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}))
		_, err := tarWriter.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, tarWriter.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func TestSkillImportZipWithResources(t *testing.T) {
	server := newSkillImportServer(t)
	defer server.Close()

	archive := zipBytes(t, map[string][]byte{
		"pdf-report/SKILL.md":            []byte(importSampleSKILL),
		"pdf-report/references/style.md": []byte("# Style rules\nUse serif fonts.\n"),
		"pdf-report/assets/logo.png":     []byte{0x89, 'P', 'N', 'G', 0x00},
	})
	status, payload := postSkillImport(t, server, "writertoken", map[string][]byte{"skill.zip": archive})
	require.Equal(t, 200, status)
	require.Equal(t, "pdf-report", payload["name"])
	require.Equal(t, "Generate polished PDF reports from markdown drafts", payload["description"])
	require.Equal(t, "0.3.1", payload["version"])

	// Body survives, appendix carries the text resource and lists the binary.
	markdown := payload["markdown"].(string)
	require.Contains(t, markdown, "# PDF report generation")
	require.Contains(t, markdown, "### references/style.md")
	require.Contains(t, markdown, "assets/logo.png")
	require.Contains(t, markdown, "Not inlined")

	// allowed-tools → manifest tools; license becomes a warning.
	manifestMap, ok := payload["manifest"].(map[string]any)
	require.True(t, ok)
	toolsRaw, _ := manifestMap["tools"].([]any)
	tools := make([]string, 0, len(toolsRaw))
	for _, item := range toolsRaw {
		tools = append(tools, item.(string))
	}
	require.Equal(t, []string{"run_command", "artifact_read"}, tools)
	warnings, _ := payload["warnings"].([]any)
	require.NotEmpty(t, warnings)
}

func TestSkillImportTarGzWrappedFolder(t *testing.T) {
	server := newSkillImportServer(t)
	defer server.Close()

	archive := tarGzBytes(t, map[string][]byte{
		"review-kit/SKILL.md": []byte("---\nname: review-kit\ndescription: Structured code review\n---\nDo the review.\n"),
	})
	status, payload := postSkillImport(t, server, "writertoken", map[string][]byte{"kit.tar.gz": archive})
	require.Equal(t, 200, status)
	require.Equal(t, "review-kit", payload["name"])
	require.Contains(t, payload["markdown"], "Do the review.")
}

func TestSkillImportFolderUploadMultipart(t *testing.T) {
	server := newSkillImportServer(t)
	defer server.Close()

	// Folder pick keeps relative paths via the parallel `paths` field.
	status, payload := postSkillImportOrdered(t, server, "writertoken", map[string][]byte{
		"deploy-skill/SKILL.md":       []byte("---\nname: deploy-skill\ndescription: Deploy the service\n---\nRoll it out.\n"),
		"deploy-skill/scripts/pre.sh": []byte("#!/bin/sh\nset -e\n"),
	}, []string{"deploy-skill/SKILL.md", "deploy-skill/scripts/pre.sh"})
	require.Equal(t, 200, status)
	require.Equal(t, "deploy-skill", payload["name"])
	require.Contains(t, payload["markdown"].(string), "### scripts/pre.sh")
}

func TestSkillImportBareSkillMD(t *testing.T) {
	server := newSkillImportServer(t)
	defer server.Close()

	status, payload := postSkillImport(t, server, "writertoken", map[string][]byte{
		"SKILL.md": []byte("---\nname: bare\ndescription: Just instructions\n---\nStep one.\n"),
	})
	require.Equal(t, 200, status)
	require.Equal(t, "bare", payload["name"])
	require.Equal(t, "1.0.0", payload["version"])
	require.NotContains(t, payload["markdown"], "Bundled files")
}

func TestSkillImportErrors(t *testing.T) {
	server := newSkillImportServer(t)
	defer server.Close()

	// No SKILL.md at all.
	status, payload := postSkillImport(t, server, "writertoken", map[string][]byte{
		"README.md": []byte("no skill here"),
	})
	require.Equal(t, 422, status)
	require.Contains(t, payload["error"], "SKILL.md")

	// Frontmatter present but name missing.
	status, payload = postSkillImport(t, server, "writertoken", map[string][]byte{
		"SKILL.md": []byte("---\ndescription: nameless\n---\nBody.\n"),
	})
	require.Equal(t, 422, status)
	require.Contains(t, payload["error"], "name and description")

	// Broken YAML.
	status, payload = postSkillImport(t, server, "writertoken", map[string][]byte{
		"SKILL.md": []byte("---\nname: [unclosed\n---\nBody.\n"),
	})
	require.Equal(t, 422, status)

	// Reader role is refused.
	status, _ = postSkillImport(t, server, "readertoken", map[string][]byte{
		"SKILL.md": []byte("---\nname: x\ndescription: y\n---\n"),
	})
	require.Equal(t, 403, status)

	// No files field.
	status, _ = postSkillImport(t, server, "writertoken", map[string][]byte{})
	require.Equal(t, 422, status)
}

func TestSkillImportLimits(t *testing.T) {
	server := newSkillImportServer(t)
	defer server.Close()

	// A file beyond the per-file cap is rejected with 413.
	big := bytes.Repeat([]byte("x"), skillImportMaxFile+1)
	status, _ := postSkillImport(t, server, "writertoken", map[string][]byte{
		"SKILL.md": []byte("---\nname: big\ndescription: y\n---\n"),
		"blob.bin": big,
	})
	require.Equal(t, 413, status)
}

func TestSkillImportFrontmatterParsing(t *testing.T) {
	// CRLF line endings and a BOM must not break frontmatter detection.
	raw := "\xEF\xBB\xBF---\r\nname: crlf\r\ndescription: windows author\r\n---\r\nBody.\r\n"
	front, body, err := parseSkillFrontmatter(raw)
	require.NoError(t, err)
	require.Equal(t, "crlf", front.Name)
	require.Equal(t, "windows author", front.Description)
	require.Contains(t, body, "Body.")

	// Missing closing fence is a format error.
	_, _, err = parseSkillFrontmatter("---\nname: x\ndescription: y\n")
	require.ErrorContains(t, err, "closed")

	// No frontmatter at all.
	_, _, err = parseSkillFrontmatter("# Just markdown\n")
	require.ErrorContains(t, err, "frontmatter")
}

func TestSkillImportZipSlipSanitized(t *testing.T) {
	// Archive paths with traversal hops are normalized, never crash the parser.
	files := []skillImportFile{{Path: cleanRelPath("../../etc/passwd"), Data: []byte("x")}}
	require.Equal(t, "etc/passwd", files[0].Path)

	archive := zipBytes(t, map[string][]byte{
		"../outside/SKILL.md": []byte("---\nname: escaped\ndescription: traversal\n---\n"),
	})
	status, payload := postSkillImport(t, server2(t), "writertoken", map[string][]byte{"a.zip": archive})
	require.Equal(t, 200, status)
	require.Equal(t, "escaped", payload["name"])
}

func server2(t *testing.T) *httptest.Server {
	t.Helper()
	server := newSkillImportServer(t)
	t.Cleanup(server.Close)
	return server
}
