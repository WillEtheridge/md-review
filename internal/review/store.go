package review

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mdreview.dev/mdreview/internal/filesystem"
	"mdreview.dev/mdreview/internal/limits"
)

const (
	threadIDPrefix  = "thread_"
	messageIDPrefix = "message_"
)

// Snapshot is one review sidecar with attachment state calculated against the
// current Markdown bytes.
type Snapshot struct {
	Path             string           `json:"path"`
	DocumentRevision string           `json:"documentRevision"`
	ReviewRevision   *string          `json:"reviewRevision"`
	Threads          []ResolvedThread `json:"threads"`
}

// CurrentRevisions identifies the exact files observed while handling a conflict.
type CurrentRevisions struct {
	DocumentRevision string  `json:"documentRevision"`
	ReviewRevision   *string `json:"reviewRevision"`
}

// ConflictError reports a semantic conflict without applying a mutation.
type ConflictError struct {
	Kind    error
	Current CurrentRevisions
}

// Error implements error.
func (conflict *ConflictError) Error() string {
	return conflict.Kind.Error()
}

// Unwrap permits errors.Is checks against ErrDocumentChanged or ErrReviewChanged.
func (conflict *ConflictError) Unwrap() error {
	return conflict.Kind
}

// CreateThreadInput is the complete semantic operation accepted from the HTTP layer.
type CreateThreadInput struct {
	DocumentPath             string
	ExpectedDocumentRevision string
	ExpectedReviewRevision   *string
	Anchor                   Anchor
	MessageBody              string
}

// CreateThreadResult describes an applied text- or document-thread creation.
type CreateThreadResult struct {
	DocumentRevision string         `json:"documentRevision"`
	ReviewRevision   string         `json:"reviewRevision"`
	Thread           ResolvedThread `json:"thread"`
}

// ReplyInput identifies one thread and the human reply to append.
type ReplyInput struct {
	DocumentPath             string
	ExpectedDocumentRevision string
	ExpectedReviewRevision   string
	ThreadID                 string
	MessageBody              string
}

// ChangeStatusInput identifies one thread and an allowed human status transition.
type ChangeStatusInput struct {
	DocumentPath             string
	ExpectedDocumentRevision string
	ExpectedReviewRevision   string
	ThreadID                 string
	Status                   ThreadStatus
}

// DeleteThreadInput identifies one unreplied thread to remove.
type DeleteThreadInput struct {
	DocumentPath             string
	ExpectedDocumentRevision string
	ExpectedReviewRevision   string
	ThreadID                 string
}

// MutationResult describes an applied reply or status change.
type MutationResult struct {
	DocumentRevision string         `json:"documentRevision"`
	ReviewRevision   string         `json:"reviewRevision"`
	Thread           ResolvedThread `json:"thread"`
}

// DeleteThreadResult describes an applied unreplied-thread deletion.
type DeleteThreadResult struct {
	DocumentRevision string `json:"documentRevision"`
	ReviewRevision   string `json:"reviewRevision"`
	DeletedThreadID  string `json:"deletedThreadId"`
}

// StoreOptions provides deterministic clocks and IDs to tests. Production
// callers normally use the zero value.
type StoreOptions struct {
	// Now supplies the creation time for browser-authored messages.
	Now func() time.Time

	// NewID may be called concurrently for different sidecars and must be safe
	// for concurrent use. The production generator uses crypto/rand.
	NewID func(prefix string) (string, error)
}

type gateway interface {
	ReadFile(relativePath string, maxBytes int64) ([]byte, error)
	MutateFile(
		ctx context.Context,
		relativePath string,
		options filesystem.MutationOptions,
		callback filesystem.MutationCallback,
	) ([]byte, error)
}

// Store performs semantic sidecar reads and mutations through a contained
// filesystem gateway.
type Store struct {
	filesystem gateway
	now        func() time.Time
	newID      func(prefix string) (string, error)
	// mutex serializes all in-process mutations. External writers remain
	// outside this protocol and are detected only by the one final file check.
	mutex sync.Mutex
}

// NewStore binds a review store to one contained workspace filesystem.
func NewStore(gateway *filesystem.FS, options StoreOptions) (*Store, error) {
	if gateway == nil {
		return nil, errors.New("review store requires a filesystem")
	}
	return newStore(gateway, options)
}

