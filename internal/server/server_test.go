package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"mdreview.dev/mdreview/internal/limits"
	"mdreview.dev/mdreview/internal/review"
	"mdreview.dev/mdreview/internal/workspace"
)

func TestRequestIDFailureRetainsSecurityAndErrorContract(t *testing.T) {
	server, err := New(Config{
		Assets: fstest.MapFS{
			"index.html": &fstest.MapFile{Data: []byte("application shell")},
		},
		Workspace: fakeWorkspace{},
		Review:    fakeReviewStore{},
		BoundHost: "127.0.0.1:4242",
		NewRequestID: func() (string, error) {
			return "", errors.New("entropy unavailable")
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	response := httptest.NewRecorder()
	server.ServeHTTP(
		response,
		authenticatedRequest(http.MethodGet, "http://127.0.0.1:4242/api/state"),
	)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	assertSecurityHeaders(t, response)
	assertErrorCode(t, response, "internalError")
	var envelope errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if got := response.Header().Get("X-Request-ID"); got != fallbackRequestID ||
		envelope.Error.RequestID != got {
		t.Fatalf(
			"request IDs = header %q, envelope %q, want %q",
			got,
			envelope.Error.RequestID,
			fallbackRequestID,
		)
	}
}

func TestServerRejectsWrongHostAndAPIMethods(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "http://localhost:4242/", nil)
	request.Host = "localhost:4242"
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("wrong host status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	assertErrorCode(t, response, "invalidHost")

	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4242/api/state", nil)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("method response = %d, Allow %q", response.Code, response.Header().Get("Allow"))
	}
	assertErrorCode(t, response, "methodNotAllowed")
}

func TestServerServesShellAndImmutableAssets(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4242/", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("shell response = %d, cache %q", response.Code, response.Header().Get("Cache-Control"))
	}
	assertSecurityHeaders(t, response)
	if !strings.Contains(response.Body.String(), "application shell") {
		t.Fatalf("shell body = %q", response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4242/assets/app-abcdef.js", nil)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("asset response = %d, cache %q", response.Code, response.Header().Get("Cache-Control"))
	}
}

func TestServerRejectsTraversalAndUnknownAPIs(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4242/%2e%2e/assets/app-abcdef.js", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("traversal status = %d, want %d", response.Code, http.StatusNotFound)
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4242/api/not-real", nil)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown API status = %d, want %d", response.Code, http.StatusNotFound)
	}
	assertErrorCode(t, response, "endpointNotFound")
}

func TestStateAndDocumentUseFrozenTransportCasing(t *testing.T) {
	initial := "README.md"
	server := newTestServerWithWorkspace(t, fakeWorkspace{
		snapshot: workspace.Snapshot{
			Revision:            1,
			DocumentCount:       1,
			InitialDocumentPath: &initial,
			Navigation: []workspace.NavigationEntry{{
				Kind: workspace.EntryKindDocument, Name: "README.md", Path: "README.md", SizeBytes: 10,
				Availability: workspace.AvailabilityReady, DocumentMetadataRevision: strings.Repeat("c", 64),
			}},
			Warnings: []workspace.Warning{{Path: "vendor/.gitignore", Code: workspace.WarningCodeIgnoreFileTooLarge, Message: "This ignore file exceeds 1 MiB and was skipped."}},
		},
		document: workspace.DocumentContent{Path: "README.md", Revision: strings.Repeat("a", 64), Source: "# mdReview\n"},
	})
	request := authenticatedRequest(http.MethodGet, "http://127.0.0.1:4242/api/state")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("state status = %d", response.Code)
	}
	var state stateResponse
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.WorkspaceRevision != 1 || state.DocumentCount != 1 || state.InitialDocumentPath == nil ||
		*state.InitialDocumentPath != "README.md" || state.Navigation[0].Availability == nil || *state.Navigation[0].Availability != "ready" ||
		state.Navigation[0].DocumentMetadataRevision == nil ||
		*state.Navigation[0].DocumentMetadataRevision != strings.Repeat("c", 64) ||
		state.Warnings[0].Code != "ignoreFileTooLarge" || state.Status != "changed" {
		t.Fatalf("state = %#v", state)
	}
	var rawState map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &rawState); err != nil {
		t.Fatalf("decode raw state: %v", err)
	}
	rawNavigation := rawState["navigation"].([]any)
	rawDocument := rawNavigation[0].(map[string]any)
	if reviewRevision, exists := rawDocument["reviewMetadataRevision"]; !exists || reviewRevision != nil {
		t.Fatalf("reviewMetadataRevision = %#v, exists %t; want explicit null", reviewRevision, exists)
	}

	request = authenticatedRequest(http.MethodGet, "http://127.0.0.1:4242/api/document?path=README.md")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("document status = %d", response.Code)
	}
	var document documentResponse
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	if document.Path != "README.md" || document.Source != "# mdReview\n" || document.Revision != strings.Repeat("a", 64) {
		t.Fatalf("document = %#v", document)
	}
}

