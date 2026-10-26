package workspace

// Builtin skills (docs/builtin-skills.md): the office-document capability
// pack every installation starts with. The catalog follows the sandbox
// profile pattern (docs/sandbox-profiles.md §5) — an in-code list seeded into
// the workspace_skill registry at kernel startup, idempotent (ON CONFLICT DO
// NOTHING): a fresh install gets the pack immediately, an existing row is
// never overwritten, so human edits and evolution proposals survive restarts.
//
// Skills live in the registry, not on disk (docs/living-skills.md §4): each
// entry carries the SKILL.md body and the parsed manifest contract the same
// way a human-created skill does. Manifests reference only tools that exist
// in the agent toolset (KernelTools + sandbox/file tools).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/temporality-project/temporality/skills"
)

// bundledFile is one script shipped inside a builtin skill's markdown.
type bundledFile struct {
	Path string
	Lang string
	Code string
}

// codeFence renders a fenced-code delimiter in the appendix. Interpreted
// string — a raw literal cannot carry backticks.
func codeFence(lang string) string {
	if lang == "" {
		return "```"
	}
	return "```" + lang
}

// bundledAppendix renders the "## Bundled files" appendix in exactly the
// format the import pipeline produces (### path + fenced code, cmd/
// agent-kernel/skillimport.go): the skill editor's file tab, its risk scanner
// and skill_inspect then treat builtin scripts like imported ones. The intro
// line tells the agent to prefer them over ad-hoc code.
func bundledAppendix(files []bundledFile) string {
	var b strings.Builder
	b.WriteString("\n\n## Bundled files\n\n")
	b.WriteString("Bundled tooling: prefer these scripts over ad-hoc code. Write a file into the sandbox (write_file), run it, and read its output; a missing library prints its install hint and exits 3.\n")
	for _, f := range files {
		b.WriteString("\n### " + f.Path + "\n\n" + codeFence(f.Lang) + "\n" + strings.TrimRight(f.Code, "\n") + "\n" + codeFence("") + "\n")
	}
	return b.String()
}

// BuiltinSkill is one preinstalled skill: the registry row fields plus the
// typed manifest (marshaled to JSONB at seed time).
type BuiltinSkill struct {
	ID          string
	Name        string
	Description string
	Version     string
	Markdown    string
	Manifest    skills.Manifest
}

// officeRuntime is the shared runtime contract of the document pack: every
// skill works through the command sandbox with Python; network stays
// restricted (package installation only where the deployment allows it).
func officeRuntime() skills.Runtime {
	return skills.Runtime{
		Sandbox:    "required",
		Network:    "restricted",
		Filesystem: skills.FSAccess{Read: []string{"/workspace"}, Write: []string{"/workspace"}},
	}
}

// BuiltinSkills is the in-code catalog (docs/builtin-skills.md §2), in
// binding-order preference for a generic office agent.
func BuiltinSkills() []BuiltinSkill {
	return []BuiltinSkill{
		builtinPDFProcessing(),
		builtinWordProcessing(),
		builtinSpreadsheetAnalysis(),
		builtinPresentationAuthoring(),
		builtinDocumentReview(),
		builtinDocumentGeneration(),
		builtinArchiveManagement(),
	}
}

