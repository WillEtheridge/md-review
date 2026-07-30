// Package review owns version-1 review sidecar parsing, validation, anchor
// resolution, and semantic mutation. It never accepts an arbitrary sidecar
// path and does not own HTTP status codes or browser state.
package review

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"mdreview.dev/mdreview/internal/limits"
)

var (
	// ErrInvalid indicates malformed JSON or an invalid schema-version-1 value.
	ErrInvalid = errors.New("review sidecar is invalid")

	// ErrUnsupportedSchema indicates a syntactically valid, unsupported schema version.
	ErrUnsupportedSchema = errors.New("review sidecar schema is unsupported")

	// ErrTooLarge indicates an existing or resulting sidecar exceeds its content limit.
	ErrTooLarge = errors.New("review sidecar exceeds content limit")

	// ErrInvalidOperation indicates an invalid browser-originated semantic operation.
	ErrInvalidOperation = errors.New("invalid review operation")

	// ErrDocumentChanged indicates the Markdown no longer matches the browser's
	// whole-document revision.
	ErrDocumentChanged = errors.New("document changed")

	// ErrReviewChanged indicates the adjacent sidecar no longer matches the
	// browser's whole-sidecar revision.
	ErrReviewChanged = errors.New("review sidecar changed")

	// ErrUnsafe indicates the derived sidecar is a symlink or non-regular file.
	ErrUnsafe = errors.New("review sidecar is unsafe")

	// ErrUnavailable indicates the sidecar could not be read or written safely.
	ErrUnavailable = errors.New("review sidecar is unavailable")
)

// AnchorType identifies a persisted review anchor.
type AnchorType string

const (
	// AnchorDocument identifies a document-level thread.
	AnchorDocument AnchorType = "document"

	// AnchorText identifies a thread anchored to exact Markdown source bytes.
	AnchorText AnchorType = "text"
)

// ThreadStatus is the persisted workflow state of a review thread.
type ThreadStatus string

const (
	// StatusOpen means feedback requires attention.
	StatusOpen ThreadStatus = "open"

	// StatusHandled means a person or agent reports that feedback was addressed.
	StatusHandled ThreadStatus = "handled"

	// StatusResolved means the human reviewer accepted and closed the thread.
	StatusResolved ThreadStatus = "resolved"
)

// ByteRange is a zero-based, half-open UTF-8 byte range.
type ByteRange struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
}

// Anchor is the immutable persisted location and selected content of a thread.
// Range, Source, and Text are present only for text anchors.
type Anchor struct {
	Type   AnchorType `json:"type"`
	Range  *ByteRange `json:"range,omitempty"`
	Source string     `json:"source,omitempty"`
	Text   string     `json:"text,omitempty"`
}

// Author is the presentation-only author recorded on a review message.
type Author struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// Message is one persisted review message.
type Message struct {
	ID        string  `json:"id"`
	Author    Author  `json:"author"`
	Body      string  `json:"body"`
	CreatedAt string  `json:"createdAt"`
	EditedAt  *string `json:"editedAt,omitempty"`
}

// Thread is one persisted review conversation.
type Thread struct {
	ID       string       `json:"id"`
	Anchor   Anchor       `json:"anchor"`
	Status   ThreadStatus `json:"status"`
	Messages []Message    `json:"messages"`
}

// AttachmentState describes a calculated anchor state that is never persisted.
type AttachmentState string

const (
	// AttachmentDocument identifies a document-level thread.
	AttachmentDocument AttachmentState = "document"

	// AttachmentAttached identifies a text anchor attached to CurrentRange.
	AttachmentAttached AttachmentState = "attached"

	// AttachmentDetached identifies a text anchor with no unambiguous current range.
	AttachmentDetached AttachmentState = "detached"
)

// Attachment is the current calculated location of a persisted anchor.
type Attachment struct {
	State        AttachmentState `json:"state"`
	CurrentRange *ByteRange      `json:"currentRange,omitempty"`
}

// ResolvedThread combines persisted thread data with its calculated attachment.
type ResolvedThread struct {
	ID         string       `json:"id"`
	Anchor     Anchor       `json:"anchor"`
	Attachment Attachment   `json:"attachment"`
	Status     ThreadStatus `json:"status"`
	Messages   []Message    `json:"messages"`
}

