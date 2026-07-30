// Package server implements the browser-facing HTTP security boundary.
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mdreview.dev/mdreview/internal/gatee"
	"mdreview.dev/mdreview/internal/limits"
	"mdreview.dev/mdreview/internal/review"
	"mdreview.dev/mdreview/internal/workspace"
)

const (
	contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; font-src 'self'; img-src blob:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	fallbackRequestID     = "request-id-unavailable"
)

type reviewOperationRoute uint8

const (
	reviewOperationUnknown reviewOperationRoute = iota
	reviewOperationReply
	reviewOperationStatus
	reviewOperationDelete
	reviewOperationEditMessage
)

// Config provides immutable dependencies for a loopback HTTP server.
// Workspace-backed endpoints are added separately once internal/workspace is
// available; this package does not recreate that package's domain types.
type Config struct {
	Assets    fs.FS
	Workspace Workspace
	Review    ReviewStore
	BoundHost string
	// Measurements enables the loopback-only Gate E counter endpoint.
	// Ordinary production callers leave it nil.
	Measurements *gatee.Counters
	// NewRequestID overrides request-ID generation for deterministic
	// failure-path tests. Production callers leave it nil.
	NewRequestID func() (string, error)
}

// Server validates requests and serves embedded browser assets.
type Server struct {
	assets       fs.FS
	workspace    Workspace
	review       ReviewStore
	boundHost    string
	newRequestID func() (string, error)
	assetPermits chan struct{}
	measurements *gatee.Counters
}

// Workspace is the server's consumer-owned read-only view of an indexed
// workspace. Operating-system paths never cross this interface except the
// authenticated health identity returned by Root.
type Workspace interface {
	Root() string
	Snapshot(context.Context) (workspace.Snapshot, error)
	ReadDocument(context.Context, string) (workspace.DocumentContent, error)
	ReadAsset(context.Context, string, string, func(io.Reader, int64) error) error
}

// ReviewStore is the server's document-scoped review view. Implementations
// derive sidecars from indexed Markdown identities rather than accepting
// client-supplied sidecar paths.
type ReviewStore interface {
	Read(context.Context, string) (review.Snapshot, error)
	CreateThread(context.Context, review.CreateThreadInput) (review.CreateThreadResult, error)
	Reply(context.Context, review.ReplyInput) (review.MutationResult, error)
	EditMessage(context.Context, review.EditMessageInput) (review.MutationResult, error)
	ChangeStatus(context.Context, review.ChangeStatusInput) (review.MutationResult, error)
	DeleteThread(context.Context, review.DeleteThreadInput) (review.DeleteThreadResult, error)
}

// New constructs a loopback server. BoundHost must exactly match the
// listener's loopback host and port, for example 127.0.0.1:4242.
func New(config Config) (*Server, error) {
	if config.Assets == nil {
		return nil, fmt.Errorf("server assets are required")
	}
	if config.Workspace == nil || config.Workspace.Root() == "" {
		return nil, fmt.Errorf("server workspace is required")
	}
	if config.Review == nil {
		return nil, fmt.Errorf("server review store is required")
	}
	if config.BoundHost == "" {
		return nil, fmt.Errorf("server bound host is required")
	}
	requestIDGenerator := config.NewRequestID
	if requestIDGenerator == nil {
		requestIDGenerator = newRequestID
	}
	return &Server{
		assets:       config.Assets,
		workspace:    config.Workspace,
		review:       config.Review,
		boundHost:    config.BoundHost,
		newRequestID: requestIDGenerator,
		assetPermits: make(chan struct{}, limits.MaxConcurrentImageStreams),
		measurements: config.Measurements,
	}, nil
}

// Handler returns the configured HTTP handler.
func (server *Server) Handler() http.Handler {
	return server
}

