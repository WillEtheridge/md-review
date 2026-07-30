package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mdreview.dev/mdreview/internal/review"
	"mdreview.dev/mdreview/internal/workspace"
)

func TestReviewOperationsDecodeFrozenContractsAndReturnAffectedState(t *testing.T) {
	documentRevision := strings.Repeat("d", 64)
	reviewRevision := strings.Repeat("e", 64)
	mutationResult := review.MutationResult{
		DocumentRevision: documentRevision,
		ReviewRevision:   reviewRevision,
		Thread: review.ResolvedThread{
			ID:         "thread_existing",
			Anchor:     review.Anchor{Type: review.AnchorDocument},
			Attachment: review.Attachment{State: review.AttachmentDocument},
			Status:     review.StatusOpen,
			Messages:   []review.Message{},
		},
		Targets: review.TargetFingerprints{
			Threads:  map[string]string{"thread_existing": strings.Repeat("1", 64)},
			Messages: map[string]string{},
		},
	}
	testWorkspace := fakeWorkspace{document: workspace.DocumentContent{
		Path: "README.md", Revision: documentRevision, Source: "# Review\n",
	}}

	t.Run("reply", func(t *testing.T) {
		var captured review.ReplyInput
		server := newTestServerWithServices(t, testWorkspace, fakeReviewStore{
			replyResult: mutationResult,
			replyInput:  &captured,
		})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, reviewOperationRequestForTest(
			t,
			http.MethodPost,
			"/api/threads/"+routeID("thread_existing")+"/messages",
			m3ContractFixture(t, "reply-request.json"),
		))
		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		if captured.ThreadID != "thread_existing" ||
			captured.DocumentPath != "README.md" ||
			captured.MessageBody != "This reply reopens the thread." ||
			captured.TargetFingerprint != strings.Repeat("1", 64) {
			t.Fatalf("captured reply = %#v", captured)
		}
		assertMutationResponse(t, response, mutationResult)
	})

	t.Run("edit message", func(t *testing.T) {
		var captured review.EditMessageInput
		server := newTestServerWithServices(t, testWorkspace, fakeReviewStore{
			editResult: mutationResult,
			editInput:  &captured,
		})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, reviewOperationRequestForTest(
			t,
			http.MethodPatch,
			"/api/messages/"+routeID("message_/ %"),
			m3ContractFixture(t, "edit-message-request.json"),
		))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		if captured.MessageID != "message_/ %" ||
			captured.MessageBody != "Please clarify the introduction and conclusion." ||
			captured.TargetFingerprint != strings.Repeat("2", 64) {
			t.Fatalf("captured edit = %#v", captured)
		}
		assertMutationResponse(t, response, mutationResult)
	})

	t.Run("status", func(t *testing.T) {
		var captured review.ChangeStatusInput
		server := newTestServerWithServices(t, testWorkspace, fakeReviewStore{
			statusResult: mutationResult,
			statusInput:  &captured,
		})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, reviewOperationRequestForTest(
			t,
			http.MethodPatch,
			"/api/threads/"+routeID("..")+"/status",
			m3ContractFixture(t, "status-request.json"),
		))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		if captured.ThreadID != ".." ||
			captured.Status != review.StatusResolved ||
			captured.TargetFingerprint != strings.Repeat("1", 64) {
			t.Fatalf("captured status = %#v", captured)
		}
		assertMutationResponse(t, response, mutationResult)
	})

	t.Run("delete", func(t *testing.T) {
		var captured review.DeleteThreadInput
		deleteResult := review.DeleteThreadResult{
			DocumentRevision: documentRevision,
			ReviewRevision:   reviewRevision,
			DeletedThreadID:  "线程/one",
		}
		server := newTestServerWithServices(t, testWorkspace, fakeReviewStore{
			deleteResult: deleteResult,
			deleteInput:  &captured,
		})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, reviewOperationRequestForTest(
			t,
			http.MethodDelete,
			"/api/threads/"+routeID("线程/one"),
			m3ContractFixture(t, "delete-thread-request.json"),
		))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		if captured.ThreadID != "线程/one" ||
			captured.TargetFingerprint != strings.Repeat("1", 64) {
			t.Fatalf("captured delete = %#v", captured)
		}
		var result review.DeleteThreadResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode delete response: %v", err)
		}
		if result != deleteResult {
			t.Fatalf("delete response = %#v, want %#v", result, deleteResult)
		}
	})
}

