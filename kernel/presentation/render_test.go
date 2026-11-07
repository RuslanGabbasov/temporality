package presentation

import (
	"strings"
	"testing"
)

// Format conformance (docs/presentation-engine.md §7.7): every visualizer
// must carry the same data into Text, SVG and PNG — labels, values, legends,
// correct sizes, no clipped content, Cyrillic intact.

func sampleDocument() Document {
	doc, err := Validate([]byte(`{
		"version": "1.0",
		"type": "presentation",
		"id": "report-1",
		"title": "Отчёт тестирования",
		"content": [
			{"type": "text", "content": "Тестирование завершено."},
			{"type": "kpi", "items": [
				{"label": "Passed", "value": 942, "status": "success"},
				{"label": "Failed", "value": 31, "status": "danger"},
				{"label": "Skipped", "value": 27, "status": "warning", "delta": "+4%"}
			]},
			{"type": "chart", "props": {"chartType": "bar", "title": "Распределение результатов", "showValues": true}, "data": {
				"labels": ["Passed", "Failed", "Skipped"],
				"values": [942, 31, 27],
				"unit": "тестов"
			}},
			{"type": "table", "data": {
				"columns": [{"key": "suite", "title": "Набор"}, {"key": "result", "title": "Результат", "kind": "status"}],
				"rows": [
					{"suite": "auth", "result": {"text": "pass", "status": "success"}},
					{"suite": "billing", "result": {"text": "fail", "status": "danger"}}
				]
			}},
			{"type": "flowchart", "data": {
				"nodes": [
					{"id": "a", "label": "Сборка", "status": "success"},
					{"id": "b", "label": "Тесты"},
					{"id": "c", "label": "Публикация"}
				],
				"edges": [{"from": "a", "to": "b"}, {"from": "b", "to": "c", "label": "зелёный"}]
			}},
			{"type": "timeline", "items": [
				{"label": "Ран запущен", "at": "10:00", "status": "info"},
				{"label": "Ошибка найдена", "at": "10:07", "status": "danger", "description": "billing: таймаут"}
			]}
		]
	}`), Policy{})
	if err != nil {
		panic(err)
	}
	return doc
}

// glyphs collects the text of all glyph ops in a drawing.
func glyphs(d Drawing) []string {
	out := []string{}
	var walk func(ops []DrawOp)
	walk = func(ops []DrawOp) {
		for _, op := range ops {
			switch v := op.(type) {
			case Glyph:
				out = append(out, v.Text)
			}
		}
	}
	walk(d.Ops)
	return out
}