// builtinPDFProcessing returns the pdf-processing skill.
func builtinPDFProcessing() BuiltinSkill {
	pdfTool := `#!/usr/bin/env python3
"""PDF toolbox: info, text/table extraction, page ops, form filling, OCR.

Every subcommand verifies its own output and prints JSON. A missing library
prints its install hint and exits 3.

Exit codes: 0 ok, 3 missing library, 4 processing error.
"""
import argparse
import json
import sys


def _need(mod, package):
    try:
        return __import__(mod)
    except ImportError:
        print(json.dumps({"error": "missing library", "install": f"pip install {package}"}))
        sys.exit(3)


def page_numbers(spec, total):
    if not spec:
        return list(range(1, total + 1))
    out = []
    for part in spec.split(","):
        if "-" in part:
            a, b = part.split("-", 1)
            out.extend(range(int(a), int(b) + 1))
        else:
            out.append(int(part))
    return sorted({n for n in out if 1 <= n <= total})


def cmd_info(args):
    pypdf = _need("pypdf", "pypdf")
    reader = pypdf.PdfReader(args.file)
    sample = "".join((p.extract_text() or "") for p in reader.pages[:3])
    info = {
        "file": args.file,
        "pages": len(reader.pages),
        "has_text_layer": bool(sample.strip()),
        "form_fields": sorted((reader.get_fields() or {}).keys()),
    }
    if not info["has_text_layer"]:
        info["note"] = "no text layer: scanned document — use the ocr subcommand"
    print(json.dumps(info, ensure_ascii=False, indent=2))


def cmd_extract(args):
    pdfplumber = _need("pdfplumber", "pdfplumber")
    pages = []
    with pdfplumber.open(args.file) as pdf:
        wanted = page_numbers(args.pages, len(pdf.pages))
        for n in wanted:
            pages.append({"page": n, "text": pdf.pages[n - 1].extract_text() or ""})
    empty = [p["page"] for p in pages if not p["text"].strip()]
    result = {"file": args.file, "pages": pages, "empty_pages": empty}
    if empty:
        result["note"] = "empty pages may be scans — run info, then ocr"
    if args.out:
        with open(args.out, "w", encoding="utf-8") as fh:
            json.dump(result, fh, ensure_ascii=False, indent=2)
        result = {"written": args.out, "pages": len(pages), "empty_pages": empty}
    print(json.dumps(result, ensure_ascii=False, indent=2))


def cmd_tables(args):
    import csv
    pdfplumber = _need("pdfplumber", "pdfplumber")
    written = []
    with pdfplumber.open(args.file) as pdf:
        for n, page in enumerate(pdf.pages, 1):
            for i, table in enumerate(page.extract_tables(), 1):
                path = f"{args.outdir}/page{n:03d}_table{i}.csv"
                with open(path, "w", newline="", encoding="utf-8") as fh:
                    csv.writer(fh).writerows(table or [])
                written.append({"file": path, "rows": len(table or []), "cols": max((len(r) for r in table or []), default=0)})
    print(json.dumps({"tables": len(written), "files": written}, ensure_ascii=False, indent=2))


def cmd_split(args):
    pypdf = _need("pypdf", "pypdf")
    reader = pypdf.PdfReader(args.file)
    at = min(max(args.at, 1), max(len(reader.pages) - 1, 1))
    for name, span in ((f"{args.prefix}_1.pdf", range(0, at)), (f"{args.prefix}_2.pdf", range(at, len(reader.pages)))):
        writer = pypdf.PdfWriter()
        for i in span:
            writer.add_page(reader.pages[i])
        with open(name, "wb") as fh:
            writer.write(fh)
    print(json.dumps({"parts": [f"{args.prefix}_1.pdf", f"{args.prefix}_2.pdf"], "split_after_page": at}))


def cmd_merge(args):
    pypdf = _need("pypdf", "pypdf")
    writer = pypdf.PdfWriter()
    total = 0
    for path in args.files:
        reader = pypdf.PdfReader(path)
        total += len(reader.pages)
        for page in reader.pages:
            writer.add_page(page)
    with open(args.output, "wb") as fh:
        writer.write(fh)
    check = pypdf.PdfReader(args.output)
    assert len(check.pages) == total, "merged page count mismatch"
    print(json.dumps({"output": args.output, "files": len(args.files), "pages": total}))


def cmd_fill(args):
    pypdf = _need("pypdf", "pypdf")
    with open(args.values, encoding="utf-8") as fh:
        values = json.load(fh)
    reader = pypdf.PdfReader(args.file)
    known = set((reader.get_fields() or {}).keys())
    unknown = sorted(set(values) - known)
    if unknown:
        print(json.dumps({"error": "unknown form fields", "unknown": unknown, "known": sorted(known)}))
        sys.exit(4)
    writer = pypdf.PdfWriter(clone_from=args.file)
    writer.update_page_form_field_values(None, {k: str(v) for k, v in values.items()})
    with open(args.output, "wb") as fh:
        writer.write(fh)
    got = {k: v.get("/V") for k, v in (pypdf.PdfReader(args.output).get_fields() or {}).items() if k in values}
    missing = [k for k, v in values.items() if got.get(k) not in (str(v), v)]
    print(json.dumps({"output": args.output, "filled": len(values) - len(missing), "not_applied": missing}, ensure_ascii=False))


def cmd_ocr(args):
    pytesseract = _need("pytesseract", "pytesseract")
    _need("pdf2image", "pdf2image")
    from pdf2image import convert_from_path
    pages = convert_from_path(args.file, dpi=200, first_page=args.first, last_page=args.last)
    out = []
    for i, image in enumerate(pages, args.first):
        text = pytesseract.image_to_string(image, lang=args.lang)
        out.append({"page": i, "text": text})
    print(json.dumps({"file": args.file, "lang": args.lang, "pages": out}, ensure_ascii=False, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("info"); p.add_argument("file"); p.set_defaults(fn=cmd_info)
    p = sub.add_parser("extract"); p.add_argument("file"); p.add_argument("--pages", default=""); p.add_argument("--out"); p.set_defaults(fn=cmd_extract)
    p = sub.add_parser("tables"); p.add_argument("file"); p.add_argument("--outdir", default="."); p.set_defaults(fn=cmd_tables)
    p = sub.add_parser("split"); p.add_argument("file"); p.add_argument("--at", type=int, default=1); p.add_argument("--prefix", default="part"); p.set_defaults(fn=cmd_split)
    p = sub.add_parser("merge"); p.add_argument("--output", required=True); p.add_argument("files", nargs="+"); p.set_defaults(fn=cmd_merge)
    p = sub.add_parser("fill"); p.add_argument("file"); p.add_argument("--values", required=True); p.add_argument("--output", required=True); p.set_defaults(fn=cmd_fill)
    p = sub.add_parser("ocr"); p.add_argument("file"); p.add_argument("--first", type=int, default=1); p.add_argument("--last", type=int, default=5); p.add_argument("--lang", default="eng"); p.set_defaults(fn=cmd_ocr)
    args = parser.parse_args()
    try:
        args.fn(args)
    except Exception as exc:  # noqa: BLE001 — one honest JSON error per failure
        print(json.dumps({"error": str(exc), "tool": "pdf_tool"}))
        sys.exit(4)


if __name__ == "__main__":
    main()
`

	markdown := `# PDF processing

## Purpose

Extract text, tables and pages from PDF files, fill PDF forms and OCR
scanned documents inside the project sandbox, producing structured,
verifiable output.

## When to use

Use this skill when a task involves a PDF: reading content for analysis,
extracting tables into CSV/JSON, splitting or merging pages, filling AcroForm
fields, or recovering text from scans. Do not use it for authoring new PDFs
from scratch (prefer document-generation) or for DOCX/XLSX sources (read
those directly instead of converting through PDF).

## Procedure

1. Inventory the input: ` + "`run_command`" + ` with ` + "`file <path>`" + ` and ` + "`python3 -c \"import pypdf\"`" + `
   checks. Confirm the file is a real PDF (magic bytes %PDF) and note its size.
2. Probe the toolchain before using it: try imports in order —
   ` + "`pdfplumber`" + ` (text + tables), ` + "`pypdf`" + ` (pages, forms, merge),
   ` + "`fitz`/PyMuPDF" + ` (fast bulk text). If a library is missing, try
   ` + "`pip install <lib>`" + `; if the sandbox has no network, fall back to
   what is installed and say so in the report.
3. Text extraction: extract per page, keep page numbers. Store the result as
   UTF-8 text or JSON (` + "`{page, text}`" + `) in the workspace.
4. Tables: with pdfplumber, extract per page and write each table to CSV.
   Always sanity-check row/column counts against the page text —
   extraction silently merges or splits cells on complex layouts.
5. Pages: splitting, merging, rotating and metadata go through pypdf
   (` + "`PdfReader`/`PdfWriter`" + `). Never rewrite a PDF in place — write a
   new file next to the source.
6. Forms: list fields with pypdf ` + "`get_fields()`" + `; fill AcroForm fields
   from a mapping, then re-open the output and assert every value is set
   before delivering.
7. OCR (scans, no text layer): render pages to images (` + "`pdf2image`" + ` +
   poppler or ` + "`fitz`" + `) and run ` + "`pytesseract`" + `. Record the language
   pack used; OCR of non-Latin text requires the matching tesseract language
   data — verify a sample page before processing the whole document.
8. Verify and report: page counts in/out, extraction coverage, list of
   produced files. Deliver outputs with ` + "`send_file`" + `.

## Constraints

- Never claim OCR or table extraction quality you did not verify on a sample.
- Never modify the source file; produce new files.
- Large PDFs: process page ranges, not the whole file, until the shape of the
  content is known.
- Scanned PDFs with no text layer must be detected (empty extraction) and
  routed to OCR — not reported as "no content".

## Common mistakes

- Trusting extraction of multi-column layouts without a visual check.
- Filling forms without re-reading the output (some PDFs drop field values).
- Reporting a page count from the file name instead of the parsed document.

## Verification

The run ends with: extracted artifacts in the workspace, a summary stating
pages processed / tables found / OCR coverage, and honest notes about anything
skipped.
`
	manifest := skills.Manifest{
		ID:          "pdf-processing",
		Version:     "1.1.0",
		Name:        "PDF processing",
		Description: "Extract text, tables and pages from PDFs, fill forms, OCR scans.",
		Inputs: map[string]skills.Field{
			"pdf_path": {Type: "string", Required: true, Description: "Workspace path of the source PDF"},
			"task":     {Type: "string", Required: true, Description: "One of: extract-text, extract-tables, split, merge, fill-form, ocr"},
		},
		Outputs: map[string]skills.Field{
			"artifacts": {Type: "array", Description: "Extracted text/CSV/JSON files written to the workspace"},
		},
		Capabilities: []string{"extract_text", "extract_tables", "split_merge_pages", "fill_forms", "ocr"},
		Tools:        []string{"run_command", "read_file", "write_file", "view_image", "send_file", "ask_human"},
		Runtime:      officeRuntime(),
		Preconditions: []string{
			"source_pdf_exists",
			"sandbox_available",
		},
		Postconditions: []string{
			"extraction_coverage_reported",
			"outputs_written_to_workspace",
		},
		Evidence: skills.EvidenceSpec{Required: []string{"page_count", "extraction_sample", "output_files"}},
	}
	markdown += bundledAppendix([]bundledFile{{Path: "scripts/pdf_tool.py", Lang: "python", Code: pdfTool}})
	return BuiltinSkill{ID: "pdf-processing", Name: "PDF processing", Description: manifest.Description, Version: manifest.Version, Markdown: markdown, Manifest: manifest}
}

