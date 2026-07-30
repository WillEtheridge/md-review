//go:build linux

package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mdreview.dev/mdreview/internal/filesystem"
)

type fakeClock struct {
	mutex sync.Mutex
	now   time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_800_000_000, 0)}
}

func (clock *fakeClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

func (clock *fakeClock) Advance(duration time.Duration) {
	clock.mutex.Lock()
	clock.now = clock.now.Add(duration)
	clock.mutex.Unlock()
}

func openPollingWorkspace(
	t *testing.T,
	root string,
	clock *fakeClock,
	scan scanFunction,
) *Service {
	t.Helper()
	gateway, err := filesystem.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gateway.Close(); err != nil {
			t.Error(err)
		}
	})
	service, err := openWithScanner(
		gateway,
		Options{Now: clock.Now},
		scan,
	)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestOpenWithScannerLeavesCallerGatewayOpenOnFailure(t *testing.T) {
	gateway, err := filesystem.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })

	want := errors.New("scan failed")
	_, err = openWithScanner(
		gateway,
		Options{},
		func(context.Context, *filesystem.FS, ignoreFileCache) (scanResult, error) {
			return scanResult{}, want
		},
	)
	if !errors.Is(err, want) {
		t.Fatalf("openWithScanner() error = %v, want %v", err, want)
	}
	if _, err := gateway.ReadDirectory(""); err != nil {
		t.Fatalf("caller-owned gateway was closed: %v", err)
	}
}

func TestSnapshotFreshnessExactBoundaryAndNoRequestWork(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, "README.md", "# initial\n")
	clock := newFakeClock()
	var scans atomic.Int32
	countingScan := func(
		ctx context.Context,
		gateway *filesystem.FS,
		previous ignoreFileCache,
	) (scanResult, error) {
		scans.Add(1)
		return scanWorkspace(ctx, gateway, previous)
	}
	service := openPollingWorkspace(t, root, clock, countingScan)
	if got := scans.Load(); got != 1 {
		t.Fatalf("startup scans = %d, want 1", got)
	}

	clock.Advance(scanFreshness - time.Nanosecond)
	first, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("fresh scans = %d, want 1", got)
	}

	clock.Advance(time.Nanosecond)
	second, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := scans.Load(); got != 2 {
		t.Fatalf("boundary scans = %d, want 2", got)
	}
	if first.Revision != 1 || second.Revision != 1 {
		t.Fatalf(
			"unchanged revisions = %d then %d, want 1 then 1",
			first.Revision,
			second.Revision,
		)
	}

	clock.Advance(10 * scanFreshness)
	if got := scans.Load(); got != 2 {
		t.Fatalf("clock advance without request caused %d scans, want 2", got)
	}
}

func TestSnapshotCoalescesConcurrentStaleRequests(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, "README.md", "# initial\n")
	clock := newFakeClock()
	var scans atomic.Int32
	scanStarted := make(chan struct{})
	releaseScan := make(chan struct{})
	countingScan := func(
		ctx context.Context,
		gateway *filesystem.FS,
		previous ignoreFileCache,
	) (scanResult, error) {
		call := scans.Add(1)
		if call == 2 {
			close(scanStarted)
			select {
			case <-ctx.Done():
				return scanResult{}, ctx.Err()
			case <-releaseScan:
			}
		}
		return scanWorkspace(ctx, gateway, previous)
	}
	service := openPollingWorkspace(t, root, clock, countingScan)
	clock.Advance(scanFreshness)

	const callers = 12
	start := make(chan struct{})
	results := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			_, err := service.Snapshot(context.Background())
			results <- err
		}()
	}
	close(start)
	<-scanStarted
	close(releaseScan)

	for range callers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := scans.Load(); got != 2 {
		t.Fatalf("coalesced scans including startup = %d, want 2", got)
	}
}