func drawingContainsText(d Drawing, needle string) bool {
	for _, text := range glyphs(d) {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func TestFormatConformance(t *testing.T) {
	doc := sampleDocument()

	// Text format carries every label and value (§7.6).
	text := RenderText(doc)
	for _, needle := range []string{"Отчёт тестирования", "Passed", "942", "Failed", "31", "Skipped", "27", "auth", "billing", "Сборка", "Тесты", "Публикация", "зелёный", "10:00", "Ошибка найдена"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("text fallback lost %q:\n%s", needle, text)
		}
	}

	// SVG format: same content, standalone, sized, no scripts (§7.4).
	drawing := LayoutDocument(doc, LayoutOptions{IncludeTitle: true})
	svg := string(RenderSVG(drawing))
	if !strings.HasPrefix(svg, "<?xml") || !strings.Contains(svg, "<svg") {
		t.Fatal("svg is not a standalone document")
	}
	if !strings.Contains(svg, `viewBox="0 0`) {
		t.Fatal("svg lacks viewBox")
	}
	if strings.Contains(strings.ToLower(svg), "<script") || strings.Contains(strings.ToLower(svg), "javascript") {
		t.Fatal("svg carries executable content")
	}
	for _, needle := range []string{"Отчёт тестирования", "Passed", "942", "Распределение результатов", "Сборка", "зелёный", "billing"} {
		if !drawingContainsText(drawing, needle) {
			t.Fatalf("svg drawing lost %q (glyphs: %v)", needle, glyphs(drawing))
		}
	}

	// PNG format: valid image, correct dimensions, non-empty (§7.5).
	png, err := RenderPNG(drawing)
	if err != nil {
		t.Fatalf("png render failed: %v", err)
	}
	if !strings.HasPrefix(string(png[:8]), "\x89PNG") {
		t.Fatal("png signature missing")
	}
	if len(png) < 2000 {
		t.Fatalf("png suspiciously small (%d bytes)", len(png))
	}

	// Sizes: the drawing must fit its declared frame (§7.7 no clipped
	// content — every glyph and shape op sits inside the frame).
	for _, op := range drawing.Ops {
		checkOpInFrame(t, drawing, op)
	}
}

func checkOpInFrame(t *testing.T, d Drawing, op DrawOp) {
	t.Helper()
	const tolerance = 2.0
	inside := func(x, y float64) {
		if x < -tolerance || y < -tolerance || x > d.Width+tolerance || y > d.Height+tolerance {
			t.Fatalf("op %T at (%.1f, %.1f) escapes frame %.1fx%.1f", op, x, y, d.Width, d.Height)
		}
	}
	switch v := op.(type) {
	case Rect:
		inside(v.X, v.Y)
		inside(v.X+v.W, v.Y+v.H)
	case Glyph:
		inside(v.X, v.Y)
	case Dot:
		inside(v.CX+v.R, v.CY+v.R)
	case Segment:
		inside(v.X1, v.Y1)
		inside(v.X2, v.Y2)
	case Wedge:
		inside(v.CX+v.R, v.CY+v.R)
	case Poly:
		for _, p := range v.Points {
			inside(p.X, p.Y)
		}
	case Arrow:
		inside(v.X, v.Y)
	}
}

func TestChartFormatsShareValues(t *testing.T) {
	doc := mustValid(t, `{
		"version": "1.0", "type": "presentation",
		"content": [{"type": "chart", "props": {"chartType": "line", "title": "Latency", "showValues": true}, "data": {
			"labels": ["mon", "tue"], "values": [120, 95], "unit": "ms", "y_title": "p95"
		}}]
	}`)
	text := RenderText(doc)
	if !strings.Contains(text, "mon: 120") || !strings.Contains(text, "tue: 95") || !strings.Contains(text, "ms") {
		t.Fatalf("text chart lost values:\n%s", text)
	}
	drawing := LayoutDocument(doc, LayoutOptions{})
	for _, needle := range []string{"Latency", "120", "95", "p95", "mon", "tue"} {
		if !drawingContainsText(drawing, needle) {
			t.Fatalf("chart drawing lost %q", needle)
		}
	}
}

func TestNegativeValuesRender(t *testing.T) {
	// §6.3: zero and negative values must not break rendering.
	doc := mustValid(t, `{
		"version": "1.0", "type": "presentation",
		"content": [{"type": "chart", "props": {"chartType": "bar"}, "data": {"labels": ["profit", "loss"], "values": [500, -120]}}]
	}`)
	drawing := LayoutDocument(doc, LayoutOptions{})
	png, err := RenderPNG(drawing)
	if err != nil {
		t.Fatalf("negative bar chart png failed: %v", err)
	}
	if len(png) == 0 {
		t.Fatal("empty png")
	}
}

func TestAdaptFallbackOrder(t *testing.T) {
	doc := sampleDocument()

	// Markdown-capable channel with tables: markdown body, no attachments
	// needed (charts are native?? no — charts off, so attachments).
	web := Adapt(doc, Capabilities{Markdown: true, Tables: true, Images: true, Charts: true, Diagrams: true})
	if web.Kind != "markdown" || web.Body == "" {
		t.Fatalf("web delivery broken: %+v", web)
	}
	if len(web.Attachments) != 0 {
		t.Fatalf("web must not need static attachments: %d", len(web.Attachments))
	}

	// Matrix-style: markdown yes, tables no, files yes -> markdown + SVG
	// attachments for the visual components.
	matrix := Adapt(doc, Capabilities{Markdown: true, Images: true, Files: true})
	if matrix.Kind != "markdown" {
		t.Fatalf("matrix delivery kind: %s", matrix.Kind)
	}
	if len(matrix.Attachments) == 0 {
		t.Fatal("matrix must carry static chart/diagram attachments")
	}
	for _, attachment := range matrix.Attachments {
		if attachment.Format != "svg" {
			t.Fatalf("expected svg for file-capable channel, got %s", attachment.Format)
		}
	}

	// Telegram-style: images but no files -> PNG attachments.
	telegram := Adapt(doc, Capabilities{Markdown: true, Images: true})
	if len(telegram.Attachments) == 0 {
		t.Fatal("telegram must carry PNG attachments")
	}
	for _, attachment := range telegram.Attachments {
		if attachment.Format != "png" {
			t.Fatalf("expected png, got %s", attachment.Format)
		}
	}

	// Text-only channel: no images at all, data survives in the body
	// (§9.4: fallback must not lose critical information).
	textOnly := Adapt(doc, Capabilities{})
	if textOnly.Kind != "text" || len(textOnly.Attachments) != 0 {
		t.Fatalf("text-only delivery: %+v", textOnly)
	}
	if !strings.Contains(textOnly.Body, "942") {
		t.Fatal("text fallback lost the data")
	}
	if len(textOnly.Notes) == 0 {
		t.Fatal("fallback notes must explain the limitation")
	}
}

func TestChannelPresets(t *testing.T) {
	presets := ChannelPresets()
	for _, name := range []string{"web", "matrix", "telegram", "slack", "mattermost", "webhook"} {
		if _, ok := presets[name]; !ok {
			t.Fatalf("preset %s missing", name)
		}
	}
	// Spec §9.3: Telegram receives PNG (photos, not file uploads), Matrix
	// carries SVG as files.
	if presets["telegram"].Files != false || presets["telegram"].Images != true || presets["telegram"].Charts != false {
		t.Fatal("telegram preset drifted")
	}
	if presets["matrix"].Files != true {
		t.Fatal("matrix preset drifted")
	}
}

func TestLifecycleTransitions(t *testing.T) {
	legal := [][2]Status{
		{StatusDraft, StatusPublished},
		{StatusPublished, StatusUpdated},
		{StatusUpdated, StatusUpdated},
		{StatusPublished, StatusArchived},
		{StatusUpdated, StatusExpired},
	}
	for _, move := range legal {
		if !CanTransition(move[0], move[1]) {
			t.Fatalf("%s -> %s must be legal", move[0], move[1])
		}
	}
	illegal := [][2]Status{
		{StatusArchived, StatusPublished},
		{StatusExpired, StatusPublished},
		{StatusArchived, StatusExpired},
	}
	for _, move := range illegal {
		if CanTransition(move[0], move[1]) {
			t.Fatalf("%s -> %s must be illegal", move[0], move[1])
		}
	}
	if err := ValidateTransition(StatusPublished, StatusArchived); err != nil {
		t.Fatalf("legal transition refused: %v", err)
	}
	if err := ValidateTransition(StatusArchived, StatusPublished); err == nil {
		t.Fatal("illegal transition accepted")
	}
}

func TestExtractFencedPresentations(t *testing.T) {
	answer := "До блока текст.\n\n```presentation\n{\"version\": \"1.0\", \"type\": \"presentation\", \"content\": [{\"type\": \"text\", \"content\": \"x\"}]}\n```\n\nОбычный текст после.\n```go\nfmt.Println()\n```\n\n```presentation\n{\"version\": \"1.0\", \"type\": \"presentation\", \"content\": [{\"type\": \"text\", \"content\": \"второй\"}]}\n```"
	blocks := ExtractFencedPresentations(answer)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 presentation blocks, got %d", len(blocks))
	}
	if !strings.Contains(blocks[0], "первого") && !strings.Contains(blocks[0], "\"x\"") {
		t.Fatalf("first block wrong: %s", blocks[0])
	}
	if !strings.Contains(blocks[1], "второй") {
		t.Fatalf("second block wrong: %s", blocks[1])
	}
	// Non-presentation fences and unterminated fences are ignored.
	if got := ExtractFencedPresentations("```presentation\nno close"); len(got) != 0 {
		t.Fatalf("unterminated fence captured: %v", got)
	}
	if got := ExtractFencedPresentations("```json\n{}\n```"); len(got) != 0 {
		t.Fatalf("json fence captured: %v", got)
	}
}