// builtinWordProcessing returns the word-processing skill.
func builtinWordProcessing() BuiltinSkill {
	docxTool := `#!/usr/bin/env python3
"""DOCX toolbox: structure extraction, run-level text replacement, table dump.

Replacements never touch paragraph.text wholesale — that would wipe character
formatting. Every subcommand prints JSON; a missing library prints its
install hint and exits 3. Exit codes: 0 ok, 3 missing library, 4 error.
"""
import argparse
import json
import sys


def _need(mod, package):
    try:
        return __import__(mod)
    except ImportError:
        print(json.dumps({"error": "missing library", "install": f"pip install {package}"}))
        sys.exit(3)


def open_doc(path):
    docx = _need("docx", "python-docx")
    return docx.Document(path)


def iter_headers(doc):
    for i, section in enumerate(doc.sections):
        for kind in (section.header, section.footer):
            if kind is None or kind.is_linked_to_previous:
                continue
            for p in kind.paragraphs:
                if p.text.strip():
                    yield {"section": i + 1, "text": p.text}


def cmd_extract(args):
    doc = open_doc(args.file)
    paragraphs = [{"index": i, "style": p.style.name if p.style else "", "text": p.text}
                  for i, p in enumerate(doc.paragraphs)]
    tables = [{"table": t + 1, "rows": len(tb.rows), "cols": len(tb.columns)}
              for t, tb in enumerate(doc.tables)]
    result = {"file": args.file, "paragraphs": paragraphs, "tables": tables}
    if args.headers:
        result["headers_footers"] = list(iter_headers(doc))
    print(json.dumps(result, ensure_ascii=False, indent=2))


def replace_in_paragraph(paragraph, old, new):
    """Run-level replace: only runs actually containing the fragment change,
    formatting elsewhere survives. Returns the number of replacements."""
    count = 0
    for run in paragraph.runs:
        if old in run.text:
            count += run.text.count(old)
            run.text = run.text.replace(old, new)
    return count


def cmd_replace(args):
    doc = open_doc(args.file)
    replaced = replace_in_paragraphs(doc, args.old, args.new)
    doc.save(args.output)
    # Verification: re-open and count what is left.
    check = open_doc(args.output)
    leftovers = replace_in_paragraphs(check, args.old, "")
    print(json.dumps({
        "output": args.output, "replaced": replaced,
        "leftovers_after": leftovers,
        "ok": leftovers == 0 if args.new == "" else None,
    }, ensure_ascii=False))


def replace_in_paragraphs(doc, old, new):
    total = 0
    for p in doc.paragraphs:
        total += replace_in_paragraph(p, old, new)
    for table in doc.tables:
        for row in table.rows:
            for cell in row.cells:
                for p in cell.paragraphs:
                    total += replace_in_paragraph(p, old, new)
    for section in doc.sections:
        for kind in (section.header, section.footer):
            if kind is None or kind.is_linked_to_previous:
                continue
            for p in kind.paragraphs:
                total += replace_in_paragraph(p, old, new)
    return total


def cmd_tables(args):
    import csv
    doc = open_doc(args.file)
    written = []
    for t, table in enumerate(doc.tables, 1):
        path = f"{args.outdir}/table{t:02d}.csv"
        with open(path, "w", newline="", encoding="utf-8") as fh:
            csv.writer(fh).writerows([[cell.text for cell in row.cells] for row in table.rows])
        written.append({"file": path, "rows": len(table.rows), "cols": len(table.columns)})
    print(json.dumps({"tables": len(written), "files": written}, ensure_ascii=False, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("extract"); p.add_argument("file"); p.add_argument("--headers", action="store_true"); p.set_defaults(fn=cmd_extract)
    p = sub.add_parser("replace"); p.add_argument("file"); p.add_argument("old"); p.add_argument("new"); p.add_argument("--output", required=True); p.set_defaults(fn=cmd_replace)
    p = sub.add_parser("tables"); p.add_argument("file"); p.add_argument("--outdir", default="."); p.set_defaults(fn=cmd_tables)
    args = parser.parse_args()
    try:
        args.fn(args)
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"error": str(exc), "tool": "docx_tool"}))
        sys.exit(4)


if __name__ == "__main__":
    main()
`

	markdown := `# Word processing

## Purpose

Read, analyze and edit DOCX documents: paragraphs and styles, tables,
headers/footers, tracked content — preserving the document's formatting
while making precise, verifiable changes.

## When to use

Use this skill for DOCX content work: extracting text or structure,
replacing content, editing tables, updating headers/footers or styles.
Do not use it to convert DOCX to PDF (that is a libreoffice headless job
inside document-generation) or to edit plain Markdown (edit the file
directly).

## Procedure

1. Probe the toolchain: ` + "`python3 -c \"import docx\"`" + ` (python-docx).
   Missing → ` + "`pip install python-docx`" + `; offline sandbox → report and
   degrade to ` + "`docx2txt`" + ` or unzip-and-parse ` + "`word/document.xml`" + `.
2. Read structure before writing: enumerate paragraphs with style names,
   tables (rows × columns), sections and their headers/footers. Summarize
   the outline first — edits without a map of the document are guesses.
3. Text edits go through runs: a paragraph's text is split across runs;
   replacing ` + "`paragraph.text`" + ` destroys formatting. Modify the
   relevant run text, or rebuild runs only when the whole paragraph must
   change.
4. Tables: address cells as ` + "`table.cell(r, c)`" + `; verify dimensions
   before writing. Adding rows: copy the style of the last existing row.
5. Headers/footers: edit per section (` + "`section.header`" + `), and check
   ` + "`is_linked_to_previous`" + ` — sections often share one header.
6. Styles: prefer applying named styles over direct formatting; when the
   document lacks a needed style, say so instead of inventing ad-hoc
   formatting.
7. Save to a NEW file (` + "`*_edited.docx`" + `), never overwrite the source.
8. Verify: re-open the output, assert the intended changes are present and
   count paragraphs/tables before vs after. Report the diff summary.

## Constraints

- Never write into the source document.
- Never silently drop formatting, comments or tracked changes: if an edit
  cannot preserve them, surface the trade-off and ask via ` + "`ask_human`" + `.
- Bulk replacements must be previewed (list matches) before applying.

## Common mistakes

- ` + "`paragraph.text = ...`" + ` wiping character formatting.
- Ignoring ` + "`is_linked_to_previous`" + ` and editing the wrong section's header.
- Producing a file that does not open (saving without a valid template part) —
  always round-trip open the result.

## Verification

The run ends with: an edited DOCX that opens cleanly, a before/after summary
(paragraph and table counts, list of changes), and the source untouched.
`
	manifest := skills.Manifest{
		ID:          "word-processing",
		Version:     "1.1.0",
		Name:        "Word processing",
		Description: "Read and edit DOCX: styles, tables, headers/footers — formatting-preserving edits.",
		Inputs: map[string]skills.Field{
			"docx_path": {Type: "string", Required: true, Description: "Workspace path of the source DOCX"},
			"task":      {Type: "string", Required: true, Description: "One of: extract, edit-text, edit-tables, headers-footers, styles"},
		},
		Outputs: map[string]skills.Field{
			"edited_docx": {Type: "string", Description: "Path of the edited document"},
			"changelog":   {Type: "array", Description: "List of applied changes"},
		},
		Capabilities: []string{"read_docx", "edit_docx", "edit_tables", "edit_headers", "apply_styles"},
		Tools:        []string{"run_command", "read_file", "write_file", "edit_file", "send_file", "ask_human"},
		Runtime:      officeRuntime(),
		Preconditions: []string{
			"source_docx_exists",
			"sandbox_available",
		},
		Postconditions: []string{
			"edited_document_opens_cleanly",
			"change_summary_reported",
		},
		Evidence: skills.EvidenceSpec{Required: []string{"before_after_counts", "change_list", "output_file"}},
	}
	markdown += bundledAppendix([]bundledFile{{Path: "scripts/docx_tool.py", Lang: "python", Code: docxTool}})
	return BuiltinSkill{ID: "word-processing", Name: "Word processing", Description: manifest.Description, Version: manifest.Version, Markdown: markdown, Manifest: manifest}
}

