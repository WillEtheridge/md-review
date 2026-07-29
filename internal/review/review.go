// Package review owns version-1 review sidecar parsing, validation, anchor
// resolution, and semantic mutation. It never accepts an arbitrary sidecar
// path and does not own HTTP status codes or browser state.
package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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

	// ErrDocumentChanged indicates a stale text anchor cannot be rebased exactly once.
	ErrDocumentChanged = errors.New("document changed")

	// ErrReviewChanged indicates bounded semantic mutation retries were exhausted.
	ErrReviewChanged = errors.New("review sidecar kept changing")

	// ErrTargetChanged indicates an exact thread or message target changed or disappeared.
	ErrTargetChanged = errors.New("review target changed")

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

// Document is a validated, lossless schema-version-1 sidecar.
type Document struct {
	root        *value
	threadsNode *value
	threads     []Thread
}

// Decode validates a complete schema-version-1 sidecar while retaining raw
// values for every unmutated field.
func Decode(data []byte) (*Document, error) {
	if int64(len(data)) > limits.MaxReviewSidecarBytes {
		return nil, ErrTooLarge
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: sidecar must be UTF-8", ErrInvalid)
	}
	root, err := parseJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if root.kind != objectValue {
		return nil, fmt.Errorf("%w: root must be an object", ErrInvalid)
	}

	schemaNode, ok := root.get("schemaVersion")
	if !ok || schemaNode.kind != numberValue {
		return nil, fmt.Errorf("%w: schemaVersion must be an integer", ErrInvalid)
	}
	version, err := exactUint(schemaNode, "schemaVersion")
	if err != nil {
		return nil, err
	}
	if version != 1 {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedSchema, version)
	}

	threadsNode, ok := root.get("threads")
	if !ok || threadsNode.kind != arrayValue {
		return nil, fmt.Errorf("%w: threads must be an array", ErrInvalid)
	}
	threads, err := validateThreads(threadsNode)
	if err != nil {
		return nil, err
	}
	return &Document{root: root, threadsNode: threadsNode, threads: threads}, nil
}

// NewDocument constructs an empty schema-version-1 sidecar.
func NewDocument() *Document {
	document, err := Decode([]byte(`{"schemaVersion":1,"threads":[]}`))
	if err != nil {
		panic(err)
	}
	return document
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

// AppendThread adds a fully validated thread while preserving all existing
// fields and value lexemes.
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

	raw, err := json.Marshal(thread)
	if err != nil {
		return fmt.Errorf("encode new thread: %w", err)
	}
	node, err := parseJSON(raw)
	if err != nil {
		return fmt.Errorf("parse encoded thread: %w", err)
	}
	node.markTreeDirty()
	document.threadsNode.items = append(document.threadsNode.items, node)
	document.threadsNode.markDirty()
	document.root.markDirty()
	document.threads = append(document.threads, cloneThread(thread))
	return nil
}

// Bytes returns deterministic valid JSON and rejects an oversized result.
func (document *Document) Bytes() ([]byte, error) {
	result, err := document.root.bytes()
	if err != nil {
		return nil, err
	}
	if int64(len(result)) > limits.MaxReviewSidecarBytes {
		return nil, ErrTooLarge
	}
	if _, err := Decode(result); err != nil {
		return nil, fmt.Errorf("validate emitted sidecar: %w", err)
	}
	return result, nil
}

func validateThreads(node *value) ([]Thread, error) {
	threadIDs := make(map[string]struct{}, len(node.items))
	messageIDs := make(map[string]struct{})
	threads := make([]Thread, 0, len(node.items))
	for index, threadNode := range node.items {
		thread, err := decodeThread(threadNode)
		if err != nil {
			return nil, fmt.Errorf("thread %d: %w", index, err)
		}
		if _, exists := threadIDs[thread.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate thread ID %q", ErrInvalid, thread.ID)
		}
		threadIDs[thread.ID] = struct{}{}
		for _, message := range thread.Messages {
			if _, exists := messageIDs[message.ID]; exists {
				return nil, fmt.Errorf("%w: duplicate message ID %q", ErrInvalid, message.ID)
			}
			messageIDs[message.ID] = struct{}{}
		}
		threads = append(threads, thread)
	}
	return threads, nil
}

