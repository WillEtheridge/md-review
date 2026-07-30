package workspace_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"mdreview.dev/mdreview/internal/filesystem"
	"mdreview.dev/mdreview/internal/limits"
	"mdreview.dev/mdreview/internal/workspace"
)

func openWorkspace(t *testing.T, root string) *workspace.Service {
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
	service, err := workspace.New(gateway, workspace.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func copyDiscoveryFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.CopyFS(root, os.DirFS("testdata/discovery")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDiscoveryCompatibilityFixture(t *testing.T) {
	root := copyDiscoveryFixture(t)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, ".git", "never.md"),
		[]byte("not discoverable"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	service := openWorkspace(t, root)
	snapshot, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 1 {
		t.Fatalf("Revision = %d, want 1", snapshot.Revision)
	}
	if snapshot.DocumentCount != 11 {
		t.Fatalf("DocumentCount = %d, want 11", snapshot.DocumentCount)
	}
	if snapshot.InitialDocumentPath == nil || *snapshot.InitialDocumentPath != "README.md" {
		t.Fatalf("InitialDocumentPath = %v, want README.md", snapshot.InitialDocumentPath)
	}
	if len(snapshot.Warnings) != 0 {
		t.Fatalf("Warnings = %+v, want none", snapshot.Warnings)
	}

	wantOrder := []string{
		".hidden/",
		".hidden/notes.md",
		"alpha/",
		"alpha/index.md",
		"docs/",
		"docs/deeper/",
		"docs/deeper/guide.md",
		"docs/deeper/only-here.md",
		"docs/nested-keep.md",
		"Zoo/",
		"Zoo/index.md",
		"A.md",
		"a.md",
		"B.md",
		"README.md",
		"wild-keep.md",
	}
	if got := flattenNavigation(snapshot.Navigation); !slices.Equal(got, wantOrder) {
		t.Fatalf("navigation order:\n got %v\nwant %v", got, wantOrder)
	}
	for _, excluded := range []string{
		".git/never.md",
		"UPPER.MD",
		"anchored.md",
		"root-ignored.md",
		"wild-drop.md",
		"ignored-dir/never.md",
		"docs/nested-drop.md",
		"docs/only-here.md",
		"docs/root-ignored.md",
		"docs/side.md.review.json",
	} {
		if slices.Contains(flattenNavigation(snapshot.Navigation), excluded) {
			t.Errorf("excluded path %q appears in navigation", excluded)
		}
	}
}

func TestSnapshotReturnsIndependentNavigation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := openWorkspace(t, root)

	first, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first.Navigation[0].Name = "mutated"
	*first.InitialDocumentPath = "mutated.md"

	second, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Navigation[0].Name != "README.md" {
		t.Fatalf("snapshot navigation was mutated: %+v", second.Navigation)
	}
	if second.InitialDocumentPath == nil || *second.InitialDocumentPath != "README.md" {
		t.Fatalf("snapshot initial path was mutated: %v", second.InitialDocumentPath)
	}
}

func TestUnsafeNestedIgnoreKeepsInheritedRules(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("skip.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", filepath.Join(root, "nested", ".gitignore")); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"nested/skip.md":    "ignored",
		"nested/visible.md": "visible",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	service := openWorkspace(t, root)
	snapshot, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := flattenNavigation(snapshot.Navigation), []string{"nested/", "nested/visible.md"}; !slices.Equal(got, want) {
		t.Fatalf("navigation = %v, want %v", got, want)
	}
	if len(snapshot.Warnings) != 1 ||
		snapshot.Warnings[0].Code != workspace.WarningCodeIgnoreFileUnsafe {
		t.Fatalf("Warnings = %+v, want one unsafe ignore warning", snapshot.Warnings)
	}
}

func TestUnreadableDirectoryWarnsAndDoesNotBlockSnapshot(t *testing.T) {
	root := t.TempDir()
	unreadable := filepath.Join(root, "unreadable")
	if err := os.Mkdir(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unreadable, "hidden.md"), []byte("hidden"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(unreadable, 0o700); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Error(err)
		}
	})
	if err := os.WriteFile(filepath.Join(root, "visible.md"), []byte("visible"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := openWorkspace(t, root)
	snapshot, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := flattenNavigation(snapshot.Navigation), []string{"visible.md"}; !slices.Equal(got, want) {
		t.Fatalf("navigation = %v, want %v", got, want)
	}
	if len(snapshot.Warnings) != 1 ||
		snapshot.Warnings[0].Path != "unreadable" ||
		snapshot.Warnings[0].Code != workspace.WarningCodeDirectoryUnreadable {
		t.Fatalf("Warnings = %+v, want directoryUnreadable for unreadable", snapshot.Warnings)
	}
}

func TestOversizedMarkdownRemainsVisible(t *testing.T) {
	root := t.TempDir()
	sizeBytes := limits.MaxMarkdownDocumentBytes + 1
	if err := os.WriteFile(filepath.Join(root, "oversized.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(root, "oversized.md"), sizeBytes); err != nil {
		t.Fatal(err)
	}
	service := openWorkspace(t, root)

	snapshot, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DocumentCount != 1 {
		t.Fatalf("DocumentCount = %d, want 1", snapshot.DocumentCount)
	}
	document := snapshot.Navigation[0]
	if document.Path != "oversized.md" ||
		document.SizeBytes != sizeBytes ||
		document.Availability != workspace.AvailabilityTooLarge {
		t.Fatalf("oversized navigation entry = %+v", document)
	}
	if _, err := service.ReadDocument(context.Background(), "oversized.md"); !errors.Is(err, workspace.ErrDocumentTooLarge) {
		t.Fatalf("ReadDocument oversized error = %v, want ErrDocumentTooLarge", err)
	}
}

func TestReadDocumentContractsAndChangedEntries(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"valid.md":    []byte("# Valid\n"),
		"invalid.md":  {0xff, 0xfe},
		"removed.md":  []byte("removed"),
		"replaced.md": []byte("inside"),
		"grown.md":    []byte("small"),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	service := openWorkspace(t, root)

	content, err := service.ReadDocument(context.Background(), "valid.md")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(files["valid.md"])
	if content.Path != "valid.md" ||
		content.Source != string(files["valid.md"]) ||
		content.Revision != hex.EncodeToString(sum[:]) {
		t.Fatalf("valid content = %+v", content)
	}
	if _, err := service.ReadDocument(context.Background(), "invalid.md"); !errors.Is(err, workspace.ErrDocumentInvalidUTF8) {
		t.Fatalf("invalid UTF-8 error = %v, want ErrDocumentInvalidUTF8", err)
	}
	if _, err := service.ReadDocument(context.Background(), "../valid.md"); !errors.Is(err, workspace.ErrInvalidRelativePath) {
		t.Fatalf("invalid path error = %v, want ErrInvalidRelativePath", err)
	}
	if _, err := service.ReadDocument(context.Background(), "missing.md"); !errors.Is(err, workspace.ErrDocumentNotIndexed) {
		t.Fatalf("missing path error = %v, want ErrDocumentNotIndexed", err)
	}

	if err := os.Remove(filepath.Join(root, "removed.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadDocument(context.Background(), "removed.md"); !errors.Is(err, workspace.ErrDocumentNotIndexed) {
		t.Fatalf("removed document error = %v, want ErrDocumentNotIndexed", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "replaced.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "replaced.md")); err != nil {
		t.Fatal(err)
	}
	if content, err := service.ReadDocument(context.Background(), "replaced.md"); !errors.Is(err, workspace.ErrUnsafeEntry) {
		t.Fatalf("replaced document = %+v, error = %v, want ErrUnsafeEntry", content, err)
	}

	if err := os.Truncate(
		filepath.Join(root, "grown.md"),
		limits.MaxMarkdownDocumentBytes+1,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadDocument(context.Background(), "grown.md"); !errors.Is(err, workspace.ErrDocumentTooLarge) {
		t.Fatalf("grown document error = %v, want ErrDocumentTooLarge", err)
	}
}

func TestReadDocumentAfterGatewayCloseReportsReadFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "document.md"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	gateway, err := filesystem.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := workspace.New(gateway, workspace.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadDocument(context.Background(), "document.md"); !errors.Is(err, workspace.ErrDocumentRead) {
		t.Fatalf("ReadDocument after gateway close error = %v, want ErrDocumentRead", err)
	}
}

func TestInitialDocumentFallsBackDepthFirstAndEmptyIsNil(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "nested.md"), []byte("nested"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "root.md"), []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := openWorkspace(t, root)
	snapshot, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.InitialDocumentPath == nil || *snapshot.InitialDocumentPath != "a/nested.md" {
		t.Fatalf("InitialDocumentPath = %v, want a/nested.md", snapshot.InitialDocumentPath)
	}

	empty := openWorkspace(t, t.TempDir())
	emptySnapshot, err := empty.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if emptySnapshot.InitialDocumentPath != nil || emptySnapshot.DocumentCount != 0 {
		t.Fatalf("empty snapshot = %+v", emptySnapshot)
	}
}

func TestContextCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "document.md"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := openWorkspace(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := service.Snapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Snapshot canceled error = %v", err)
	}
	if _, err := service.ReadDocument(ctx, "document.md"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadDocument canceled error = %v", err)
	}
}

func flattenNavigation(entries []workspace.NavigationEntry) []string {
	var paths []string
	for _, entry := range entries {
		if entry.Kind == workspace.EntryKindDirectory {
			paths = append(paths, entry.Path+"/")
			paths = append(paths, flattenNavigation(entry.Children)...)
			continue
		}
		paths = append(paths, entry.Path)
	}
	return paths
}
