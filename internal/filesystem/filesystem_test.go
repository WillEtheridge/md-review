package filesystem

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPortableFilesystemReadsAndScansRegularFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "guide.md"), []byte("guide"), 0o600); err != nil {
		t.Fatal(err)
	}
	gateway, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })

	data, err := gateway.ReadFile("docs/guide.md", 5)
	if err != nil || string(data) != "guide" {
		t.Fatalf("ReadFile() = %q, %v", data, err)
	}
	entries, err := gateway.ReadDirectory("docs")
	if err != nil || len(entries) != 1 || entries[0].Kind != DirectoryEntryRegular {
		t.Fatalf("ReadDirectory() = %#v, %v", entries, err)
	}
	if entries[0].Metadata.RelativePath != "docs/guide.md" {
		t.Fatalf("relative metadata path = %q", entries[0].Metadata.RelativePath)
	}
}

func TestPortableFilesystemRejectsTraversalSymlinksAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	gateway, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })

	if _, err := gateway.ReadFile("../outside.md", 1024); !errors.Is(err, ErrInvalidRelativePath) {
		t.Fatalf("traversal error = %v", err)
	}
	if _, err := gateway.ReadFile("link.md", 1024); !errors.Is(err, ErrSymlink) {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestPortableMutationReplacesCompleteFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "document.md.review.json")
	if err := os.WriteFile(target, []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	gateway, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })

	emitted, err := gateway.MutateFile(
		context.Background(),
		"document.md.review.json",
		MutationOptions{MaxBytes: 1024},
		func(current []byte, exists bool) ([]byte, error) {
			if !exists || string(current) != "before" {
				t.Fatalf("callback input = %q, %t", current, exists)
			}
			return []byte("after"), nil
		},
	)
	if err != nil || string(emitted) != "after" {
		t.Fatalf("MutateFile() = %q, %v", emitted, err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "after" {
		t.Fatalf("target = %q, %v", data, err)
	}
}

func TestWithRegularFileKeepsBoundedReader(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "image.png"), []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	gateway, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })

	err = gateway.WithRegularFile("image.png", 5, func(reader io.Reader, size int64) error {
		if size != 5 {
			t.Fatalf("size = %d", size)
		}
		data, readErr := io.ReadAll(reader)
		if readErr != nil || string(data) != "image" {
			t.Fatalf("read = %q, %v", data, readErr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
