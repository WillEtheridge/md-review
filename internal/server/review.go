package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"mdreview.dev/mdreview/internal/limits"
	"mdreview.dev/mdreview/internal/review"
)

type reviewOperationRoute uint8

const (
	// reviewOperationUnknown is returned for paths that do not match a fixed
	// mutation route.
	reviewOperationUnknown reviewOperationRoute = iota
	// reviewOperationReply appends a message to the encoded thread ID.
	reviewOperationReply
	// reviewOperationStatus changes the encoded thread status.
	reviewOperationStatus
	// reviewOperationDelete removes the encoded thread.
	reviewOperationDelete
)

// serveReviewOperation handles the fixed set of mutations whose target IDs
// are encoded in their route segment.
func (server *Server) serveReviewOperation(
	response http.ResponseWriter,
	request *http.Request,
) {
	operation, targetID, ok := parseReviewOperationRoute(request.URL.Path)
	if !ok {
		server.writeError(response, http.StatusNotFound, "endpointNotFound", "This API endpoint does not exist.")
		return
	}

	switch operation {
	case reviewOperationReply:
		if !server.requireMethod(response, request, http.MethodPost) {
			return
		}
		server.serveReply(response, request, targetID)
	case reviewOperationStatus:
		if !server.requireMethod(response, request, http.MethodPatch) {
			return
		}
		server.serveChangeStatus(response, request, targetID)
	case reviewOperationDelete:
		if !server.requireMethod(response, request, http.MethodDelete) {
			return
		}
		server.serveDeleteThread(response, request, targetID)
	default:
		server.writeError(response, http.StatusNotFound, "endpointNotFound", "This API endpoint does not exist.")
	}
}

func (server *Server) serveCreateThread(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !server.validateMutationRequest(response, request) {
		return
	}

	input, err := decodeCreateThreadRequest(response, request)
	if err != nil {
		server.writeMutationDecodeError(
			response,
			err,
			"Check the comment and selected text, then try again.",
		)
		return
	}
	if !server.requireMutationDocument(response, request, input.DocumentPath) {
		return
	}
	result, err := server.review.CreateThread(request.Context(), input)
	if err != nil {
		server.writeReviewError(response, err)
		return
	}
	server.writeJSON(response, http.StatusCreated, result)
}

func (server *Server) serveReply(
	response http.ResponseWriter,
	request *http.Request,
	threadID string,
) {
	if !server.validateMutationRequest(response, request) {
		return
	}
	input, err := decodeReplyRequest(response, request)
	if err != nil {
		server.writeMutationDecodeError(response, err, "Check this review change, then try again.")
		return
	}
	input.ThreadID = threadID
	if !server.requireMutationDocument(response, request, input.DocumentPath) {
		return
	}
	result, err := server.review.Reply(request.Context(), input)
	if err != nil {
		server.writeReviewError(response, err)
		return
	}
	server.writeJSON(response, http.StatusCreated, result)
}

func (server *Server) serveChangeStatus(
	response http.ResponseWriter,
	request *http.Request,
	threadID string,
) {
	if !server.validateMutationRequest(response, request) {
		return
	}
	input, err := decodeChangeStatusRequest(response, request)
	if err != nil {
		server.writeMutationDecodeError(response, err, "Check this review change, then try again.")
		return
	}
	input.ThreadID = threadID
	if !server.requireMutationDocument(response, request, input.DocumentPath) {
		return
	}
	result, err := server.review.ChangeStatus(request.Context(), input)
	if err != nil {
		server.writeReviewError(response, err)
		return
	}
	server.writeJSON(response, http.StatusOK, result)
}

func (server *Server) serveDeleteThread(
	response http.ResponseWriter,
	request *http.Request,
	threadID string,
) {
	if !server.validateMutationRequest(response, request) {
		return
	}
	input, err := decodeDeleteThreadRequest(response, request)
	if err != nil {
		server.writeMutationDecodeError(response, err, "Check this review change, then try again.")
		return
	}
	input.ThreadID = threadID
	if !server.requireMutationDocument(response, request, input.DocumentPath) {
		return
	}
	result, err := server.review.DeleteThread(request.Context(), input)
	if err != nil {
		server.writeReviewError(response, err)
		return
	}
	server.writeJSON(response, http.StatusOK, result)
}

func (server *Server) validateMutationRequest(
	response http.ResponseWriter,
	request *http.Request,
) bool {
	// GET requests are deliberately less restricted because reads are protected
	// by the exact Host check. Mutations additionally require this origin and
	// JSON content type to block cross-site form and fetch submissions.
	if request.Header.Get("Origin") != "http://"+server.boundHost {
		server.writeError(response, http.StatusForbidden, "invalidOrigin", "This mutation origin is not allowed.")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		server.writeError(response, http.StatusUnsupportedMediaType, "unsupportedMediaType", "Send this request as JSON.")
		return false
	}
	if request.ContentLength > limits.MaxMutationRequestBodyBytes {
		server.writeError(response, http.StatusRequestEntityTooLarge, "requestTooLarge", "This review request is too large.")
		return false
	}
	return true
}

func (server *Server) requireMutationDocument(
	response http.ResponseWriter,
	request *http.Request,
	documentPath string,
) bool {
	if _, err := server.workspace.ReadDocument(request.Context(), documentPath); err != nil {
		server.writeDocumentError(response, err)
		return false
	}
	return true
}