// ServeHTTP applies host validation and response security headers before
// dispatching to the static shell or authenticated private API.
func (server *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	server.applySecurityHeaders(response)
	requestID, err := server.newRequestID()
	if err != nil || requestID == "" {
		// The response contract still needs a useful correlation value when
		// entropy is unavailable. This fixed diagnostic identifies the failure
		// class without pretending to be globally unique.
		requestID = fallbackRequestID
		response.Header().Set("X-Request-ID", requestID)
		server.writeError(response, requestID, http.StatusInternalServerError, "internalError", "An internal error occurred.")
		return
	}
	response.Header().Set("X-Request-ID", requestID)
	if request.Host != server.boundHost {
		server.writeError(response, requestID, http.StatusBadRequest, "invalidHost", "This request host is not allowed.")
		return
	}
	if strings.HasPrefix(request.URL.Path, "/api/") {
		server.serveAPI(response, request, requestID)
		return
	}
	server.serveStaticAsset(response, request)
}

func (server *Server) applySecurityHeaders(response http.ResponseWriter) {
	response.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
}

func (server *Server) serveAPI(response http.ResponseWriter, request *http.Request, requestID string) {
	response.Header().Set("Cache-Control", "no-store")
	switch request.URL.Path {
	case "/api/state":
		if !server.requireMethod(response, request, requestID, http.MethodGet) {
			return
		}
		server.serveState(response, request, requestID)
	case "/api/document":
		if !server.requireMethod(response, request, requestID, http.MethodGet) {
			return
		}
		server.serveDocument(response, request, requestID)
	case "/api/review":
		if !server.requireMethod(response, request, requestID, http.MethodGet) {
			return
		}
		server.serveReview(response, request, requestID)
	case "/api/asset":
		if !server.requireMethod(response, request, requestID, http.MethodGet) {
			return
		}
		server.serveWorkspaceAsset(response, request, requestID)
	case "/api/gate-e/counters":
		if server.measurements == nil {
			server.serveReviewOperation(response, request, requestID)
			return
		}
		if !server.requireMethod(response, request, requestID, http.MethodGet) {
			return
		}
		server.writeJSON(response, http.StatusOK, server.measurements.Snapshot())
	case "/api/threads":
		if !server.requireMethod(response, request, requestID, http.MethodPost) {
			return
		}
		server.serveCreateThread(response, request, requestID)
	default:
		server.serveReviewOperation(response, request, requestID)
	}
}

func (server *Server) serveReviewOperation(
	response http.ResponseWriter,
	request *http.Request,
	requestID string,
) {
	operation, targetID, ok := parseReviewOperationRoute(request.URL.Path)
	if !ok {
		server.writeError(response, requestID, http.StatusNotFound, "endpointNotFound", "This API endpoint does not exist.")
		return
	}

	switch operation {
	case reviewOperationReply:
		if !server.requireMethod(response, request, requestID, http.MethodPost) {
			return
		}
		server.serveReply(response, request, requestID, targetID)
	case reviewOperationStatus:
		if !server.requireMethod(response, request, requestID, http.MethodPatch) {
			return
		}
		server.serveChangeStatus(response, request, requestID, targetID)
	case reviewOperationDelete:
		if !server.requireMethod(response, request, requestID, http.MethodDelete) {
			return
		}
		server.serveDeleteThread(response, request, requestID, targetID)
	case reviewOperationEditMessage:
		if !server.requireMethod(response, request, requestID, http.MethodPatch) {
			return
		}
		server.serveEditMessage(response, request, requestID, targetID)
	default:
		server.writeError(response, requestID, http.StatusNotFound, "endpointNotFound", "This API endpoint does not exist.")
	}
}

func (server *Server) requireMethod(
	response http.ResponseWriter,
	request *http.Request,
	requestID string,
	allowed string,
) bool {
	if request.Method == allowed {
		return true
	}
	response.Header().Set("Allow", allowed)
	server.writeError(
		response,
		requestID,
		http.StatusMethodNotAllowed,
		"methodNotAllowed",
		fmt.Sprintf("This endpoint only accepts %s requests.", allowed),
	)
	return false
}

