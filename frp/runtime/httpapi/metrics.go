package httpapi

import (
	"fmt"
	"math"
	"net/http"
	"sync/atomic"

	"github.com/temporality-project/temporality/frp/render"
)

type metrics struct {
	httpRequests        atomic.Uint64
	eventsAppended      atomic.Uint64
	appendErrors        atomic.Uint64
	replays             atomic.Uint64
	replayErrors        atomic.Uint64
	replayDurationNanos atomic.Uint64
	renders             atomic.Uint64
	renderErrors        atomic.Uint64
	focusSwitches       atomic.Uint64
	ambientConsidered   atomic.Uint64
	ambientSelected     atomic.Uint64
	missedCandidates    atomic.Uint64
	attentionEntropy    atomic.Uint64
	attentionCollapse   atomic.Uint64
}

func (m *metrics) observeRender(packet render.Packet) {
	scores := make([]float64, 0)
	for _, section := range packet.Sections {
		if section.Kind != "map" {
			continue
		}
		for _, item := range section.Items {
			if candidate, ok := item.(render.MapItem); ok {
				scores = append(scores, candidate.Score)
			}
		}
	}
	considered := len(scores) + packet.OutsideFrame.NearbyRegions
	m.ambientConsidered.Add(uint64(considered))
	m.ambientSelected.Add(uint64(len(scores)))
	m.missedCandidates.Add(uint64(packet.OutsideFrame.NearbyRegions))
	entropy, collapse := attentionDistribution(scores)
	m.attentionEntropy.Store(math.Float64bits(entropy))
	m.attentionCollapse.Store(math.Float64bits(collapse))
}
func attentionDistribution(scores []float64) (float64, float64) {
	sum, max := 0.0, 0.0
	for _, score := range scores {
		if score < 0 {
			score = 0
		}
		sum += score
		if score > max {
			max = score
		}
	}
	if sum == 0 {
		return 0, 0
	}
	entropy := 0.0
	for _, score := range scores {
		if score <= 0 {
			continue
		}
		p := score / sum
		entropy -= p * math.Log(p)
	}
	return entropy, max / sum
}
func ratio(numerator, denominator uint64) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func (m *metrics) handler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("content-type", "text/plain; version=0.0.4; charset=utf-8")
	considered, selected := m.ambientConsidered.Load(), m.ambientSelected.Load()
	_, _ = fmt.Fprintf(w, `# TYPE temporality_http_requests_total counter
temporality_http_requests_total %d
# TYPE temporality_events_appended_total counter
temporality_events_appended_total %d
# TYPE temporality_event_append_errors_total counter
temporality_event_append_errors_total %d
# TYPE temporality_replays_total counter
temporality_replays_total %d
# TYPE temporality_replay_errors_total counter
temporality_replay_errors_total %d
# TYPE temporality_replay_duration_seconds_total counter
temporality_replay_duration_seconds_total %.9f
# TYPE temporality_renders_total counter
temporality_renders_total %d
# TYPE temporality_render_errors_total counter
temporality_render_errors_total %d
# TYPE temporality_focus_switches_total counter
temporality_focus_switches_total %d
# TYPE temporality_ambient_candidates_total counter
temporality_ambient_candidates_total %d
# TYPE temporality_ambient_selected_total counter
temporality_ambient_selected_total %d
# TYPE temporality_missed_relevant_items_total counter
temporality_missed_relevant_items_total %d
# TYPE temporality_ambient_hit_rate gauge
temporality_ambient_hit_rate %.9f
# TYPE temporality_attention_entropy gauge
temporality_attention_entropy %.9f
# TYPE temporality_attention_collapse_score gauge
temporality_attention_collapse_score %.9f
`, m.httpRequests.Load(), m.eventsAppended.Load(), m.appendErrors.Load(), m.replays.Load(), m.replayErrors.Load(), float64(m.replayDurationNanos.Load())/1e9, m.renders.Load(), m.renderErrors.Load(), m.focusSwitches.Load(), considered, selected, m.missedCandidates.Load(), ratio(selected, considered), math.Float64frombits(m.attentionEntropy.Load()), math.Float64frombits(m.attentionCollapse.Load()))
}
func (m *metrics) count(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { m.httpRequests.Add(1); next.ServeHTTP(w, r) })
}
