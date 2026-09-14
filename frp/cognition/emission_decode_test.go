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

func TestObservationRejectsArbitraryObjectRef(t *testing.T) {
	var emission cognition.CognitiveEmission
	if err := json.Unmarshal([]byte(`{"observation":[{"ref":{"event_id":"123"},"interpretation":"seen"}]}`), &emission); err == nil {
		t.Fatal("arbitrary ref object accepted")
	}
}