// Document is a validated schema-version-1 sidecar.
type Document struct {
	threads []Thread
}

type decodedSidecar struct {
	SchemaVersion uint64       `json:"schemaVersion"`
	Threads       *[]rawThread `json:"threads"`
}

type rawThread struct {
	ID       string       `json:"id"`
	Anchor   rawAnchor    `json:"anchor"`
	Status   ThreadStatus `json:"status"`
	Messages []rawMessage `json:"messages"`
}

type rawAnchor struct {
	Type   AnchorType `json:"type"`
	Range  *ByteRange `json:"range,omitempty"`
	Source *string    `json:"source,omitempty"`
	Text   *string    `json:"text,omitempty"`
}

type rawMessage struct {
	ID        string          `json:"id"`
	Author    Author          `json:"author"`
	Body      *string         `json:"body"`
	CreatedAt string          `json:"createdAt"`
	EditedAt  json.RawMessage `json:"editedAt"`
}

var ownedJSONFieldNames = []string{
	"schemaVersion", "threads", "id", "anchor", "status", "messages",
	"type", "range", "source", "text", "start", "end", "author", "name",
	"body", "createdAt", "editedAt",
}

// Decode validates one complete schema-version-1 sidecar.
func Decode(data []byte) (*Document, error) {
	if int64(len(data)) > limits.MaxReviewSidecarBytes {
		return nil, ErrTooLarge
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: sidecar must be UTF-8", ErrInvalid)
	}
	if err := preflightJSON(data); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%w: root must be an object", ErrInvalid)
	}
	schemaRaw, ok := root["schemaVersion"]
	if !ok {
		return nil, fmt.Errorf("%w: schemaVersion must be an integer", ErrInvalid)
	}
	var version uint64
	if err := json.Unmarshal(schemaRaw, &version); err != nil {
		return nil, fmt.Errorf("%w: schemaVersion must be a non-negative integer", ErrInvalid)
	}
	if version != 1 {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedSchema, version)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded decodedSidecar
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := ensureDecoderEOF(decoder); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if decoded.SchemaVersion != 1 || decoded.Threads == nil {
		return nil, fmt.Errorf("%w: threads must be an array", ErrInvalid)
	}
	threads, err := decodedThreads(*decoded.Threads)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := validateThreads(threads); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return &Document{threads: threads}, nil
}

// NewDocument constructs an empty schema-version-1 sidecar.
func NewDocument() *Document {
	return &Document{threads: []Thread{}}
}

// Revision returns the SHA-256 revision of exact document or sidecar bytes.
func Revision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Threads returns a copy of the validated persisted threads.
func (document *Document) Threads() []Thread {
	return cloneThreads(document.threads)
}

// ResolvedThreads calculates attachment state against the current exact
// Markdown bytes without modifying the persisted anchors.
func (document *Document) ResolvedThreads(markdown []byte) []ResolvedThread {
	result := make([]ResolvedThread, 0, len(document.threads))
	for _, thread := range document.threads {
		result = append(result, ResolvedThread{
			ID:         thread.ID,
			Anchor:     cloneAnchor(thread.Anchor),
			Attachment: ResolveAnchor(markdown, thread.Anchor),
			Status:     thread.Status,
			Messages:   cloneMessages(thread.Messages),
		})
	}
	return result
}

// AppendThread adds a fully validated thread.
func (document *Document) AppendThread(thread Thread) error {
	if err := validateThreadModel(thread); err != nil {
		return err
	}
	for _, existing := range document.threads {
		if existing.ID == thread.ID {
			return fmt.Errorf("%w: duplicate thread ID %q", ErrInvalidOperation, thread.ID)
		}
		for _, existingMessage := range existing.Messages {
			for _, message := range thread.Messages {
				if existingMessage.ID == message.ID {
					return fmt.Errorf("%w: duplicate message ID %q", ErrInvalidOperation, message.ID)
				}
			}
		}
	}

	document.threads = append(document.threads, cloneThread(thread))
	return nil
}