func TestStateConditionalTransportAndValidation(t *testing.T) {
	server := newTestServerWithWorkspace(t, fakeWorkspace{snapshot: workspace.Snapshot{Revision: 8}})

	response := httptest.NewRecorder()
	server.ServeHTTP(
		response,
		authenticatedRequest(http.MethodGet, "http://127.0.0.1:4242/api/state?since=8"),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("unchanged state status = %d", response.Code)
	}
	var unchanged stateUnchangedResponse
	if err := json.Unmarshal(response.Body.Bytes(), &unchanged); err != nil {
		t.Fatalf("decode unchanged state: %v", err)
	}
	if unchanged != (stateUnchangedResponse{Status: "unchanged", WorkspaceRevision: 8}) {
		t.Fatalf("unchanged state = %#v", unchanged)
	}

	response = httptest.NewRecorder()
	server.ServeHTTP(
		response,
		authenticatedRequest(http.MethodGet, "http://127.0.0.1:4242/api/state?since=7"),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("changed state status = %d", response.Code)
	}
	var changed stateResponse
	if err := json.Unmarshal(response.Body.Bytes(), &changed); err != nil {
		t.Fatalf("decode changed state: %v", err)
	}
	if changed.Status != "changed" || changed.WorkspaceRevision != 8 {
		t.Fatalf("changed state = %#v", changed)
	}

	for _, rawQuery := range []string{
		"since=",
		"since=0",
		"since=-1",
		"since=not-a-number",
		"since=18446744073709551616",
		"since=8&since=8",
		"other=8",
		"since=8&other=8",
	} {
		response = httptest.NewRecorder()
		server.ServeHTTP(
			response,
			authenticatedRequest(
				http.MethodGet,
				"http://127.0.0.1:4242/api/state?"+rawQuery,
			),
		)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("query %q status = %d, want %d", rawQuery, response.Code, http.StatusBadRequest)
		}
		assertErrorCode(t, response, "invalidWorkspaceRevision")
	}
}

func TestStateIncludesZeroByteDocumentSize(t *testing.T) {
	server := newTestServerWithWorkspace(t, fakeWorkspace{snapshot: workspace.Snapshot{
		Revision: 1,
		Navigation: []workspace.NavigationEntry{{
			Kind: workspace.EntryKindDocument, Name: "empty.md", Path: "empty.md", Availability: workspace.AvailabilityReady,
		}},
	}})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, authenticatedRequest(http.MethodGet, "http://127.0.0.1:4242/api/state"))
	if response.Code != http.StatusOK {
		t.Fatalf("state status = %d", response.Code)
	}
	var encoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &encoded); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	navigation := encoded["navigation"].([]any)
	document := navigation[0].(map[string]any)
	if size, exists := document["sizeBytes"]; !exists || size != float64(0) {
		t.Fatalf("zero-byte document size = %#v, exists %t", size, exists)
	}
}

