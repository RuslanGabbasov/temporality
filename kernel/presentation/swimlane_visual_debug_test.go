package presentation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Temporary visual check: renders the screenshot's swimlane to a PNG for
// inspection. Gated behind PRESENTATION_DEBUG=1 like the other debug renders.
func TestSwimlaneVisualDebug(t *testing.T) {
	if debugRenderOff() {
		t.Skip("set PRESENTATION_DEBUG=1 to write /tmp/swimlane-debug.png")
	}
	data := SwimlaneData{
		Lanes: []SwimlaneLane{
			{ID: "requester", Label: "Заявитель (человек)"},
			{ID: "agent", Label: "Агент-аналитик"},
			{ID: "registry", Label: "Реестр артефактов"},
			{ID: "approver", Label: "Согласующие (люди)"},
			{ID: "escalation", Label: "Эскалация"},
		},
		Nodes: []SwimlaneNode{
			{ID: "req", Lane: "requester", Label: "Заявка: Matrix…", Start: 0},
			{ID: "job", Lane: "agent", Label: "Job Statement …", Start: 0},
			{ID: "draft", Lane: "agent", Label: "Черновик ТЗ: г…", Start: 1},
			{ID: "dor", Lane: "agent", Label: "DoR-гейт: чек-…", Start: 2},
			{ID: "fixes", Lane: "agent", Label: "Правки по ано…", Start: 3},
			{ID: "created", Lane: "registry", Label: "artifact_creat…", Start: 0},
			{ID: "submitted", Lane: "registry", Label: "artifact_submi…", Start: 1},
			{ID: "approved", Lane: "registry", Label: "approved — кан…", Status: "success", Start: 2},
			{ID: "approve", Lane: "approver", Label: "Аппрув / аннот…", Start: 0},
			{ID: "silence", Lane: "escalation", Label: "Молчание > 2 д…", Status: "warning", Start: 0},
		},
		Edges: []SwimlaneEdge{
			{From: "req", To: "job"},
			{From: "job", To: "draft"},
			{From: "draft", To: "dor"},
			{From: "dor", To: "fixes"},
			{From: "draft", To: "created"},
			{From: "created", To: "submitted"},
			{From: "submitted", To: "approved"},
			{From: "dor", To: "approved", Label: "DoR пройден"},
			{From: "submitted", To: "approve"},
			{From: "approve", To: "submitted", Label: "нет ответа", Dashed: true},
			{From: "approved", To: "approve", Label: "правки"},
			{From: "approve", To: "silence", Label: "напоминание руковод…", Dashed: true},
		},
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	propsJSON, err := json.Marshal(SwimlaneProps{Title: "Схема процесса (swimlane): кто что делает на каждом шаре"})
	if err != nil {
		t.Fatalf("marshal props: %v", err)
	}
	doc := Document{
		Version: "1.0", Type: "presentation", ID: "vis", Title: "Swimlane visual",
		Content: []Component{{Type: "swimlane", ID: "sl", Data: dataJSON, Props: propsJSON}},
	}
	drawing := LayoutDocument(doc, LayoutOptions{Width: 760, IncludeTitle: true})
	raw, err := RenderPNG(drawing)
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	if err := os.WriteFile(filepath.Join(os.TempDir(), "swimlane-debug.png"), raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("wrote %s (%d bytes)", filepath.Join(os.TempDir(), "swimlane-debug.png"), len(raw))
}