// Bytes returns deterministic valid JSON and rejects an oversized result.
func (document *Document) Bytes() ([]byte, error) {
	if err := validateThreads(document.threads); err != nil {
		return nil, fmt.Errorf("validate emitted sidecar: %w", err)
	}
	result, err := json.MarshalIndent(struct {
		SchemaVersion uint64   `json:"schemaVersion"`
		Threads       []Thread `json:"threads"`
	}{SchemaVersion: 1, Threads: document.threads}, "", "  ")
	if err != nil {
		return nil, err
	}
	result = append(result, '\n')
	if int64(len(result)) > limits.MaxReviewSidecarBytes {
		return nil, ErrTooLarge
	}
	if _, err := Decode(result); err != nil {
		return nil, fmt.Errorf("validate emitted sidecar: %w", err)
	}
	return result, nil
}

func preflightJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := preflightValue(decoder); err != nil {
		return err
	}
	return ensureDecoderEOF(decoder)
}

func preflightValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate object member %q", key)
			}
			seen[key] = struct{}{}
			for _, owned := range ownedJSONFieldNames {
				if strings.EqualFold(key, owned) && key != owned {
					return fmt.Errorf("JSON field %q must use exact spelling %q", key, owned)
				}
			}
			if err := preflightValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := preflightValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func ensureDecoderEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func decodedThreads(rawThreads []rawThread) ([]Thread, error) {
	threads := make([]Thread, 0, len(rawThreads))
	for index, rawThread := range rawThreads {
		anchor, err := decodedAnchor(rawThread.Anchor)
		if err != nil {
			return nil, fmt.Errorf("thread %d: %w", index, err)
		}
		messages := make([]Message, 0, len(rawThread.Messages))
		for messageIndex, rawMessage := range rawThread.Messages {
			message, err := decodedMessage(rawMessage)
			if err != nil {
				return nil, fmt.Errorf("thread %d message %d: %w", index, messageIndex, err)
			}
			messages = append(messages, message)
		}
		threads = append(threads, Thread{ID: rawThread.ID, Anchor: anchor, Status: rawThread.Status, Messages: messages})
	}
	return threads, nil
}

func decodedAnchor(raw rawAnchor) (Anchor, error) {
	switch raw.Type {
	case AnchorDocument:
		if raw.Range != nil || raw.Source != nil || raw.Text != nil {
			return Anchor{}, errors.New("document anchor may contain only type")
		}
		return Anchor{Type: AnchorDocument}, nil
	case AnchorText:
		if raw.Range == nil || raw.Source == nil || raw.Text == nil {
			return Anchor{}, errors.New("text anchor requires range, source, and text")
		}
		return Anchor{Type: AnchorText, Range: raw.Range, Source: *raw.Source, Text: *raw.Text}, nil
	default:
		return Anchor{}, fmt.Errorf("invalid anchor type %q", raw.Type)
	}
}

func decodedMessage(raw rawMessage) (Message, error) {
	if raw.Body == nil {
		return Message{}, errors.New("body must be a string")
	}
	message := Message{ID: raw.ID, Author: raw.Author, Body: *raw.Body, CreatedAt: raw.CreatedAt}
	if len(raw.EditedAt) == 0 {
		return message, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw.EditedAt), []byte("null")) {
		return Message{}, errors.New("editedAt must be a string")
	}
	var editedAt string
	if err := json.Unmarshal(raw.EditedAt, &editedAt); err != nil {
		return Message{}, errors.New("editedAt must be a string")
	}
	message.EditedAt = &editedAt
	return message, nil
}

func validateThreads(threads []Thread) error {
	threadIDs := make(map[string]struct{}, len(threads))
	messageIDs := make(map[string]struct{})
	for index, thread := range threads {
		if err := validateThread(thread); err != nil {
			return fmt.Errorf("thread %d: %w", index, err)
		}
		if _, exists := threadIDs[thread.ID]; exists {
			return fmt.Errorf("duplicate thread ID %q", thread.ID)
		}
		threadIDs[thread.ID] = struct{}{}
		for _, message := range thread.Messages {
			if _, exists := messageIDs[message.ID]; exists {
				return fmt.Errorf("duplicate message ID %q", message.ID)
			}
			messageIDs[message.ID] = struct{}{}
		}
	}
	return nil
}

func validateThreadModel(thread Thread) error {
	if err := validateThread(thread); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidOperation, err)
	}
	messageIDs := make(map[string]struct{}, len(thread.Messages))
	for _, message := range thread.Messages {
		if _, exists := messageIDs[message.ID]; exists {
			return fmt.Errorf("%w: duplicate message ID %q", ErrInvalidOperation, message.ID)
		}
		messageIDs[message.ID] = struct{}{}
	}
	return nil
}

