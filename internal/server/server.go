// Package server implements the browser-facing HTTP security boundary.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"mdreview.dev/mdreview/internal/limits"
	"mdreview.dev/mdreview/internal/review"
	"mdreview.dev/mdreview/internal/workspace"
)

// contentSecurityPolicy keeps the browser shell self-contained. Markdown and
// sidecar content are untrusted, so the shell cannot execute or frame content
// supplied by a document and can request images only through this origin.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; font-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Config provides immutable dependencies for a loopback HTTP server.
// Workspace-backed endpoints are added separately once internal/workspace is
// available; this package does not recreate that package's domain types.
type Config struct {
	// Assets is the embedded browser tree served for non-API requests.
	Assets fs.FS
	// Workspace supplies indexed identities and contained reads; it never
	// exposes an operating-system path to the HTTP layer.
	Workspace Workspace
	// Review supplies document-scoped sidecar reads and semantic mutations.
	Review ReviewStore
	// BoundHost is the exact Host value accepted for this server instance.
	BoundHost string
}

// Server validates requests and serves embedded browser assets.
type Server struct {
	// assets is the immutable embedded shell; it is not workspace content.
	assets fs.FS
	// workspace and review are consumer-owned domain services reached only by
	// validated route handlers.
	workspace Workspace
	review    ReviewStore
	// boundHost is compared byte-for-byte with request.Host on every request.
	boundHost string
	// assetPermits bounds image streams across all browser tabs. A request that
	// cannot obtain a permit before cancellation performs no workspace read.
	assetPermits chan struct{}
}

// Workspace is the server's consumer-owned read-only view of an indexed
// workspace. Operating-system paths do not cross this interface.
type Workspace interface {
	// Snapshot returns the current immutable navigation index.
	Snapshot(context.Context) (workspace.Snapshot, error)
	// ReadDocument reopens one already indexed Markdown identity.
	ReadDocument(context.Context, string) (workspace.DocumentContent, error)
	// ReadAsset keeps a contained regular-file handle scoped to visit.
	ReadAsset(context.Context, string, string, func(io.Reader, int64) error) error
}

// ReviewStore is the server's document-scoped review view. Implementations
// derive sidecars from indexed Markdown identities rather than accepting
// client-supplied sidecar paths.
type ReviewStore interface {
	// Read resolves persisted anchors against the current Markdown.
	Read(context.Context, string) (review.Snapshot, error)
	// CreateThread appends the first message after checking both file revisions.
	CreateThread(context.Context, review.CreateThreadInput) (review.CreateThreadResult, error)
	// Reply appends one human message to an existing thread.
	Reply(context.Context, review.ReplyInput) (review.MutationResult, error)
	// ChangeStatus applies one browser-allowed status transition.
	ChangeStatus(context.Context, review.ChangeStatusInput) (review.MutationResult, error)
	// DeleteThread removes an unreplied thread after revision checks.
	DeleteThread(context.Context, review.DeleteThreadInput) (review.DeleteThreadResult, error)
}

