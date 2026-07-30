//go:build linux

package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"mdreview.dev/mdreview/internal/limits"
	"mdreview.dev/mdreview/internal/workspace"
)

func TestUnsafeAndOversizedIgnoreFilesWarnAndRemainTraversable(t *testing.T) {
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

	service := openWorkspace(t, root)
	snapshot, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DocumentCount != 4 {
		t.Fatalf(
			"DocumentCount = %d, want 4; navigation = %v",
			snapshot.DocumentCount,
			flattenNavigation(snapshot.Navigation),
		)
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
			t.Errorf(
				"warning code for %s = %q, want %q; warnings = %+v",
				relativePath,
				got,
				wantCode,
				snapshot.Warnings,
			)
		}
	}
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