func validateThread(thread Thread) error {
	if thread.ID == "" || !utf8.ValidString(thread.ID) {
		return errors.New("thread ID must be non-empty UTF-8")
	}
	if err := validateAnchor(thread.Anchor); err != nil {
		return err
	}
	if !validStatus(thread.Status) {
		return fmt.Errorf("invalid thread status %q", thread.Status)
	}
	if len(thread.Messages) == 0 {
		return errors.New("messages must be a non-empty array")
	}
	for index, message := range thread.Messages {
		if err := validateMessage(message); err != nil {
			return fmt.Errorf("message %d: %w", index, err)
		}
	}
	return nil
}

func validateAnchor(anchor Anchor) error {
	switch anchor.Type {
	case AnchorDocument:
		if anchor.Range != nil || anchor.Source != "" || anchor.Text != "" {
			return errors.New("document anchor may contain only type")
		}
	case AnchorText:
		if anchor.Range == nil || anchor.Source == "" || !utf8.ValidString(anchor.Source) || !utf8.ValidString(anchor.Text) {
			return errors.New("text anchor requires valid range, source, and text")
		}
		if anchor.Range.Start > anchor.Range.End {
			return errors.New("text anchor range is reversed")
		}
		if anchor.Range.End-anchor.Range.Start != uint64(len(anchor.Source)) {
			return errors.New("text anchor range does not match source bytes")
		}
		if int64(len(anchor.Source)) > limits.MaxTextAnchorSourceBytes {
			return errors.New("text anchor source exceeds content limit")
		}
	default:
		return fmt.Errorf("invalid anchor type %q", anchor.Type)
	}
	return nil
}

func validateMessage(message Message) error {
	if message.ID == "" || !utf8.ValidString(message.ID) {
		return errors.New("message ID must be non-empty UTF-8")
	}
	if (message.Author.Type != "human" && message.Author.Type != "agent") || !utf8.ValidString(message.Author.Type) {
		return fmt.Errorf("invalid author type %q", message.Author.Type)
	}
	if message.Author.Name == "" || !utf8.ValidString(message.Author.Name) {
		return errors.New("author name must be non-empty UTF-8")
	}
	if !utf8.ValidString(message.Body) || int64(len(message.Body)) > limits.MaxPersistedMessageBodyBytes {
		return errors.New("message body exceeds content limit")
	}
	if err := validateUTCTimestamp(message.CreatedAt); err != nil {
		return fmt.Errorf("createdAt %w", err)
	}
	if message.EditedAt != nil {
		if err := validateUTCTimestamp(*message.EditedAt); err != nil {
			return fmt.Errorf("editedAt %w", err)
		}
	}
	return nil
}

func validateUTCTimestamp(input string) error {
	parsed, err := time.Parse(time.RFC3339Nano, input)
	if err != nil {
		return errors.New("must be an RFC3339 timestamp")
	}
	_, offsetSeconds := parsed.Zone()
	if offsetSeconds != 0 {
		return errors.New("must use UTC")
	}
	return nil
}

func validStatus(status ThreadStatus) bool {
	switch status {
	case StatusOpen, StatusHandled, StatusResolved:
		return true
	default:
		return false
	}
}

func cloneThreads(threads []Thread) []Thread {
	result := make([]Thread, len(threads))
	for index := range threads {
		result[index] = cloneThread(threads[index])
	}
	return result
}

func cloneThread(thread Thread) Thread {
	thread.Anchor = cloneAnchor(thread.Anchor)
	thread.Messages = cloneMessages(thread.Messages)
	return thread
}

func cloneAnchor(anchor Anchor) Anchor {
	if anchor.Range != nil {
		rangeCopy := *anchor.Range
		anchor.Range = &rangeCopy
	}
	return anchor
}

func cloneMessages(messages []Message) []Message {
	result := make([]Message, len(messages))
	copy(result, messages)
	for index := range result {
		if result[index].EditedAt != nil {
			edited := *result[index].EditedAt
			result[index].EditedAt = &edited
		}
	}
	return result
}

func cloneBytes(data []byte) []byte {
	return append([]byte(nil), data...)
}
