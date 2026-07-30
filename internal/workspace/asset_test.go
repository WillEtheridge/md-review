package workspace

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"mdreview.dev/mdreview/internal/limits"
)

func TestReadAssetResolvesFromIndexedDocument(t *testing.T) {
	root := t.TempDir()
	writeAssetTestFile(t, root, "docs/guide.md", []byte("# Guide\n"))
	writeAssetTestFile(t, root, "images/diagram.png", []byte("image"))

	service, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
	})

	var content []byte
	err = service.ReadAsset(
		context.Background(),
		"docs/guide.md",
		"../images/diagram.png",
		func(reader io.Reader, sizeBytes int64) error {
			if sizeBytes != 5 {
				t.Fatalf("size = %d, want 5", sizeBytes)
			}
			var readErr error
			content, readErr = io.ReadAll(reader)
			return readErr
		},
	)
	if err != nil {
		t.Fatalf("ReadAsset() error = %v", err)
	}
	if string(content) != "image" {
		t.Fatalf("content = %q", content)
	}
}

func TestReadAssetReaderObservesContainedFileGrowthPastLimit(t *testing.T) {
	root := t.TempDir()
	writeAssetTestFile(t, root, "README.md", []byte("# Growth\n"))
	initialAsset, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
	)
	if err != nil {
		t.Fatal(err)
	}
	writeAssetTestFile(t, root, "image.png", initialAsset)
	assetPath := filepath.Join(root, "image.png")
	initialInfo, err := os.Stat(assetPath)
	if err != nil {
		t.Fatal(err)
	}

	service, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})

	var exposedBytes int64
	err = service.ReadAsset(
		context.Background(),
		"README.md",
		"image.png",
		func(reader io.Reader, sizeBytes int64) error {
			if sizeBytes != int64(len(initialAsset)) {
				t.Fatalf("opened size = %d, want %d", sizeBytes, len(initialAsset))
			}
			appendBytes := int64(limits.MaxImageAssetBytes) + 1 - sizeBytes
			if err := appendZeroBytes(assetPath, appendBytes); err != nil {
				return err
			}
			grownInfo, err := os.Stat(assetPath)
			if err != nil {
				return err
			}
			if !os.SameFile(initialInfo, grownInfo) {
				t.Fatal("asset path was replaced while growing the contained file")
			}
			exposedBytes, err = io.Copy(
				io.Discard,
				io.LimitReader(reader, int64(limits.MaxImageAssetBytes)+1),
			)
			return err
		},
	)
	if err != nil {
		t.Fatalf("ReadAsset() error = %v", err)
	}
	if exposedBytes != int64(limits.MaxImageAssetBytes)+1 {
		t.Fatalf(
			"reader exposed %d bytes, want %d",
			exposedBytes,
			int64(limits.MaxImageAssetBytes)+1,
		)
	}
	grownInfo, err := os.Lstat(assetPath)
	if err != nil {
		t.Fatal(err)
	}
	if !grownInfo.Mode().IsRegular() ||
		grownInfo.Size() != int64(limits.MaxImageAssetBytes)+1 {
		t.Fatalf("grown asset = mode %v, size %d", grownInfo.Mode(), grownInfo.Size())
	}
}

func TestReadAssetRejectsUnscopedAndUnsafeReferences(t *testing.T) {
	root := t.TempDir()
	writeAssetTestFile(t, root, "docs/guide.md", []byte("# Guide\n"))
	writeAssetTestFile(t, root, "docs/image.png", []byte("image"))
	if err := os.Symlink("image.png", filepath.Join(root, "docs", "link.png")); err != nil {
		t.Fatal(err)
	}
	service, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
	})

	references := []string{
		"",
		"/etc/passwd",
		"../../outside.png",
		"https://example.com/image.png",
		"//example.com/image.png",
		"data:image/png;base64,AAAA",
		"image.png?version=1",
		"image.png#fragment",
		`images\diagram.png`,
		"%2e%2e/%2e%2e/outside.png",
		"link.png",
	}
	for _, reference := range references {
		t.Run(reference, func(t *testing.T) {
			err := service.ReadAsset(
				context.Background(),
				"docs/guide.md",
				reference,
				func(io.Reader, int64) error {
					t.Fatal("visitor called for rejected reference")
					return nil
				},
			)
			if !errors.Is(err, ErrAssetNotFound) {
				t.Fatalf("error = %v, want ErrAssetNotFound", err)
			}
		})
	}

	err = service.ReadAsset(
		context.Background(),
		"missing.md",
		"docs/image.png",
		func(io.Reader, int64) error {
			t.Fatal("visitor called for unindexed document")
			return nil
		},
	)
	if !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("unindexed error = %v, want ErrAssetNotFound", err)
	}
}

func TestReadAssetPreservesDoubleEncodedLiteral(t *testing.T) {
	root := t.TempDir()
	writeAssetTestFile(t, root, "README.md", []byte("# Root\n"))
	writeAssetTestFile(t, root, "%2e%2e.png", []byte("literal"))
	service, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
	})

	var content []byte
	err = service.ReadAsset(
		context.Background(),
		"README.md",
		"%252e%252e.png",
		func(reader io.Reader, _ int64) error {
			var readErr error
			content, readErr = io.ReadAll(reader)
			return readErr
		},
	)
	if err != nil {
		t.Fatalf("ReadAsset() error = %v", err)
	}
	if string(content) != "literal" {
		t.Fatalf("content = %q", content)
	}
}

func TestReadAssetPropagatesCancellationAndVisitorFailure(t *testing.T) {
	root := t.TempDir()
	writeAssetTestFile(t, root, "README.md", []byte("# Root\n"))
	writeAssetTestFile(t, root, "image.png", []byte("image"))
	service, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.ReadAsset(ctx, "README.md", "image.png", func(io.Reader, int64) error {
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v, want context.Canceled", err)
	}

	expected := errors.New("stop stream")
	err = service.ReadAsset(
		context.Background(),
		"README.md",
		"image.png",
		func(io.Reader, int64) error {
			return expected
		},
	)
	if !errors.Is(err, expected) {
		t.Fatalf("visitor error = %v, want %v", err, expected)
	}
}

type zeroAssetReader struct{}

func (zeroAssetReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

func appendZeroBytes(path string, sizeBytes int64) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	_, writeErr := io.CopyN(file, zeroAssetReader{}, sizeBytes)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func writeAssetTestFile(t *testing.T, root string, relativePath string, content []byte) {
	t.Helper()
	absolutePath := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolutePath, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
