package cognition_test

import (
	"encoding/json"
	"testing"

	"github.com/temporality-project/temporality/frp/cognition"
)

func TestObservationObjectRefNormalizesToCanonicalString(t *testing.T) {
	data := []byte(`{"schema":"frp.cognitive-emission.v1","emission_id":"e","frame_id":"f","observation":[{"ref":{"type":"event","id":"123"},"interpretation":"seen"}],"reasoning":[],"claims":[],"attention":[],"actions":[],"frame_ops":[],"completion":null}`)
	var emission cognition.CognitiveEmission
	if err := json.Unmarshal(data, &emission); err != nil {
		t.Fatal(err)
	}
	if err := emission.Validate(); err != nil {
		t.Fatal(err)
	}
	if emission.Observation[0].Ref != "event:123" {
		t.Fatalf("ref not normalized: %q", emission.Observation[0].Ref)
	}
	encoded, err := json.Marshal(emission)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip map[string]any
	if err = json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	observation := roundTrip["observation"].([]any)[0].(map[string]any)
	if _, ok := observation["ref"].(string); !ok {
		t.Fatalf("canonical output ref is not string: %s", encoded)
	}
}

func TestObservationQueryRefsAreTransientButValid(t *testing.T) {
	for _, raw := range []string{`"query:Что ты можешь?"`, `{"type":"query","text":"Что ты можешь?"}`} {
		data := []byte(`{"schema":"frp.cognitive-emission.v1","emission_id":"e","frame_id":"f","observation":[{"ref":` + raw + `,"interpretation":"visible focus"}],"reasoning":[],"claims":[],"attention":[],"actions":[],"frame_ops":[],"completion":null}`)
		var emission cognition.CognitiveEmission
		if err := json.Unmarshal(data, &emission); err != nil {
			t.Fatal(err)
		}
		if err := emission.Validate(); err != nil {
			t.Fatal(err)
		}
		if emission.Observation[0].Ref != "query:Что ты можешь?" {
			t.Fatalf("unexpected query ref: %q", emission.Observation[0].Ref)
		}
	}
}

func TestObservationRejectsArbitraryObjectRef(t *testing.T) {
	var emission cognition.CognitiveEmission
	if err := json.Unmarshal([]byte(`{"observation":[{"ref":{"event_id":"123"},"interpretation":"seen"}]}`), &emission); err == nil {
		t.Fatal("arbitrary ref object accepted")
	}
}

func TestFrameOperationObjectRefNormalizesToCanonicalString(t *testing.T) {
	data := []byte(`{"schema":"frp.cognitive-emission.v1","emission_id":"e","frame_id":"f","observation":[],"reasoning":[],"claims":[],"attention":[],"actions":[],"frame_ops":[{"op":"pin","ref":{"type":"claim","id":"abc"}},{"op":"unpin","ref":"event:def"},{"op":"pin","ref":""}],"completion":null}`)
	var emission cognition.CognitiveEmission
	if err := json.Unmarshal(data, &emission); err != nil {
		t.Fatal(err)
	}
	if err := emission.Validate(); err != nil {
		t.Fatal(err)
	}
	if emission.FrameOps[0].Ref != "claim:abc" || emission.FrameOps[1].Ref != "event:def" {
		t.Fatalf("frame op refs not normalized: %#v", emission.FrameOps)
	}
	emission.ApplyDefaults()
	if len(emission.FrameOps) != 2 {
		t.Fatalf("empty frame op refs must be dropped: %#v", emission.FrameOps)
	}
	encoded, err := json.Marshal(emission)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip map[string]any
	if err = json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	firstOp := roundTrip["frame_ops"].([]any)[0].(map[string]any)
	if _, ok := firstOp["ref"].(string); !ok {
		t.Fatalf("canonical frame op ref is not string: %s", encoded)
	}
}

func TestUnanchoredObservationsAreToleratedAndDropped(t *testing.T) {
	data := []byte(`{"schema":"frp.cognitive-emission.v1","emission_id":"e","frame_id":"f","observation":[{"ref":"","interpretation":"no anchor"},{"ref":"event:123","interpretation":"seen"},{"interpretation":"ref omitted entirely"}],"reasoning":[],"claims":[],"attention":[],"actions":[],"frame_ops":[],"completion":null}`)
	var emission cognition.CognitiveEmission
	if err := json.Unmarshal(data, &emission); err != nil {
		t.Fatal(err)
	}
	if err := emission.Validate(); err != nil {
		t.Fatalf("empty observation refs must not fail validation: %v", err)
	}
	emission.ApplyDefaults()
	if len(emission.Observation) != 1 || emission.Observation[0].Ref != "event:123" {
		t.Fatalf("unanchored observations must be dropped: %#v", emission.Observation)
	}
}

func TestCompletionAcceptsObjectShapes(t *testing.T) {
	for name, raw := range map[string]string{
		"string":        `"готово"`,
		"object text":   `{"text":"готово"}`,
		"object answer": `{"answer":"готово"}`,
		"object mixed":  `{"summary":"","content":"готово"}`,
		"null":          `null`,
		"empty string":  `""`,
	} {
		data := []byte(`{"schema":"frp.cognitive-emission.v1","emission_id":"e","frame_id":"f","observation":[],"reasoning":[],"claims":[],"attention":[],"actions":[],"frame_ops":[],"completion":` + raw + `}`)
		var emission cognition.CognitiveEmission
		if err := json.Unmarshal(data, &emission); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "null" || name == "empty string" {
			if emission.Completion != nil {
				t.Fatalf("%s: completion must stay nil: %q", name, *emission.Completion)
			}
			continue
		}
		if emission.Completion == nil || *emission.Completion != "готово" {
			t.Fatalf("%s: completion not normalized: %#v", name, emission.Completion)
		}
	}
	var invalid cognition.CognitiveEmission
	if err := json.Unmarshal([]byte(`{"completion":42}`), &invalid); err == nil {
		t.Fatal("numeric completion accepted")
	}
}

func TestActionNormalizesToolCallShapes(t *testing.T) {
	for name, raw := range map[string]string{
		"id/arguments":           `{"id":"read_file","arguments":{"path":"go.mod"}}`,
		"name/params":            `{"name":"git_log","params":{"path":".","limit":5}}`,
		"name/parameters":        `{"name":"git_status","parameters":{"path":"."}}`,
		"canonical wins over id": `{"affordance":"list_files","id":"stale","args":{"path":"."}}`,
	} {
		var action cognition.ActionRequest
		if err := json.Unmarshal([]byte(raw), &action); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		switch name {
		case "id/arguments":
			if action.Affordance != "read_file" || action.Args["path"] != "go.mod" {
				t.Fatalf("%s not normalized: %#v", name, action)
			}
		case "name/params":
			if action.Affordance != "git_log" || action.Args["limit"] != float64(5) {
				t.Fatalf("%s not normalized: %#v", name, action)
			}
		case "name/parameters":
			if action.Affordance != "git_status" || action.Args["path"] != "." {
				t.Fatalf("%s not normalized: %#v", name, action)
			}
		case "canonical wins over id":
			if action.Affordance != "list_files" {
				t.Fatalf("%s: canonical affordance must win: %#v", name, action)
			}
		}
		encoded, err := json.Marshal(action)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip map[string]any
		if err = json.Unmarshal(encoded, &roundTrip); err != nil {
			t.Fatal(err)
		}
		if _, ok := roundTrip["affordance"]; !ok || roundTrip["id"] != nil {
			t.Fatalf("%s: output is not canonical: %s", name, encoded)
		}
	}
}