func decodeThread(node *value) (Thread, error) {
	if node.kind != objectValue {
		return Thread{}, fmt.Errorf("%w: thread must be an object", ErrInvalid)
	}
	id, err := requiredNonEmptyString(node, "id")
	if err != nil {
		return Thread{}, err
	}
	anchorNode, ok := node.get("anchor")
	if !ok {
		return Thread{}, fmt.Errorf("%w: anchor is required", ErrInvalid)
	}
	anchor, err := decodeAnchor(anchorNode)
	if err != nil {
		return Thread{}, err
	}
	statusText, err := requiredNonEmptyString(node, "status")
	if err != nil {
		return Thread{}, err
	}
	status := ThreadStatus(statusText)
	if !validStatus(status) {
		return Thread{}, fmt.Errorf("%w: invalid thread status %q", ErrInvalid, status)
	}
	messagesNode, ok := node.get("messages")
	if !ok || messagesNode.kind != arrayValue || len(messagesNode.items) == 0 {
		return Thread{}, fmt.Errorf("%w: messages must be a non-empty array", ErrInvalid)
	}
	messages := make([]Message, 0, len(messagesNode.items))
	for index, messageNode := range messagesNode.items {
		message, messageErr := decodeMessage(messageNode)
		if messageErr != nil {
			return Thread{}, fmt.Errorf("message %d: %w", index, messageErr)
		}
		messages = append(messages, message)
	}
	return Thread{ID: id, Anchor: anchor, Status: status, Messages: messages}, nil
}

func decodeAnchor(node *value) (Anchor, error) {
	if node.kind != objectValue {
		return Anchor{}, fmt.Errorf("%w: anchor must be an object", ErrInvalid)
	}
	typeText, err := requiredNonEmptyString(node, "type")
	if err != nil {
		return Anchor{}, err
	}
	switch AnchorType(typeText) {
	case AnchorDocument:
		return Anchor{Type: AnchorDocument}, nil
	case AnchorText:
		rangeNode, ok := node.get("range")
		if !ok || rangeNode.kind != objectValue {
			return Anchor{}, fmt.Errorf("%w: text anchor range must be an object", ErrInvalid)
		}
		start, err := requiredUint(rangeNode, "start")
		if err != nil {
			return Anchor{}, err
		}
		end, err := requiredUint(rangeNode, "end")
		if err != nil {
			return Anchor{}, err
		}
		if start > end {
			return Anchor{}, fmt.Errorf("%w: text anchor range is reversed", ErrInvalid)
		}
		source, err := requiredNonEmptyString(node, "source")
		if err != nil {
			return Anchor{}, err
		}
		if end-start != uint64(len(source)) {
			return Anchor{}, fmt.Errorf("%w: text anchor range does not match source bytes", ErrInvalid)
		}
		if int64(len(source)) > limits.MaxTextAnchorSourceBytes {
			return Anchor{}, fmt.Errorf("%w: text anchor source exceeds content limit", ErrInvalid)
		}
		text, err := requiredString(node, "text")
		if err != nil {
			return Anchor{}, err
		}
		return Anchor{
			Type:   AnchorText,
			Range:  &ByteRange{Start: start, End: end},
			Source: source,
			Text:   text,
		}, nil
	default:
		return Anchor{}, fmt.Errorf("%w: invalid anchor type %q", ErrInvalid, typeText)
	}
}