func (server *Server) serveState(response http.ResponseWriter, request *http.Request, requestID string) {
	since, ok := workspaceRevisionQuery(request)
	if !ok {
		server.writeError(
			response,
			requestID,
			http.StatusBadRequest,
			"invalidWorkspaceRevision",
			"Use the current workspace revision to check for changes.",
		)
		return
	}
	snapshot, err := server.workspace.Snapshot(request.Context())
	if err != nil {
		server.writeError(response, requestID, http.StatusInternalServerError, "workspaceUnavailable", "The workspace is temporarily unavailable.")
		return
	}
	if since != nil && *since == snapshot.Revision {
		server.writeJSON(response, http.StatusOK, stateUnchangedResponse{
			Status:            "unchanged",
			WorkspaceRevision: snapshot.Revision,
		})
		return
	}
	server.writeJSON(response, http.StatusOK, stateResponse{
		Status:              "changed",
		WorkspaceRevision:   snapshot.Revision,
		DocumentCount:       snapshot.DocumentCount,
		InitialDocumentPath: snapshot.InitialDocumentPath,
		Navigation:          navigationResponse(snapshot.Navigation),
		Warnings:            warningResponse(snapshot.Warnings),
	})
}

func (server *Server) serveDocument(response http.ResponseWriter, request *http.Request, requestID string) {
	documentPath, ok := documentPathQuery(request)
	if !ok {
		server.writeError(response, requestID, http.StatusBadRequest, "invalidDocumentPath", "Choose a valid Markdown document.")
		return
	}
	document, err := server.workspace.ReadDocument(request.Context(), documentPath)
	if err != nil {
		server.writeDocumentError(response, requestID, err)
		return
	}
	server.writeJSON(response, http.StatusOK, documentResponse{Path: document.Path, Revision: document.Revision, Source: document.Source})
}

func (server *Server) serveReview(response http.ResponseWriter, request *http.Request, requestID string) {
	documentPath, ok := documentPathQuery(request)
	if !ok {
		server.writeError(response, requestID, http.StatusBadRequest, "invalidDocumentPath", "Choose a valid Markdown document.")
		return
	}
	if _, err := server.workspace.ReadDocument(request.Context(), documentPath); err != nil {
		server.writeDocumentError(response, requestID, err)
		return
	}
	snapshot, err := server.review.Read(request.Context(), documentPath)
	if err != nil {
		server.writeReviewError(response, requestID, err)
		return
	}
	server.writeJSON(response, http.StatusOK, snapshot)
}

func (server *Server) serveCreateThread(
	response http.ResponseWriter,
	request *http.Request,
	requestID string,
) {
	if !server.validateMutationRequest(response, request, requestID) {
		return
	}

	input, err := decodeCreateThreadRequest(response, request)
	if err != nil {
		if isRequestTooLarge(err) {
			server.writeError(response, requestID, http.StatusRequestEntityTooLarge, "requestTooLarge", "This review request is too large.")
		} else {
			server.writeError(response, requestID, http.StatusUnprocessableEntity, "invalidReviewOperation", "Check the comment and selected text, then try again.")
		}
		return
	}
	if _, err := server.workspace.ReadDocument(request.Context(), input.DocumentPath); err != nil {
		server.writeDocumentError(response, requestID, err)
		return
	}
	result, err := server.review.CreateThread(request.Context(), input)
	if err != nil {
		server.writeReviewError(response, requestID, err)
		return
	}
	server.writeJSON(response, http.StatusCreated, result)
}

func (server *Server) validateMutationRequest(
	response http.ResponseWriter,
	request *http.Request,
	requestID string,
) bool {
	if request.Header.Get("Origin") != "http://"+server.boundHost {
		server.writeError(response, requestID, http.StatusForbidden, "invalidOrigin", "This mutation origin is not allowed.")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		server.writeError(response, requestID, http.StatusUnsupportedMediaType, "unsupportedMediaType", "Send this request as JSON.")
		return false
	}
	if request.ContentLength > limits.MaxMutationRequestBodyBytes {
		server.writeError(response, requestID, http.StatusRequestEntityTooLarge, "requestTooLarge", "This review request is too large.")
		return false
	}
	return true
}