func TestSnapshotCachesOrdinaryFailureButNotCancellation(t *testing.T) {
	t.Run("ordinary failure", func(t *testing.T) {
		root := t.TempDir()
		writeWorkspaceFile(t, root, "README.md", "# initial\n")
		clock := newFakeClock()
		scanFailure := errors.New("injected scan failure")
		var scans atomic.Int32
		scanStarted := make(chan struct{})
		releaseScan := make(chan struct{})
		scan := func(
			ctx context.Context,
			gateway *filesystem.FS,
			previous ignoreFileCache,
		) (scanResult, error) {
			if scans.Add(1) == 2 {
				close(scanStarted)
				select {
				case <-ctx.Done():
					return scanResult{}, ctx.Err()
				case <-releaseScan:
				}
				return scanResult{}, scanFailure
			}
			return scanWorkspace(ctx, gateway, previous)
		}
		service := openPollingWorkspace(t, root, clock, scan)
		clock.Advance(scanFreshness)

		const callers = 8
		results := make(chan error, callers)
		go func() {
			_, err := service.Snapshot(context.Background())
			results <- err
		}()
		<-scanStarted
		for range callers - 1 {
			go func() {
				_, err := service.Snapshot(context.Background())
				results <- err
			}()
		}
		close(releaseScan)
		for range callers {
			if err := <-results; !errors.Is(err, scanFailure) {
				t.Fatalf("coalesced Snapshot error = %v, want injected failure", err)
			}
		}
		if _, err := service.Snapshot(context.Background()); !errors.Is(err, scanFailure) {
			t.Fatalf("shared Snapshot error = %v, want injected failure", err)
		}
		if got := scans.Load(); got != 2 {
			t.Fatalf("failure-window scans = %d, want 2", got)
		}

		service.stateMu.RLock()
		revision := service.snapshot.Revision
		service.stateMu.RUnlock()
		if revision != 1 {
			t.Fatalf("failed scan published revision %d, want 1", revision)
		}

		clock.Advance(scanFreshness)
		if _, err := service.Snapshot(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := scans.Load(); got != 3 {
			t.Fatalf("post-failure retry scans = %d, want 3", got)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		root := t.TempDir()
		writeWorkspaceFile(t, root, "README.md", "# initial\n")
		clock := newFakeClock()
		var scans atomic.Int32
		scanStarted := make(chan struct{})
		scan := func(
			ctx context.Context,
			gateway *filesystem.FS,
			previous ignoreFileCache,
		) (scanResult, error) {
			if scans.Add(1) == 2 {
				close(scanStarted)
				<-ctx.Done()
				return scanResult{}, ctx.Err()
			}
			return scanWorkspace(ctx, gateway, previous)
		}
		service := openPollingWorkspace(t, root, clock, scan)
		clock.Advance(scanFreshness)

		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := service.Snapshot(ctx)
			result <- err
		}()
		<-scanStarted
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Snapshot error = %v, want context.Canceled", err)
		}

		if _, err := service.Snapshot(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := scans.Load(); got != 3 {
			t.Fatalf("live retry scans = %d, want 3", got)
		}
		service.stateMu.RLock()
		cachedFailure := service.lastScanFailure
		service.stateMu.RUnlock()
		if cachedFailure != nil {
			t.Fatalf("cancellation was cached as %v", cachedFailure)
		}
	})

	t.Run("waiting cancellation", func(t *testing.T) {
		root := t.TempDir()
		writeWorkspaceFile(t, root, "README.md", "# initial\n")
		clock := newFakeClock()
		var scans atomic.Int32
		scanStarted := make(chan struct{})
		releaseScan := make(chan struct{})
		scan := func(
			ctx context.Context,
			gateway *filesystem.FS,
			previous ignoreFileCache,
		) (scanResult, error) {
			if scans.Add(1) == 2 {
				close(scanStarted)
				select {
				case <-ctx.Done():
					return scanResult{}, ctx.Err()
				case <-releaseScan:
				}
			}
			return scanWorkspace(ctx, gateway, previous)
		}
		service := openPollingWorkspace(t, root, clock, scan)
		clock.Advance(scanFreshness)

		ownerResult := make(chan error, 1)
		go func() {
			_, err := service.Snapshot(context.Background())
			ownerResult <- err
		}()
		<-scanStarted

		waiterContext, cancelWaiter := context.WithCancel(context.Background())
		waiterStarted := make(chan struct{})
		waiterResult := make(chan error, 1)
		go func() {
			close(waiterStarted)
			_, err := service.Snapshot(waiterContext)
			waiterResult <- err
		}()
		<-waiterStarted
		cancelWaiter()
		if err := <-waiterResult; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting Snapshot error = %v, want context.Canceled", err)
		}

		close(releaseScan)
		if err := <-ownerResult; err != nil {
			t.Fatal(err)
		}
		if got := scans.Load(); got != 2 {
			t.Fatalf("waiter cancellation scans = %d, want 2", got)
		}
	})
}