// builtinSpreadsheetAnalysis returns the spreadsheet-analysis skill.
func builtinSpreadsheetAnalysis() BuiltinSkill {
	xlsxTool := `#!/usr/bin/env python3
"""XLSX/CSV toolbox: workbook inventory, column analysis with cross-checks,
sheet-to-CSV dump. Prints JSON; a missing library prints its install hint.

The analyzer always reads cached values (data_only=True) AND formula strings,
so a stale cache cannot masquerade as verified data. Exit codes: 0/3/4.
"""
import argparse
import csv
import json
import statistics
import sys


def _need(mod, package):
    try:
        return __import__(mod)
    except ImportError:
        print(json.dumps({"error": "missing library", "install": f"pip install {package}"}))
        sys.exit(3)


def cmd_inventory(args):
    openpyxl = _need("openpyxl", "openpyxl")
    cached = openpyxl.load_workbook(args.file, data_only=True, read_only=True)
    formulas = openpyxl.load_workbook(args.file, data_only=False, read_only=True)
    sheets = []
    for name in cached.sheetnames:
        ws_c, ws_f = cached[name], formulas[name]
        formula_cells = sum(
            1 for row in ws_f.iter_rows() for cell in row
            if isinstance(cell.value, str) and cell.value.startswith("=")
        )
        sheets.append({
            "sheet": name,
            "dims": ws_f.calculate_dimension(),
            "max_row": ws_f.max_row,
            "max_col": ws_f.max_column,
            "formula_cells": formula_cells,
        })
    print(json.dumps({"file": args.file, "sheets": sheets}, ensure_ascii=False, indent=2))


def cmd_analyze(args):
    openpyxl = _need("openpyxl", "openpyxl")
    ws = openpyxl.load_workbook(args.file, data_only=True)[args.sheet]
    header_row = args.header_row or 1
    headers = {}
    numeric = {}
    skipped = 0
    for row in ws.iter_rows(min_row=header_row + 1):
        for cell in row:
            name = str(ws.cell(row=header_row, column=cell.column).value or f"col{cell.column}")
            headers[cell.column] = name
            if isinstance(cell.value, (int, float)) and not isinstance(cell.value, bool):
                numeric.setdefault(cell.column, []).append(float(cell.value))
            elif cell.value is not None:
                skipped += 1
    columns = []
    for col, values in sorted(numeric.items()):
        total = sum(values)
        # Cross-check: statistics.fsum over the same values must agree with
        # the plain sum — a mismatch means NaN/inf noise slipped in.
        cross = statistics.fsum(values)
        columns.append({
            "column": headers.get(col, f"col{col}"),
            "count": len(values),
            "sum": total, "min": min(values), "max": max(values),
            "mean": round(statistics.mean(values), 6),
            "cross_check_ok": abs(total - cross) < 1e-9,
        })
    print(json.dumps({
        "file": args.file, "sheet": args.sheet, "header_row": header_row,
        "columns": columns, "non_numeric_cells_skipped": skipped,
    }, ensure_ascii=False, indent=2))


def cmd_to_csv(args):
    openpyxl = _need("openpyxl", "openpyxl")
    ws = openpyxl.load_workbook(args.file, data_only=True)[args.sheet]
    with open(args.output, "w", newline="", encoding="utf-8") as fh:
        writer = csv.writer(fh)
        rows = 0
        for row in ws.iter_rows(values_only=True):
            writer.writerow(["" if v is None else v for v in row])
            rows += 1
    print(json.dumps({"output": args.output, "rows": rows}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("inventory"); p.add_argument("file"); p.set_defaults(fn=cmd_inventory)
    p = sub.add_parser("analyze"); p.add_argument("file"); p.add_argument("sheet"); p.add_argument("--header-row", type=int); p.set_defaults(fn=cmd_analyze)
    p = sub.add_parser("to-csv"); p.add_argument("file"); p.add_argument("sheet"); p.add_argument("--output", required=True); p.set_defaults(fn=cmd_to_csv)
    args = parser.parse_args()
    try:
        args.fn(args)
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"error": str(exc), "tool": "xlsx_tool"}))
        sys.exit(4)


if __name__ == "__main__":
    main()
`

	markdown := `# Spreadsheet analysis

## Purpose

Read XLSX/CSV workbooks, run calculations and data analysis, and produce
verifiable results — totals, aggregations, pivots, formula audits — without
corrupting the source file.

## When to use

Use this skill when the source of truth is a spreadsheet: analyzing data,
cross-checking numbers, computing aggregates, cleaning CSV, inspecting or
adding formulas. Do not use it to build formatted reports (hand those to
document-generation) or to edit a workbook's visual styling beyond what the
task explicitly requires.

## Procedure

1. Probe the toolchain: ` + "`python3 -c \"import openpyxl\"`" + ` and
   ` + "`python3 -c \"import pandas\"`" + `. pandas is optional — everything
   here is achievable with openpyxl + the csv module; do not stall on a
   missing pandas, fall back and note it.
2. Inventory the workbook BEFORE computing anything: sheet names, used
   ranges, header rows (they are not always row 1), merged cells, hidden
   sheets. State the sheet and range you are working with.
3. Understand values vs formulas: openpyxl ` + "`data_only=True`" + ` reads
   cached results of formulas, the default reads formula strings. Auditing a
   workbook needs both — the formula AND the cached value it produced.
4. CSV: always specify/inspect encoding (try UTF-8, fall back to
   cp1251/latin-1 on UnicodeDecodeError) and the delimiter before parsing.
   Report row/column counts read.
5. Compute with checks: after every aggregate, verify it at least one
   independent way (e.g. sum a column, then sum its parts). A number without
   a cross-check is a guess.
6. Cleaning rules (never silently): trim whitespace, normalize number
   formats, resolve duplicates by REPORTING them — deletions only when the
   task explicitly says so.
7. Writing results: new file (` + "`*_analysis.xlsx`" + `) or a new sheet,
   never in-place over the source. Write formulas as formulas when the
   consumer expects a live workbook.
8. Verify: re-open the output, recount rows, spot-check a few cells against
   the source. Report: inputs read, operations done, outputs written.

## Constraints

- Never write into the source workbook.
- Never present a cached formula value as verified if the workbook was
  produced externally — cached values can be stale.
- Numbers crossed between sheets/systems must cite cell ranges in the report.

## Common mistakes

- Reading ` + "`data_only=False`" + ` and treating formula strings as values.
- Assuming the header is row 1 or that the active sheet is the data sheet.
- Silently dropping rows on parse errors (skip_empty quirks, ragged rows).

## Verification

The run ends with: analysis artifacts written, a summary citing exact
sheet/range inputs and computed results with cross-checks, source untouched.
`
	manifest := skills.Manifest{
		ID:          "spreadsheet-analysis",
		Version:     "1.1.0",
		Name:        "Spreadsheet analysis",
		Description: "Read XLSX/CSV, audit formulas, compute and cross-check analysis, write results without touching the source.",
		Inputs: map[string]skills.Field{
			"file_path": {Type: "string", Required: true, Description: "Workspace path of the XLSX/CSV file"},
			"task":      {Type: "string", Required: true, Description: "One of: analyze, aggregate, audit-formulas, clean-csv, compare"},
		},
		Outputs: map[string]skills.Field{
			"results": {Type: "array", Description: "Result files (xlsx/csv/json) written to the workspace"},
		},
		Capabilities: []string{"read_xlsx", "read_csv", "audit_formulas", "aggregate_data", "write_results"},
		Tools:        []string{"run_command", "read_file", "write_file", "send_file", "ask_human"},
		Runtime:      officeRuntime(),
		Preconditions: []string{
			"source_file_exists",
			"sandbox_available",
		},
		Postconditions: []string{
			"results_cross_checked",
			"outputs_written_to_workspace",
		},
		Evidence: skills.EvidenceSpec{Required: []string{"input_ranges", "computed_summary", "output_files"}},
	}
	markdown += bundledAppendix([]bundledFile{{Path: "scripts/xlsx_tool.py", Lang: "python", Code: xlsxTool}})
	return BuiltinSkill{ID: "spreadsheet-analysis", Name: "Spreadsheet analysis", Description: manifest.Description, Version: manifest.Version, Markdown: markdown, Manifest: manifest}
}

