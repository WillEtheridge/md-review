package sidecar

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const MaxBytes = 8 << 20

var (
	ErrReadOnly          = errors.New("sidecar is read-only")
	ErrTargetChanged     = errors.New("target changed")
	ErrTargetNotFound    = errors.New("target not found")
	ErrDuplicateID       = errors.New("duplicate ID")
	ErrUnsupportedSchema = errors.New("unsupported schema")
)

type Document struct {
	root    *value
	threads *value
}

type Message struct {
	ID        string
	Author    string
	Name      string
	Body      string
	CreatedAt time.Time
}

func Decode(data []byte) (*Document, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("%w: sidecar exceeds %d bytes", ErrReadOnly, MaxBytes)
	}
	root, err := parseJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrReadOnly, err)
	}
	if root.kind != kindObject {
		return nil, fmt.Errorf("%w: root must be an object", ErrReadOnly)
	}
	schema, ok := root.get("schemaVersion")
	if !ok || schema.kind != kindNumber {
		return nil, fmt.Errorf("%w: schemaVersion must be a number", ErrReadOnly)
	}
	number := json.Number(string(schema.raw))
	version, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: schemaVersion must be an integer", ErrReadOnly)
	}
	if version != 1 {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedSchema, version)
	}
	threads, ok := root.get("threads")
	if !ok || threads.kind != kindArray {
		return nil, fmt.Errorf("%w: threads must be an array", ErrReadOnly)
	}
	document := &Document{root: root, threads: threads}
	if err := document.validateIDsAndRanges(); err != nil {
		return nil, err
	}
	return document, nil
}

func (d *Document) Bytes() ([]byte, error) {
	result, err := d.root.bytes()
	if err != nil {
		return nil, err
	}
	if len(result) > MaxBytes {
		return nil, fmt.Errorf("%w: mutation would exceed %d bytes", ErrReadOnly, MaxBytes)
	}
	return result, nil
}

func (d *Document) SetStatus(threadID, status string) error {
	switch status {
	case "open", "handled", "resolved":
	default:
		return fmt.Errorf("invalid status %q", status)
	}
	thread, err := d.thread(threadID)
	if err != nil {
		return err
	}
	thread.set("status", stringValue(status))
	d.threads.markDirty()
	d.root.markDirty()
	return nil
}

func (d *Document) AppendMessage(threadID string, message Message) error {
	if message.ID == "" || message.Body == "" {
		return errors.New("message ID and body are required")
	}
	if message.Author != "human" && message.Author != "agent" {
		return errors.New("message author must be human or agent")
	}
	if d.messageIDExists(message.ID) {
		return fmt.Errorf("%w: message %q", ErrDuplicateID, message.ID)
	}
	thread, err := d.thread(threadID)
	if err != nil {
		return err
	}
	messages, ok := thread.get("messages")
	if !ok || messages.kind != kindArray {
		return fmt.Errorf("%w: thread messages must be an array", ErrReadOnly)
	}
	createdAt := message.CreatedAt.UTC().Format(time.RFC3339Nano)
	raw := []byte(fmt.Sprintf(
		`{"id":%s,"author":{"type":%s,"name":%s},"body":%s,"createdAt":%s}`,
		quoted(message.ID),
		quoted(message.Author),
		quoted(message.Name),
		quoted(message.Body),
		quoted(createdAt),
	))
	entry, err := parseJSON(raw)
	if err != nil {
		return err
	}
	entry.dirty = true
	messages.items = append(messages.items, entry)
	messages.markDirty()
	thread.markDirty()
	d.threads.markDirty()
	d.root.markDirty()
	return nil
}

func (d *Document) ThreadFingerprint(threadID string) (string, error) {
	thread, err := d.thread(threadID)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(thread.raw)
	return hex.EncodeToString(sum[:]), nil
}

