package review

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"mdreview.dev/mdreview/internal/filesystem"
	"mdreview.dev/mdreview/internal/limits"
)

type mutationTargetKind uint8

const (
	threadMutationTarget mutationTargetKind = iota
	messageMutationTarget
)

type targetMutation struct {
	documentPath      string
	targetID          string
	targetFingerprint string
	targetKind        mutationTargetKind
	apply             func(*Document) (string, error)
}

type targetMutationOutcome struct {
	documentRevision string
	reviewRevision   string
	thread           ResolvedThread
	targets          TargetFingerprints
}

// Reply appends one server-authored human Reviewer message. A reply reopens a
// handled or resolved thread and preserves a currently open status.
func (store *Store) Reply(ctx context.Context, input ReplyInput) (MutationResult, error) {
	if err := validateTargetOperation(
		input.DocumentPath,
		input.ExpectedDocumentRevision,
		input.ExpectedReviewRevision,
		input.ThreadID,
		input.TargetFingerprint,
	); err != nil {
		return MutationResult{}, err
	}
	if err := validateMessageBody(input.MessageBody); err != nil {
		return MutationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}

	messageID, err := store.newID(messageIDPrefix)
	if err != nil {
		return MutationResult{}, fmt.Errorf("%w: create message ID: %v", ErrUnavailable, err)
	}
	if !validGeneratedID(messageID, messageIDPrefix) {
		return MutationResult{}, fmt.Errorf("%w: ID generator returned an invalid ID", ErrUnavailable)
	}
	message := Message{
		ID:        messageID,
		Author:    Author{Type: "human", Name: "Reviewer"},
		Body:      input.MessageBody,
		CreatedAt: store.now().UTC().Format(time.RFC3339Nano),
	}
	outcome, err := store.mutateTarget(ctx, targetMutation{
		documentPath:      input.DocumentPath,
		targetID:          input.ThreadID,
		targetFingerprint: input.TargetFingerprint,
		targetKind:        threadMutationTarget,
		apply: func(document *Document) (string, error) {
			if err := document.appendReply(input.ThreadID, message); err != nil {
				return "", err
			}
			return input.ThreadID, nil
		},
	})
	if err != nil {
		return MutationResult{}, err
	}
	return outcome.mutationResult(), nil
}

// EditMessage replaces one current human message body while retaining its ID,
// author, and creation time and recording a server-owned edit timestamp.
func (store *Store) EditMessage(
	ctx context.Context,
	input EditMessageInput,
) (MutationResult, error) {
	if err := validateTargetOperation(
		input.DocumentPath,
		input.ExpectedDocumentRevision,
		input.ExpectedReviewRevision,
		input.MessageID,
		input.TargetFingerprint,
	); err != nil {
		return MutationResult{}, err
	}
	if err := validateMessageBody(input.MessageBody); err != nil {
		return MutationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}
	editedAt := store.now().UTC().Format(time.RFC3339Nano)
	outcome, err := store.mutateTarget(ctx, targetMutation{
		documentPath:      input.DocumentPath,
		targetID:          input.MessageID,
		targetFingerprint: input.TargetFingerprint,
		targetKind:        messageMutationTarget,
		apply: func(document *Document) (string, error) {
			return document.editHumanMessage(input.MessageID, input.MessageBody, editedAt)
		},
	})
	if err != nil {
		return MutationResult{}, err
	}
	return outcome.mutationResult(), nil
}

