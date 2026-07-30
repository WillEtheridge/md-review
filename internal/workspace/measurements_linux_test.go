//go:build linux

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mdreview.dev/mdreview/internal/gatee"
)

func TestMeasurementsCountScansContentReadsAndIgnoreReuse(t *testing.T) {
	root := t.TempDir()
	document := []byte("# Measured\n")
	ignore := []byte("ignored/\n")
	if err := os.WriteFile(filepath.Join(root, "README.md"), document, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), ignore, 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Unix(1_700_000_000, 0)
	counters := &gatee.Counters{}
	service, err := Open(root, Options{
		Measurements: counters,
		Now: func() time.Time {
			return now
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})

	startup := counters.Snapshot()
	if startup.CompleteWorkspaceScans != 1 ||
		startup.GitignoreContentOpens != 1 ||
		startup.GitignoreContentBytes != uint64(len(ignore)) ||
		startup.MarkdownContentOpens != 0 {
		t.Fatalf("startup counters = %+v", startup)
	}

	if _, err := service.ReadDocument(context.Background(), "README.md"); err != nil {
		t.Fatal(err)
	}
	afterRead := counters.Snapshot()
	if afterRead.MarkdownContentOpens != 1 ||
		afterRead.MarkdownContentBytes != uint64(len(document)) {
		t.Fatalf("document counters = %+v", afterRead)
	}

	now = now.Add(time.Second)
	if _, err := service.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	afterUnchangedScan := counters.Snapshot()
	if afterUnchangedScan.CompleteWorkspaceScans != 2 ||
		afterUnchangedScan.GitignoreContentOpens != 1 {
		t.Fatalf("unchanged scan counters = %+v", afterUnchangedScan)
	}
}