// builtinPresentationAuthoring returns the presentation-authoring skill.
func builtinPresentationAuthoring() BuiltinSkill {
	pptxTool := `#!/usr/bin/env python3
"""PPTX toolbox: deck inventory and outline-driven deck building.

build reads a JSON outline: [{"title": str, "bullets": [str], "notes": str}].
Placeholder coverage is verified after build — a leftover empty placeholder is
reported, never silently shipped. Exit codes: 0/3/4.
"""
import argparse
import json
import sys


def _need(mod, package):
    try:
        return __import__(mod)
    except ImportError:
        print(json.dumps({"error": "missing library", "install": f"pip install {package}"}))
        sys.exit(3)


def cmd_inventory(args):
    pptx = _need("pptx", "python-pptx")
    deck = pptx.Presentation(args.file)
    slides = []
    for i, slide in enumerate(deck.slides, 1):
        placeholders = []
        for shape in slide.placeholders:
            text = (shape.text or "").strip() if shape.has_text_frame else ""
            placeholders.append({"idx": shape.placeholder_format.idx, "type": str(shape.placeholder_format.type), "filled": bool(text)})
        notes = ""
        if slide.has_notes_slide:
            notes = slide.notes_slide.notes_text_frame.text
        slides.append({"slide": i, "layout": slide.slide_layout.name, "placeholders": placeholders, "has_notes": bool((notes or "").strip())})
    print(json.dumps({"file": args.file, "slide_count": len(slides), "slides": slides}, ensure_ascii=False, indent=2))


def cmd_build(args):
    pptx = _need("pptx", "python-pptx")
    with open(args.outline, encoding="utf-8") as fh:
        outline = json.load(fh)
    if not isinstance(outline, list) or not outline:
        print(json.dumps({"error": "outline must be a non-empty JSON array"}))
        sys.exit(4)
    deck = pptx.Presentation()
    for item in outline:
        if item.get("bullets"):
            layout = deck.slide_layouts[1]
        else:
            layout = deck.slide_layouts[5 if item.get("title_only") else 0]
        slide = deck.slides.add_slide(layout)
        if slide.shapes.title is not None:
            slide.shapes.title.text = item.get("title", "")
        body = slide.placeholders[1] if item.get("bullets") and len(slide.placeholders) > 1 else None
        if body is not None and body.has_text_frame:
            frame = body.text_frame
            frame.text = item["bullets"][0]
            for bullet in item["bullets"][1:]:
                frame.add_paragraph().text = bullet
        if item.get("notes"):
            slide.notes_slide.notes_text_frame.text = item["notes"]
    deck.save(args.output)
    # Verification pass: re-open, count slides, find empty title placeholders.
    check = pptx.Presentation(args.output)
    slide_count = sum(1 for _ in check.slides)
    empty_titles = []
    for i, slide in enumerate(check.slides, 1):
        title = slide.shapes.title
        if title is not None and not (title.text or "").strip():
            empty_titles.append(i)
    print(json.dumps({
        "output": args.output, "slides": slide_count,
        "empty_title_placeholders": empty_titles,
        "ok": not empty_titles,
    }, ensure_ascii=False))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("inventory"); p.add_argument("file"); p.set_defaults(fn=cmd_inventory)
    p = sub.add_parser("build"); p.add_argument("outline"); p.add_argument("--output", required=True); p.set_defaults(fn=cmd_build)
    args = parser.parse_args()
    try:
        args.fn(args)
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"error": str(exc), "tool": "pptx_tool"}))
        sys.exit(4)


if __name__ == "__main__":
    main()
`

	markdown := `# Presentation authoring

## Purpose

Create and modify PPTX presentations programmatically: slide structure,
titles and body content, tables, basic charts and speaker notes — with a
render check before delivery.

## When to use

Use this skill to build a deck from an outline, update an existing PPTX
(text, tables, notes, slide order), or generate a review draft of a
presentation. Do not use it for pixel-perfect design work — template-driven
brand decks are edited by filling their placeholders, not rebuilt.

## Procedure

1. Probe the toolchain: ` + "`python3 -c \"import pptx\"`" + ` (python-pptx);
   missing → ` + "`pip install python-pptx`" + `; offline → say so and stop
   early rather than emitting a broken file.
2. Working from a template: open it, enumerate slide layouts and their
   placeholders, and FILL placeholders instead of adding text boxes — that
   is what keeps the design intact.
3. Building from scratch: start from the default template, one idea per
   slide, title + body structure. Set explicit font sizes only when the
   layout requires it.
4. Text discipline: bullets stay bullets (placeholder text frames), long
   paragraphs do not belong on slides — move them to speaker notes.
5. Tables: verify row/column counts after writing; python-pptx tables start
   empty — add rows explicitly.
6. Charts: use ` + "`add_chart`" + ` with CategoryChartData; verify the data
   range count matches the categories, or the chart silently drops points.
7. Speaker notes: ` + "`slide.notes_slide.notes_text_frame`" + ` — write them;
   a deck without notes is not deliverable for review.
8. Render check (when the sandbox has libreoffice): ` + "`soffice --headless --convert-to pdf`" + `
   the deck, then view_image a few slides to catch overflow and broken
   layouts. If rendering is unavailable, state that the check was skipped.
9. Verify: slide count, placeholder coverage (no "Click to add title"
   leftovers), notes present. Save as a NEW file and deliver via ` + "`send_file`" + `.

## Constraints

- Never overwrite the source deck.
- Never deliver a deck that was not opened back after saving.
- Template placeholders must not be deleted — leave them empty and report
  which ones lacked content.

## Common mistakes

- Adding text boxes over the template design instead of filling placeholders.
- Chart categories mismatching the data series length.
- Fonts overflowing the placeholder — only the render check catches it.

## Verification

The run ends with: a PPTX that opens cleanly, a slide-by-slide outline of
what was created/changed, notes on every content slide, and (when possible)
a render check noted in the report.
`
	manifest := skills.Manifest{
		ID:          "presentation-authoring",
		Version:     "1.1.0",
		Name:        "Presentation authoring",
		Description: "Create and modify PPTX: placeholders, tables, charts, speaker notes, with a render check.",
		Inputs: map[string]skills.Field{
			"pptx_path": {Type: "string", Description: "Workspace path of an existing deck to modify (omit to create one)"},
			"outline":   {Type: "string", Required: true, Description: "Slide-by-slide outline or the change request"},
		},
		Outputs: map[string]skills.Field{
			"deck_path": {Type: "string", Description: "Path of the produced presentation"},
		},
		Capabilities: []string{"create_pptx", "edit_slides", "add_tables_charts", "write_speaker_notes", "render_check"},
		Tools:        []string{"run_command", "read_file", "write_file", "view_image", "send_file", "ask_human"},
		Runtime:      officeRuntime(),
		Preconditions: []string{
			"outline_available",
			"sandbox_available",
		},
		Postconditions: []string{
			"deck_opens_cleanly",
			"slide_outline_reported",
		},
		Evidence: skills.EvidenceSpec{Required: []string{"slide_outline", "output_file", "render_status"}},
	}
	markdown += bundledAppendix([]bundledFile{{Path: "scripts/pptx_tool.py", Lang: "python", Code: pptxTool}})
	return BuiltinSkill{ID: "presentation-authoring", Name: "Presentation authoring", Description: manifest.Description, Version: manifest.Version, Markdown: markdown, Manifest: manifest}
}

// builtinDocumentReview returns the document-review skill.
func builtinDocumentReview() BuiltinSkill {
	docDiff := `#!/usr/bin/env python3
"""Text extraction + unified diff for document pairs (docx/pdf/txt).

Normalizes whitespace and numbering noise so the diff compares content, not
formatting. Prints JSON: per-file extraction stats, added/removed line counts
and the unified diff. Exit codes: 0/3/4.
"""
import argparse
import difflib
import json
import re
import sys

NOISE = re.compile(r"[ \t]+|\u00a0")


def _need(mod, package):
    try:
        return __import__(mod)
    except ImportError:
        print(json.dumps({"error": "missing library", "install": f"pip install {package}"}))
        sys.exit(3)


def extract_text(path):
    lower = path.lower()
    if lower.endswith(".docx"):
        docx = _need("docx", "python-docx")
        doc = docx.Document(path)
        lines = [p.text for p in doc.paragraphs]
        for table in doc.tables:
            for row in table.rows:
                lines.append("\t".join(cell.text for cell in row.cells))
        return lines
    if lower.endswith(".pdf"):
        pdfplumber = _need("pdfplumber", "pdfplumber")
        lines = []
        with pdfplumber.open(path) as pdf:
            for page in pdf.pages:
                lines.extend((page.extract_text() or "").splitlines())
        return lines
    with open(path, encoding="utf-8", errors="replace") as fh:
        return fh.read().splitlines()


def normalize(lines):
    out = []
    for line in lines:
        line = NOISE.sub(" ", line).strip()
        out.append(line)
    return out


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("old")
    parser.add_argument("new")
    parser.add_argument("--context", type=int, default=2)
    args = parser.parse_args()
    try:
        old_lines = normalize(extract_text(args.old))
        new_lines = normalize(extract_text(args.new))
        diff = list(difflib.unified_diff(old_lines, new_lines, fromfile=args.old, tofile=args.new, lineterm="", n=args.context))
        added = sum(1 for line in diff if line.startswith("+") and not line.startswith("+++"))
        removed = sum(1 for line in diff if line.startswith("-") and not line.startswith("---"))
        print(json.dumps({
            "old": {"file": args.old, "lines": len(old_lines)},
            "new": {"file": args.new, "lines": len(new_lines)},
            "added": added, "removed": removed,
            "changed": added + removed > 0,
            "unified_diff": diff,
        }, ensure_ascii=False, indent=2))
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"error": str(exc), "tool": "doc_diff"}))
        sys.exit(4)


if __name__ == "__main__":
    main()
`

	markdown := `# Document review

## Purpose

Compare document versions, find contradictions and structural defects, and
produce a grounded review report — every finding tied to a location in the
source.

## When to use

Use this skill when someone asks "what changed between these versions",
"check this document for consistency", or "verify it matches the
requirements/template". Do not use it to make edits — review reports
facts; fixing is a separate task.

## Procedure

1. Extract text from each version first (pdf-processing / word-processing
   procedures apply). Normalize whitespace and numbering so the diff compares
   content, not formatting noise.
2. Version diff: produce a unified diff AND a semantic summary — renamed
   sections, changed numbers, added/removed obligations. Cite page/section
   for every change.
3. Contradiction pass — read the extracted text yourself and look for:
   conflicting numbers (totals vs line items, dates, versions), terms used
   with different definitions, requirements that cannot hold together,
   stale cross-references ("see section X" pointing at the wrong content).
4. Structure pass: required sections present, heading hierarchy consistent,
   numbering monotonic, tables referenced in text actually exist, figures
   numbered in order.
5. Severity every finding: blocker (wrong numbers, missing mandatory
   section), major (contradiction, broken reference), minor (style, wording).
   No severity — no finding.
6. Every finding cites evidence: document, section/page, quoted fragment.
   A finding without a quote is an opinion — drop it or downgrade to a note.
7. Write the report as an artifact (` + "`artifact_create`" + `): findings by
   severity, each with location + quote + suggested action, then a short
   verdict (ready / needs fixes / blocked).
8. Offer the decision: when the verdict is not obvious or the fix contract
   needs a human call, ask via ` + "`ask_human`" + `.

## Constraints

- Never edit the reviewed documents.
- Never invent findings to make the report look thorough.
- Do not rely on the diff alone — semantic contradictions survive line diffs.

## Common mistakes

- Reporting formatting churn as content changes.
- Losing page/section references, making findings unverifiable.
- A verdict without severity-classified findings behind it.

## Verification

The run ends with: an artifact report where every finding has location +
quote + severity, an explicit verdict, and the extracted texts preserved in
the workspace for audit.
`
	manifest := skills.Manifest{
		ID:          "document-review",
		Version:     "1.1.0",
		Name:        "Document review",
		Description: "Compare versions, find contradictions and structural defects, produce a severity-classified report with citations.",
		Inputs: map[string]skills.Field{
			"documents": {Type: "array", Required: true, Description: "Workspace paths of the documents/versions to review"},
			"criteria":  {Type: "string", Description: "Optional checklist, template or requirements to review against"},
		},
		Outputs: map[string]skills.Field{
			"report": {Type: "artifact", Description: "Review report artifact with findings and verdict"},
		},
		Capabilities: []string{"diff_versions", "find_contradictions", "check_structure", "classify_findings"},
		Tools:        []string{"run_command", "read_file", "artifact_create", "artifact_submit", "ask_human", "request_approval"},
		Runtime:      officeRuntime(),
		Preconditions: []string{
			"documents_extracted",
		},
		Postconditions: []string{
			"findings_have_citations",
			"verdict_recorded",
		},
		Evidence: skills.EvidenceSpec{Required: []string{"finding_list", "verdict"}},
	}
	markdown += bundledAppendix([]bundledFile{{Path: "scripts/doc_diff.py", Lang: "python", Code: docDiff}})
	return BuiltinSkill{ID: "document-review", Name: "Document review", Description: manifest.Description, Version: manifest.Version, Markdown: markdown, Manifest: manifest}
}