func TestSnapshotPublishesDocumentAndSidecarMetadataTransitions(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, "README.md", "# initial\n")
	clock := newFakeClock()
	service := openPollingWorkspace(t, root, clock, scanWorkspace)

	snapshot := mustSnapshot(t, service)
	readme := mustDocumentEntry(t, snapshot, "README.md")
	assertMetadataRevision(t, readme.DocumentMetadataRevision)
	if readme.ReviewMetadataRevision != nil {
		t.Fatalf("initial review metadata = %v, want nil", readme.ReviewMetadataRevision)
	}

	replaceWorkspaceFile(t, root, "README.md.review.json", `{invalid`)
	snapshot = advanceAndSnapshot(t, service, clock)
	if snapshot.Revision != 2 {
		t.Fatalf("sidecar-create revision = %d, want 2", snapshot.Revision)
	}
	readme = mustDocumentEntry(t, snapshot, "README.md")
	if readme.ReviewMetadataRevision == nil {
		t.Fatal("invalid sidecar was not paired")
	}
	firstReviewMetadata := *readme.ReviewMetadataRevision
	assertMetadataRevision(t, firstReviewMetadata)

	replaceWorkspaceFile(
		t,
		root,
		"README.md.review.json",
		`{"schemaVersion":2,"threads":[]}`,
	)
	snapshot = advanceAndSnapshot(t, service, clock)
	readme = mustDocumentEntry(t, snapshot, "README.md")
	if snapshot.Revision != 3 ||
		readme.ReviewMetadataRevision == nil ||
		*readme.ReviewMetadataRevision == firstReviewMetadata {
		t.Fatalf("sidecar-modify state = revision %d, entry %+v", snapshot.Revision, readme)
	}

	if err := os.Remove(filepath.Join(root, "README.md.review.json")); err != nil {
		t.Fatal(err)
	}
	snapshot = advanceAndSnapshot(t, service, clock)
	if snapshot.Revision != 4 ||
		mustDocumentEntry(t, snapshot, "README.md").ReviewMetadataRevision != nil {
		t.Fatalf("sidecar-delete state = revision %d, navigation %+v", snapshot.Revision, snapshot.Navigation)
	}

	replaceWorkspaceFile(t, root, "notes.md", "# notes\n")
	snapshot = advanceAndSnapshot(t, service, clock)
	if snapshot.Revision != 5 || snapshot.DocumentCount != 2 {
		t.Fatalf("document-create snapshot = %+v", snapshot)
	}

	oldDocumentMetadata := mustDocumentEntry(
		t,
		snapshot,
		"README.md",
	).DocumentMetadataRevision
	replaceWorkspaceFile(t, root, "README.md", "# initial\n")
	snapshot = advanceAndSnapshot(t, service, clock)
	if snapshot.Revision != 6 ||
		mustDocumentEntry(t, snapshot, "README.md").DocumentMetadataRevision ==
			oldDocumentMetadata {
		t.Fatalf("same-content replacement snapshot = %+v", snapshot)
	}

	oldDocumentMetadata = mustDocumentEntry(
		t,
		snapshot,
		"README.md",
	).DocumentMetadataRevision
	replaceWorkspaceFile(t, root, "README.md", "# changed\n")
	snapshot = advanceAndSnapshot(t, service, clock)
	if snapshot.Revision != 7 ||
		mustDocumentEntry(t, snapshot, "README.md").DocumentMetadataRevision ==
			oldDocumentMetadata {
		t.Fatalf("document-modify snapshot = %+v", snapshot)
	}

	if err := os.Rename(
		filepath.Join(root, "README.md"),
		filepath.Join(root, "renamed.md"),
	); err != nil {
		t.Fatal(err)
	}
	snapshot = advanceAndSnapshot(t, service, clock)
	if snapshot.Revision != 8 ||
		findDocumentEntry(snapshot.Navigation, "README.md") != nil ||
		findDocumentEntry(snapshot.Navigation, "renamed.md") == nil {
		t.Fatalf("document-rename snapshot = %+v", snapshot)
	}

	if err := os.Remove(filepath.Join(root, "notes.md")); err != nil {
		t.Fatal(err)
	}
	snapshot = advanceAndSnapshot(t, service, clock)
	if snapshot.Revision != 9 || snapshot.DocumentCount != 1 {
		t.Fatalf("document-delete snapshot = %+v", snapshot)
	}
}

