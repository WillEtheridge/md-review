//go:build linux

package filesystem

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func forEachResolutionMode(t *testing.T, test func(*testing.T, ResolutionMode)) {
	t.Helper()
	for _, mode := range []ResolutionMode{Openat2Only, OpenatFallback} {
		name := "openat2"
		if mode == OpenatFallback {
			name = "openat-fallback"
		}
		t.Run(name, func(t *testing.T) {
			test(t, mode)
		})
	}
}

func openTestFS(t *testing.T, root string, mode ResolutionMode) *FS {
	t.Helper()
	filesystem, err := Open(root, mode)
	if err != nil {
		if mode == Openat2Only &&
			(errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL)) {
			t.Skipf("openat2 is unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := filesystem.Close(); err != nil {
			t.Error(err)
		}
	})
	return filesystem
}

func TestContainedReadAndLimitPlusOneBound(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(root, "docs", "guide.md"),
			[]byte("hello"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)

		data, err := filesystem.ReadFile("docs/guide.md", 5)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "hello" {
			t.Fatalf("data = %q, want hello", data)
		}
		if _, err := filesystem.ReadFile("docs/guide.md", 4); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("bounded read error = %v, want ErrTooLarge", err)
		}
	})
}

func TestReadBoundedIndependentlyChecksLimitPlusOne(t *testing.T) {
	if _, err := readBounded(bytes.NewReader([]byte("growing")), 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("readBounded over limit error = %v, want ErrTooLarge", err)
	}
	data, err := readBounded(bytes.NewReader([]byte("four")), 4)
	if err != nil || string(data) != "four" {
		t.Fatalf("readBounded exact limit = %q, %v", data, err)
	}
}

func TestTraversalAndSymlinksAreRejected(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.WriteFile(
			filepath.Join(outside, "secret.md"),
			[]byte("secret"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			filepath.Join(outside, "secret.md"),
			filepath.Join(root, "file.md"),
		); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "directory")); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)

		for _, candidate := range []string{
			"../secret.md",
			"/etc/passwd",
			"directory/../file.md",
			"./file.md",
		} {
			if _, err := filesystem.ReadFile(candidate, 1024); err == nil {
				t.Fatalf("ReadFile(%q) unexpectedly succeeded", candidate)
			}
		}
		for _, candidate := range []string{"file.md", "directory/secret.md"} {
			if _, err := filesystem.ReadFile(candidate, 1024); err == nil {
				t.Fatalf("ReadFile(%q) followed a symlink", candidate)
			}
		}
	})
}

