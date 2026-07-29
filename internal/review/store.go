package review

import (
	"bytes"
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
	"mdreview.dev/mdreview/internal/gatee"
	"mdreview.dev/mdreview/internal/limits"
)

const (
	threadIDPrefix  = "thread_"
	messageIDPrefix = "message_"
)

// ResultDurability reports whether an applied sidecar rename is known to be
// crash-durable.
type ResultDurability string

const (
	// DurabilityDurable means both the temporary file and containing directory synced.
	DurabilityDurable ResultDurability = "durable"

	// DurabilityUncertain means the rename applied but containing-directory sync failed.
	DurabilityUncertain ResultDurability = "uncertain"
)

// Snapshot is one review sidecar with attachment state calculated against the
// current Markdown bytes.
type Snapshot struct {
	Path             string             `json:"path"`
	DocumentRevision string             `json:"documentRevision"`
	ReviewRevision   *string            `json:"reviewRevision"`
	Threads          []ResolvedThread   `json:"threads"`
	Targets          TargetFingerprints `json:"targets"`
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

// CurrentTargetState identifies the exact files and target observed after a
// target conflict. TargetFingerprint is nil when the target no longer exists.
type CurrentTargetState struct {
	DocumentRevision  string  `json:"documentRevision"`
	ReviewRevision    *string `json:"reviewRevision"`
	TargetFingerprint *string `json:"targetFingerprint"`
}

// TargetChangedError reports that a target changed or disappeared without
// applying a mutation.
type TargetChangedError struct {
	Current CurrentTargetState
}

// Error implements error.
func (conflict *TargetChangedError) Error() string {
	return ErrTargetChanged.Error()
}

// Unwrap permits errors.Is checks against ErrTargetChanged.
func (conflict *TargetChangedError) Unwrap() error {
	return ErrTargetChanged
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
	DocumentRevision string             `json:"documentRevision"`
	ReviewRevision   string             `json:"reviewRevision"`
	Durability       ResultDurability   `json:"durability"`
	Thread           ResolvedThread     `json:"thread"`
	Targets          TargetFingerprints `json:"targets"`
}

// ReplyInput identifies one thread and the human reply to append.
type ReplyInput struct {
	DocumentPath             string
	ExpectedDocumentRevision string
	ExpectedReviewRevision   string
	ThreadID                 string
	TargetFingerprint        string
	MessageBody              string
}

// EditMessageInput identifies one existing human message and its replacement body.
type EditMessageInput struct {
	DocumentPath             string
	ExpectedDocumentRevision string
	ExpectedReviewRevision   string
	MessageID                string
	TargetFingerprint        string
	MessageBody              string
}

// ChangeStatusInput identifies one thread and an allowed human status transition.
type ChangeStatusInput struct {
	DocumentPath             string
	ExpectedDocumentRevision string
	ExpectedReviewRevision   string
	ThreadID                 string
	TargetFingerprint        string
	Status                   ThreadStatus
}

// DeleteThreadInput identifies one unreplied thread to remove.
type DeleteThreadInput struct {
	DocumentPath             string
	ExpectedDocumentRevision string
	ExpectedReviewRevision   string
	ThreadID                 string
	TargetFingerprint        string
}

// MutationResult describes an applied reply, message edit, or status change.
type MutationResult struct {
	DocumentRevision string             `json:"documentRevision"`
	ReviewRevision   string             `json:"reviewRevision"`
	Durability       ResultDurability   `json:"durability"`
	Thread           ResolvedThread     `json:"thread"`
	Targets          TargetFingerprints `json:"targets"`
}

// DeleteThreadResult describes an applied unreplied-thread deletion.
type DeleteThreadResult struct {
	DocumentRevision string           `json:"documentRevision"`
	ReviewRevision   string           `json:"reviewRevision"`
	Durability       ResultDurability `json:"durability"`
	DeletedThreadID  string           `json:"deletedThreadId"`
}

// StoreOptions provides deterministic clocks and IDs to tests. Production
// callers normally use the zero value.
type StoreOptions struct {
	// Now supplies the creation time for browser-authored messages.
	Now func() time.Time

	// NewID may be called concurrently for different sidecars and must be safe
	// for concurrent use. The production generator uses crypto/rand.
	NewID func(prefix string) (string, error)

	// MutationAttempts bounds retries after an observed direct sidecar change.
	// Zero uses the filesystem gateway default.
	MutationAttempts int

	// Measurements records Gate E content-read counters when the compiled
	// baseline opts in. Ordinary production callers leave it nil.
	Measurements *gatee.Counters
}

type keyedLock struct {
	mutex      sync.Mutex
	references int
}

type gateway interface {
	ReadFile(relativePath string, maxBytes int64) ([]byte, error)
	MutateFile(
		ctx context.Context,
		relativePath string,
		options filesystem.MutationOptions,
		callback filesystem.MutationCallback,
	) ([]byte, filesystem.Durability, error)
}

// Store performs semantic sidecar reads and mutations through a contained
// filesystem gateway.
type Store struct {
	filesystem       gateway
	now              func() time.Time
	newID            func(prefix string) (string, error)
	mutationAttempts int
	measurements     *gatee.Counters

	// locksMu protects locks and reference counts. A keyed mutex is held only
	// for one derived sidecar, so unrelated documents can mutate concurrently.
	locksMu sync.Mutex
	locks   map[string]*keyedLock
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
	if options.MutationAttempts < 0 ||
		options.MutationAttempts > filesystem.MaxMutationAttempts {
		return nil, fmt.Errorf(
			"%w: mutation attempts must be between 0 and %d",
			ErrInvalidOperation,
			filesystem.MaxMutationAttempts,
		)
	}
	return &Store{
		filesystem:       files,
		now:              now,
		newID:            newID,
		mutationAttempts: options.MutationAttempts,
		measurements:     options.Measurements,
		locks:            make(map[string]*keyedLock),
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
			Targets:          newTargetFingerprints(),
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
		Targets:          document.targetFingerprints(),
	}, nil
}

// CreateThread creates one open thread with one Reviewer-authored message for
// a document identity already verified against the workspace index. Stale
// review revisions do not reject this target-free operation; each callback
// decodes and merges against the latest adjacent sidecar bytes observed before
// replacement. Uncoordinated direct writers retain the filesystem gateway's
// documented final revision-check-to-rename race.
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

	unlock := store.lock(sidecarPath)
	defer unlock()

	var (
		currentDocumentRevision string
		currentMarkdown         []byte
		createdThread           Thread
	)
	updated, filesystemDurability, err := store.filesystem.MutateFile(
		ctx,
		sidecarPath,
		filesystem.MutationOptions{
			MaxBytes:    limits.MaxReviewSidecarBytes,
			MaxAttempts: store.mutationAttempts,
		},
		func(current []byte, exists bool) ([]byte, error) {
			store.measurements.RecordSidecarContentRead(len(current))
			markdown, readErr := store.readMarkdown(input.DocumentPath)
			if readErr != nil {
				return nil, fmt.Errorf("%w: read document: %v", ErrUnavailable, readErr)
			}
			if !utf8.Valid(markdown) {
				return nil, fmt.Errorf("%w: document must be UTF-8", ErrUnavailable)
			}
			currentMarkdown = markdown
			currentDocumentRevision = Revision(markdown)

			document := NewDocument()
			var reviewRevision *string
			if exists {
				decoded, decodeErr := Decode(current)
				if decodeErr != nil {
					return nil, decodeErr
				}
				document = decoded
				revision := Revision(current)
				reviewRevision = &revision
			}

			anchor, anchorErr := anchorForCurrentDocument(
				markdown,
				currentDocumentRevision,
				input.ExpectedDocumentRevision,
				input.Anchor,
			)
			if anchorErr != nil {
				if errors.Is(anchorErr, ErrDocumentChanged) {
					return nil, &ConflictError{
						Kind: ErrDocumentChanged,
						Current: CurrentRevisions{
							DocumentRevision: currentDocumentRevision,
							ReviewRevision:   reviewRevision,
						},
					}
				}
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

	durability, err := resultDurability(filesystemDurability)
	if err != nil {
		return CreateThreadResult{}, err
	}
	emitted, err := Decode(updated)
	if err != nil {
		return CreateThreadResult{}, fmt.Errorf("%w: decode emitted sidecar: %v", ErrUnavailable, err)
	}
	targets, ok := emitted.targetsForThread(createdThread.ID)
	if !ok {
		return CreateThreadResult{}, fmt.Errorf("%w: emitted thread is missing", ErrUnavailable)
	}
	return CreateThreadResult{
		DocumentRevision: currentDocumentRevision,
		ReviewRevision:   Revision(updated),
		Durability:       durability,
		Thread: ResolvedThread{
			ID:         createdThread.ID,
			Anchor:     cloneAnchor(createdThread.Anchor),
			Attachment: ResolveAnchor(currentMarkdown, createdThread.Anchor),
			Status:     createdThread.Status,
			Messages:   cloneMessages(createdThread.Messages),
		},
		Targets: targets,
	}, nil
}

func (store *Store) readSidecar(sidecarPath string) ([]byte, bool, error) {
	data, err := store.filesystem.ReadFile(sidecarPath, limits.MaxReviewSidecarBytes)
	store.measurements.RecordSidecarContentRead(len(data))
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
	store.measurements.RecordMarkdownContentRead(len(data))
	return data, err
}

func (store *Store) lock(sidecarPath string) func() {
	store.locksMu.Lock()
	lock := store.locks[sidecarPath]
	if lock == nil {
		lock = &keyedLock{}
		store.locks[sidecarPath] = lock
	}
	lock.references++
	store.locksMu.Unlock()

	lock.mutex.Lock()
	return func() {
		lock.mutex.Unlock()
		store.locksMu.Lock()
		lock.references--
		if lock.references == 0 {
			delete(store.locks, sidecarPath)
		}
		store.locksMu.Unlock()
	}
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

func anchorForCurrentDocument(
	markdown []byte,
	currentRevision string,
	expectedRevision string,
	submitted Anchor,
) (Anchor, error) {
	if submitted.Type == AnchorDocument {
		return Anchor{Type: AnchorDocument}, nil
	}
	if currentRevision == expectedRevision {
		rangeValue := submitted.Range
		if rangeValue.End > uint64(len(markdown)) ||
			!stringMatchesRange(markdown, *rangeValue, submitted.Source) {
			return Anchor{}, fmt.Errorf("%w: anchor source does not match its range", ErrInvalidOperation)
		}
		return cloneAnchor(submitted), nil
	}

	source := []byte(submitted.Source)
	first := bytes.Index(markdown, source)
	if first < 0 {
		return Anchor{}, ErrDocumentChanged
	}
	if bytes.Index(markdown[first+1:], source) >= 0 {
		return Anchor{}, ErrDocumentChanged
	}
	rebased := cloneAnchor(submitted)
	rebased.Range = &ByteRange{Start: uint64(first), End: uint64(first + len(submitted.Source))}
	return rebased, nil
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

func resultDurability(input filesystem.Durability) (ResultDurability, error) {
	switch input {
	case filesystem.DurabilityDurable:
		return DurabilityDurable, nil
	case filesystem.DurabilityUncertain:
		// The rename already applied. Returning this distinct success prevents a
		// caller from blindly retrying and creating the thread twice.
		return DurabilityUncertain, nil
	default:
		return "", fmt.Errorf("%w: mutation returned unknown durability", ErrUnavailable)
	}
}