func (server *Server) serveReply(
	response http.ResponseWriter,
	request *http.Request,
	requestID string,
	threadID string,
) {
	if !server.validateMutationRequest(response, request, requestID) {
		return
	}
	input, err := decodeReplyRequest(response, request)
	if err != nil {
		server.writeMutationDecodeError(response, requestID, err)
		return
	}
	input.ThreadID = threadID
	if _, err := server.workspace.ReadDocument(request.Context(), input.DocumentPath); err != nil {
		server.writeDocumentError(response, requestID, err)
		return
	}
	result, err := server.review.Reply(request.Context(), input)
	if err != nil {
		server.writeReviewError(response, requestID, err)
		return
	}
	server.writeJSON(response, http.StatusCreated, result)
}

func (server *Server) serveEditMessage(
	response http.ResponseWriter,
	request *http.Request,
	requestID string,
	messageID string,
) {
	if !server.validateMutationRequest(response, request, requestID) {
		return
	}
	input, err := decodeEditMessageRequest(response, request)
	if err != nil {
		server.writeMutationDecodeError(response, requestID, err)
		return
	}
	input.MessageID = messageID
	if _, err := server.workspace.ReadDocument(request.Context(), input.DocumentPath); err != nil {
		server.writeDocumentError(response, requestID, err)
		return
	}
	result, err := server.review.EditMessage(request.Context(), input)
	if err != nil {
		server.writeReviewError(response, requestID, err)
		return
	}
	server.writeJSON(response, http.StatusOK, result)
}

func (server *Server) serveChangeStatus(
	response http.ResponseWriter,
	request *http.Request,
	requestID string,
	threadID string,
) {
	if !server.validateMutationRequest(response, request, requestID) {
		return
	}
	input, err := decodeChangeStatusRequest(response, request)
	if err != nil {
		server.writeMutationDecodeError(response, requestID, err)
		return
	}
	input.ThreadID = threadID
	if _, err := server.workspace.ReadDocument(request.Context(), input.DocumentPath); err != nil {
		server.writeDocumentError(response, requestID, err)
		return
	}
	result, err := server.review.ChangeStatus(request.Context(), input)
	if err != nil {
		server.writeReviewError(response, requestID, err)
		return
	}
	server.writeJSON(response, http.StatusOK, result)
}

func (server *Server) serveDeleteThread(
	response http.ResponseWriter,
	request *http.Request,
	requestID string,
	threadID string,
) {
	if !server.validateMutationRequest(response, request, requestID) {
		return
	}
	input, err := decodeDeleteThreadRequest(response, request)
	if err != nil {
		server.writeMutationDecodeError(response, requestID, err)
		return
	}
	input.ThreadID = threadID
	if _, err := server.workspace.ReadDocument(request.Context(), input.DocumentPath); err != nil {
		server.writeDocumentError(response, requestID, err)
		return
	}
	result, err := server.review.DeleteThread(request.Context(), input)
	if err != nil {
		server.writeReviewError(response, requestID, err)
		return
	}
	server.writeJSON(response, http.StatusOK, result)
}

func (server *Server) writeMutationDecodeError(
	response http.ResponseWriter,
	requestID string,
	err error,
) {
	if isRequestTooLarge(err) {
		server.writeError(response, requestID, http.StatusRequestEntityTooLarge, "requestTooLarge", "This review request is too large.")
		return
	}
	server.writeError(response, requestID, http.StatusUnprocessableEntity, "invalidReviewOperation", "Check this review change, then try again.")
}

func documentPathQuery(request *http.Request) (string, bool) {
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(values) != 1 || len(values["path"]) != 1 || values.Get("path") == "" {
		return "", false
	}
	return values.Get("path"), true
}

