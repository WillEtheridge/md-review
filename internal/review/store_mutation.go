package review

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"mdreview.dev/mdreview/internal/filesystem"
	"mdreview.dev/mdreview/internal/limits"
)

// Reply appends one server-authored human Reviewer message. A reply reopens a
// handled or resolved thread and preserves a currently open status.
func (store *Store) Reply(ctx context.Context, input ReplyInput) (MutationResult, error) {
	if err := validateMutationOperation(input.DocumentPath, input.ExpectedDocumentRevision, input.ExpectedReviewRevision, input.ThreadID); err != nil {
		return MutationResult{}, err
	}
	if err := validateMessageBody(input.MessageBody); err != nil {
		return MutationResult{}, err
	}
	messageID, err := store.newID(messageIDPrefix)
	if err != nil {
		return MutationResult{}, fmt.Errorf("%w: create message ID: %v", ErrUnavailable, err)
	}
	if !validGeneratedID(messageID, messageIDPrefix) {
		return MutationResult{}, fmt.Errorf("%w: ID generator returned an invalid ID", ErrUnavailable)
	}
	return store.mutate(ctx, mutationInput{
		documentPath: input.DocumentPath, expectedDocumentRevision: input.ExpectedDocumentRevision,
		expectedReviewRevision: input.ExpectedReviewRevision, affectedThreadID: input.ThreadID,
		apply: func(document *Document) error {
			return document.appendReply(input.ThreadID, Message{ID: messageID, Author: Author{Type: "human", Name: "Reviewer"}, Body: input.MessageBody, CreatedAt: store.now().UTC().Format(time.RFC3339Nano)})
		},
	})
}

// EditMessage replaces one current human message body while retaining its ID,
// author, and creation time and recording a server-owned edit timestamp.
func (store *Store) EditMessage(ctx context.Context, input EditMessageInput) (MutationResult, error) {
	if err := validateMutationOperation(input.DocumentPath, input.ExpectedDocumentRevision, input.ExpectedReviewRevision, input.MessageID); err != nil {
		return MutationResult{}, err
	}
	if err := validateMessageBody(input.MessageBody); err != nil {
		return MutationResult{}, err
	}
	return store.mutate(ctx, mutationInput{
		documentPath: input.DocumentPath, expectedDocumentRevision: input.ExpectedDocumentRevision,
		expectedReviewRevision: input.ExpectedReviewRevision,
		apply: func(document *Document) error {
			_, err := document.editHumanMessage(
				input.MessageID,
				input.MessageBody,
				store.now().UTC().Format(time.RFC3339Nano),
			)
			return err
		},
		affectedThread: func(document *Document) string {
			threadID, _ := document.threadIDForMessage(input.MessageID)
			return threadID
		},
	})
}

// ChangeStatus applies one browser-allowed resolve or reopen transition.
func (store *Store) ChangeStatus(ctx context.Context, input ChangeStatusInput) (MutationResult, error) {
	if err := validateMutationOperation(input.DocumentPath, input.ExpectedDocumentRevision, input.ExpectedReviewRevision, input.ThreadID); err != nil {
		return MutationResult{}, err
	}
	if input.Status != StatusOpen && input.Status != StatusResolved {
		return MutationResult{}, fmt.Errorf("%w: browser status must be open or resolved", ErrInvalidOperation)
	}
	return store.mutate(ctx, mutationInput{documentPath: input.DocumentPath, expectedDocumentRevision: input.ExpectedDocumentRevision, expectedReviewRevision: input.ExpectedReviewRevision, affectedThreadID: input.ThreadID, apply: func(document *Document) error { return document.changeThreadStatus(input.ThreadID, input.Status) }})
}

// DeleteThread removes one target thread only while it has exactly one message.
func (store *Store) DeleteThread(ctx context.Context, input DeleteThreadInput) (DeleteThreadResult, error) {
	if err := validateMutationOperation(input.DocumentPath, input.ExpectedDocumentRevision, input.ExpectedReviewRevision, input.ThreadID); err != nil {
		return DeleteThreadResult{}, err
	}
	result, err := store.mutate(ctx, mutationInput{documentPath: input.DocumentPath, expectedDocumentRevision: input.ExpectedDocumentRevision, expectedReviewRevision: input.ExpectedReviewRevision, apply: func(document *Document) error { return document.deleteUnrepliedThread(input.ThreadID) }})
	if err != nil {
		return DeleteThreadResult{}, err
	}
	return DeleteThreadResult{DocumentRevision: result.DocumentRevision, ReviewRevision: result.ReviewRevision, DeletedThreadID: input.ThreadID}, nil
}

type mutationInput struct {
	documentPath             string
	expectedDocumentRevision string
	expectedReviewRevision   string
	affectedThreadID         string
	affectedThread           func(*Document) string
	apply                    func(*Document) error
}