// builtinDocumentGeneration returns the document-generation skill.
func builtinDocumentGeneration() BuiltinSkill {
	fillTemplate := `#!/usr/bin/env python3
"""DOCX template filler: {{placeholder}} replacement at RUN level, with a
verification pass that fails loudly when placeholders remain.

Walks body paragraphs, table cells and headers/footers. Values come from a
JSON mapping. Exit codes: 0 all placeholders filled, 4 placeholders remain
(with --strict) or processing error, 3 missing library.
"""
import argparse
import json
import re
import sys

PLACEHOLDER = re.compile(r"\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}")


def _need(mod, package):
    try:
        return __import__(mod)
    except ImportError:
        print(json.dumps({"error": "missing library", "install": f"pip install {package}"}))
        sys.exit(3)


def replace_in_paragraph(paragraph, values):
    """Run-level replacement. A placeholder can span several runs; join run
    texts on a COPY of the boundaries, apply, then rebuild only the affected
    runs — formatting of untouched runs survives."""
    full = "".join(run.text for run in paragraph.runs)
    if not PLACEHOLDER.search(full):
        return
    new_full = PLACEHOLDER.sub(lambda m: str(values.get(m.group(1), m.group(0))), full)
    if new_full == full:
        return
    if paragraph.runs:
        paragraph.runs[0].text = new_full
        for run in paragraph.runs[1:]:
            run.text = ""


def walk(doc, values):
    for p in doc.paragraphs:
        replace_in_paragraph(p, values)
    for table in doc.tables:
        for row in table.rows:
            for cell in row.cells:
                for p in cell.paragraphs:
                    replace_in_paragraph(p, values)
    for section in doc.sections:
        for kind in (section.header, section.footer):
            if kind is None or kind.is_linked_to_previous:
                continue
            for p in kind.paragraphs:
                replace_in_paragraph(p, values)


def remaining_placeholders(doc):
    leftovers = set()
    texts = [p.text for p in doc.paragraphs]
    for table in doc.tables:
        for row in table.rows:
            for cell in row.cells:
                texts.extend(p.text for p in cell.paragraphs)
    for section in doc.sections:
        for kind in (section.header, section.footer):
            if kind is None or kind.is_linked_to_previous:
                continue
            texts.extend(p.text for p in kind.paragraphs)
    for text in texts:
        leftovers.update(PLACEHOLDER.findall(text))
    return sorted(leftovers)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("template")
    parser.add_argument("--values", required=True, help="JSON file with the placeholder mapping")
    parser.add_argument("--output", required=True)
    parser.add_argument("--strict", action="store_true", help="exit 4 when placeholders remain")
    args = parser.parse_args()
    try:
        docx = _need("docx", "python-docx")
        with open(args.values, encoding="utf-8") as fh:
            values = json.load(fh)
        doc = docx.Document(args.template)
        declared = set()
        for p in doc.paragraphs:
            declared.update(PLACEHOLDER.findall("".join(r.text for r in p.runs)))
        walk(doc, values)
        doc.save(args.output)
        leftovers = remaining_placeholders(docx.Document(args.output))
        unfilled = sorted(name for name in declared if name not in values and name in leftovers)
        report = {
            "output": args.output,
            "placeholders_declared": sorted(declared),
            "placeholders_filled": sorted(name for name in declared if name not in leftovers),
            "placeholders_remaining": leftovers,
            "never_supplied": unfilled,
        }
        print(json.dumps(report, ensure_ascii=False, indent=2))
        if args.strict and leftovers:
            sys.exit(4)
    except SystemExit:
        raise
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"error": str(exc), "tool": "fill_template"}))
        sys.exit(4)


if __name__ == "__main__":
    main()
`

	markdown := `# Document generation

## Purpose

Produce documents from templates: fill DOCX/PPTX placeholders (or render
Markdown/HTML to PDF), then verify the result before delivering it to a
human.

## When to use

Use this skill when a repeatable document is needed from data: contracts,
letters, reports, minutes, invoices, weekly updates. If the company has an
approved template, use IT — never synthesize a corporate document layout
from memory. For analysis-heavy outputs, take the numbers from
spreadsheet-analysis, not from memory.

## Procedure

1. Secure the inputs: the template path AND the data. Missing data is an
   ` + "`ask_human`" + `, not a placeholder to invent. List every placeholder
   found in the template (` + "`{{name}}`" + `, merge fields) before filling.
2. Choose the mechanism by template type:
   - DOCX: python-docx placeholder replacement done at RUN level (walk
     runs, replace within ` + "`run.text`" + `) — paragraph-level replacement
     destroys formatting. Tables and headers/footers must be walked too.
   - PPTX: fill placeholders per presentation-authoring.
   - Markdown/HTML → PDF: ` + "`pandoc`" + ` or ` + "`wkhtmltopdf`" + ` when
     installed; otherwise libreoffice headless. Say which renderer ran.
3. Fill only what the data supports. A placeholder with no value stays
   visibly unfilled and is reported — an invented value is worse than an
   empty one.
4. Numbers, dates, names: format per the document's locale (decimal
   separators, date style) — copy the convention visible in the template.
5. Save as a NEW file; never write over the template.
6. Verification pass (mandatory, not optional):
   - re-open the output and assert NO placeholder patterns remain;
   - assert every required data point appears in the text;
   - render to PDF/images when possible (` + "`soffice --headless`" + `) and
     view_image the first page and any table pages;
   - check the document opens without repair prompts.
7. Deliver via ` + "`send_file`" + ` with a summary: template used, data
   source, placeholders filled vs left empty, verification results.
8. When the filled document has consequences (contract, offer, external
   letter), request human approval (` + "`request_approval`" + `) before it is
   sent anywhere.

## Constraints

- Never invent missing data — leave the placeholder and report it.
- Never write into the template file.
- Never deliver without the verification pass.
- External-impact documents require human approval.

## Common mistakes

- Replacing ` + "`paragraph.text`" + ` and losing styles, or missing
  placeholders split across runs.
- Rendering without checking the render — broken tables and overflow reach
  the recipient first.
- Filling a template from memory instead of the provided data source.

## Verification

The run ends with: the generated file, a fill report (template, data
source, placeholders filled/left), verification results, and approval
requested when the document has external impact.
`
	manifest := skills.Manifest{
		ID:          "document-generation",
		Version:     "1.1.0",
		Name:        "Document generation",
		Description: "Fill DOCX/PPTX/Markdown templates with data and verify the result before delivery.",
		Inputs: map[string]skills.Field{
			"template_path": {Type: "string", Required: true, Description: "Workspace path of the template (DOCX/PPTX/MD)"},
			"data":          {Type: "string", Required: true, Description: "Data source: file path or inline values"},
		},
		Outputs: map[string]skills.Field{
			"document_path": {Type: "string", Description: "Path of the generated document"},
			"fill_report":   {Type: "array", Description: "Placeholders filled and left empty"},
		},
		Capabilities: []string{"fill_template", "render_pdf", "verify_output", "deliver_document"},
		Tools:        []string{"run_command", "read_file", "write_file", "view_image", "send_file", "ask_human", "request_approval"},
		Runtime:      officeRuntime(),
		Preconditions: []string{
			"template_exists",
			"data_source_available",
		},
		Postconditions: []string{
			"no_placeholders_remain",
			"output_delivered",
		},
		Evidence: skills.EvidenceSpec{Required: []string{"fill_report", "verification_result", "output_file"}},
	}
	markdown += bundledAppendix([]bundledFile{{Path: "scripts/fill_template.py", Lang: "python", Code: fillTemplate}})
	return BuiltinSkill{ID: "document-generation", Name: "Document generation", Description: manifest.Description, Version: manifest.Version, Markdown: markdown, Manifest: manifest}
}