// ChangeStatus applies one browser-allowed resolve or reopen transition.
// Browser operations never set handled.
func (store *Store) ChangeStatus(
	ctx context.Context,
	input ChangeStatusInput,
) (MutationResult, error) {
	if err := validateTargetOperation(
		input.DocumentPath,
		input.ExpectedDocumentRevision,
		input.ExpectedReviewRevision,
		input.ThreadID,
		input.TargetFingerprint,
	); err != nil {
		return MutationResult{}, err
	}
	if input.Status != StatusOpen && input.Status != StatusResolved {
		return MutationResult{}, fmt.Errorf(
			"%w: browser status must be open or resolved",
			ErrInvalidOperation,
		)
	}
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}
	outcome, err := store.mutateTarget(ctx, targetMutation{
		documentPath:      input.DocumentPath,
		targetID:          input.ThreadID,
		targetFingerprint: input.TargetFingerprint,
		targetKind:        threadMutationTarget,
		apply: func(document *Document) (string, error) {
			if err := document.changeThreadStatus(input.ThreadID, input.Status); err != nil {
				return "", err
			}
			return input.ThreadID, nil
		},
	})
	if err != nil {
		return MutationResult{}, err
	}
	return outcome.mutationResult(), nil
}

// DeleteThread removes one target thread only while it has exactly one message.
func (store *Store) DeleteThread(
	ctx context.Context,
	input DeleteThreadInput,
) (DeleteThreadResult, error) {
	if err := validateTargetOperation(
		input.DocumentPath,
		input.ExpectedDocumentRevision,
		input.ExpectedReviewRevision,
		input.ThreadID,
		input.TargetFingerprint,
	); err != nil {
		return DeleteThreadResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return DeleteThreadResult{}, err
	}
	outcome, err := store.mutateTarget(ctx, targetMutation{
		documentPath:      input.DocumentPath,
		targetID:          input.ThreadID,
		targetFingerprint: input.TargetFingerprint,
		targetKind:        threadMutationTarget,
		apply: func(document *Document) (string, error) {
			return "", document.deleteUnrepliedThread(input.ThreadID)
		},
	})
	if err != nil {
		return DeleteThreadResult{}, err
	}
	return DeleteThreadResult{
		DocumentRevision: outcome.documentRevision,
		ReviewRevision:   outcome.reviewRevision,
		DeletedThreadID:  input.ThreadID,
	}, nil
}

func (store *Store) mutateTarget(
	ctx context.Context,
	mutation targetMutation,
) (targetMutationOutcome, error) {
	// The filesystem gateway may rerun this semantic callback after observing
	// an external replacement, so each attempt decodes and checks the latest
	// target. Its final check-to-rename window remains an explicit race with
	// uncoordinated direct writers; the per-sidecar lock covers only this Store.
	sidecarPath := deriveSidecarPath(mutation.documentPath)
	unlock := store.lock(sidecarPath)
	defer unlock()

	var (
		currentDocumentRevision string
		currentMarkdown         []byte
		affectedThreadID        string
	)
	updated, err := store.filesystem.MutateFile(
		ctx,
		sidecarPath,
		filesystem.MutationOptions{
			MaxBytes:    limits.MaxReviewSidecarBytes,
			MaxAttempts: store.mutationAttempts,
		},
		func(current []byte, exists bool) ([]byte, error) {
			markdown, readErr := store.filesystem.ReadFile(
				mutation.documentPath,
				limits.MaxMarkdownDocumentBytes,
			)
			if readErr != nil {
				return nil, fmt.Errorf("%w: read document: %v", ErrUnavailable, readErr)
			}
			if !utf8.Valid(markdown) {
				return nil, fmt.Errorf("%w: document must be UTF-8", ErrUnavailable)
			}
			currentMarkdown = markdown
			currentDocumentRevision = Revision(markdown)

			var reviewRevision *string
			if !exists {
				return nil, targetChanged(
					currentDocumentRevision,
					reviewRevision,
					nil,
				)
			}
			document, decodeErr := Decode(current)
			if decodeErr != nil {
				return nil, decodeErr
			}
			revision := Revision(current)
			reviewRevision = &revision

			fingerprint, _, targetExists := document.mutationTargetFingerprint(
				mutation.targetKind,
				mutation.targetID,
			)
			if !targetExists || fingerprint != mutation.targetFingerprint {
				var currentFingerprint *string
				if targetExists {
					currentFingerprint = &fingerprint
				}
				return nil, targetChanged(
					currentDocumentRevision,
					reviewRevision,
					currentFingerprint,
				)
			}

			affectedThreadID, decodeErr = mutation.apply(document)
			if decodeErr != nil {
				return nil, decodeErr
			}
			return document.Bytes()
		},
	)
	if err != nil {
		return targetMutationOutcome{}, store.translateMutationError(
			err,
			mutation.documentPath,
		)
	}

	outcome := targetMutationOutcome{
		documentRevision: currentDocumentRevision,
		reviewRevision:   Revision(updated),
	}
	if affectedThreadID == "" {
		return outcome, nil
	}
	emitted, err := Decode(updated)
	if err != nil {
		return targetMutationOutcome{}, fmt.Errorf(
			"%w: decode emitted sidecar: %v",
			ErrUnavailable,
			err,
		)
	}
	thread, ok := emitted.resolvedThread(currentMarkdown, affectedThreadID)
	if !ok {
		return targetMutationOutcome{}, fmt.Errorf(
			"%w: emitted thread is missing",
			ErrUnavailable,
		)
	}
	targets, ok := emitted.targetsForThread(affectedThreadID)
	if !ok {
		return targetMutationOutcome{}, fmt.Errorf(
			"%w: emitted target fingerprints are missing",
			ErrUnavailable,
		)
	}
	outcome.thread = thread
	outcome.targets = targets
	return outcome, nil
}