func TestScanTreatsSidecarRenameAsRemovalAndAddition(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, "README.md", "# readme\n")
	writeWorkspaceFile(t, root, "notes.md", "# notes\n")
	writeWorkspaceFile(
		t,
		root,
		"README.md.review.json",
		`{"schemaVersion":1,"threads":[]}`,
	)
	clock := newFakeClock()
	service := openPollingWorkspace(t, root, clock, scanWorkspace)

	initial := mustSnapshot(t, service)
	if mustDocumentEntry(t, initial, "README.md").ReviewMetadataRevision == nil ||
		mustDocumentEntry(t, initial, "notes.md").ReviewMetadataRevision != nil {
		t.Fatalf("initial sidecar pairing = %+v", initial.Navigation)
	}

	if err := os.Rename(
		filepath.Join(root, "README.md.review.json"),
		filepath.Join(root, "notes.md.review.json"),
	); err != nil {
		t.Fatal(err)
	}
	renamed := advanceAndSnapshot(t, service, clock)
	if renamed.Revision != 2 ||
		mustDocumentEntry(t, renamed, "README.md").ReviewMetadataRevision != nil ||
		mustDocumentEntry(t, renamed, "notes.md").ReviewMetadataRevision == nil {
		t.Fatalf("renamed sidecar pairing = %+v", renamed.Navigation)
	}
}

func TestScanPairsExactSidecarRegardlessOfBroadJSONIgnore(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, ".gitignore", "*.json\n")
	writeWorkspaceFile(t, root, "README.md", "# included\n")
	writeWorkspaceFile(
		t,
		root,
		"README.md.review.json",
		`{"schemaVersion":1,"threads":[]}`,
	)
	writeWorkspaceFile(
		t,
		root,
		"unpaired.md.review.json",
		`{"schemaVersion":1,"threads":[]}`,
	)
	clock := newFakeClock()
	service := openPollingWorkspace(t, root, clock, scanWorkspace)

	snapshot := mustSnapshot(t, service)
	readme := mustDocumentEntry(t, snapshot, "README.md")
	if readme.ReviewMetadataRevision == nil {
		t.Fatal("broad JSON ignore suppressed the exact paired sidecar")
	}
	originalReviewMetadata := *readme.ReviewMetadataRevision
	*readme.ReviewMetadataRevision = "mutated caller copy"
	if got := mustDocumentEntry(
		t,
		mustSnapshot(t, service),
		"README.md",
	).ReviewMetadataRevision; got == nil || *got != originalReviewMetadata {
		t.Fatalf("caller mutated published review metadata: %v", got)
	}
	if snapshot.DocumentCount != 1 || len(service.documents) != 1 {
		t.Fatalf(
			"unpaired sidecar entered index: count %d, documents %+v",
			snapshot.DocumentCount,
			service.documents,
		)
	}

	if err := os.Remove(filepath.Join(root, "README.md.review.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		"unpaired.md.review.json",
		filepath.Join(root, "README.md.review.json"),
	); err != nil {
		t.Fatal(err)
	}
	snapshot = advanceAndSnapshot(t, service, clock)
	readme = mustDocumentEntry(t, snapshot, "README.md")
	if readme.ReviewMetadataRevision == nil {
		t.Fatal("unsafe observed sidecar did not expose a metadata revision")
	}
	if !warningExists(snapshot.Warnings, "README.md.review.json", WarningCodeEntryUnsafe) {
		t.Fatalf("unsafe paired sidecar warning missing: %+v", snapshot.Warnings)
	}
}