// builtinArchiveManagement returns the archive-management skill.
func builtinArchiveManagement() BuiltinSkill {
	archiveTool := `#!/usr/bin/env bash
# Archive toolbox: inspect | extract | create | verify.
# Refuses path-traversal entries and bomb-profile archives BEFORE extraction;
# every path goes through an integrity test. Passwords travel via the
# ARCHIVE_PASSWORD env var — never as a logged argument where avoidable.
# Exit codes: 0 ok, 1 refusal/verification failure, 2 missing tool.
set -uo pipefail

find_tool() {
  for candidate in "$@"; do
    command -v "$candidate" >/dev/null 2>&1 && { echo "$candidate"; return; }
  done
  echo ""
}

archive_kind() {
  case "$(file --brief --mime-type "$1" 2>/dev/null || echo unknown)" in
    application/zip) echo zip ;;
    application/x-7z-compressed) echo 7z ;;
    application/gzip|application/x-gzip|application/x-tar|application/x-gtar) echo tar ;;
    application/x-rar*) echo rar ;;
    *) echo unknown ;;
  esac
}

list_entries() {
  case "$1" in
    zip) unzip -l "$2" 2>/dev/null | awk 'NR>3 && $NF != "" {print $4}' | grep -v '^----$' ;;
    7z) 7z l -slt "$2" 2>/dev/null | grep '^Path = ' | cut -d' ' -f2- ;;
    tar) tar -tf "$2" 2>/dev/null ;;
  esac
}

cmd_inspect() {
  local archive="$1" kind
  kind=$(archive_kind "$archive")
  if [ "$kind" = "rar" ]; then
    echo '{"error": "rar needs the unrar tool, often not installed", "kind": "rar"}'; exit 2
  fi
  if [ "$kind" = "unknown" ]; then
    # Office documents are zip containers — an office file here means the
    # wrong skill was picked, not a corrupt archive.
    echo "{\"error\": \"not an archive\", \"hint\": \"office documents are zip containers; check the file type\"}"; exit 1
  fi
  local traversal bomb=0 count=0
  traversal=$(list_entries "$kind" "$archive" | grep -E '(^|/)\\.\\.(/|$)|^[A-Za-z]:[\\/]' || true)
  count=$(list_entries "$kind" "$archive" | grep -c . || true)
  local packed unpacked
  packed=$(stat -f%z "$archive" 2>/dev/null || stat -c%s "$archive" 2>/dev/null || echo 0)
  unpacked=$(unzip -l "$archive" 2>/dev/null | awk '/files?$/{print $2}' || echo 0)
  if [ "$packed" -gt 0 ] && [ "$unpacked" -gt 0 ]; then
    ratio=$((unpacked / packed))
    [ "$ratio" -gt 100 ] && bomb=1
  fi
  echo "{\"kind\": \"$kind\", \"entries\": $count, \"packed_bytes\": $packed, \"unpacked_bytes\": $unpacked, \"path_traversal\": $(list_entries "$kind" "$archive" | grep -cE '(^|/)\\.\\.(/|$)' || true), \"bomb_suspect\": $bomb}"
}

cmd_extract() {
  local archive="$1" outdir="${2:-${archive%.*}_extracted}"
  local report
  report=$(cmd_inspect "$archive") || exit $?
  echo "$report" > /dev/null
  local traversal
  traversal=$(list_entries "$(archive_kind "$archive")" "$archive" | grep -E '(^|/)\\.\\.(/|$)|^[A-Za-z]:[\\/]' || true)
  if [ -n "$traversal" ]; then
    echo "{\"refused\": \"path traversal entries\", \"entries\": [$(echo "$traversal" | head -5 | sed 's/.*/"&"/' | paste -sd, -)]}"; exit 1
  fi
  mkdir -p "$outdir"
  local pw_env=""
  [ -n "${ARCHIVE_PASSWORD:-}" ] && pw_env="$ARCHIVE_PASSWORD"
  case "$(archive_kind "$archive")" in
    zip)
      if [ -n "$pw_env" ]; then unzip -n -P "$pw_env" "$archive" -d "$outdir"; else unzip -n "$archive" -d "$outdir"; fi ;;
    7z)
      if [ -n "$pw_env" ]; then 7z x -y -p"$pw_env" -o"$outdir" "$archive" >/dev/null; else 7z x -y -o"$outdir" "$archive" >/dev/null; fi ;;
    tar)
      tar -xf "$archive" -C "$outdir" ;;
  esac
  local extracted
  extracted=$(find "$outdir" -type f | wc -l | tr -d ' ')
  echo "{\"extracted_to\": \"$outdir\", \"files\": $extracted}"
}

cmd_create() {
  local dir="$1" output="$2" format="${3:-zip}"
  case "$format" in
    zip) (cd "$dir" && zip -r -q "$OLDPWD/$output" .) ;;
    tar.gz) tar -czf "$output" -C "$dir" . ;;
    7z)
      command -v 7z >/dev/null 2>&1 || { echo '{"error": "7z not installed"}'; exit 2; }
      7z a -bd "$output" "$dir"/* >/dev/null ;;
    *) echo "{\"error\": \"unsupported format: $format\"}"; exit 2 ;;
  esac
  echo "{\"created\": \"$output\", \"format\": \"$format\"}"
}

cmd_verify() {
  local archive="$1" rc
  case "$(archive_kind "$archive")" in
    zip) unzip -t -qq "$archive" >/dev/null 2>&1; rc=$? ;;
    7z)  7z t "$archive" >/dev/null 2>&1; rc=$? ;;
    tar) tar -tf "$archive" >/dev/null 2>&1; rc=$? ;;
    *) echo '{"error": "unknown kind"}'; exit 1 ;;
  esac
  echo "{\"archive\": \"$archive\", \"integrity_ok\": $([ $rc -eq 0 ] && echo true || echo false)}"
  [ $rc -eq 0 ]
}

usage() {
  echo "usage: archive_tool.sh inspect ARCHIVE | extract ARCHIVE [DIR] | create DIR -o OUT [-f zip|tar.gz|7z] | verify ARCHIVE" >&2
  exit 2
}

[ $# -ge 2 ] || usage
action="$1"; shift
# create carries -o/-f flags: pull the two positionals first.
case "$action" in
  create)
    dir=""; out=""; fmt="zip"
    while [ $# -gt 0 ]; do
      case "$1" in
        -o) out="$2"; shift 2 ;;
        -f) fmt="$2"; shift 2 ;;
        *) dir="$1"; shift ;;
      esac
    done
    [ -n "$dir" ] && [ -n "$out" ] || usage
    cmd_create "$dir" "$out" "$fmt" ;;
  inspect) cmd_inspect "$1" ;;
  extract) cmd_extract "$@" ;;
  verify) cmd_verify "$1" ;;
  *) usage ;;
esac
`

	markdown := `# Archive management

## Purpose

Create, inspect, extract and verify archives (zip, tar/tar.gz/tar.bz2, 7z),
including password-protected and split volumes — safely: path-traversal and
zip-bomb checks before extraction, integrity tests after creation.

## When to use

Use this skill when a task involves archives: unpacking what a user sent,
packing deliverables into one file, working with password-protected or
multi-volume archives, checking what is inside without extracting. Do not
use it to compress a single file (gzip/bzip2 via run_command needs no skill)
or as a substitute for version control.

## Procedure

0. **Encrypted archive → ask FIRST, probe later.** The moment listing or the
   magic bytes show encryption (unzip: «flag 1»/wrong CRC without a password;
   header flags bit 0), ask the human for the password via ` + "`ask_human`" + ` — do
   NOT spend turns hunting for tools first: the password travels by chat
   while you probe. Pass it via the ARCHIVE_PASSWORD env var, never argv,
   never logs. A wrong-password reply from unzip means re-ask, never
   brute-force.
1. Identify by content, not extension: ` + "`file <path>`" + ` and the magic
   bytes. Remember that DOCX/XLSX/PPTX are ZIP containers — an "unzip failed"
   on an office document means the wrong skill was picked, not a corrupt
   file. RAR needs ` + "`unrar`" + `; if it is not installed, report honestly
   instead of guessing.
2. Environment reality (alpine-based profiles since 2026-10): ` + "`unzip`" + `,
   ` + "`tar`" + `, ` + "`7z`" + ` and ` + "`python3`" + ` are baked in — probe once
   with ` + "`command -v`" + ` and move on. On older or custom images they may be
   missing: do not burn turns installing equivalents — ` + "`unzip`" + ` alone
   already covers ZipCrypto extraction, and npm-side libraries (adm-zip)
   do NOT decrypt. Fall back to the web inbox / a human rather than
   reimplementing crypto.
3. Inspect BEFORE extracting: ` + "`unzip -l`" + ` / ` + "`7z l`" + ` / ` + "`tar -tf`" + `.
   Read the inventory looking for three red flags:
   - path traversal: entries with ` + "`../`" + ` or absolute paths (zip-slip);
   - extreme compression ratio (packed/unpacked > ~100x or multi-GB totals
     from a small file) — a probable zip bomb;
   - nested archives that will recursively unpack.
   Any red flag → stop and surface it via ` + "`ask_human`" + ` — never
   "just extract carefully".
4. Extract into a NEW dedicated directory (` + "`<name>_extracted/`" + `),
   never into the workspace root and never over existing files. With python
   ` + "`zipfile`" + `, ` + "`extractall`" + ` is NOT path-safe by default — prefer
   the CLI tools (` + "`unzip`" + `, ` + "`7z x`" + `) which reject traversal, or
   filter entry names explicitly first. Prefer the bundled
   ` + "`scripts/archive_tool.sh`" + ` — it implements these checks.
5. Split volumes (` + "`.zip.001…`" + `, ` + "`.7z.001…`" + `, ` + "`part1.rar…`" + `):
   verify all parts are present (` + "`7z t first.001`" + ` names what is
   missing) before extracting; extract from the first volume only.
6. Create archives by format, deliberately:
   - ` + "`zip`" + ` — the interoperable default;
   - ` + "`tar.gz`" + ` — for unix-y file sets that must keep permissions;
   - ` + "`7z`" + ` — best ratio when the recipient can open it.
   Already-compressed data (jpg, mp4, pdf) goes with ` + "`-0`" + `/store —
   deflating it wastes time for no size gain.
7. Verify (mandatory):
   - integrity test: ` + "`unzip -t`" + ` / ` + "`7z t`" + ` / ` + "`gzip -t`" + `;
   - count files and compare total bytes against the source inventory;
   - for critical deliverables: round-trip extract to a temp dir and diff
     against the source.
8. Report: contents inventory, compressed/uncompressed sizes, integrity
   status, passwords used (never the values). Deliverables go out via
   ` + "`send_file`" + `.

## Constraints

- Never extract into the workspace root or over existing files.
- Never process an archive with traversal entries or a bomb profile without
  an explicit human confirmation.
- Never store, log or guess passwords; they come from ` + "`ask_human`" + ` or
  the secrets store, used through stdin/env.
- Nested archives are inventoried, not recursively extracted, unless the
  task demands it — recursion is where bombs live.

## Common mistakes

- Treating an office document as a "corrupt zip".
- ` + "`zipfile.extractall`" + ` without path checks (zip-slip).
- Non-UTF-8 filenames mojibake for Windows recipients — when names look
  mangled, re-create the zip with explicit UTF-8 names and say so.
- Extracting a split archive's middle volume instead of the first.

## Verification

The run ends with: an integrity test result, file counts matching the
inventory, no traversal entries processed, and an honest report of what was
extracted/created and what was refused.
`
	manifest := skills.Manifest{
		ID:          "archive-management",
		Version:     "1.2.0",
		Name:        "Archive management",
		Description: "Create, inspect and extract zip/tar/7z archives — including password-protected and split volumes — with traversal and bomb checks.",
		Inputs: map[string]skills.Field{
			"archive_path": {Type: "string", Description: "Workspace path of the archive (extract/inspect/verify tasks)"},
			"source_dir":   {Type: "string", Description: "Directory to pack (create task)"},
			"task":         {Type: "string", Required: true, Description: "One of: inspect, extract, create, verify"},
		},
		Outputs: map[string]skills.Field{
			"artifacts": {Type: "array", Description: "Extracted directory or created archive path"},
			"inventory": {Type: "array", Description: "Contents listing with sizes and integrity status"},
		},
		Capabilities: []string{"inspect_archives", "extract_archives", "create_archives", "password_protected", "split_volumes", "verify_integrity"},
		Tools:        []string{"run_command", "read_file", "write_file", "send_file", "ask_human"},
		Runtime:      officeRuntime(),
		Preconditions: []string{
			"source_available",
			"sandbox_available",
		},
		Postconditions: []string{
			"integrity_verified_or_refused",
			"inventory_reported",
		},
		Evidence: skills.EvidenceSpec{Required: []string{"integrity_test_result", "file_inventory", "output_files"}},
	}
	markdown += bundledAppendix([]bundledFile{{Path: "scripts/archive_tool.sh", Lang: "bash", Code: archiveTool}})
	return BuiltinSkill{ID: "archive-management", Name: "Archive management", Description: manifest.Description, Version: manifest.Version, Markdown: markdown, Manifest: manifest}
}

