// Package workspace discovers Markdown documents and exposes immutable,
// request-refreshed navigation snapshots through slash-relative identities.
//
// It owns ignore semantics and index membership. All operating-system access
// remains in the portable filesystem gateway.
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"sync"
	"time"
	"unicode/utf8"

	"mdreview.dev/mdreview/internal/filesystem"
	"mdreview.dev/mdreview/internal/limits"
)

// Options controls workspace construction.
type Options struct {
	// Now supplies scan freshness time and may be called concurrently.
	// Production callers leave it nil.
	Now func() time.Time
}

// EntryKind identifies a navigation node.
type EntryKind string

const (
	// EntryKindDirectory identifies a directory containing discovered documents.
	EntryKindDirectory EntryKind = "directory"

	// EntryKindDocument identifies an indexed Markdown document.
	EntryKindDocument EntryKind = "document"
)

// Availability describes whether an indexed document can be opened.
type Availability string

const (
	// AvailabilityReady indicates that scan metadata is within the read limit.
	AvailabilityReady Availability = "ready"

	// AvailabilityTooLarge indicates that scan metadata exceeds the Markdown limit.
	AvailabilityTooLarge Availability = "tooLarge"
)

const (
	// WarningCodeIgnoreFileTooLarge identifies an over-limit .gitignore.
	WarningCodeIgnoreFileTooLarge = "ignoreFileTooLarge"

	// WarningCodeIgnoreFileUnsafe identifies a non-regular or unreadable .gitignore.
	WarningCodeIgnoreFileUnsafe = "ignoreFileUnsafe"

	// WarningCodeDirectoryUnreadable identifies a directory that could not be scanned.
	WarningCodeDirectoryUnreadable = "directoryUnreadable"

	// WarningCodeEntryUnsafe identifies a symlink, special file, or unreadable entry.
	WarningCodeEntryUnsafe = "entryUnsafe"
)

// NavigationEntry is one directory or document in deterministic navigation order.
type NavigationEntry struct {
	// Kind distinguishes directories from documents.
	Kind EntryKind

	// Name is the final path component displayed in navigation.
	Name string

	// Path is the slash-relative workspace identity.
	Path string

	// Children contains nested navigation only when Kind is EntryKindDirectory.
	Children []NavigationEntry

	// SizeBytes is the scan-time regular-file size for documents.
	SizeBytes int64

	// Availability is meaningful only when Kind is EntryKindDocument.
	Availability Availability

	// DocumentMetadataRevision is the opaque hash of the scan-time portable
	// metadata signature for a document.
	DocumentMetadataRevision string

	// ReviewMetadataRevision is the opaque metadata hash for an observed exact
	// adjacent sidecar, including an unsafe entry. Nil means no sidecar entry
	// was observed.
	ReviewMetadataRevision *string
}

// Warning reports a skipped unsafe or unreadable workspace entry.
type Warning struct {
	// Path is the slash-relative entry identity.
	Path string

	// Code is a stable machine-readable warning category.
	Code string

	// Message describes why the entry or ignore file was skipped.
	Message string
}

// Snapshot is one immutable published workspace index exposed to callers.
type Snapshot struct {
	// Revision starts at one and identifies this in-process index state.
	Revision uint64

	// DocumentCount is the number of indexed Markdown documents.
	DocumentCount int

	// InitialDocumentPath selects root README.md or the first depth-first document.
	InitialDocumentPath *string

	// Navigation contains directories first and then documents at every level.
	Navigation []NavigationEntry

	// Warnings contains deterministic scan diagnostics for skipped entries.
	Warnings []Warning
}

// DocumentContent is one safely reopened UTF-8 Markdown document.
type DocumentContent struct {
	// Path is the indexed slash-relative document identity.
	Path string

	// Revision is the lower-case hexadecimal SHA-256 of Source bytes.
	Revision string

	// Source is the validated UTF-8 Markdown source.
	Source string
}

var (
	// ErrDocumentNotIndexed indicates a valid path absent from the current index.
	ErrDocumentNotIndexed = errors.New("document is not indexed")

	// ErrDocumentTooLarge indicates a document beyond the Markdown content limit.
	ErrDocumentTooLarge = errors.New("document exceeds content limit")

	// ErrDocumentInvalidUTF8 indicates document bytes that are not valid UTF-8.
	ErrDocumentInvalidUTF8 = errors.New("document is not valid UTF-8")

	// ErrInvalidRelativePath indicates a non-canonical API document identifier.
	ErrInvalidRelativePath = errors.New("invalid relative document path")

	// ErrUnsafeEntry indicates that a reopened indexed path became unsafe.
	ErrUnsafeEntry = errors.New("unsafe workspace entry")

	// ErrDocumentRead indicates an ordinary failure reopening an indexed document.
	ErrDocumentRead = errors.New("document read failed")
)