func (store *Store) mutate(ctx context.Context, input mutationInput) (MutationResult, error) {
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	var markdown []byte
	var documentRevision string
	var affectedThreadID = input.affectedThreadID
	updated, err := store.filesystem.MutateFile(ctx, deriveSidecarPath(input.documentPath), filesystem.MutationOptions{MaxBytes: limits.MaxReviewSidecarBytes}, func(current []byte, exists bool) ([]byte, error) {
		markdownNow, readErr := store.readMarkdown(input.documentPath)
		if readErr != nil {
			return nil, fmt.Errorf("%w: read document: %v", ErrUnavailable, readErr)
		}
		if !utf8.Valid(markdownNow) {
			return nil, fmt.Errorf("%w: document must be UTF-8", ErrUnavailable)
		}
		markdown, documentRevision = markdownNow, Revision(markdownNow)
		var reviewRevision *string
		if exists {
			revision := Revision(current)
			reviewRevision = &revision
		}
		if documentRevision != input.expectedDocumentRevision {
			return nil, revisionConflict(ErrDocumentChanged, documentRevision, reviewRevision)
		}
		if !exists || *reviewRevision != input.expectedReviewRevision {
			return nil, revisionConflict(ErrReviewChanged, documentRevision, reviewRevision)
		}
		document, decodeErr := Decode(current)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if input.affectedThread != nil {
			affectedThreadID = input.affectedThread(document)
		}
		if applyErr := input.apply(document); applyErr != nil {
			return nil, applyErr
		}
		return document.Bytes()
	})
	if err != nil {
		return MutationResult{}, store.translateMutationError(err, input.documentPath)
	}
	result := MutationResult{DocumentRevision: documentRevision, ReviewRevision: Revision(updated)}
	if affectedThreadID == "" {
		return result, nil
	}
	document, err := Decode(updated)
	if err != nil {
		return MutationResult{}, fmt.Errorf("%w: decode emitted sidecar: %v", ErrUnavailable, err)
	}
	thread, ok := document.resolvedThread(markdown, affectedThreadID)
	if !ok {
		return MutationResult{}, fmt.Errorf("%w: emitted thread is missing", ErrUnavailable)
	}
	result.Thread = thread
	return result, nil
}

func (document *Document) resolvedThread(markdown []byte, threadID string) (ResolvedThread, bool) {
	for _, thread := range document.ResolvedThreads(markdown) {
		if thread.ID == threadID {
			return thread, true
		}
	}
	return ResolvedThread{}, false
}

func validateMutationOperation(documentPath, documentRevision, reviewRevision, targetID string) error {
	if err := validateDocumentPath(documentPath); err != nil {
		return err
	}
	if !validRevision(documentRevision) {
		return fmt.Errorf("%w: expected document revision is invalid", ErrInvalidOperation)
	}
	if !validRevision(reviewRevision) {
		return fmt.Errorf("%w: expected review revision is invalid", ErrInvalidOperation)
	}
	if targetID == "" || !utf8.ValidString(targetID) {
		return fmt.Errorf("%w: target ID must be non-empty UTF-8", ErrInvalidOperation)
	}
	return nil
}

func validateMessageBody(body string) error {
	if body == "" || !utf8.ValidString(body) {
		return fmt.Errorf("%w: message body must be non-empty UTF-8", ErrInvalidOperation)
	}
	if int64(len(body)) > limits.MaxPersistedMessageBodyBytes {
		return fmt.Errorf("%w: message body exceeds content limit", ErrInvalidOperation)
	}
	return nil
}

func (document *Document) findThreadNode(threadID string) (int, *value, bool) {
	for index := range document.threads {
		if document.threads[index].ID == threadID {
			return index, document.threadsNode.items[index], true
		}
	}
	return 0, nil, false
}

func (document *Document) findMessageNode(messageID string) (int, int, *value, *value, bool) {
	for threadIndex, thread := range document.threads {
		for messageIndex, message := range thread.Messages {
			if message.ID != messageID {
				continue
			}
			threadNode := document.threadsNode.items[threadIndex]
			messagesNode, _ := threadNode.get("messages")
			return threadIndex, messageIndex, threadNode, messagesNode.items[messageIndex], true
		}
	}
	return 0, 0, nil, nil, false
}

func (document *Document) threadIDForMessage(messageID string) (string, bool) {
	for _, thread := range document.threads {
		for _, message := range thread.Messages {
			if message.ID == messageID {
				return thread.ID, true
			}
		}
	}
	return "", false
}

