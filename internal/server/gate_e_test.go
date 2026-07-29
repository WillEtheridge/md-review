package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mdreview.dev/mdreview/internal/gatee"
)

func TestGateECounterEndpointIsOptIn(t *testing.T) {
	disabled := newTestServer(t)
	response := httptest.NewRecorder()
	disabled.ServeHTTP(
		response,
		authenticatedRequest(
			http.MethodGet,
			"http://127.0.0.1:4242/api/gate-e/counters",
		),
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("disabled status = %d, want %d", response.Code, http.StatusNotFound)
	}
	assertErrorCode(t, response, "endpointNotFound")

	counters := &gatee.Counters{}
	counters.RecordCompleteWorkspaceScan()
	counters.RecordMarkdownContentRead(12)
	enabled := newTestServer(t)
	enabled.measurements = counters

	response = httptest.NewRecorder()
	enabled.ServeHTTP(
		response,
		authenticatedRequest(
			http.MethodGet,
			"http://127.0.0.1:4242/api/gate-e/counters",
		),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("enabled status = %d, body %q", response.Code, response.Body.String())
	}
	var observed gatee.Snapshot
	if err := json.Unmarshal(response.Body.Bytes(), &observed); err != nil {
		t.Fatal(err)
	}
	if observed.CompleteWorkspaceScans != 1 ||
		observed.MarkdownContentOpens != 1 ||
		observed.MarkdownContentBytes != 12 {
		t.Fatalf("enabled counters = %+v", observed)
	}
}