func (document *Document) mutationTargetFingerprint(
	kind mutationTargetKind,
	targetID string,
) (fingerprint string, threadID string, exists bool) {
	switch kind {
	case threadMutationTarget:
		fingerprint, exists = document.threadFingerprint(targetID)
		return fingerprint, targetID, exists
	case messageMutationTarget:
		return document.messageFingerprint(targetID)
	default:
		return "", "", false
	}
}

func (document *Document) resolvedThread(
	markdown []byte,
	threadID string,
) (ResolvedThread, bool) {
	for _, thread := range document.ResolvedThreads(markdown) {
		if thread.ID == threadID {
			return thread, true
		}
	}
	return ResolvedThread{}, false
}

func (outcome targetMutationOutcome) mutationResult() MutationResult {
	return MutationResult{
		DocumentRevision: outcome.documentRevision,
		ReviewRevision:   outcome.reviewRevision,
		Thread:           outcome.thread,
		Targets:          outcome.targets,
	}
}

func targetChanged(
	documentRevision string,
	reviewRevision *string,
	targetFingerprint *string,
) *TargetChangedError {
	return &TargetChangedError{
		Current: CurrentTargetState{
			DocumentRevision:  documentRevision,
			ReviewRevision:    reviewRevision,
			TargetFingerprint: targetFingerprint,
		},
	}
}

func validateTargetOperation(
	documentPath string,
	expectedDocumentRevision string,
	expectedReviewRevision string,
	targetID string,
	targetFingerprint string,
) error {
	if err := validateDocumentPath(documentPath); err != nil {
		return err
	}
	// Whole-file revisions are required protocol state but are deliberately not
	// equality preconditions here. Workflow mutations are independent of
	// Markdown attachment, and an exact target fingerprint permits unrelated
	// current sidecar changes to merge.
	if !validRevision(expectedDocumentRevision) {
		return fmt.Errorf("%w: expected document revision is invalid", ErrInvalidOperation)
	}
	if !validRevision(expectedReviewRevision) {
		return fmt.Errorf("%w: expected review revision is invalid", ErrInvalidOperation)
	}
	if targetID == "" || !utf8.ValidString(targetID) {
		return fmt.Errorf("%w: target ID must be non-empty UTF-8", ErrInvalidOperation)
	}
	if !validRevision(targetFingerprint) {
		return fmt.Errorf("%w: target fingerprint is invalid", ErrInvalidOperation)
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