// New constructs a loopback server. BoundHost must exactly match the
// listener's loopback host and port, for example 127.0.0.1:4242.
func New(config Config) (*Server, error) {
	if config.Assets == nil {
		return nil, fmt.Errorf("server assets are required")
	}
	if config.Workspace == nil {
		return nil, fmt.Errorf("server workspace is required")
	}
	if config.Review == nil {
		return nil, fmt.Errorf("server review store is required")
	}
	if config.BoundHost == "" {
		return nil, fmt.Errorf("server bound host is required")
	}
	return &Server{
		assets:       config.Assets,
		workspace:    config.Workspace,
		review:       config.Review,
		boundHost:    config.BoundHost,
		assetPermits: make(chan struct{}, limits.MaxConcurrentImageStreams),
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
	if request.Host != server.boundHost {
		server.writeError(response, http.StatusBadRequest, "invalidHost", "This request host is not allowed.")
		return
	}
	if strings.HasPrefix(request.URL.Path, "/api/") {
		server.serveAPI(response, request)
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

func (server *Server) serveAPI(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	switch request.URL.Path {
	case "/api/state":
		if !server.requireMethod(response, request, http.MethodGet) {
			return
		}
		server.serveState(response, request)
	case "/api/document":
		if !server.requireMethod(response, request, http.MethodGet) {
			return
		}
		server.serveDocument(response, request)
	case "/api/review":
		if !server.requireMethod(response, request, http.MethodGet) {
			return
		}
		server.serveReview(response, request)
	case "/api/asset":
		if !server.requireMethod(response, request, http.MethodGet) {
			return
		}
		server.serveWorkspaceAsset(response, request)
	case "/api/threads":
		if !server.requireMethod(response, request, http.MethodPost) {
			return
		}
		server.serveCreateThread(response, request)
	default:
		server.serveReviewOperation(response, request)
	}
}

func (server *Server) requireMethod(
	response http.ResponseWriter,
	request *http.Request,
	allowed string,
) bool {
	if request.Method == allowed {
		return true
	}
	response.Header().Set("Allow", allowed)
	server.writeError(
		response,
		http.StatusMethodNotAllowed,
		"methodNotAllowed",
		fmt.Sprintf("This endpoint only accepts %s requests.", allowed),
	)
	return false
}

func (server *Server) serveState(response http.ResponseWriter, request *http.Request) {
	// A conditional state response lets polling cheaply distinguish an unchanged
	// index while keeping all navigation and warning data server-owned.
	since, ok := workspaceRevisionQuery(request)
	if !ok {
		server.writeError(
			response,
			http.StatusBadRequest,
			"invalidWorkspaceRevision",
			"Use the current workspace revision to check for changes.",
		)
		return
	}
	snapshot, err := server.workspace.Snapshot(request.Context())
	if err != nil {
		server.writeError(response, http.StatusInternalServerError, "workspaceUnavailable", "The workspace is temporarily unavailable.")
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

func (server *Server) serveDocument(response http.ResponseWriter, request *http.Request) {
	documentPath, ok := documentPathQuery(request)
	if !ok {
		server.writeError(response, http.StatusBadRequest, "invalidDocumentPath", "Choose a valid Markdown document.")
		return
	}
	document, err := server.workspace.ReadDocument(request.Context(), documentPath)
	if err != nil {
		server.writeDocumentError(response, err)
		return
	}
	server.writeJSON(response, http.StatusOK, documentResponse{Path: document.Path, Revision: document.Revision, Source: document.Source})
}

func (server *Server) serveReview(response http.ResponseWriter, request *http.Request) {
	documentPath, ok := documentPathQuery(request)
	if !ok {
		server.writeError(response, http.StatusBadRequest, "invalidDocumentPath", "Choose a valid Markdown document.")
		return
	}
	if _, err := server.workspace.ReadDocument(request.Context(), documentPath); err != nil {
		// Reading the document first prevents a valid sidecar identity from being
		// queried after its Markdown document has left the current index.
		server.writeDocumentError(response, err)
		return
	}
	snapshot, err := server.review.Read(request.Context(), documentPath)
	if err != nil {
		server.writeReviewError(response, err)
		return
	}
	server.writeJSON(response, http.StatusOK, snapshot)
}

func documentPathQuery(request *http.Request) (string, bool) {
	// Reject duplicate query keys rather than choosing one value implicitly;
	// callers must identify exactly one indexed document.
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(values) != 1 || len(values["path"]) != 1 || values.Get("path") == "" {
		return "", false
	}
	return values.Get("path"), true
}

func workspaceRevisionQuery(request *http.Request) (*uint64, bool) {
	// A missing `since` means a full snapshot. When present, it must be one
	// positive decimal revision so malformed polling requests fail closed.
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

func (server *Server) writeDocumentError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workspace.ErrInvalidRelativePath):
		server.writeError(response, http.StatusBadRequest, "invalidDocumentPath", "Choose a valid Markdown document.")
	case errors.Is(err, workspace.ErrDocumentNotIndexed), errors.Is(err, workspace.ErrUnsafeEntry):
		server.writeError(response, http.StatusNotFound, "documentNotFound", "This Markdown document is not available.")
	case errors.Is(err, workspace.ErrDocumentTooLarge):
		server.writeError(response, http.StatusRequestEntityTooLarge, "documentTooLarge", "This Markdown file is too large to open.")
	case errors.Is(err, workspace.ErrDocumentInvalidUTF8):
		server.writeError(response, http.StatusUnprocessableEntity, "documentInvalidUtf8", "This Markdown file is not valid UTF-8.")
	case errors.Is(err, workspace.ErrDocumentRead):
		server.writeError(response, http.StatusInternalServerError, "documentUnavailable", "This Markdown document could not be read.")
	default:
		server.writeError(response, http.StatusInternalServerError, "internalError", "An internal error occurred.")
	}
}

func (server *Server) serveStaticAsset(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		http.NotFound(response, request)
		return
	}
	decodedPath, err := url.PathUnescape(request.URL.EscapedPath())
	// fs.ValidPath protects the embedded tree, while the explicit parent check
	// prevents a path such as %2e%2e from being normalized into an asset lookup.
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

func (server *Server) writeError(response http.ResponseWriter, status int, code string, message string) {
	server.writeJSON(response, status, errorResponse{Error: apiError{Code: code, Message: message}})
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
	Code    string `json:"code"`
	Message string `json:"message"`
}