func TestScanReusesAndPrunesSignatureKeyedIgnoreRules(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, ".gitignore", "ignored.md\n")
	writeWorkspaceFile(t, root, "README.md", "# included\n")
	writeWorkspaceFile(t, root, "ignored.md", "# ignored\n")
	clock := newFakeClock()
	var ignoreReads atomic.Int32
	var scans atomic.Int32
	scan := func(
		ctx context.Context,
		gateway *filesystem.FS,
		previous ignoreFileCache,
	) (scanResult, error) {
		scans.Add(1)
		return scanWorkspaceWithIgnoreReader(
			ctx,
			gateway,
			previous,
			func(
				directory *filesystem.Directory,
				name string,
				maxBytes int64,
			) ([]byte, error) {
				if name != ".gitignore" {
					t.Fatalf("scanner attempted a content read for %q", name)
				}
				ignoreReads.Add(1)
				return directory.ReadFile(name, maxBytes)
			},
		)
	}
	service := openPollingWorkspace(t, root, clock, scan)
	if got := ignoreReads.Load(); got != 1 {
		t.Fatalf("startup ignore reads = %d, want 1", got)
	}

	for range 3 {
		clock.Advance(scanFreshness)
		snapshot := mustSnapshot(t, service)
		if snapshot.Revision != 1 {
			t.Fatalf("unchanged scan revision = %d, want 1", snapshot.Revision)
		}
	}
	if got := scans.Load(); got != 4 {
		t.Fatalf("metadata scans = %d, want 4", got)
	}
	if got := ignoreReads.Load(); got != 1 {
		t.Fatalf("unchanged ignore reads = %d, want 1", got)
	}

	replaceWorkspaceFile(t, root, ".gitignore", "other.md\n")
	snapshot := advanceAndSnapshot(t, service, clock)
	if got := ignoreReads.Load(); got != 2 {
		t.Fatalf("changed ignore reads = %d, want 2", got)
	}
	if findDocumentEntry(snapshot.Navigation, "ignored.md") == nil {
		t.Fatalf("changed ignore rules were not applied: %+v", snapshot.Navigation)
	}

	if err := os.Remove(filepath.Join(root, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	_ = advanceAndSnapshot(t, service, clock)
	service.stateMu.RLock()
	cachedIgnoreFiles := len(service.ignoreFiles)
	service.stateMu.RUnlock()
	if cachedIgnoreFiles != 0 {
		t.Fatalf("deleted ignore cache entries = %d, want 0", cachedIgnoreFiles)
	}
}

func mustSnapshot(t *testing.T, service *Service) Snapshot {
	t.Helper()
	snapshot, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func advanceAndSnapshot(
	t *testing.T,
	service *Service,
	clock *fakeClock,
) Snapshot {
	t.Helper()
	clock.Advance(scanFreshness)
	return mustSnapshot(t, service)
}

func mustDocumentEntry(
	t *testing.T,
	snapshot Snapshot,
	documentPath string,
) NavigationEntry {
	t.Helper()
	entry := findDocumentEntry(snapshot.Navigation, documentPath)
	if entry == nil {
		t.Fatalf("document %q missing from %+v", documentPath, snapshot.Navigation)
	}
	return *entry
}

func findDocumentEntry(
	entries []NavigationEntry,
	documentPath string,
) *NavigationEntry {
	for index := range entries {
		entry := &entries[index]
		if entry.Kind == EntryKindDocument && entry.Path == documentPath {
			return entry
		}
		if nested := findDocumentEntry(entry.Children, documentPath); nested != nil {
			return nested
		}
	}
	return nil
}

func warningExists(warnings []Warning, path, code string) bool {
	for _, warning := range warnings {
		if warning.Path == path && warning.Code == code {
			return true
		}
	}
	return false
}

func assertMetadataRevision(t *testing.T, revision string) {
	t.Helper()
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(revision) {
		t.Fatalf("metadata revision = %q, want 64 lowercase hex characters", revision)
	}
}

func writeWorkspaceFile(t *testing.T, root, relativePath, content string) {
	t.Helper()
	absolutePath := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolutePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func replaceWorkspaceFile(t *testing.T, root, relativePath, content string) {
	t.Helper()
	absolutePath := filepath.Join(root, filepath.FromSlash(relativePath))
	temporaryPath := absolutePath + ".replacement"
	if err := os.WriteFile(temporaryPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporaryPath, absolutePath); err != nil {
		t.Fatal(err)
	}
}