func newStore(files gateway, options StoreOptions) (*Store, error) {
	if files == nil {
		return nil, errors.New("review store requires a filesystem")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newID := options.NewID
	if newID == nil {
		newID = randomID
	}
	return &Store{
		filesystem: files,
		now:        now,
		newID:      newID,
	}, nil
}

// Read returns the adjacent sidecar for a document identity already verified
// against the workspace index. A missing sidecar is an empty mutable review
// with a nil revision. Invalid, unsupported, oversized, and unsafe sidecars
// remain distinguishable read-only failures.
func (store *Store) Read(ctx context.Context, documentPath string) (Snapshot, error) {
	if err := validateDocumentPath(documentPath); err != nil {
		return Snapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	markdown, err := store.readMarkdown(documentPath)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: read document: %v", ErrUnavailable, err)
	}
	if !utf8.Valid(markdown) {
		return Snapshot{}, fmt.Errorf("%w: document must be UTF-8", ErrUnavailable)
	}
	sidecarPath := deriveSidecarPath(documentPath)
	data, exists, err := store.readSidecar(sidecarPath)
	if err != nil {
		return Snapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if !exists {
		return Snapshot{
			Path:             documentPath,
			DocumentRevision: Revision(markdown),
			Threads:          []ResolvedThread{},
		}, nil
	}
	document, err := Decode(data)
	if err != nil {
		return Snapshot{}, err
	}
	revision := Revision(data)
	return Snapshot{
		Path:             documentPath,
		DocumentRevision: Revision(markdown),
		ReviewRevision:   &revision,
		Threads:          document.ResolvedThreads(markdown),
	}, nil
}

// CreateThread creates one open thread with one Reviewer-authored message.
// Both files must still have exactly the revisions supplied by the browser.
func (store *Store) CreateThread(
	ctx context.Context,
	input CreateThreadInput,
) (CreateThreadResult, error) {
	if err := validateCreateInput(input); err != nil {
		return CreateThreadResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return CreateThreadResult{}, err
	}
	threadID, err := store.newID(threadIDPrefix)
	if err != nil {
		return CreateThreadResult{}, fmt.Errorf("%w: create thread ID: %v", ErrUnavailable, err)
	}
	messageID, err := store.newID(messageIDPrefix)
	if err != nil {
		return CreateThreadResult{}, fmt.Errorf("%w: create message ID: %v", ErrUnavailable, err)
	}
	if !validGeneratedID(threadID, threadIDPrefix) ||
		!validGeneratedID(messageID, messageIDPrefix) {
		return CreateThreadResult{}, fmt.Errorf("%w: ID generator returned an invalid ID", ErrUnavailable)
	}
	createdAt := store.now().UTC().Format(time.RFC3339Nano)
	sidecarPath := deriveSidecarPath(input.DocumentPath)
	store.mutex.Lock()
	defer store.mutex.Unlock()

	var (
		currentDocumentRevision string
		currentMarkdown         []byte
		createdThread           Thread
	)
	updated, err := store.filesystem.MutateFile(
		ctx,
		sidecarPath,
		filesystem.MutationOptions{
			MaxBytes: limits.MaxReviewSidecarBytes,
		},
		func(current []byte, exists bool) ([]byte, error) {
			markdown, readErr := store.readMarkdown(input.DocumentPath)
			if readErr != nil {
				return nil, fmt.Errorf("%w: read document: %v", ErrUnavailable, readErr)
			}
			if !utf8.Valid(markdown) {
				return nil, fmt.Errorf("%w: document must be UTF-8", ErrUnavailable)
			}
			currentMarkdown = markdown
			currentDocumentRevision = Revision(markdown)

			var reviewRevision *string
			if exists {
				revision := Revision(current)
				reviewRevision = &revision
			}
			if currentDocumentRevision != input.ExpectedDocumentRevision {
				return nil, revisionConflict(ErrDocumentChanged, currentDocumentRevision, reviewRevision)
			}
			if !sameNullableRevision(input.ExpectedReviewRevision, reviewRevision) {
				return nil, revisionConflict(ErrReviewChanged, currentDocumentRevision, reviewRevision)
			}

			document := NewDocument()
			if exists {
				var decodeErr error
				document, decodeErr = Decode(current)
				if decodeErr != nil {
					return nil, decodeErr
				}
			}
			anchor, anchorErr := exactAnchor(markdown, input.Anchor)
			if anchorErr != nil {
				return nil, anchorErr
			}

			createdThread = Thread{
				ID:     threadID,
				Anchor: anchor,
				Status: StatusOpen,
				Messages: []Message{{
					ID:        messageID,
					Author:    Author{Type: "human", Name: "Reviewer"},
					Body:      input.MessageBody,
					CreatedAt: createdAt,
				}},
			}
			if appendErr := document.AppendThread(createdThread); appendErr != nil {
				return nil, appendErr
			}
			return document.Bytes()
		},
	)
	if err != nil {
		return CreateThreadResult{}, store.translateMutationError(
			err,
			input.DocumentPath,
		)
	}

	if _, err := Decode(updated); err != nil {
		return CreateThreadResult{}, fmt.Errorf("%w: decode emitted sidecar: %v", ErrUnavailable, err)
	}
	return CreateThreadResult{
		DocumentRevision: currentDocumentRevision,
		ReviewRevision:   Revision(updated),
		Thread: ResolvedThread{
			ID:         createdThread.ID,
			Anchor:     cloneAnchor(createdThread.Anchor),
			Attachment: ResolveAnchor(currentMarkdown, createdThread.Anchor),
			Status:     createdThread.Status,
			Messages:   cloneMessages(createdThread.Messages),
		},
	}, nil
}

func (store *Store) readSidecar(sidecarPath string) ([]byte, bool, error) {
	data, err := store.filesystem.ReadFile(sidecarPath, limits.MaxReviewSidecarBytes)
	if err == nil {
		return data, true, nil
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, false, nil
	case errors.Is(err, filesystem.ErrTooLarge):
		return nil, false, fmt.Errorf("%w: %v", ErrTooLarge, err)
	case errors.Is(err, filesystem.ErrSymlink),
		errors.Is(err, filesystem.ErrNotRegular),
		errors.Is(err, filesystem.ErrNotDirectory):
		return nil, false, fmt.Errorf("%w: %v", ErrUnsafe, err)
	default:
		return nil, false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
}

func (store *Store) translateMutationError(err error, documentPath string) error {
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		return conflict
	}
	switch {
	case errors.Is(err, filesystem.ErrMutationConflict):
		markdown, readErr := store.readMarkdown(documentPath)
		if readErr != nil {
			return fmt.Errorf("%w: mutation conflict and document reread failed", ErrReviewChanged)
		}
		current, exists, sidecarErr := store.readSidecar(deriveSidecarPath(documentPath))
		var reviewRevision *string
		if sidecarErr == nil && exists {
			revision := Revision(current)
			reviewRevision = &revision
		}
		return &ConflictError{
			Kind: ErrReviewChanged,
			Current: CurrentRevisions{
				DocumentRevision: Revision(markdown),
				ReviewRevision:   reviewRevision,
			},
		}
	case errors.Is(err, filesystem.ErrUnsafeMutationTarget):
		return fmt.Errorf("%w: %v", ErrUnsafe, err)
	case errors.Is(err, filesystem.ErrMutationTooLarge):
		return fmt.Errorf("%w: %v", ErrTooLarge, err)
	case errors.Is(err, filesystem.ErrInvalidRelativePath),
		errors.Is(err, filesystem.ErrInvalidMutationOptions):
		return fmt.Errorf("%w: %v", ErrInvalidOperation, err)
	case errors.Is(err, filesystem.ErrMutationIO):
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	default:
		return err
	}
}

func (store *Store) readMarkdown(documentPath string) ([]byte, error) {
	data, err := store.filesystem.ReadFile(documentPath, limits.MaxMarkdownDocumentBytes)
	return data, err
}

func validateCreateInput(input CreateThreadInput) error {
	if err := validateDocumentPath(input.DocumentPath); err != nil {
		return err
	}
	if !validRevision(input.ExpectedDocumentRevision) {
		return fmt.Errorf("%w: expected document revision is invalid", ErrInvalidOperation)
	}
	if input.ExpectedReviewRevision != nil && !validRevision(*input.ExpectedReviewRevision) {
		return fmt.Errorf("%w: expected review revision is invalid", ErrInvalidOperation)
	}
	if !utf8.ValidString(input.MessageBody) || input.MessageBody == "" {
		return fmt.Errorf("%w: message body must be non-empty UTF-8", ErrInvalidOperation)
	}
	if int64(len(input.MessageBody)) > limits.MaxPersistedMessageBodyBytes {
		return fmt.Errorf("%w: message body exceeds content limit", ErrInvalidOperation)
	}
	switch input.Anchor.Type {
	case AnchorDocument:
		if input.Anchor.Range != nil || input.Anchor.Source != "" || input.Anchor.Text != "" {
			return fmt.Errorf("%w: document anchor has text fields", ErrInvalidOperation)
		}
	case AnchorText:
		if input.Anchor.Range == nil || input.Anchor.Source == "" || input.Anchor.Text == "" {
			return fmt.Errorf("%w: text anchor fields are required", ErrInvalidOperation)
		}
		if !utf8.ValidString(input.Anchor.Source) || !utf8.ValidString(input.Anchor.Text) {
			return fmt.Errorf("%w: text anchor fields must be UTF-8", ErrInvalidOperation)
		}
		if int64(len(input.Anchor.Source)) > limits.MaxTextAnchorSourceBytes {
			return fmt.Errorf("%w: text anchor source exceeds content limit", ErrInvalidOperation)
		}
		if input.Anchor.Range.Start > input.Anchor.Range.End {
			return fmt.Errorf("%w: text anchor range is reversed", ErrInvalidOperation)
		}
		if input.Anchor.Range.End-input.Anchor.Range.Start != uint64(len(input.Anchor.Source)) {
			return fmt.Errorf("%w: text anchor range does not match source bytes", ErrInvalidOperation)
		}
	default:
		return fmt.Errorf("%w: invalid anchor type", ErrInvalidOperation)
	}
	return nil
}

func exactAnchor(markdown []byte, submitted Anchor) (Anchor, error) {
	if submitted.Type == AnchorDocument {
		return Anchor{Type: AnchorDocument}, nil
	}
	rangeValue := submitted.Range
	if rangeValue.End > uint64(len(markdown)) ||
		!stringMatchesRange(markdown, *rangeValue, submitted.Source) {
		return Anchor{}, fmt.Errorf("%w: anchor source does not match its range", ErrInvalidOperation)
	}
	return cloneAnchor(submitted), nil
}

func sameNullableRevision(expected, actual *string) bool {
	if expected == nil || actual == nil {
		return expected == nil && actual == nil
	}
	return *expected == *actual
}

func revisionConflict(kind error, documentRevision string, reviewRevision *string) *ConflictError {
	return &ConflictError{Kind: kind, Current: CurrentRevisions{
		DocumentRevision: documentRevision,
		ReviewRevision:   reviewRevision,
	}}
}

func stringMatchesRange(markdown []byte, rangeValue ByteRange, source string) bool {
	return rangeValue.Start <= rangeValue.End &&
		rangeValue.End <= uint64(len(markdown)) &&
		string(markdown[int(rangeValue.Start):int(rangeValue.End)]) == source
}

func validateDocumentPath(documentPath string) error {
	if err := filesystem.ValidateRelativePath(documentPath); err != nil {
		return fmt.Errorf("%w: invalid document path", ErrInvalidOperation)
	}
	if !strings.HasSuffix(documentPath, ".md") {
		return fmt.Errorf("%w: document path must end in .md", ErrInvalidOperation)
	}
	return nil
}

func deriveSidecarPath(documentPath string) string {
	return documentPath + ".review.json"
}

func validRevision(revision string) bool {
	if len(revision) != sha256HexLength {
		return false
	}
	for _, character := range revision {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

const sha256HexLength = 64

func validGeneratedID(id, prefix string) bool {
	return strings.HasPrefix(id, prefix) && len(strings.TrimPrefix(id, prefix)) >= 22
}

func randomID(prefix string) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(random), nil
}