func TestReviewReadUsesIndexedDocumentAndFrozenContract(t *testing.T) {
	reviewRevision := strings.Repeat("b", 64)
	documentRevision := strings.Repeat("a", 64)
	readPath := ""
	server := newTestServerWithServices(t, fakeWorkspace{
		document: workspace.DocumentContent{
			Path: "README.md", Revision: documentRevision, Source: "# mdReview\n",
		},
	}, fakeReviewStore{
		snapshot: review.Snapshot{
			Path:             "README.md",
			DocumentRevision: documentRevision,
			ReviewRevision:   &reviewRevision,
			Threads:          []review.ResolvedThread{},
		},
		readPath: &readPath,
	})

	response := httptest.NewRecorder()
	server.ServeHTTP(
		response,
		authenticatedRequest(http.MethodGet, "http://127.0.0.1:4242/api/review?path=README.md"),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("review status = %d, body = %s", response.Code, response.Body.String())
	}
	if readPath != "README.md" {
		t.Fatalf("review store path = %q, want README.md", readPath)
	}
	var snapshot review.Snapshot
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode review: %v", err)
	}
	if snapshot.DocumentRevision != documentRevision ||
		snapshot.ReviewRevision == nil ||
		*snapshot.ReviewRevision != reviewRevision ||
		snapshot.Threads == nil {
		t.Fatalf("review snapshot = %#v", snapshot)
	}

	response = httptest.NewRecorder()
	server.ServeHTTP(
		response,
		authenticatedRequest(
			http.MethodGet,
			"http://127.0.0.1:4242/api/review?path=README.md&extra=value",
		),
	)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("extra review query status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestCreateThreadDecodesFrozenContractAndReturnsCreated(t *testing.T) {
	requestBody := contractFixture(t, "create-text-request.json")
	var captured review.CreateThreadInput
	documentRevision := strings.Repeat("d", 64)
	result := review.CreateThreadResult{
		DocumentRevision: documentRevision,
		ReviewRevision:   strings.Repeat("c", 64),
		Thread: review.ResolvedThread{
			ID: "thread_CCCCCCCCCCCCCCCCCC",
			Anchor: review.Anchor{
				Type: review.AnchorText,
				Range: &review.ByteRange{
					Start: 2,
					End:   10,
				},
				Source: "mdReview",
				Text:   "mdReview",
			},
			Attachment: review.Attachment{
				State:        review.AttachmentAttached,
				CurrentRange: &review.ByteRange{Start: 2, End: 10},
			},
			Status: review.StatusOpen,
			Messages: []review.Message{{
				ID:        "message_DDDDDDDDDDDDDDDDDD",
				Author:    review.Author{Type: "human", Name: "Reviewer"},
				Body:      "Explain the name.",
				CreatedAt: "2026-07-28T14:30:00Z",
			}},
		},
	}
	server := newTestServerWithServices(t, fakeWorkspace{
		document: workspace.DocumentContent{
			Path: "README.md", Revision: documentRevision, Source: "# mdReview\n",
		},
	}, fakeReviewStore{
		createResult: result,
		createInput:  &captured,
	})
	request := createThreadRequestForTest(t, requestBody)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}
	if captured.DocumentPath != "README.md" ||
		captured.ExpectedReviewRevision != nil ||
		captured.Anchor.Type != review.AnchorText ||
		captured.Anchor.Range == nil ||
		captured.Anchor.Range.Start != 2 ||
		captured.Anchor.Range.End != 10 ||
		captured.MessageBody != "Explain the name." {
		t.Fatalf("captured create input = %#v", captured)
	}
	var created review.CreateThreadResult
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ReviewRevision != result.ReviewRevision ||
		created.Thread.ID != result.Thread.ID {
		t.Fatalf("create response = %#v", created)
	}
}

func TestCreateThreadRejectsMutationBoundaryViolations(t *testing.T) {
	validBody := contractFixture(t, "create-document-request.json")
	tests := []struct {
		name        string
		origin      string
		contentType string
		body        []byte
		status      int
		code        string
	}{
		{
			name:        "missing origin",
			contentType: "application/json", body: validBody,
			status: http.StatusForbidden, code: "invalidOrigin",
		},
		{
			name:   "wrong origin",
			origin: "http://localhost:4242", contentType: "application/json", body: validBody,
			status: http.StatusForbidden, code: "invalidOrigin",
		},
		{
			name:   "non json",
			origin: "http://127.0.0.1:4242", contentType: "text/plain", body: validBody,
			status: http.StatusUnsupportedMediaType, code: "unsupportedMediaType",
		},
		{
			name:   "unknown field",
			origin: "http://127.0.0.1:4242", contentType: "application/json",
			body:   []byte(`{"documentPath":"README.md","expectedDocumentRevision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expectedReviewRevision":null,"anchor":{"type":"document"},"message":{"body":"body"},"unexpected":true}`),
			status: http.StatusUnprocessableEntity, code: "invalidReviewOperation",
		},
		{
			name:   "missing review revision",
			origin: "http://127.0.0.1:4242", contentType: "application/json",
			body:   []byte(`{"documentPath":"README.md","expectedDocumentRevision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","anchor":{"type":"document"},"message":{"body":"body"}}`),
			status: http.StatusUnprocessableEntity, code: "invalidReviewOperation",
		},
		{
			name:   "oversized body",
			origin: "http://127.0.0.1:4242", contentType: "application/json",
			body: append(
				append(
					[]byte(`{"documentPath":"`),
					bytes.Repeat([]byte("x"), int(limits.MaxMutationRequestBodyBytes)+1)...,
				),
				[]byte(`"}`)...,
			),
			status: http.StatusRequestEntityTooLarge, code: "requestTooLarge",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			request := httptest.NewRequest(
				http.MethodPost,
				"http://127.0.0.1:4242/api/threads",
				bytes.NewReader(test.body),
			)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.status, response.Body.String())
			}
			assertErrorCode(t, response, test.code)
		})
	}
}