func decodeMessage(node *value) (Message, error) {
	if node.kind != objectValue {
		return Message{}, fmt.Errorf("%w: message must be an object", ErrInvalid)
	}
	id, err := requiredNonEmptyString(node, "id")
	if err != nil {
		return Message{}, err
	}
	authorNode, ok := node.get("author")
	if !ok || authorNode.kind != objectValue {
		return Message{}, fmt.Errorf("%w: author must be an object", ErrInvalid)
	}
	authorType, err := requiredNonEmptyString(authorNode, "type")
	if err != nil {
		return Message{}, err
	}
	if authorType != "human" && authorType != "agent" {
		return Message{}, fmt.Errorf("%w: invalid author type %q", ErrInvalid, authorType)
	}
	authorName, err := requiredNonEmptyString(authorNode, "name")
	if err != nil {
		return Message{}, err
	}
	body, err := requiredString(node, "body")
	if err != nil {
		return Message{}, err
	}
	if int64(len(body)) > limits.MaxPersistedMessageBodyBytes {
		return Message{}, fmt.Errorf("%w: message body exceeds content limit", ErrInvalid)
	}
	createdAt, err := requiredString(node, "createdAt")
	if err != nil {
		return Message{}, err
	}
	if err := validateUTCTimestamp(createdAt); err != nil {
		return Message{}, fmt.Errorf("%w: createdAt %v", ErrInvalid, err)
	}
	var editedAt *string
	if editedNode, exists := node.get("editedAt"); exists {
		edited, editedErr := decodeString(editedNode, "editedAt")
		if editedErr != nil {
			return Message{}, editedErr
		}
		if timestampErr := validateUTCTimestamp(edited); timestampErr != nil {
			return Message{}, fmt.Errorf("%w: editedAt %v", ErrInvalid, timestampErr)
		}
		editedAt = &edited
	}
	return Message{
		ID:        id,
		Author:    Author{Type: authorType, Name: authorName},
		Body:      body,
		CreatedAt: createdAt,
		EditedAt:  editedAt,
	}, nil
}

func validateThreadModel(thread Thread) error {
	raw, err := json.Marshal(thread)
	if err != nil {
		return fmt.Errorf("%w: encode thread: %v", ErrInvalidOperation, err)
	}
	node, err := parseJSON(raw)
	if err != nil {
		return fmt.Errorf("%w: parse thread: %v", ErrInvalidOperation, err)
	}
	if _, err := decodeThread(node); err != nil {
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

func exactUint(node *value, name string) (uint64, error) {
	if node.kind != numberValue {
		return 0, fmt.Errorf("%w: %s must be an integer", ErrInvalid, name)
	}
	number := json.Number(string(node.raw))
	result, err := strconv.ParseUint(number.String(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s must be a non-negative integer", ErrInvalid, name)
	}
	return result, nil
}

func requiredUint(object *value, name string) (uint64, error) {
	field, ok := object.get(name)
	if !ok {
		return 0, fmt.Errorf("%w: %s is required", ErrInvalid, name)
	}
	return exactUint(field, name)
}

func requiredString(object *value, name string) (string, error) {
	field, ok := object.get(name)
	if !ok {
		return "", fmt.Errorf("%w: %s is required", ErrInvalid, name)
	}
	return decodeString(field, name)
}

func requiredNonEmptyString(object *value, name string) (string, error) {
	result, err := requiredString(object, name)
	if err != nil {
		return "", err
	}
	if result == "" {
		return "", fmt.Errorf("%w: %s must not be empty", ErrInvalid, name)
	}
	return result, nil
}

func decodeString(node *value, name string) (string, error) {
	if node.kind != stringValueKind {
		return "", fmt.Errorf("%w: %s must be a string", ErrInvalid, name)
	}
	var result string
	if err := json.Unmarshal(node.raw, &result); err != nil {
		return "", fmt.Errorf("%w: %s is invalid", ErrInvalid, name)
	}
	if !utf8.ValidString(result) {
		return "", fmt.Errorf("%w: %s must be UTF-8", ErrInvalid, name)
	}
	return result, nil
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