func (document *Document) appendReply(threadID string, message Message) error {
	threadIndex, threadNode, ok := document.findThreadNode(threadID)
	if !ok {
		return fmt.Errorf("%w: thread %q does not exist", ErrInvalidOperation, threadID)
	}
	for _, thread := range document.threads {
		for _, existing := range thread.Messages {
			if existing.ID == message.ID {
				return fmt.Errorf("%w: duplicate message ID %q", ErrInvalidOperation, message.ID)
			}
		}
	}
	thread := cloneThread(document.threads[threadIndex])
	thread.Messages = append(thread.Messages, message)
	if thread.Status == StatusHandled || thread.Status == StatusResolved {
		thread.Status = StatusOpen
	}
	if err := validateThreadModel(thread); err != nil {
		return err
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode reply: %w", err)
	}
	messageNode, err := parseJSON(raw)
	if err != nil {
		return fmt.Errorf("parse encoded reply: %w", err)
	}
	messageNode.markTreeDirty()
	messagesNode, _ := threadNode.get("messages")
	messagesNode.items = append(messagesNode.items, messageNode)
	messagesNode.markDirty()
	if thread.Status != document.threads[threadIndex].Status {
		if err := replaceObjectString(threadNode, "status", string(thread.Status)); err != nil {
			return err
		}
	}
	threadNode.markDirty()
	document.threadsNode.markDirty()
	document.root.markDirty()
	document.threads[threadIndex] = thread
	return nil
}

func (document *Document) editHumanMessage(messageID, body, editedAt string) (string, error) {
	threadIndex, messageIndex, threadNode, messageNode, ok := document.findMessageNode(messageID)
	if !ok {
		return "", fmt.Errorf("%w: message %q does not exist", ErrInvalidOperation, messageID)
	}
	current := document.threads[threadIndex].Messages[messageIndex]
	if current.Author.Type != "human" {
		return "", fmt.Errorf("%w: only human messages can be edited", ErrInvalidOperation)
	}
	updated := current
	updated.Body = body
	updated.EditedAt = &editedAt
	thread := cloneThread(document.threads[threadIndex])
	thread.Messages[messageIndex] = updated
	if err := validateThreadModel(thread); err != nil {
		return "", err
	}
	if err := replaceObjectString(messageNode, "body", body); err != nil {
		return "", err
	}
	if err := replaceObjectString(messageNode, "editedAt", editedAt); err != nil {
		return "", err
	}
	messageNode.markDirty()
	messagesNode, _ := threadNode.get("messages")
	messagesNode.markDirty()
	threadNode.markDirty()
	document.threadsNode.markDirty()
	document.root.markDirty()
	document.threads[threadIndex] = thread
	return thread.ID, nil
}

func (document *Document) changeThreadStatus(threadID string, status ThreadStatus) error {
	threadIndex, threadNode, ok := document.findThreadNode(threadID)
	if !ok {
		return fmt.Errorf("%w: thread %q does not exist", ErrInvalidOperation, threadID)
	}
	current := document.threads[threadIndex].Status
	allowed := (status == StatusResolved && (current == StatusOpen || current == StatusHandled)) || (status == StatusOpen && current == StatusResolved)
	if !allowed {
		return fmt.Errorf("%w: cannot change thread status from %q to %q", ErrInvalidOperation, current, status)
	}
	if err := replaceObjectString(threadNode, "status", string(status)); err != nil {
		return err
	}
	threadNode.markDirty()
	document.threadsNode.markDirty()
	document.root.markDirty()
	document.threads[threadIndex].Status = status
	return nil
}

func (document *Document) deleteUnrepliedThread(threadID string) error {
	threadIndex, _, ok := document.findThreadNode(threadID)
	if !ok {
		return fmt.Errorf("%w: thread %q does not exist", ErrInvalidOperation, threadID)
	}
	if len(document.threads[threadIndex].Messages) != 1 {
		return fmt.Errorf("%w: a thread with replies cannot be deleted", ErrInvalidOperation)
	}
	document.threadsNode.items = append(document.threadsNode.items[:threadIndex], document.threadsNode.items[threadIndex+1:]...)
	document.threads = append(document.threads[:threadIndex], document.threads[threadIndex+1:]...)
	document.threadsNode.markDirty()
	document.root.markDirty()
	return nil
}

func replaceObjectString(object *value, name, replacement string) error {
	raw, err := json.Marshal(replacement)
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	replacementNode, err := parseJSON(raw)
	if err != nil {
		return fmt.Errorf("parse encoded %s: %w", name, err)
	}
	for index := range object.members {
		if object.members[index].key == name {
			object.members[index].value = replacementNode
			return nil
		}
	}
	keyRaw, err := json.Marshal(name)
	if err != nil {
		return fmt.Errorf("encode member %s: %w", name, err)
	}
	object.members = append(object.members, member{key: name, keyRaw: keyRaw, value: replacementNode})
	return nil
}