func TestPathComponentReplacementCannotEscape(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		outside := t.TempDir()
		swap := filepath.Join(root, "swap")
		if err := os.Mkdir(swap, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(swap, "document.md"),
			[]byte("inside"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(outside, "document.md"),
			[]byte("outside secret"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		var once sync.Once
		filesystem.hooks.beforeOpen = func(relativePath string) {
			if relativePath != "swap/document.md" {
				return
			}
			once.Do(func() {
				if err := os.Rename(swap, filepath.Join(root, "original")); err != nil {
					panic(err)
				}
				if err := os.Symlink(outside, swap); err != nil {
					panic(err)
				}
			})
		}

		data, err := filesystem.ReadFile("swap/document.md", 1024)
		if err == nil {
			t.Fatalf("replacement read succeeded with %q", data)
		}
		if bytes.Contains(data, []byte("outside secret")) {
			t.Fatal("outside content was returned")
		}
	})
}

func TestFallbackRejectsReplacementDuringComponentWalk(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(nested, "document.md"),
		[]byte("inside"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(outside, "document.md"),
		[]byte("outside secret"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	filesystem := openTestFS(t, root, OpenatFallback)
	var once sync.Once
	filesystem.hooks.beforeFallbackComponent = func(prefix string) {
		if prefix != "a/b" {
			return
		}
		once.Do(func() {
			if err := os.Rename(nested, filepath.Join(root, "a", "original")); err != nil {
				panic(err)
			}
			if err := os.Symlink(outside, nested); err != nil {
				panic(err)
			}
		})
	}

	data, err := filesystem.ReadFile("a/b/document.md", 1024)
	if err == nil {
		t.Fatalf("component-walk replacement read succeeded with %q", data)
	}
	if bytes.Contains(data, []byte("outside secret")) {
		t.Fatal("outside content was returned")
	}
}

func TestDirectoryReplacementCannotEscape(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		outside := t.TempDir()
		swap := filepath.Join(root, "swap")
		if err := os.Mkdir(swap, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("secret"), 0o644); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		var once sync.Once
		filesystem.hooks.beforeOpen = func(relativePath string) {
			if relativePath != "swap" {
				return
			}
			once.Do(func() {
				if err := os.Rename(swap, filepath.Join(root, "original")); err != nil {
					panic(err)
				}
				if err := os.Symlink(outside, swap); err != nil {
					panic(err)
				}
			})
		}

		entries, err := filesystem.ReadDirectory("swap")
		if err == nil {
			t.Fatalf("replacement scan succeeded with %v", entries)
		}
		for _, entry := range entries {
			if entry.Name == "secret.md" {
				t.Fatal("outside directory entry was returned")
			}
		}
	})
}

func TestDescriptorCarriedScanRejectsChildReplacement(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		outside := t.TempDir()
		swap := filepath.Join(root, "swap")
		if err := os.Mkdir(swap, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(swap, "inside.md"), []byte("inside"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(outside, "secret.md"),
			[]byte("outside secret"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		var once sync.Once
		filesystem.hooks.beforeOpen = func(relativePath string) {
			if relativePath != "swap" {
				return
			}
			once.Do(func() {
				if err := os.Rename(swap, filepath.Join(root, "original")); err != nil {
					panic(err)
				}
				if err := os.Symlink(outside, swap); err != nil {
					panic(err)
				}
			})
		}

		err := filesystem.WithRootDirectory(func(rootDirectory *Directory) error {
			entries, err := rootDirectory.ReadEntries()
			if err != nil {
				return err
			}
			if len(entries) != 1 || entries[0].Name != "swap" {
				t.Fatalf("root entries = %+v, want swap", entries)
			}
			return rootDirectory.OpenDirectory("swap", func(child *Directory) error {
				entries, err := child.ReadEntries()
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if entry.Name == "secret.md" {
						t.Fatal("descriptor-carried scan returned outside entry")
					}
				}
				return nil
			})
		})
		if err == nil {
			t.Fatal("descriptor-carried scan followed replacement")
		}
	})
}

func TestDescriptorCarriedNestedReadUsesOpenedParent(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(root, "docs", ".gitignore"),
			[]byte("ignored.md\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)

		err := filesystem.WithRootDirectory(func(rootDirectory *Directory) error {
			return rootDirectory.OpenDirectory("docs", func(docs *Directory) error {
				data, err := docs.ReadFile(".gitignore", 1024)
				if err != nil {
					return err
				}
				if string(data) != "ignored.md\n" {
					t.Fatalf("nested ignore data = %q", data)
				}
				return nil
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestDescriptorCarriedRecursiveScanKeepsOpenedChildAfterReplacement(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		outside := t.TempDir()
		docsPath := filepath.Join(root, "docs")
		if err := os.Mkdir(docsPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(docsPath, ".gitignore"),
			[]byte("inside.md\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(docsPath, "inside.md"),
			[]byte("inside"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(outside, ".gitignore"),
			[]byte("outside-secret.md\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(outside, "secret.md"),
			[]byte("outside secret"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		var once sync.Once
		filesystem.hooks.afterOpenDirectory = func(relativePath string) {
			if relativePath != "docs" {
				return
			}
			once.Do(func() {
				if err := os.Rename(docsPath, filepath.Join(root, "original")); err != nil {
					panic(err)
				}
				if err := os.Symlink(outside, docsPath); err != nil {
					panic(err)
				}
			})
		}

		err := filesystem.WithRootDirectory(func(rootDirectory *Directory) error {
			return rootDirectory.OpenDirectory("docs", func(docs *Directory) error {
				data, err := docs.ReadFile(".gitignore", 1024)
				if err != nil {
					return err
				}
				if string(data) != "inside.md\n" {
					t.Fatalf("nested ignore after replacement = %q, want contained data", data)
				}
				entries, err := docs.ReadEntries()
				if err != nil {
					return err
				}
				names := make([]string, 0, len(entries))
				for _, entry := range entries {
					names = append(names, entry.Name)
				}
				if !slices.Contains(names, "inside.md") || slices.Contains(names, "secret.md") {
					t.Fatalf("nested entries after replacement = %v, want contained entries", names)
				}
				return nil
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestSpecialFilesAreRejectedWithoutBlocking(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		if err := unix.Mkfifo(filepath.Join(root, "pipe.md"), 0o600); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", filepath.Join(root, "socket.md"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := listener.Close(); err != nil {
				t.Error(err)
			}
		})
		filesystem := openTestFS(t, root, mode)

		for _, candidate := range []string{"pipe.md", "socket.md"} {
			if _, err := filesystem.ReadFile(candidate, 1024); err == nil {
				t.Fatalf("ReadFile(%q) unexpectedly succeeded", candidate)
			}
		}
	})
}

func TestRequireRegularRejectsDeviceMode(t *testing.T) {
	fd, err := unix.Open("/dev/null", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if _, err := requireRegular(fd); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("requireRegular(/dev/null) = %v, want ErrNotRegular", err)
	}
}

func TestReadDirectoryClassifiesWithoutFollowingEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "guide.md"), []byte("guide"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	filesystem := openTestFS(t, root, Auto)

	entries, err := filesystem.ReadDirectory("")
	if err != nil {
		t.Fatal(err)
	}
	kinds := make(map[string]DirectoryEntryKind, len(entries))
	for _, entry := range entries {
		kinds[entry.Name] = entry.Kind
	}
	want := map[string]DirectoryEntryKind{
		"docs":     DirectoryEntryDirectory,
		"guide.md": DirectoryEntryRegular,
		"link":     DirectoryEntrySymlink,
		"pipe":     DirectoryEntrySpecial,
	}
	for name, kind := range want {
		if kinds[name] != kind {
			t.Errorf("%s kind = %v, want %v", name, kinds[name], kind)
		}
	}
}

func TestReadDirectoryReturnsComparableLinuxMetadata(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		documentPath := filepath.Join(root, "guide.md")
		if err := os.WriteFile(documentPath, []byte("guide"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("guide.md", filepath.Join(root, "guide-link")); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)

		entries, err := filesystem.ReadDirectory("")
		if err != nil {
			t.Fatal(err)
		}
		byName := make(map[string]DirectoryEntry, len(entries))
		for _, entry := range entries {
			byName[entry.Name] = entry
			if entry.Metadata.Kind != entry.Kind {
				t.Fatalf(
					"%s metadata kind = %v, entry kind = %v",
					entry.Name,
					entry.Metadata.Kind,
					entry.Kind,
				)
			}
		}

		var documentStat unix.Stat_t
		if err := unix.Lstat(documentPath, &documentStat); err != nil {
			t.Fatal(err)
		}
		want := MetadataSignature{
			Kind:      DirectoryEntryRegular,
			Device:    uint64(documentStat.Dev),
			Inode:     documentStat.Ino,
			SizeBytes: documentStat.Size,
			ModificationTime: NanosecondTimestamp{
				Seconds:     documentStat.Mtim.Sec,
				Nanoseconds: documentStat.Mtim.Nsec,
			},
			ChangeTime: NanosecondTimestamp{
				Seconds:     documentStat.Ctim.Sec,
				Nanoseconds: documentStat.Ctim.Nsec,
			},
		}
		if got := byName["guide.md"].Metadata; got != want {
			t.Fatalf("guide.md metadata = %+v, want %+v", got, want)
		}
		if got := byName["guide-link"].Metadata; got.Kind != DirectoryEntrySymlink ||
			got.Device == 0 ||
			got.Inode == 0 {
			t.Fatalf("guide-link metadata = %+v", got)
		}

		before := byName["guide.md"].Metadata
		if err := os.WriteFile(documentPath, []byte("updated guide"), 0o644); err != nil {
			t.Fatal(err)
		}
		entries, err = filesystem.ReadDirectory("")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Name == "guide.md" {
				if entry.Metadata == before {
					t.Fatalf("metadata did not change after replacement: %+v", entry.Metadata)
				}
				return
			}
		}
		t.Fatal("guide.md missing after replacement")
	})
}

func TestAutoModeUsesAvailableSafeResolver(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "document.md"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	filesystem := openTestFS(t, root, Auto)
	data, err := filesystem.ReadFile("document.md", 16)
	if err != nil || string(data) != "ok" {
		t.Fatalf("auto read = %q, %v", data, err)
	}
}

func TestCloseRejectsLaterOperations(t *testing.T) {
	root := t.TempDir()
	filesystem, err := Open(root, Auto)
	if err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Close(); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	if _, err := filesystem.ReadDirectory(""); !errors.Is(err, ErrClosed) {
		t.Fatalf("ReadDirectory after Close = %v, want ErrClosed", err)
	}
}