func (server *Server) writeMutationDecodeError(
	response http.ResponseWriter,
	err error,
	invalidOperationMessage string,
) {
	if isRequestTooLarge(err) {
		server.writeError(response, http.StatusRequestEntityTooLarge, "requestTooLarge", "This review request is too large.")
		return
	}
	server.writeError(response, http.StatusUnprocessableEntity, "invalidReviewOperation", invalidOperationMessage)
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
		return reviewOperationReply, id, true
	case len(segments) == 4 &&
		segments[0] == "api" &&
		segments[1] == "threads" &&
		segments[3] == "status":
		id, ok := decodeRouteID(segments[2])
		if !ok {
			return reviewOperationUnknown, "", false
		}
		return reviewOperationStatus, id, true
	case len(segments) == 3 &&
		segments[0] == "api" &&
		segments[1] == "threads":
		id, ok := decodeRouteID(segments[2])
		if !ok {
			return reviewOperationUnknown, "", false
		}
		return reviewOperationDelete, id, true
	default:
		return reviewOperationUnknown, "", false
	}
}

func decodeRouteID(segment string) (string, bool) {
	// IDs are encoded as a canonical base64url segment so arbitrary UTF-8 IDs
	// cannot introduce route separators or alternate decoded spellings.
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
	var transport createThreadRequest
	if err := decodeMutationJSON(response, request, &transport); err != nil {
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
	if err := decodeMutationJSON(response, request, &transport); err != nil {
		return review.ReplyInput{}, err
	}
	return review.ReplyInput{
		DocumentPath:             transport.DocumentPath,
		ExpectedDocumentRevision: transport.ExpectedDocumentRevision,
		ExpectedReviewRevision:   transport.ExpectedReviewRevision,
		MessageBody:              transport.Message.Body,
	}, nil
}

func decodeChangeStatusRequest(
	response http.ResponseWriter,
	request *http.Request,
) (review.ChangeStatusInput, error) {
	var transport statusOperationRequest
	if err := decodeMutationJSON(response, request, &transport); err != nil {
		return review.ChangeStatusInput{}, err
	}
	return review.ChangeStatusInput{
		DocumentPath:             transport.DocumentPath,
		ExpectedDocumentRevision: transport.ExpectedDocumentRevision,
		ExpectedReviewRevision:   transport.ExpectedReviewRevision,
		Status:                   transport.Status,
	}, nil
}

func decodeDeleteThreadRequest(
	response http.ResponseWriter,
	request *http.Request,
) (review.DeleteThreadInput, error) {
	var transport reviewOperationRequest
	if err := decodeMutationJSON(response, request, &transport); err != nil {
		return review.DeleteThreadInput{}, err
	}
	return review.DeleteThreadInput{
		DocumentPath:             transport.DocumentPath,
		ExpectedDocumentRevision: transport.ExpectedDocumentRevision,
		ExpectedReviewRevision:   transport.ExpectedReviewRevision,
	}, nil
}

func decodeMutationJSON(
	response http.ResponseWriter,
	request *http.Request,
	destination any,
) error {
	// MaxBytesReader bounds the body even when Content-Length is absent or
	// dishonest; DisallowUnknownFields keeps the browser/API contract strict.
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
	// Decode the discriminator first, then decode the selected shape strictly so
	// document anchors cannot smuggle text fields and vice versa.
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

func (server *Server) writeReviewError(response http.ResponseWriter, err error) {
	var conflict *review.ConflictError
	if errors.As(err, &conflict) {
		code := "reviewChanged"
		message := "The review changed on disk. Your comment was not submitted."
		if errors.Is(conflict, review.ErrDocumentChanged) {
			code = "documentChanged"
			message = "The document changed on disk. Your change was not submitted."
		}
		server.writeConflict(response, code, message, conflict.Current)
		return
	}
	switch {
	case errors.Is(err, review.ErrInvalidOperation):
		server.writeError(response, http.StatusUnprocessableEntity, "invalidReviewOperation", "Check the comment and selected text, then try again.")
	case errors.Is(err, review.ErrTooLarge):
		server.writeError(response, http.StatusRequestEntityTooLarge, "reviewTooLarge", "This review sidecar is too large to open or update.")
	case errors.Is(err, review.ErrUnsupportedSchema):
		server.writeError(response, http.StatusUnprocessableEntity, "reviewUnsupportedSchema", "This review uses a newer unsupported schema version.")
	case errors.Is(err, review.ErrInvalid):
		server.writeError(response, http.StatusUnprocessableEntity, "reviewInvalid", "This review sidecar is invalid and was left unchanged.")
	case errors.Is(err, review.ErrUnsafe):
		server.writeError(response, http.StatusUnprocessableEntity, "reviewUnsafe", "This review sidecar is not a safe regular file.")
	case errors.Is(err, review.ErrUnavailable):
		server.writeError(response, http.StatusInternalServerError, "reviewUnavailable", "This review sidecar could not be read or updated.")
	default:
		server.writeError(response, http.StatusInternalServerError, "internalError", "An internal error occurred.")
	}
}

func (server *Server) writeConflict(
	response http.ResponseWriter,
	code string,
	message string,
	current review.CurrentRevisions,
) {
	server.writeJSON(response, http.StatusConflict, conflictResponse{
		Error:   apiError{Code: code, Message: message},
		Current: current,
	})
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

type reviewOperationRequest struct {
	DocumentPath             string `json:"documentPath"`
	ExpectedDocumentRevision string `json:"expectedDocumentRevision"`
	ExpectedReviewRevision   string `json:"expectedReviewRevision"`
}

type replyOperationRequest struct {
	reviewOperationRequest
	Message struct {
		Body string `json:"body"`
	} `json:"message"`
}

type statusOperationRequest struct {
	reviewOperationRequest
	Status review.ThreadStatus `json:"status"`
}

type conflictResponse struct {
	Error   apiError                `json:"error"`
	Current review.CurrentRevisions `json:"current"`
}
