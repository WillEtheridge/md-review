//go:build linux

package containedfs

import (
	"bytes"
	"errors"
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
		if mode == Openat2Only && (errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL)) {
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

func TestContainedReadAndBound(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "docs", "guide.md")
		if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)

		data, err := filesystem.ReadFile("docs/guide.md", 5)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "hello" {
			t.Fatalf("data = %q", data)
		}
		if _, err := filesystem.ReadFile("docs/guide.md", 4); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("bounded read error = %v", err)
		}
	})
}

func TestTraversalAndSymlinksAreRejected(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("secret"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "file.md")); err != nil {
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
		if err := os.WriteFile(filepath.Join(swap, "document.md"), []byte("inside"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "document.md"), []byte("outside secret"), 0o644); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		var once sync.Once
		filesystem.Hooks.BeforeOpen = func(relativePath string) {
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
	if err := os.WriteFile(filepath.Join(nested, "document.md"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "document.md"), []byte("outside secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	filesystem := openTestFS(t, root, OpenatFallback)
	var once sync.Once
	filesystem.Hooks.BeforeFallbackComponent = func(prefix string) {
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

func TestAtomicWriteIsContainedAndPreservesPermissions(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "docs", "guide.md.review.json")
		if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)

		if err := filesystem.WriteFileAtomic(
			"docs/guide.md.review.json",
			[]byte(`{"schemaVersion":1}`),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != `{"schemaVersion":1}` {
			t.Fatalf("written data = %q", data)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o640 {
			t.Fatalf("permissions = %o, want 640", got)
		}

		entries, err := os.ReadDir(filepath.Dir(target))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("unexpected temporary entries: %v", entries)
		}
	})
}

func TestAtomicWriteRejectsSymlinkDestination(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "review.json")); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)

		err := filesystem.WriteFileAtomic("review.json", []byte("replacement"), 0o600)
		if !errors.Is(err, ErrSymlink) {
			t.Fatalf("write error = %v, want ErrSymlink", err)
		}
		data, err := os.ReadFile(outside)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "outside" {
			t.Fatalf("outside data changed to %q", data)
		}
	})
}

func TestAssetResolution(t *testing.T) {
	valid := map[string]string{
		"image.png":          "docs/image.png",
		"../images/plot.png": "images/plot.png",
	}
	for reference, expected := range valid {
		result, err := ResolveAsset("docs/guide.md", reference)
		if err != nil || result != expected {
			t.Fatalf("ResolveAsset(%q) = %q, %v", reference, result, err)
		}
	}
	for _, reference := range []string{
		"../../secret",
		"/etc/passwd",
		"https://example.com/image.png",
		"image.png?query=yes",
		"image.png#fragment",
	} {
		if result, err := ResolveAsset("docs/guide.md", reference); err == nil {
			t.Fatalf("ResolveAsset(%q) = %q, want error", reference, result)
		}
	}
}

func TestRequireRegularRejectsDeviceMode(t *testing.T) {
	fd, err := unix.Open("/dev/null", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := requireRegular(fd); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("requireRegular(/dev/null) = %v", err)
	}
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

func TestScanReplacementDoesNotTraverseOutside(t *testing.T) {
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
		if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("secret"), 0o644); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		var once sync.Once
		filesystem.Hooks.BeforeOpen = func(relativePath string) {
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
		result, err := filesystem.Scan()
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(result.Markdown, "swap/secret.md") {
			t.Fatalf("scanner escaped root: %v", result.Markdown)
		}
	})
}
