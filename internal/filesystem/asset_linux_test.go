//go:build linux

package filesystem

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWithRegularFileScopesContainedReader(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "image.png"), []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	filesystem, err := Open(root, Auto)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filesystem.Close()
	})

	var content []byte
	err = filesystem.WithRegularFile("image.png", 32, func(reader io.Reader, sizeBytes int64) error {
		if sizeBytes != 9 {
			t.Fatalf("size = %d, want 9", sizeBytes)
		}
		var readErr error
		content, readErr = io.ReadAll(reader)
		return readErr
	})
	if err != nil {
		t.Fatalf("WithRegularFile() error = %v", err)
	}
	if string(content) != "png-bytes" {
		t.Fatalf("content = %q", content)
	}
}

func TestWithRegularFileRejectsLimitAndUnsafeEntry(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.png"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("large.png", filepath.Join(root, "link.png")); err != nil {
		t.Fatal(err)
	}
	filesystem, err := Open(root, OpenatFallback)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filesystem.Close()
	})

	visit := func(io.Reader, int64) error {
		t.Fatal("visitor called for rejected asset")
		return nil
	}
	if err := filesystem.WithRegularFile("large.png", 4, visit); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("large error = %v, want ErrTooLarge", err)
	}
	if err := filesystem.WithRegularFile("link.png", 32, visit); !errors.Is(err, ErrSymlink) {
		t.Fatalf("symlink error = %v, want ErrSymlink", err)
	}
}

func TestWithRegularFilePropagatesVisitorFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "image.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	filesystem, err := Open(root, Auto)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filesystem.Close()
	})

	expected := errors.New("stop")
	err = filesystem.WithRegularFile("image.png", 32, func(io.Reader, int64) error {
		return expected
	})
	if !errors.Is(err, expected) {
		t.Fatalf("visitor error = %v, want %v", err, expected)
	}
}