func TestReviewErrorsAndConflictsUseStableTransport(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "invalid operation", err: review.ErrInvalidOperation, status: http.StatusUnprocessableEntity, code: "invalidReviewOperation"},
		{name: "too large", err: review.ErrTooLarge, status: http.StatusRequestEntityTooLarge, code: "reviewTooLarge"},
		{name: "unsupported", err: review.ErrUnsupportedSchema, status: http.StatusUnprocessableEntity, code: "reviewUnsupportedSchema"},
		{name: "invalid", err: review.ErrInvalid, status: http.StatusUnprocessableEntity, code: "reviewInvalid"},
		{name: "unsafe", err: review.ErrUnsafe, status: http.StatusUnprocessableEntity, code: "reviewUnsafe"},
		{name: "unavailable", err: review.ErrUnavailable, status: http.StatusInternalServerError, code: "reviewUnavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServerWithServices(t, fakeWorkspace{
				document: workspace.DocumentContent{Path: "README.md"},
			}, fakeReviewStore{readErr: test.err})
			response := httptest.NewRecorder()
			server.ServeHTTP(
				response,
				authenticatedRequest(
					http.MethodGet,
					"http://127.0.0.1:4242/api/review?path=README.md",
				),
			)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			assertErrorCode(t, response, test.code)
		})
	}

	current := review.CurrentRevisions{
		DocumentRevision: strings.Repeat("e", 64),
		ReviewRevision:   nil,
	}
	server := newTestServerWithServices(t, fakeWorkspace{
		document: workspace.DocumentContent{Path: "README.md"},
	}, fakeReviewStore{
		createErr: &review.ConflictError{
			Kind:    review.ErrDocumentChanged,
			Current: current,
		},
	})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, createThreadRequestForTest(t, contractFixture(t, "create-document-request.json")))
	if response.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, body = %s", response.Code, response.Body.String())
	}
	var conflict conflictResponse
	if err := json.Unmarshal(response.Body.Bytes(), &conflict); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if conflict.Error.Code != "documentChanged" || conflict.Current != current {
		t.Fatalf("conflict = %#v", conflict)
	}
}

func TestDocumentErrorsUseFrozenCodesWithoutPathDisclosure(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "invalid path", err: workspace.ErrInvalidRelativePath, status: http.StatusBadRequest, code: "invalidDocumentPath"},
		{name: "not indexed", err: workspace.ErrDocumentNotIndexed, status: http.StatusNotFound, code: "documentNotFound"},
		{name: "unsafe replacement", err: workspace.ErrUnsafeEntry, status: http.StatusNotFound, code: "documentNotFound"},
		{name: "too large", err: workspace.ErrDocumentTooLarge, status: http.StatusRequestEntityTooLarge, code: "documentTooLarge"},
		{name: "invalid utf8", err: workspace.ErrDocumentInvalidUTF8, status: http.StatusUnprocessableEntity, code: "documentInvalidUtf8"},
		{name: "read unavailable", err: workspace.ErrDocumentRead, status: http.StatusInternalServerError, code: "documentUnavailable"},
		{name: "unexpected", err: errors.New("unexpected /secret/path"), status: http.StatusInternalServerError, code: "internalError"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServerWithWorkspace(t, fakeWorkspace{documentErr: test.err})
			response := httptest.NewRecorder()
			server.ServeHTTP(response, authenticatedRequest(http.MethodGet, "http://127.0.0.1:4242/api/document?path=README.md"))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			assertErrorCode(t, response, test.code)
			if strings.Contains(response.Body.String(), "/secret/path") {
				t.Fatal("error response leaked a workspace path")
			}
		})
	}
}

func newTestServer(t *testing.T) *Server {
	return newTestServerWithWorkspace(t, fakeWorkspace{})
}

func newTestServerWithWorkspace(t *testing.T, testWorkspace fakeWorkspace) *Server {
	return newTestServerWithServices(t, testWorkspace, fakeReviewStore{})
}