type indexedDocument struct {
	sizeBytes      int64
	availability   Availability
	metadata       filesystem.MetadataSignature
	reviewMetadata *filesystem.MetadataSignature
}

type contextMutex struct {
	token chan struct{}
}

// A channel-backed mutex lets a cancelled request stop waiting for the active
// scan without creating a goroutine solely to acquire sync.Mutex.
func newContextMutex() contextMutex {
	token := make(chan struct{}, 1)
	token <- struct{}{}
	return contextMutex{token: token}
}

func (mutex contextMutex) lock(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-mutex.token:
		return nil
	}
}

func (mutex contextMutex) unlock() {
	mutex.token <- struct{}{}
}

const scanFreshness = time.Second

// Service maintains a request-refreshed workspace index through a
// caller-owned portable filesystem gateway.
type Service struct {
	gateway *filesystem.FS
	now     func() time.Time
	scan    scanFunction
	scanMu  contextMutex

	// stateMu protects the complete published index, ignore cache, successful
	// scan time, and last ordinary scan failure. Filesystem scanning happens
	// outside this lock and publishes one complete candidate at a time.
	stateMu             sync.RWMutex
	snapshot            Snapshot
	documents           map[string]indexedDocument
	ignoreFiles         ignoreFileCache
	lastSuccessfulScan  time.Time
	lastScanFailure     error
	lastScanFailureTime time.Time
}

// New builds revision one through gateway. The caller retains ownership of the
// gateway and must keep it open while the service is in use.
func New(gateway *filesystem.FS, options Options) (*Service, error) {
	return openWithScanner(gateway, options, scanWorkspace)
}

func openWithScanner(
	gateway *filesystem.FS,
	options Options,
	scan scanFunction,
) (*Service, error) {
	if gateway == nil {
		return nil, errors.New("workspace requires a filesystem")
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}
	if scan == nil {
		return nil, errors.New("workspace scanner is required")
	}
	result, err := scan(context.Background(), gateway, nil)
	if err != nil {
		return nil, fmt.Errorf("scan workspace: %w", err)
	}
	result.snapshot.Revision = 1
	return &Service{
		gateway:            gateway,
		now:                now,
		scan:               scan,
		scanMu:             newContextMutex(),
		snapshot:           result.snapshot,
		documents:          result.documents,
		ignoreFiles:        result.ignoreFiles,
		lastSuccessfulScan: now(),
	}, nil
}

// Snapshot returns an independent copy of the current workspace index. A stale
// request synchronously drives one coalesced portable metadata scan.
func (service *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	now := service.now()
	if snapshot, err, available := service.cachedSnapshot(now); available {
		return snapshot, err
	}

	if err := service.scanMu.lock(ctx); err != nil {
		return Snapshot{}, err
	}
	defer service.scanMu.unlock()

	now = service.now()
	if snapshot, err, available := service.cachedSnapshot(now); available {
		return snapshot, err
	}

	service.stateMu.RLock()
	previousIgnoreFiles := cloneIgnoreFileCache(service.ignoreFiles)
	service.stateMu.RUnlock()

	result, err := service.scan(ctx, service.gateway, previousIgnoreFiles)
	completedAt := service.now()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Snapshot{}, ctxErr
		}
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return Snapshot{}, err
		}
		failure := fmt.Errorf("scan workspace: %w", err)
		service.stateMu.Lock()
		service.lastScanFailure = failure
		service.lastScanFailureTime = completedAt
		service.stateMu.Unlock()
		return Snapshot{}, failure
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	service.stateMu.Lock()
	result.snapshot.Revision = service.snapshot.Revision
	if !sameSnapshotState(service.snapshot, result.snapshot) {
		if service.snapshot.Revision == math.MaxUint64 {
			service.stateMu.Unlock()
			return Snapshot{}, errors.New("workspace revision is exhausted")
		}
		result.snapshot.Revision++
	}
	service.snapshot = result.snapshot
	service.documents = result.documents
	service.ignoreFiles = result.ignoreFiles
	service.lastSuccessfulScan = completedAt
	service.lastScanFailure = nil
	service.lastScanFailureTime = time.Time{}
	snapshot := cloneSnapshot(service.snapshot)
	service.stateMu.Unlock()
	return snapshot, nil
}