func (d *Document) validateIDsAndRanges() error {
	threadIDs := make(map[string]struct{})
	messageIDs := make(map[string]struct{})
	for _, thread := range d.threads.items {
		if thread.kind != kindObject {
			return fmt.Errorf("%w: thread must be an object", ErrReadOnly)
		}
		id, err := requiredString(thread, "id")
		if err != nil {
			return err
		}
		if _, exists := threadIDs[id]; exists {
			return fmt.Errorf("%w: thread %q", ErrDuplicateID, id)
		}
		threadIDs[id] = struct{}{}
		messages, ok := thread.get("messages")
		if !ok || messages.kind != kindArray {
			return fmt.Errorf("%w: messages must be an array", ErrReadOnly)
		}
		for _, message := range messages.items {
			if message.kind != kindObject {
				return fmt.Errorf("%w: message must be an object", ErrReadOnly)
			}
			messageID, err := requiredString(message, "id")
			if err != nil {
				return err
			}
			if _, exists := messageIDs[messageID]; exists {
				return fmt.Errorf("%w: message %q", ErrDuplicateID, messageID)
			}
			messageIDs[messageID] = struct{}{}
		}
		if err := validateTextRange(thread); err != nil {
			return err
		}
	}
	return nil
}

func validateTextRange(thread *value) error {
	anchor, ok := thread.get("anchor")
	if !ok || anchor.kind != kindObject {
		return fmt.Errorf("%w: anchor must be an object", ErrReadOnly)
	}
	anchorType, err := requiredString(anchor, "type")
	if err != nil {
		return err
	}
	if anchorType == "document" {
		return nil
	}
	if anchorType != "text" {
		return fmt.Errorf("%w: invalid anchor type", ErrReadOnly)
	}
	rangeValue, ok := anchor.get("range")
	if !ok || rangeValue.kind != kindObject {
		return fmt.Errorf("%w: text range must be an object", ErrReadOnly)
	}
	start, err := requiredUint(rangeValue, "start")
	if err != nil {
		return err
	}
	end, err := requiredUint(rangeValue, "end")
	if err != nil {
		return err
	}
	if start > end {
		return fmt.Errorf("%w: reversed text range", ErrReadOnly)
	}
	return nil
}

func requiredUint(object *value, name string) (uint64, error) {
	field, ok := object.get(name)
	if !ok || field.kind != kindNumber {
		return 0, fmt.Errorf("%w: %s must be a number", ErrReadOnly, name)
	}
	number := json.Number(string(field.raw))
	result, err := strconv.ParseUint(number.String(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s must be a non-negative integer", ErrReadOnly, name)
	}
	return result, nil
}

func requiredString(object *value, name string) (string, error) {
	field, ok := object.get(name)
	if !ok || field.kind != kindString {
		return "", fmt.Errorf("%w: %s must be a string", ErrReadOnly, name)
	}
	var result string
	if err := json.Unmarshal(field.raw, &result); err != nil {
		return "", fmt.Errorf("%w: %s is invalid", ErrReadOnly, name)
	}
	return result, nil
}

func (d *Document) thread(id string) (*value, error) {
	for _, thread := range d.threads.items {
		threadID, err := requiredString(thread, "id")
		if err != nil {
			return nil, err
		}
		if threadID == id {
			return thread, nil
		}
	}
	return nil, fmt.Errorf("%w: thread %q", ErrTargetNotFound, id)
}

func (d *Document) messageIDExists(id string) bool {
	for _, thread := range d.threads.items {
		messages, ok := thread.get("messages")
		if !ok || messages.kind != kindArray {
			continue
		}
		for _, message := range messages.items {
			messageID, err := requiredString(message, "id")
			if err == nil && messageID == id {
				return true
			}
		}
	}
	return false
}

func quoted(text string) string {
	raw, _ := json.Marshal(text)
	return string(raw)
}

func RawValue(data []byte, path ...string) ([]byte, error) {
	root, err := parseJSON(data)
	if err != nil {
		return nil, err
	}
	current := root
	for _, part := range path {
		next, ok := current.get(part)
		if !ok {
			return nil, fmt.Errorf("missing %s", part)
		}
		current = next
	}
	return bytes.TrimSpace(clone(current.raw)), nil
}