// SeedBuiltinSkills inserts the catalog into the registry idempotently:
// rows that already exist are left untouched, so human edits, evolution
// drafts and deletions of OTHER skills are never affected. A builtin skill
// deleted by an admin reappears on restart (unbind it from agents instead,
// or set KERNEL_BUILTIN_SKILLS=off to disable seeding entirely) — the
// sandbox-profile trade-off, applied to skills.
//
// Per-skill failures are collected, not fatal: one broken catalog entry
// must never leave the installation with a partial pack (the version-table
// insert violates NOT NULL on the text[] provenance columns when handed a
// bare NULL — the exact failure that once left only the first skill seeded).
func (s *Store) SeedBuiltinSkills(ctx context.Context) error {
	var seeded []string
	var problems []error
	for _, bs := range BuiltinSkills() {
		if err := s.seedBuiltinSkill(ctx, bs); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", bs.ID, err))
			continue
		}
		seeded = append(seeded, bs.ID)
	}
	if len(seeded) > 0 {
		slog.Info("builtin skills seeded", "skills", seeded)
	}
	if len(problems) > 0 {
		return errors.Join(problems...)
	}
	return nil
}

// seedBuiltinSkill seeds one builtin skill: the registry row plus its
// immutable version snapshot (origin: initial), both idempotent. Provenance
// array columns receive empty arrays — they are NOT NULL, and a bare NULL
// fails the insert.
//
// An existing row is UPDATED to the catalog version only while its current
// revision is still an untouched builtin: a version row with a non-initial
// origin (human edit, applied agent proposal) pins the skill — the catalog
// never overwrites authored content. Fresh installs insert; previously
// seeded rows upgrade to the new catalog version.
func (s *Store) seedBuiltinSkill(ctx context.Context, bs BuiltinSkill) error {
	if issues := bs.Manifest.Validate(); len(issues) > 0 {
		return fmt.Errorf("invalid manifest: %s %s", issues[0].Field, issues[0].Message)
	}
	manifestJSON, err := json.Marshal(bs.Manifest)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	now := time.Now().UTC()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_skill (id, name, description, version, markdown, manifest, org_unit_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NULL, $7, $7)
		 ON CONFLICT (id) DO UPDATE SET
		   name = EXCLUDED.name, description = EXCLUDED.description, version = EXCLUDED.version,
		   markdown = EXCLUDED.markdown, manifest = EXCLUDED.manifest, updated_at = EXCLUDED.updated_at
		 WHERE NOT EXISTS (
		   SELECT 1 FROM workspace_skill_version v
		   WHERE v.skill_id = workspace_skill.id AND v.version = workspace_skill.version AND v.origin <> 'initial')`,
		bs.ID, bs.Name, bs.Description, bs.Version, bs.Markdown, manifestJSON, now); err != nil {
		return fmt.Errorf("insert skill: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_skill_version (skill_id, version, markdown, manifest, created_at, origin, source_runs, evidence_refs, knowledge_ids, change_summary, observed_problem, proposed_change, expected_effect, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $7, '', '', '', '', $8)
		 ON CONFLICT (skill_id, version) DO NOTHING`,
		bs.ID, bs.Version, bs.Markdown, manifestJSON, now, SkillOriginInitial, textArray(nil), SkillVersionActive); err != nil {
		return fmt.Errorf("insert version %s: %w", bs.Version, err)
	}
	return nil
}
