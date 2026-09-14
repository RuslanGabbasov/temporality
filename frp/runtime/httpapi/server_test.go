package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/temporality-project/temporality/frp/runtime/httpapi"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

func TestAppendGetAndReplay(t *testing.T) {
	handler := httpapi.New(memory.New(),slog.New(slog.NewTextHandler(io.Discard,nil)))
	body := []byte(`{"event_id":"018f47a7-34b2-7d10-a932-4f3ff37a4a01","episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a4a02","type":"episode.started","payload":{},"provenance":{"source":"test"}}`)
	created := serve(handler,http.MethodPost,"/v1/events",body)
	if created.Code != http.StatusCreated { t.Fatalf("append status=%d body=%s",created.Code,created.Body.String()) }

	got := serve(handler,http.MethodGet,"/v1/events/018f47a7-34b2-7d10-a932-4f3ff37a4a01",nil)
	if got.Code != http.StatusOK { t.Fatalf("get status=%d body=%s",got.Code,got.Body.String()) }

	replayed := serve(handler,http.MethodPost,"/v1/replay",[]byte(`{"episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a4a02"}`))
	if replayed.Code != http.StatusOK { t.Fatalf("replay status=%d body=%s",replayed.Code,replayed.Body.String()) }
	var response struct { Events []json.RawMessage `json:"events"`; Digest string `json:"digest"` }
	if err := json.Unmarshal(replayed.Body.Bytes(),&response); err != nil { t.Fatal(err) }
	if len(response.Events) != 1 || response.Digest == "" { t.Fatalf("unexpected replay response: %s",replayed.Body.String()) }
}

func serve(handler http.Handler, method,path string,body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method,path,bytes.NewReader(body))
	request.Header.Set("content-type","application/json")
	response := httptest.NewRecorder(); handler.ServeHTTP(response,request); return response
}
