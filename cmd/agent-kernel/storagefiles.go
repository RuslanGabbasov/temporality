package main

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/storage"
	"github.com/temporality-project/temporality/workspace"
)

// File-level browser routes for one project's storage areas (docs/sandbox-storage.md
// §7.1, phase 2): listing, preview/download, upload, delete. Reads are
// reader-gated; anything that mutates the persistent filesystem is
// writer-gated and audited — a delete in the workspace area is a destructive
// act on material agents and humans produced.

// registerStorageFileRoutes mounts the per-project file routes. They live
// under the same /v1/workspace/storage prefix as the read-only inventory.
func registerStorageFileRoutes(mux *http.ServeMux, gate *controlplane.Gate, visibleProjects func(*http.Request) (map[string][]string, error), root string, ws *workspace.Store) {
	// Central guards shared by every route: configured root, project
	// visibility, and the area/path pair resolved inside the area.
	authorize := func(w http.ResponseWriter, r *http.Request, writer bool) (project, area, rel string, ok bool) {
		if root == "" {
			writeError(w, 503, errors.New("storage file access requires KERNEL_SANDBOX_ROOT to be configured"))
			return "", "", "", false
		}
		project = r.PathValue("projectID")
		if err := storage.ValidProjectID(project); err != nil {
			writeError(w, 400, err)
			return "", "", "", false
		}
		role := controlplane.RoleReader
		if writer {
			role = controlplane.RoleWriter
		}
		if !gate.Allow(w, r, role, project) {
			return "", "", "", false
		}
		if visibleProjects != nil {
			visible, err := visibleProjects(r)
			if err != nil {
				writeError(w, 500, err)
				return "", "", "", false
			}
			if _, known := visible[project]; !known {
				writeError(w, 404, fmt.Errorf("project %q is not visible", project))
				return "", "", "", false
			}
		}
		area = r.URL.Query().Get("area")
		rel = r.URL.Query().Get("path")
		if err := storage.ValidArea(area); err != nil {
			writeError(w, 400, err)
			return "", "", "", false
		}
		return project, area, rel, true
	}

	// Directory listing (lazy tree loading).
	mux.HandleFunc("GET /v1/workspace/storage/projects/{projectID}/dir", func(w http.ResponseWriter, r *http.Request) {
		project, area, rel, ok := authorize(w, r, false)
		if !ok {
			return
		}
		entries, err := storage.ListDir(root, project, area, rel)
		if err != nil {
			writeError(w, httpStatusFor(err), err)
			return
		}
		writeJSON(w, 200, map[string]any{"entries": entries})
	})

	// File content: inline preview by default, attachment with ?download=1.
	// Directory requests redirect the caller to the listing route.
	mux.HandleFunc("GET /v1/workspace/storage/projects/{projectID}/file", func(w http.ResponseWriter, r *http.Request) {
		project, area, rel, ok := authorize(w, r, false)
		if !ok {
			return
		}
		if strings.TrimSpace(rel) == "" {
			writeError(w, 400, errors.New("path is required"))
			return
		}
		abs, size, modAt, isDir, err := storage.StatFile(root, project, area, rel)
		if err != nil {
			writeError(w, httpStatusFor(err), err)
			return
		}
		if isDir {
			writeError(w, 400, fmt.Errorf("%q is a directory — list it instead", rel))
			return
		}
		f, err := os.Open(abs)
		if err != nil {
			writeError(w, httpStatusFor(err), err)
			return
		}
		defer f.Close()
		name := path.Base(filepath.ToSlash(rel))
		contentType := detectContentType(name)
		disposition := "inline"
		if r.URL.Query().Get("download") == "1" {
			disposition = "attachment"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
		w.Header().Set("Last-Modified", modAt.UTC().Format(http.TimeFormat))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, modAt, f)
		_ = size
	})

	// Upload: raw body written to ?path= (parent dirs are created).
	// ?overwrite=1 replaces an existing file, otherwise 409.
	mux.HandleFunc("POST /v1/workspace/storage/projects/{projectID}/files", func(w http.ResponseWriter, r *http.Request) {
		project, area, rel, ok := authorize(w, r, true)
		if !ok {
			return
		}
		if strings.TrimSpace(rel) == "" {
			writeError(w, 422, errors.New("path is required"))
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, storage.MaxPreviewBytes*8))
		if err != nil {
			writeError(w, 400, err)
			return
		}
		if err := storage.StoreFile(root, project, area, rel, data, r.URL.Query().Get("overwrite") == "1"); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				writeError(w, 409, err)
				return
			}
			writeError(w, httpStatusFor(err), err)
			return
		}
		_ = auditStorageFile(r, ws, workspace.AuditArtifactUpdated, area, project+"/"+rel, map[string]any{"action": "upload", "bytes": len(data)})
		writeJSON(w, 201, map[string]any{"path": rel})
	})

	// Delete one file or directory (never the area root itself).
	mux.HandleFunc("DELETE /v1/workspace/storage/projects/{projectID}/file", func(w http.ResponseWriter, r *http.Request) {
		project, area, rel, ok := authorize(w, r, true)
		if !ok {
			return
		}
		if strings.TrimSpace(rel) == "" {
			writeError(w, 400, errors.New("path is required — deleting the whole area is the sweeper's job"))
			return
		}
		if err := storage.DeletePath(root, project, area, rel); err != nil {
			writeError(w, httpStatusFor(err), err)
			return
		}
		_ = auditStorageFile(r, ws, workspace.AuditArtifactDeleted, area, project+"/"+rel, map[string]any{"action": "delete"})
		writeJSON(w, 200, map[string]any{"deleted": rel})
	})
}

// auditStorageFile best-effort records a mutating storage-file operation; the
// response must not depend on the audit write.
func auditStorageFile(r *http.Request, store *workspace.Store, action, area, subject string, details map[string]any) error {
	if store == nil {
		return nil
	}
	details["area"] = area
	return store.RecordAccessAudit(r.Context(), requestUserID(r), action, "storage_file", subject, details)
}

// httpStatusFor maps filesystem errors onto HTTP: missing → 404, invalid
// paths → 400, everything else → 500.
func httpStatusFor(err error) int {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return 404
	case errors.Is(err, storage.ErrInvalidProject):
		return 400
	default:
		if strings.Contains(err.Error(), "escapes the") || strings.Contains(err.Error(), "not allowed") {
			return 400
		}
		return 500
	}
}

// detectContentType keeps browsers from executing uploaded content: the mime
// type is derived from the extension, unknown ones stay octet-stream (the
// nosniff header backs this up).
func detectContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".json":
		return "application/json"
	case ".txt", ".log", ".md", ".go", ".ts", ".tsx", ".js", ".py", ".sh", ".yaml", ".yml", ".toml", ".sql", ".css", ".html", ".csv":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