func workspaceRevisionQuery(request *http.Request) (*uint64, bool) {
	if request.URL.RawQuery == "" {
		return nil, true
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(values) != 1 || len(values["since"]) != 1 || values.Get("since") == "" {
		return nil, false
	}
	revision, err := strconv.ParseUint(values.Get("since"), 10, 64)
	if err != nil || revision == 0 {
		return nil, false
	}
	return &revision, true
}

func parseReviewOperationRoute(urlPath string) (reviewOperationRoute, string, bool) {
	segments := strings.Split(strings.TrimPrefix(urlPath, "/"), "/")
	switch {
	case len(segments) == 4 &&
		segments[0] == "api" &&
		segments[1] == "threads" &&
		segments[3] == "messages":
		id, ok := decodeRouteID(segments[2])
		if !ok {
			return reviewOperationUnknown, "", false
		}
		return reviewOperationReply, id, ok
	case len(segments) == 4 &&
		segments[0] == "api" &&
		segments[1] == "threads" &&
		segments[3] == "status":
		id, ok := decodeRouteID(segments[2])
		if !ok {
			return reviewOperationUnknown, "", false
		}
		return reviewOperationStatus, id, ok
	case len(segments) == 3 &&
		segments[0] == "api" &&
		segments[1] == "threads":
		id, ok := decodeRouteID(segments[2])
		if !ok {
			return reviewOperationUnknown, "", false
		}
		return reviewOperationDelete, id, ok
	case len(segments) == 3 &&
		segments[0] == "api" &&
		segments[1] == "messages":
		id, ok := decodeRouteID(segments[2])
		if !ok {
			return reviewOperationUnknown, "", false
		}
		return reviewOperationEditMessage, id, ok
	default:
		return reviewOperationUnknown, "", false
	}
}

func decodeRouteID(segment string) (string, bool) {
	if len(segment) < 2 || segment[0] != '~' {
		return "", false
	}
	encoded := segment[1:]
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil ||
		len(decoded) == 0 ||
		!utf8.Valid(decoded) ||
		base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return "", false
	}
	return string(decoded), true
}

func decodeCreateThreadRequest(
	response http.ResponseWriter,
	request *http.Request,
) (review.CreateThreadInput, error) {
	request.Body = http.MaxBytesReader(response, request.Body, limits.MaxMutationRequestBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var transport createThreadRequest
	if err := decoder.Decode(&transport); err != nil {
		return review.CreateThreadInput{}, err
	}
	if err := requireJSONEnd(decoder); err != nil {
		return review.CreateThreadInput{}, err
	}

	expectedReviewRevision, err := decodeNullableRevision(transport.ExpectedReviewRevision)
	if err != nil {
		return review.CreateThreadInput{}, err
	}
	anchor, err := decodeThreadAnchor(transport.Anchor)
	if err != nil {
		return review.CreateThreadInput{}, err
	}
	return review.CreateThreadInput{
		DocumentPath:             transport.DocumentPath,
		ExpectedDocumentRevision: transport.ExpectedDocumentRevision,
		ExpectedReviewRevision:   expectedReviewRevision,
		Anchor:                   anchor,
		MessageBody:              transport.Message.Body,
	}, nil
}

func decodeReplyRequest(
	response http.ResponseWriter,
	request *http.Request,
) (review.ReplyInput, error) {
	var transport replyOperationRequest
	if err := decodeStrictRequest(response, request, &transport); err != nil {
		return review.ReplyInput{}, err
	}
	return review.ReplyInput{
		DocumentPath:             transport.DocumentPath,
		ExpectedDocumentRevision: transport.ExpectedDocumentRevision,
		ExpectedReviewRevision:   transport.ExpectedReviewRevision,
		TargetFingerprint:        transport.TargetFingerprint,
		MessageBody:              transport.Message.Body,
	}, nil
}

func decodeEditMessageRequest(
	response http.ResponseWriter,
	request *http.Request,
) (review.EditMessageInput, error) {
	var transport editMessageOperationRequest
	if err := decodeStrictRequest(response, request, &transport); err != nil {
		return review.EditMessageInput{}, err
	}
	return review.EditMessageInput{
		DocumentPath:             transport.DocumentPath,
		ExpectedDocumentRevision: transport.ExpectedDocumentRevision,
		ExpectedReviewRevision:   transport.ExpectedReviewRevision,
		TargetFingerprint:        transport.TargetFingerprint,
		MessageBody:              transport.Message.Body,
	}, nil
}

func decodeChangeStatusRequest(
	response http.ResponseWriter,
	request *http.Request,
) (review.ChangeStatusInput, error) {
	var transport statusOperationRequest
	if err := decodeStrictRequest(response, request, &transport); err != nil {
		return review.ChangeStatusInput{}, err
	}
	return review.ChangeStatusInput{
		DocumentPath:             transport.DocumentPath,
		ExpectedDocumentRevision: transport.ExpectedDocumentRevision,
		ExpectedReviewRevision:   transport.ExpectedReviewRevision,
		TargetFingerprint:        transport.TargetFingerprint,
		Status:                   transport.Status,
	}, nil
}

func decodeDeleteThreadRequest(
	response http.ResponseWriter,
	request *http.Request,
) (review.DeleteThreadInput, error) {
	var transport targetOperationRequest
	if err := decodeStrictRequest(response, request, &transport); err != nil {
		return review.DeleteThreadInput{}, err
	}
	return review.DeleteThreadInput{
		DocumentPath:             transport.DocumentPath,
		ExpectedDocumentRevision: transport.ExpectedDocumentRevision,
		ExpectedReviewRevision:   transport.ExpectedReviewRevision,
		TargetFingerprint:        transport.TargetFingerprint,
	}, nil
}

func decodeStrictRequest(
	response http.ResponseWriter,
	request *http.Request,
	destination any,
) error {
	request.Body = http.MaxBytesReader(response, request.Body, limits.MaxMutationRequestBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireJSONEnd(decoder)
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request contains more than one JSON value")
		}
		return err
	}
	return nil
}