func newTestServerWithServices(
	t *testing.T,
	testWorkspace fakeWorkspace,
	testReview fakeReviewStore,
) *Server {
	t.Helper()
	server, err := New(Config{
		Assets: fstest.MapFS{
			"index.html":           &fstest.MapFile{Data: []byte("application shell")},
			"assets/app-abcdef.js": &fstest.MapFile{Data: []byte("console.log('app')")},
		},
		Workspace: testWorkspace,
		Review:    testReview,
		BoundHost: "127.0.0.1:4242",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return server
}

func contractFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "testdata", "contracts", "m2", name))
	if err != nil {
		t.Fatalf("read M2 contract %s: %v", name, err)
	}
	return data
}

func createThreadRequestForTest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	request := authenticatedRequest(
		http.MethodPost,
		"http://127.0.0.1:4242/api/threads",
	)
	request.Body = http.NoBody
	if len(body) > 0 {
		request = httptest.NewRequest(
			http.MethodPost,
			"http://127.0.0.1:4242/api/threads",
			bytes.NewReader(body),
		)
	}
	request.Header.Set("Origin", "http://127.0.0.1:4242")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func authenticatedRequest(method string, target string) *http.Request {
	return httptest.NewRequest(method, target, nil)
}

type fakeWorkspace struct {
	snapshot    workspace.Snapshot
	snapshotErr error
	document    workspace.DocumentContent
	documentErr error
	assetRead   func(context.Context, string, string, func(io.Reader, int64) error) error
}

type fakeReviewStore struct {
	snapshot     review.Snapshot
	readErr      error
	readPath     *string
	createResult review.CreateThreadResult
	createErr    error
	createInput  *review.CreateThreadInput
	replyResult  review.MutationResult
	replyErr     error
	replyInput   *review.ReplyInput
	statusResult review.MutationResult
	statusErr    error
	statusInput  *review.ChangeStatusInput
	deleteResult review.DeleteThreadResult
	deleteErr    error
	deleteInput  *review.DeleteThreadInput
}

func (fake fakeReviewStore) Read(_ context.Context, path string) (review.Snapshot, error) {
	if fake.readPath != nil {
		*fake.readPath = path
	}
	return fake.snapshot, fake.readErr
}

func (fake fakeReviewStore) CreateThread(
	_ context.Context,
	input review.CreateThreadInput,
) (review.CreateThreadResult, error) {
	if fake.createInput != nil {
		*fake.createInput = input
	}
	return fake.createResult, fake.createErr
}

func (fake fakeReviewStore) Reply(
	_ context.Context,
	input review.ReplyInput,
) (review.MutationResult, error) {
	if fake.replyInput != nil {
		*fake.replyInput = input
	}
	return fake.replyResult, fake.replyErr
}

func (fake fakeReviewStore) ChangeStatus(
	_ context.Context,
	input review.ChangeStatusInput,
) (review.MutationResult, error) {
	if fake.statusInput != nil {
		*fake.statusInput = input
	}
	return fake.statusResult, fake.statusErr
}

func (fake fakeReviewStore) DeleteThread(
	_ context.Context,
	input review.DeleteThreadInput,
) (review.DeleteThreadResult, error) {
	if fake.deleteInput != nil {
		*fake.deleteInput = input
	}
	return fake.deleteResult, fake.deleteErr
}

func (fake fakeWorkspace) Root() string {
	return "/canonical/workspace"
}

func (fake fakeWorkspace) Snapshot(context.Context) (workspace.Snapshot, error) {
	return fake.snapshot, fake.snapshotErr
}

func (fake fakeWorkspace) ReadDocument(context.Context, string) (workspace.DocumentContent, error) {
	return fake.document, fake.documentErr
}

func (fake fakeWorkspace) ReadAsset(
	ctx context.Context,
	documentPath string,
	reference string,
	visit func(io.Reader, int64) error,
) error {
	if fake.assetRead == nil {
		return workspace.ErrAssetNotFound
	}
	return fake.assetRead(ctx, documentPath, reference, visit)
}

func assertSecurityHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
		t.Fatalf("CSP = %q", got)
	}
	if response.Header().Get("Referrer-Policy") != "no-referrer" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", response.Header())
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing request ID")
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unexpected CORS opt-in")
	}
}

func assertErrorCode(t *testing.T, response *httptest.ResponseRecorder, want string) {
	t.Helper()
	var envelope errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if envelope.Error.Code != want || envelope.Error.RequestID == "" {
		t.Fatalf("error = %#v, want code %q", envelope.Error, want)
	}
}