func TestReviewOperationRoutesEnforceMethodsAndMutationBoundary(t *testing.T) {
	validBody := m3ContractFixture(t, "reply-request.json")
	route := "/api/threads/" + routeID("thread_existing") + "/messages"
	tests := []struct {
		name        string
		method      string
		body        []byte
		prepare     func(*http.Request)
		status      int
		code        string
		allowMethod string
	}{
		{
			name: "wrong method", method: http.MethodPatch, body: validBody,
			status: http.StatusMethodNotAllowed, code: "methodNotAllowed",
			allowMethod: http.MethodPost,
		},
		{
			name: "missing origin", method: http.MethodPost, body: validBody,
			prepare: func(request *http.Request) {
				request.Header.Del("Origin")
			},
			status: http.StatusForbidden, code: "invalidOrigin",
		},
		{
			name: "non JSON", method: http.MethodPost, body: validBody,
			prepare: func(request *http.Request) {
				request.Header.Set("Content-Type", "text/plain")
			},
			status: http.StatusUnsupportedMediaType, code: "unsupportedMediaType",
		},
		{
			name: "unknown field", method: http.MethodPost,
			body:   []byte(`{"documentPath":"README.md","unexpected":true}`),
			status: http.StatusUnprocessableEntity, code: "invalidReviewOperation",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := reviewOperationRequestForTest(t, test.method, route, test.body)
			if test.prepare != nil {
				test.prepare(request)
			}
			response := httptest.NewRecorder()
			newTestServer(t).ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf(
					"status = %d, want %d; body = %s",
					response.Code,
					test.status,
					response.Body.String(),
				)
			}
			assertErrorCode(t, response, test.code)
			if test.allowMethod != "" &&
				response.Header().Get("Allow") != test.allowMethod {
				t.Fatalf(
					"Allow = %q, want %q",
					response.Header().Get("Allow"),
					test.allowMethod,
				)
			}
		})
	}
}

func TestTargetChangedUsesFrozenConflictShape(t *testing.T) {
	currentFingerprint := strings.Repeat("f", 64)
	current := review.CurrentTargetState{
		DocumentRevision:  strings.Repeat("d", 64),
		ReviewRevision:    nil,
		TargetFingerprint: &currentFingerprint,
	}
	server := newTestServerWithServices(t, fakeWorkspace{
		document: workspace.DocumentContent{Path: "README.md"},
	}, fakeReviewStore{
		replyErr: &review.TargetChangedError{Current: current},
	})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, reviewOperationRequestForTest(
		t,
		http.MethodPost,
		"/api/threads/"+routeID("thread_existing")+"/messages",
		m3ContractFixture(t, "reply-request.json"),
	))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var conflict targetConflictResponse
	if err := json.Unmarshal(response.Body.Bytes(), &conflict); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if conflict.Error.Code != "targetChanged" ||
		conflict.Current.DocumentRevision != current.DocumentRevision ||
		conflict.Current.TargetFingerprint == nil ||
		*conflict.Current.TargetFingerprint != currentFingerprint {
		t.Fatalf("conflict = %#v", conflict)
	}
}

func assertMutationResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	want review.MutationResult,
) {
	t.Helper()
	var result review.MutationResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode mutation response: %v", err)
	}
	if result.ReviewRevision != want.ReviewRevision ||
		result.Thread.ID != want.Thread.ID ||
		result.Targets.Threads["thread_existing"] != want.Targets.Threads["thread_existing"] {
		t.Fatalf("mutation response = %#v, want %#v", result, want)
	}
}

func reviewOperationRequestForTest(
	t *testing.T,
	method string,
	urlPath string,
	body []byte,
) *http.Request {
	t.Helper()
	request := httptest.NewRequest(
		method,
		"http://127.0.0.1:4242"+urlPath,
		bytes.NewReader(body),
	)
	request.Header.Set("Origin", "http://127.0.0.1:4242")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func m3ContractFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "testdata", "contracts", "m3", name))
	if err != nil {
		t.Fatalf("read M3 contract %s: %v", name, err)
	}
	return data
}

func routeID(id string) string {
	return "~" + base64.RawURLEncoding.EncodeToString([]byte(id))
}