func decodeNullableRevision(raw json.RawMessage) (*string, error) {
	if raw == nil {
		return nil, errors.New("expectedReviewRevision is required")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var revision string
	if err := json.Unmarshal(raw, &revision); err != nil {
		return nil, errors.New("expectedReviewRevision must be a string or null")
	}
	return &revision, nil
}

func decodeThreadAnchor(raw json.RawMessage) (review.Anchor, error) {
	if raw == nil {
		return review.Anchor{}, errors.New("anchor is required")
	}
	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &discriminator); err != nil {
		return review.Anchor{}, err
	}
	switch discriminator.Type {
	case string(review.AnchorDocument):
		var anchor struct {
			Type string `json:"type"`
		}
		if err := decodeStrictRaw(raw, &anchor); err != nil {
			return review.Anchor{}, err
		}
		return review.Anchor{Type: review.AnchorDocument}, nil
	case string(review.AnchorText):
		var anchor struct {
			Type   string           `json:"type"`
			Range  review.ByteRange `json:"range"`
			Source string           `json:"source"`
			Text   string           `json:"text"`
		}
		if err := decodeStrictRaw(raw, &anchor); err != nil {
			return review.Anchor{}, err
		}
		return review.Anchor{
			Type:   review.AnchorText,
			Range:  &anchor.Range,
			Source: anchor.Source,
			Text:   anchor.Text,
		}, nil
	default:
		return review.Anchor{}, errors.New("anchor type is invalid")
	}
}

func decodeStrictRaw(raw json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireJSONEnd(decoder)
}

func isRequestTooLarge(err error) bool {
	var maximum *http.MaxBytesError
	return errors.As(err, &maximum)
}

func (server *Server) writeDocumentError(response http.ResponseWriter, requestID string, err error) {
	switch {
	case errors.Is(err, workspace.ErrInvalidRelativePath):
		server.writeError(response, requestID, http.StatusBadRequest, "invalidDocumentPath", "Choose a valid Markdown document.")
	case errors.Is(err, workspace.ErrDocumentNotIndexed), errors.Is(err, workspace.ErrUnsafeEntry):
		server.writeError(response, requestID, http.StatusNotFound, "documentNotFound", "This Markdown document is not available.")
	case errors.Is(err, workspace.ErrDocumentTooLarge):
		server.writeError(response, requestID, http.StatusRequestEntityTooLarge, "documentTooLarge", "This Markdown file is too large to open.")
	case errors.Is(err, workspace.ErrDocumentInvalidUTF8):
		server.writeError(response, requestID, http.StatusUnprocessableEntity, "documentInvalidUtf8", "This Markdown file is not valid UTF-8.")
	case errors.Is(err, workspace.ErrDocumentRead):
		server.writeError(response, requestID, http.StatusInternalServerError, "documentUnavailable", "This Markdown document could not be read.")
	default:
		server.writeError(response, requestID, http.StatusInternalServerError, "internalError", "An internal error occurred.")
	}
}

