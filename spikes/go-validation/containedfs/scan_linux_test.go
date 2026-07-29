//go:build linux

package containedfs

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestScannerUsesDescriptorsAndReadsSafeIgnoreFiles(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		for _, directory := range []string{
			"docs",
			"docs/nested",
			".hidden",
			".git",
		} {
			if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		files := map[string]string{
			"README.md":              "root",
			"docs/guide.md":          "guide",
			"docs/nested/detail.MD":  "detail",
			".hidden/notes.md":       "hidden",
			".git/never.md":          "git",
			"docs/not-markdown.txt":  "text",
			".gitignore":             "ignored.md\n",
			"docs/nested/.gitignore": "*.tmp\n",
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		filesystem := openTestFS(t, root, mode)
		result, err := filesystem.Scan()
		if err != nil {
			t.Fatal(err)
		}
		expectedMarkdown := []string{
			".hidden/notes.md",
			"README.md",
			"docs/guide.md",
			"docs/nested/detail.MD",
		}
		if !slices.Equal(result.Markdown, expectedMarkdown) {
			t.Fatalf("Markdown = %v, want %v", result.Markdown, expectedMarkdown)
		}
		if string(result.IgnoreData[".gitignore"]) != "ignored.md\n" {
			t.Fatalf("root ignore = %q", result.IgnoreData[".gitignore"])
		}
		if string(result.IgnoreData["docs/nested/.gitignore"]) != "*.tmp\n" {
			t.Fatalf("nested ignore = %q", result.IgnoreData["docs/nested/.gitignore"])
		}
	})
}

func TestSpecialAndOversizedIgnoreFilesNeverBlock(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		for _, directory := range []string{"fifo", "socket", "symlink", "oversized"} {
			if err := os.Mkdir(filepath.Join(root, directory), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(root, directory, "visible.md"),
				[]byte("visible"),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
		}
		if err := unix.Mkfifo(filepath.Join(root, "fifo", ".gitignore"), 0o600); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", filepath.Join(root, "socket", ".gitignore"))
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		if err := os.Symlink(
			"/dev/null",
			filepath.Join(root, "symlink", ".gitignore"),
		); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(root, "oversized", ".gitignore"),
			bytes.Repeat([]byte("x"), MaxIgnoreBytes+1),
			0o600,
		); err != nil {
			t.Fatal(err)
		}

		filesystem := openTestFS(t, root, mode)
		result, err := filesystem.Scan()
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Markdown) != 4 {
			t.Fatalf("Markdown = %v", result.Markdown)
		}
		if len(result.Warnings) < 4 {
			t.Fatalf("Warnings = %v", result.Warnings)
		}
		for _, unsafe := range []string{
			"fifo/.gitignore",
			"socket/.gitignore",
			"symlink/.gitignore",
			"oversized/.gitignore",
		} {
			if _, exists := result.IgnoreData[unsafe]; exists {
				t.Fatalf("unsafe ignore file %s was read", unsafe)
			}
		}
	})
}

func TestScannerRejectsMarkdownSpecialFiles(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		if err := unix.Mkfifo(filepath.Join(root, "pipe.md"), 0o600); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", filepath.Join(root, "socket.md"))
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		if err := os.Symlink("/etc/passwd", filepath.Join(root, "link.md")); err != nil {
			t.Fatal(err)
		}

		filesystem := openTestFS(t, root, mode)
		result, err := filesystem.Scan()
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Markdown) != 0 {
			t.Fatalf("special Markdown files were indexed: %v", result.Markdown)
		}
		reasons := make([]string, 0, len(result.Warnings))
		for _, warning := range result.Warnings {
			reasons = append(reasons, warning.Path+":"+warning.Reason)
		}
		joined := strings.Join(reasons, "\n")
		for _, name := range []string{"pipe.md", "socket.md", "link.md"} {
			if !strings.Contains(joined, name) {
				t.Fatalf("missing warning for %s in %s", name, joined)
			}
		}
	})
}
