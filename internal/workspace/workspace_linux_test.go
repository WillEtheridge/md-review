//go:build linux

package workspace_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"mdreview.dev/mdreview/internal/filesystem"
	"mdreview.dev/mdreview/internal/limits"
	"mdreview.dev/mdreview/internal/workspace"
)

func forEachWorkspaceMode(
	t *testing.T,
	test func(*testing.T, filesystem.ResolutionMode),
) {
	t.Helper()
	for _, mode := range []filesystem.ResolutionMode{
		filesystem.Openat2Only,
		filesystem.OpenatFallback,
	} {
		name := "openat2"
		if mode == filesystem.OpenatFallback {
			name = "openat-fallback"
		}
		t.Run(name, func(t *testing.T) {
			test(t, mode)
		})
	}
}

func openWorkspace(
	t *testing.T,
	root string,
	mode filesystem.ResolutionMode,
) *workspace.Service {
	t.Helper()
	service, err := workspace.Open(root, workspace.Options{FilesystemMode: mode})
	if err != nil {
		if mode == filesystem.Openat2Only &&
			(errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL)) {
			t.Skipf("openat2 is unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})
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
	forEachWorkspaceMode(t, func(t *testing.T, mode filesystem.ResolutionMode) {
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

		service := openWorkspace(t, root, mode)
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
	})
}

func TestSnapshotReturnsIndependentNavigation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := openWorkspace(t, root, filesystem.Auto)

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

func TestRootReturnsCanonicalWorkspacePath(t *testing.T) {
	realRoot := t.TempDir()
	linkParent := t.TempDir()
	linkRoot := filepath.Join(linkParent, "workspace")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	service := openWorkspace(t, linkRoot, filesystem.Auto)
	if service.Root() != realRoot {
		t.Fatalf("Root() = %q, want canonical %q", service.Root(), realRoot)
	}
}

func TestUnsafeAndOversizedIgnoreFilesWarnAndRemainTraversable(t *testing.T) {
	forEachWorkspaceMode(t, func(t *testing.T, mode filesystem.ResolutionMode) {
		root := t.TempDir()
		cases := readUnsafeIgnoreCases(t)
		for _, test := range cases {
			if err := os.Mkdir(filepath.Join(root, test.Directory), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(root, test.Directory, "visible.md"),
				[]byte("visible"),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			ignorePath := filepath.Join(root, test.Directory, ".gitignore")
			switch test.Kind {
			case "fifo":
				if err := unix.Mkfifo(ignorePath, 0o600); err != nil {
					t.Fatal(err)
				}
			case "socket":
				listener, err := net.Listen("unix", ignorePath)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := listener.Close(); err != nil {
						t.Error(err)
					}
				})
			case "symlink":
				if err := os.Symlink("/dev/null", ignorePath); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(
					ignorePath,
					bytes.Repeat([]byte("x"), int(limits.MaxGitignoreFileBytes+1)),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatalf("unknown unsafe ignore fixture kind %q", test.Kind)
			}
		}
		if err := unix.Mkfifo(filepath.Join(root, "pipe.md"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/etc/passwd", filepath.Join(root, "link.md")); err != nil {
			t.Fatal(err)
		}

		service := openWorkspace(t, root, mode)
		snapshot, err := service.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.DocumentCount != 4 {
			t.Fatalf("DocumentCount = %d, want 4; navigation = %v", snapshot.DocumentCount, flattenNavigation(snapshot.Navigation))
		}
		codesByPath := make(map[string]string, len(snapshot.Warnings))
		for _, warning := range snapshot.Warnings {
			codesByPath[warning.Path] = warning.Code
		}
		wantCodes := map[string]string{
			"pipe.md": workspace.WarningCodeEntryUnsafe,
			"link.md": workspace.WarningCodeEntryUnsafe,
		}
		for _, test := range cases {
			wantCodes[test.Directory+"/.gitignore"] = test.WarningCode
		}
		for relativePath, wantCode := range wantCodes {
			if got := codesByPath[relativePath]; got != wantCode {
				t.Errorf("warning code for %s = %q, want %q; warnings = %+v", relativePath, got, wantCode, snapshot.Warnings)
			}
		}
	})
}

type unsafeIgnoreCase struct {
	Directory   string `json:"directory"`
	Kind        string `json:"kind"`
	WarningCode string `json:"warningCode"`
}

func readUnsafeIgnoreCases(t *testing.T) []unsafeIgnoreCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "unsafe-ignore-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []unsafeIgnoreCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("unsafe ignore compatibility fixture is empty")
	}
	return cases
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

	service := openWorkspace(t, root, filesystem.Auto)
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

	service := openWorkspace(t, root, filesystem.Auto)
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
	service := openWorkspace(t, root, filesystem.Auto)

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
	service := openWorkspace(t, root, filesystem.Auto)

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

func TestReadDocumentAfterCloseReportsReadFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "document.md"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := openWorkspace(t, root, filesystem.Auto)
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadDocument(context.Background(), "document.md"); !errors.Is(err, workspace.ErrDocumentRead) {
		t.Fatalf("ReadDocument after Close error = %v, want ErrDocumentRead", err)
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
	service := openWorkspace(t, root, filesystem.Auto)
	snapshot, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.InitialDocumentPath == nil || *snapshot.InitialDocumentPath != "a/nested.md" {
		t.Fatalf("InitialDocumentPath = %v, want a/nested.md", snapshot.InitialDocumentPath)
	}

	empty := openWorkspace(t, t.TempDir(), filesystem.Auto)
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
	service := openWorkspace(t, root, filesystem.Auto)
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