func (server *Server) writeReviewError(response http.ResponseWriter, requestID string, err error) {
	var targetConflict *review.TargetChangedError
	if errors.As(err, &targetConflict) {
		server.writeJSON(response, http.StatusConflict, targetConflictResponse{
			Error: apiError{
				Code:      "targetChanged",
				Message:   "This review target changed on disk. Your change was not submitted.",
				RequestID: requestID,
			},
			Current: targetConflict.Current,
		})
		return
	}
	var conflict *review.ConflictError
	if errors.As(err, &conflict) {
		code := "reviewChanged"
		message := "The review changed on disk. Your comment was not submitted."
		if errors.Is(conflict, review.ErrDocumentChanged) {
			code = "documentChanged"
			message = "The selected source changed or is no longer unique."
		}
		server.writeConflict(response, requestID, code, message, conflict.Current)
		return
	}
	switch {
	case errors.Is(err, review.ErrInvalidOperation):
		server.writeError(response, requestID, http.StatusUnprocessableEntity, "invalidReviewOperation", "Check the comment and selected text, then try again.")
	case errors.Is(err, review.ErrTooLarge):
		server.writeError(response, requestID, http.StatusRequestEntityTooLarge, "reviewTooLarge", "This review sidecar is too large to open or update.")
	case errors.Is(err, review.ErrUnsupportedSchema):
		server.writeError(response, requestID, http.StatusUnprocessableEntity, "reviewUnsupportedSchema", "This review uses a newer unsupported schema version.")
	case errors.Is(err, review.ErrInvalid):
		server.writeError(response, requestID, http.StatusUnprocessableEntity, "reviewInvalid", "This review sidecar is invalid and was left unchanged.")
	case errors.Is(err, review.ErrUnsafe):
		server.writeError(response, requestID, http.StatusUnprocessableEntity, "reviewUnsafe", "This review sidecar is not a safe regular file.")
	case errors.Is(err, review.ErrUnavailable):
		server.writeError(response, requestID, http.StatusInternalServerError, "reviewUnavailable", "This review sidecar could not be read or updated.")
	default:
		server.writeError(response, requestID, http.StatusInternalServerError, "internalError", "An internal error occurred.")
	}
}

func (server *Server) writeConflict(
	response http.ResponseWriter,
	requestID string,
	code string,
	message string,
	current review.CurrentRevisions,
) {
	server.writeJSON(response, http.StatusConflict, conflictResponse{
		Error:   apiError{Code: code, Message: message, RequestID: requestID},
		Current: current,
	})
}

func (server *Server) serveStaticAsset(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		http.NotFound(response, request)
		return
	}
	decodedPath, err := url.PathUnescape(request.URL.EscapedPath())
	if err != nil || containsParentPathSegment(decodedPath) {
		http.NotFound(response, request)
		return
	}
	requestedPath := strings.TrimPrefix(path.Clean("/"+decodedPath), "/")
	if requestedPath == "" {
		requestedPath = "index.html"
	}
	if !fs.ValidPath(requestedPath) {
		http.NotFound(response, request)
		return
	}
	content, err := fs.ReadFile(server.assets, requestedPath)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	if requestedPath == "index.html" {
		response.Header().Set("Cache-Control", "no-cache")
	} else if immutableAssetPath(requestedPath) {
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		response.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(response, request, requestedPath, zeroModTime, bytes.NewReader(content))
}

var zeroModTime = time.Time{}

func immutableAssetPath(requestedPath string) bool {
	return strings.HasPrefix(requestedPath, "assets/") && strings.Count(path.Base(requestedPath), "-") >= 1
}

