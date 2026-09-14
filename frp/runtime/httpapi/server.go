package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/replay"
	"github.com/temporality-project/temporality/frp/substrate"
)

type Server struct {
	store substrate.EventStore
	replay *replay.Service
	log *slog.Logger
	now func() time.Time
}

func New(store substrate.EventStore, log *slog.Logger) http.Handler {
	s := &Server{store:store, replay:replay.New(store), log:log, now:time.Now}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/events", s.appendEvent)
	mux.HandleFunc("GET /v1/events/{id}", s.getEvent)
	mux.HandleFunc("POST /v1/replay", s.replayEvents)
	return mux
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, map[string]string{"status":"ok","protocol":protocol.Name,"version":protocol.Version}) }

func (s *Server) appendEvent(w http.ResponseWriter, r *http.Request) {
	var event protocol.Event
	if err := decodeJSON(r, &event); err != nil { writeError(w,http.StatusBadRequest,err); return }
	if event.EventID == "" { event.EventID = newUUID() }
	event.ApplyDefaults(s.now())
	if err := event.Validate(); err != nil { writeError(w,http.StatusUnprocessableEntity,err); return }
	if err := s.store.Append(r.Context(), event); err != nil { writeError(w,http.StatusConflict,err); return }
	writeJSON(w,http.StatusCreated,event)
}

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	event, err := s.store.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, substrate.ErrNotFound) { writeError(w,http.StatusNotFound,err); return }
	if err != nil { s.log.Error("get event", "error", err); writeError(w,http.StatusInternalServerError,errors.New("internal error")); return }
	writeJSON(w,http.StatusOK,event)
}

type replayRequest struct { EpisodeID string `json:"episode_id"`; BranchID string `json:"branch_id"`; AsOf *time.Time `json:"as_of,omitempty"` }
func (s *Server) replayEvents(w http.ResponseWriter, r *http.Request) {
	var request replayRequest
	if err := decodeJSON(r,&request); err != nil { writeError(w,http.StatusBadRequest,err); return }
	result, err := s.replay.Replay(r.Context(),substrate.EventFilter{EpisodeID:request.EpisodeID,BranchID:request.BranchID,AsOf:request.AsOf})
	if err != nil { s.log.Error("replay", "error", err); writeError(w,http.StatusInternalServerError,errors.New("internal error")); return }
	writeJSON(w,http.StatusOK,result)
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
func writeJSON(w http.ResponseWriter, status int, value any) { w.Header().Set("content-type","application/json"); w.WriteHeader(status); _ = json.NewEncoder(w).Encode(value) }
func writeError(w http.ResponseWriter, status int, err error) { writeJSON(w,status,map[string]string{"error":err.Error()}) }
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil { panic(err) }
	b[6] = (b[6] & 0x0f) | 0x40; b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return strings.Join([]string{h[0:8],h[8:12],h[12:16],h[16:20],h[20:32]},"-")
}
