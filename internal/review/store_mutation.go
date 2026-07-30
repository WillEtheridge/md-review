package review

import (
	"context"
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
	// These fields are the browser's whole-document and whole-sidecar
	// preconditions. They are checked again after opening the current sidecar.
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
		// Read the Markdown inside the sidecar mutation callback. This makes the
		// document and sidecar revisions describe one attempted operation rather
		// than two unrelated reads performed by the HTTP layer.
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

func (document *Document) findThread(threadID string) (int, bool) {
	for index := range document.threads {
		if document.threads[index].ID == threadID {
			return index, true
		}
	}
	return 0, false
}

func (document *Document) appendReply(threadID string, message Message) error {
	threadIndex, ok := document.findThread(threadID)
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
	document.threads[threadIndex] = thread
	return nil
}

func (document *Document) changeThreadStatus(threadID string, status ThreadStatus) error {
	threadIndex, ok := document.findThread(threadID)
	if !ok {
		return fmt.Errorf("%w: thread %q does not exist", ErrInvalidOperation, threadID)
	}
	current := document.threads[threadIndex].Status
	allowed := (status == StatusResolved && (current == StatusOpen || current == StatusHandled)) || (status == StatusOpen && current == StatusResolved)
	if !allowed {
		return fmt.Errorf("%w: cannot change thread status from %q to %q", ErrInvalidOperation, current, status)
	}
	document.threads[threadIndex].Status = status
	return nil
}

func (document *Document) deleteUnrepliedThread(threadID string) error {
	threadIndex, ok := document.findThread(threadID)
	if !ok {
		return fmt.Errorf("%w: thread %q does not exist", ErrInvalidOperation, threadID)
	}
	if len(document.threads[threadIndex].Messages) != 1 {
		return fmt.Errorf("%w: a thread with replies cannot be deleted", ErrInvalidOperation)
	}
	document.threads = append(document.threads[:threadIndex], document.threads[threadIndex+1:]...)
	return nil
}