func (service *Service) cachedSnapshot(
	now time.Time,
) (Snapshot, error, bool) {
	service.stateMu.RLock()
	defer service.stateMu.RUnlock()

	if service.lastScanFailure != nil &&
		isFresh(now, service.lastScanFailureTime) {
		return Snapshot{}, service.lastScanFailure, true
	}
	if isFresh(now, service.lastSuccessfulScan) {
		return cloneSnapshot(service.snapshot), nil, true
	}
	return Snapshot{}, nil, false
}

func isFresh(now, observed time.Time) bool {
	return now.Sub(observed) < scanFreshness
}

// ReadDocument reopens an indexed document through the contained gateway.
//
// It rejects changed unsafe file types, content over the fixed Markdown limit,
// and invalid UTF-8. Revision is calculated from the returned source bytes.
func (service *Service) ReadDocument(
	ctx context.Context,
	relativePath string,
) (DocumentContent, error) {
	if err := ctx.Err(); err != nil {
		return DocumentContent{}, err
	}
	if err := filesystem.ValidateRelativePath(relativePath); err != nil {
		return DocumentContent{}, ErrInvalidRelativePath
	}
	service.stateMu.RLock()
	document, exists := service.documents[relativePath]
	service.stateMu.RUnlock()
	if !exists {
		return DocumentContent{}, ErrDocumentNotIndexed
	}
	if document.availability == AvailabilityTooLarge {
		return DocumentContent{}, ErrDocumentTooLarge
	}

	source, err := service.gateway.ReadFile(relativePath, limits.MaxMarkdownDocumentBytes)
	if err != nil {
		return DocumentContent{}, classifyDocumentReadError(relativePath, err)
	}
	if err := ctx.Err(); err != nil {
		return DocumentContent{}, err
	}
	if !utf8.Valid(source) {
		return DocumentContent{}, ErrDocumentInvalidUTF8
	}

	revision := sha256.Sum256(source)
	return DocumentContent{
		Path:     relativePath,
		Revision: hex.EncodeToString(revision[:]),
		Source:   string(source),
	}, nil
}

func classifyDocumentReadError(relativePath string, err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: %s", ErrDocumentNotIndexed, relativePath)
	case errors.Is(err, filesystem.ErrTooLarge):
		return fmt.Errorf("%w: %s", ErrDocumentTooLarge, relativePath)
	case errors.Is(err, filesystem.ErrSymlink),
		errors.Is(err, filesystem.ErrNotRegular),
		errors.Is(err, filesystem.ErrNotDirectory):
		return fmt.Errorf("%w: %s: %w", ErrUnsafeEntry, relativePath, err)
	default:
		return fmt.Errorf("%w: %s: %w", ErrDocumentRead, relativePath, err)
	}
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	cloned := snapshot
	cloned.Navigation = cloneNavigation(snapshot.Navigation)
	cloned.Warnings = append([]Warning(nil), snapshot.Warnings...)
	if snapshot.InitialDocumentPath != nil {
		initial := *snapshot.InitialDocumentPath
		cloned.InitialDocumentPath = &initial
	}
	return cloned
}

func cloneNavigation(entries []NavigationEntry) []NavigationEntry {
	cloned := make([]NavigationEntry, len(entries))
	for index, entry := range entries {
		cloned[index] = entry
		cloned[index].Children = cloneNavigation(entry.Children)
		if entry.ReviewMetadataRevision != nil {
			revision := *entry.ReviewMetadataRevision
			cloned[index].ReviewMetadataRevision = &revision
		}
	}
	return cloned
}

func cloneIgnoreFileCache(input ignoreFileCache) ignoreFileCache {
	if input == nil {
		return nil
	}
	cloned := make(ignoreFileCache, len(input))
	for relativePath, cached := range input {
		copied := cached
		copied.rules = append(ignoreRules(nil), cached.rules...)
		if cached.warning != nil {
			warning := *cached.warning
			copied.warning = &warning
		}
		cloned[relativePath] = copied
	}
	return cloned
}

func sameSnapshotState(first, second Snapshot) bool {
	first.Revision = 0
	second.Revision = 0
	return reflect.DeepEqual(first, second)
}

func metadataRevision(signature filesystem.MetadataSignature) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte{1, byte(signature.Kind)})
	_, _ = hash.Write([]byte(signature.RelativePath))
	writeMetadataUint64(hash, uint64(signature.SizeBytes))
	writeMetadataUint64(hash, uint64(signature.ModificationUnixNano))
	return hex.EncodeToString(hash.Sum(nil))
}

type metadataHashWriter interface {
	Write([]byte) (int, error)
}

func writeMetadataUint64(writer metadataHashWriter, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = writer.Write(encoded[:])
}