func TestRenderTextEveryComponent(t *testing.T) {
	// §7.6: the text renderer must render every registered component type.
	doc := mustValid(t, `{
		"version": "1.0", "type": "presentation", "title": "Все компоненты",
		"content": [
			{"type": "text", "content": "строка"},
			{"type": "markdown", "content": "**жирный**"},
			{"type": "heading", "content": "Заголовок", "props": {"level": 2}},
			{"type": "paragraph", "content": "абзац"},
			{"type": "list", "props": {"ordered": true}, "items": ["первый", {"label": "второй", "description": "с описанием"}]},
			{"type": "quote", "content": "цитата", "props": {"attribution": "автор"}},
			{"type": "code", "content": "println(42)", "props": {"language": "go"}},
			{"type": "link", "content": "ссылка", "props": {"href": "https://example.com"}},
			{"type": "badge", "content": "новое", "props": {"status": "success"}},
			{"type": "divider"},
			{"type": "table", "data": {"columns": [{"key": "a", "title": "А"}], "rows": [{"a": "1"}]}},
			{"type": "card", "props": {"title": "Карточка"}, "children": [{"type": "text", "content": "внутри"}]},
			{"type": "card-grid", "children": [
				{"type": "card", "props": {"title": "Первая"}, "children": [{"type": "text", "content": "1"}]},
				{"type": "card", "props": {"title": "Вторая"}, "children": [{"type": "text", "content": "2"}]}
			]},
			{"type": "comparison", "items": [
				{"label": "Вариант А", "status": "success", "fields": [{"label": "Цена", "value": "100"}]},
				{"label": "Вариант Б", "fields": [{"label": "Цена", "value": "80"}]}
			]},
			{"type": "key-value", "items": [{"label": "версия", "value": "1.0"}]},
			{"type": "kpi", "items": [{"label": "покрытие", "value": "87%", "status": "success"}]},
			{"type": "progress", "items": [{"label": "готовность", "value": 40, "max": 100}]},
			{"type": "status", "content": "готово", "props": {"status": "success", "detail": "всё хорошо"}},
			{"type": "alert", "content": "внимание", "props": {"severity": "warning", "title": "Проверить"}},
			{"type": "accordion", "items": [{"label": "детали", "content": "тут подробности"}]},
			{"type": "chart", "props": {"chartType": "pie", "title": "Круговая"}, "data": {"labels": ["а", "б"], "values": [3, 1]}},
			{"type": "gauge", "data": {"value": 72, "label": "загрузка", "unit": "%"}},
			{"type": "flowchart", "data": {"nodes": [{"id": "a", "label": "старт"}, {"id": "b", "label": "финиш"}], "edges": [{"from": "a", "to": "b"}]}},
			{"type": "sequence-diagram", "data": {"participants": [{"id": "p", "label": "Клиент"}, {"id": "q", "label": "Сервер"}], "messages": [{"from": "p", "to": "q", "label": "запрос"}]}},
			{"type": "timeline", "items": [{"label": "событие", "at": "2026-01-01"}]},
			{"type": "gantt", "data": {"tasks": [{"label": "работа", "start": "2026-01-01", "end": "2026-01-05"}]}},
			{"type": "image", "content": "скриншот", "props": {"src": "/workspace/shot.png", "caption": "Скрин"}},
			{"type": "image-gallery", "items": [{"src": "/workspace/a.png", "alt": "А"}]},
			{"type": "file", "content": "отчёт", "props": {"name": "report.pdf", "size": "1.2 MB"}},
			{"type": "button", "content": "Одобрить", "actions": [{"type": "submit", "target": "t", "payload": {"decision": "approve"}}]}
		]
	}`)
	text := RenderText(doc)
	for _, needle := range []string{"строка", "жирный", "Заголовок", "1.", "цитата", "автор", "println(42)", "https://example.com", "SUCCESS", "новое", "А", "1", "Карточка", "внутри", "Вариант А", "версия", "покрытие", "готовность", "готово", "всё хорошо", "Проверить", "детали", "Круговая", "72", "старт", "Клиент", "событие", "работа", "Скрин", "report.pdf", "Одобрить"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("text renderer lost %q from:\n%s", needle, text)
		}
	}
	// Every component type also lays out and renders to SVG+PNG without
	// error (the four-format DoD, §7.7).
	drawing := LayoutDocument(doc, LayoutOptions{IncludeTitle: true})
	if len(RenderSVG(drawing)) == 0 {
		t.Fatal("empty svg")
	}
	if _, err := RenderPNG(drawing); err != nil {
		t.Fatalf("png failed: %v", err)
	}
}

func TestCyrillicInStaticFormats(t *testing.T) {
	// §7.7: Cyrillic must survive every format.
	doc := mustValid(t, `{
		"version": "1.0", "type": "presentation",
		"content": [{"type": "text", "content": "Привет, мир! Тестирование кириллицы."}]
	}`)
	text := RenderText(doc)
	if !strings.Contains(text, "Привет, мир") {
		t.Fatal("cyrillic lost in text")
	}
	drawing := LayoutDocument(doc, LayoutOptions{})
	if !drawingContainsText(drawing, "Привет, мир") {
		t.Fatal("cyrillic lost in svg drawing")
	}
	if _, err := RenderPNG(drawing); err != nil {
		t.Fatalf("cyrillic png failed: %v", err)
	}
}