func containsParentPathSegment(value string) bool {
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func (server *Server) writeError(response http.ResponseWriter, requestID string, status int, code string, message string) {
	server.writeJSON(response, status, errorResponse{Error: apiError{Code: code, Message: message, RequestID: requestID}})
}

func (server *Server) writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

type stateResponse struct {
	Status              string            `json:"status"`
	WorkspaceRevision   uint64            `json:"workspaceRevision"`
	DocumentCount       int               `json:"documentCount"`
	InitialDocumentPath *string           `json:"initialDocumentPath"`
	Navigation          []navigationEntry `json:"navigation"`
	Warnings            []scanWarning     `json:"warnings"`
}

type stateUnchangedResponse struct {
	Status            string `json:"status"`
	WorkspaceRevision uint64 `json:"workspaceRevision"`
}

type navigationEntry struct {
	Kind                     string            `json:"kind"`
	Name                     string            `json:"name"`
	Path                     string            `json:"path"`
	Children                 []navigationEntry `json:"children,omitempty"`
	SizeBytes                *int64            `json:"sizeBytes,omitempty"`
	Availability             *string           `json:"availability,omitempty"`
	DocumentMetadataRevision *string           `json:"documentMetadataRevision,omitempty"`
	ReviewMetadataRevision   **string          `json:"reviewMetadataRevision,omitempty"`
}

type scanWarning struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type documentResponse struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
	Source   string `json:"source"`
}

type createThreadRequest struct {
	DocumentPath             string          `json:"documentPath"`
	ExpectedDocumentRevision string          `json:"expectedDocumentRevision"`
	ExpectedReviewRevision   json.RawMessage `json:"expectedReviewRevision"`
	Anchor                   json.RawMessage `json:"anchor"`
	Message                  struct {
		Body string `json:"body"`
	} `json:"message"`
}

type targetOperationRequest struct {
	DocumentPath             string `json:"documentPath"`
	ExpectedDocumentRevision string `json:"expectedDocumentRevision"`
	ExpectedReviewRevision   string `json:"expectedReviewRevision"`
	TargetFingerprint        string `json:"targetFingerprint"`
}

type replyOperationRequest struct {
	targetOperationRequest
	Message struct {
		Body string `json:"body"`
	} `json:"message"`
}

type editMessageOperationRequest struct {
	targetOperationRequest
	Message struct {
		Body string `json:"body"`
	} `json:"message"`
}

type statusOperationRequest struct {
	targetOperationRequest
	Status review.ThreadStatus `json:"status"`
}

type conflictResponse struct {
	Error   apiError                `json:"error"`
	Current review.CurrentRevisions `json:"current"`
}

type targetConflictResponse struct {
	Error   apiError                  `json:"error"`
	Current review.CurrentTargetState `json:"current"`
}

func navigationResponse(entries []workspace.NavigationEntry) []navigationEntry {
	response := make([]navigationEntry, 0, len(entries))
	for _, entry := range entries {
		converted := navigationEntry{Kind: string(entry.Kind), Name: entry.Name, Path: entry.Path}
		if entry.Kind == workspace.EntryKindDirectory {
			converted.Children = navigationResponse(entry.Children)
		} else {
			sizeBytes := entry.SizeBytes
			converted.SizeBytes = &sizeBytes
			availability := string(entry.Availability)
			converted.Availability = &availability
			documentMetadataRevision := entry.DocumentMetadataRevision
			converted.DocumentMetadataRevision = &documentMetadataRevision
			reviewMetadataRevision := entry.ReviewMetadataRevision
			converted.ReviewMetadataRevision = &reviewMetadataRevision
		}
		response = append(response, converted)
	}
	return response
}

func warningResponse(warnings []workspace.Warning) []scanWarning {
	response := make([]scanWarning, 0, len(warnings))
	for _, warning := range warnings {
		response = append(response, scanWarning{Path: warning.Path, Code: warning.Code, Message: warning.Message})
	}
	return response
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

func newRequestID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
