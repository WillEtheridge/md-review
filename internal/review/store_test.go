//go:build linux

package review

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"mdreview.dev/mdreview/internal/filesystem"
	"mdreview.dev/mdreview/internal/gatee"
	"mdreview.dev/mdreview/internal/limits"
)

func TestStoreReadMissingAndExistingSidecars(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md", []byte("# mdReview\n"), 0o644)
	gateway := openFilesystem(t, root)
	store := deterministicStore(t, gateway)

	missing, err := store.Read(context.Background(), "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Path != "README.md" ||
		missing.DocumentRevision != Revision([]byte("# mdReview\n")) ||
		missing.ReviewRevision != nil ||
		len(missing.Threads) != 0 {
		t.Fatalf("missing snapshot = %+v", missing)
	}

	sidecar := []byte(validSidecar(
		`{"id":"thread_existing","anchor":{"type":"text","range":{"start":0,"end":8},` +
			`"source":"mdReview","text":"mdReview"},"status":"open","messages":[` +
			validMessage("message_existing") + `]}`,
	))
	writeFile(t, root, "README.md.review.json", sidecar, 0o640)
	existing, err := store.Read(context.Background(), "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if existing.ReviewRevision == nil || *existing.ReviewRevision != Revision(sidecar) {
		t.Fatalf("review revision = %v", existing.ReviewRevision)
	}
	if len(existing.Threads) != 1 ||
		existing.Threads[0].Attachment.State != AttachmentAttached ||
		existing.Threads[0].Attachment.CurrentRange.Start != 2 ||
		existing.Threads[0].Attachment.CurrentRange.End != 10 {
		t.Fatalf("resolved existing thread = %+v", existing.Threads)
	}
	if existing.Threads[0].Anchor.Range.Start != 0 {
		t.Fatal("Read replaced the persisted original range")
	}
}

func TestStoreMeasurementsCountMarkdownAndSidecarContentReads(t *testing.T) {
	root := t.TempDir()
	markdown := []byte("# measured\n")
	sidecar := []byte("{\"schemaVersion\":1,\"threads\":[]}\n")
	writeFile(t, root, "README.md", markdown, 0o644)
	writeFile(t, root, "README.md.review.json", sidecar, 0o640)
	counters := &gatee.Counters{}
	options := deterministicStoreOptions()
	options.Measurements = counters
	store, err := NewStore(openFilesystem(t, root), options)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Read(context.Background(), "README.md"); err != nil {
		t.Fatal(err)
	}
	observed := counters.Snapshot()
	if observed.MarkdownContentOpens != 1 ||
		observed.MarkdownContentBytes != uint64(len(markdown)) ||
		observed.SidecarContentOpens != 1 ||
		observed.SidecarContentBytes != uint64(len(sidecar)) {
		t.Fatalf("content counters = %+v", observed)
	}
}

func TestStoreReadKeepsSidecarFailureStatesDistinct(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, root string)
		want    error
	}{
		{
			name: "invalid",
			prepare: func(t *testing.T, root string) {
				writeFile(t, root, "README.md.review.json", []byte(`{"schemaVersion":1,`), 0o640)
			},
			want: ErrInvalid,
		},
		{
			name: "unsupported",
			prepare: func(t *testing.T, root string) {
				writeFile(t, root, "README.md.review.json", []byte(`{"schemaVersion":2,"threads":[]}`), 0o640)
			},
			want: ErrUnsupportedSchema,
		},
		{
			name: "oversized",
			prepare: func(t *testing.T, root string) {
				writeFile(
					t,
					root,
					"README.md.review.json",
					bytes.Repeat([]byte(" "), int(limits.MaxReviewSidecarBytes+1)),
					0o640,
				)
			},
			want: ErrTooLarge,
		},
		{
			name: "symlink",
			prepare: func(t *testing.T, root string) {
				outside := filepath.Join(t.TempDir(), "outside.json")
				if err := os.WriteFile(outside, []byte(`{"schemaVersion":1,"threads":[]}`), 0o640); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "README.md.review.json")); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrUnsafe,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "README.md", []byte("# title\n"), 0o644)
			test.prepare(t, root)
			store := deterministicStore(t, openFilesystem(t, root))
			if _, err := store.Read(context.Background(), "README.md"); !errors.Is(err, test.want) {
				t.Fatalf("Read error = %v, want %v", err, test.want)
			}
			if _, err := os.Lstat(filepath.Join(root, "README.md.review.json")); err != nil {
				t.Fatalf("read-only sidecar was changed or removed: %v", err)
			}
		})
	}
}

func TestStoreCreatesTextAndDocumentThreads(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md", []byte("# mdReview\n"), 0o644)
	gateway := openFilesystem(t, root)

	oldUmask := setUmask(t, 0o027)
	defer setUmask(t, oldUmask)

	store := deterministicStore(t, gateway)
	textResult, err := store.CreateThread(context.Background(), CreateThreadInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: Revision([]byte("# mdReview\n")),
		Anchor: Anchor{
			Type:   AnchorText,
			Range:  &ByteRange{Start: 2, End: 10},
			Source: "mdReview",
			Text:   "mdReview",
		},
		MessageBody: "Explain the name.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if textResult.Durability != DurabilityDurable ||
		textResult.DocumentRevision != Revision([]byte("# mdReview\n")) ||
		textResult.Thread.ID != "thread_0000000000000000000001" ||
		textResult.Thread.Status != StatusOpen ||
		textResult.Thread.Attachment.State != AttachmentAttached {
		t.Fatalf("text result = %+v", textResult)
	}
	message := textResult.Thread.Messages[0]
	if message.ID != "message_0000000000000000000002" ||
		message.Author != (Author{Type: "human", Name: "Reviewer"}) ||
		message.CreatedAt != "2026-07-28T14:30:00Z" {
		t.Fatalf("created message = %+v", message)
	}

	sidecarPath := filepath.Join(root, "README.md.review.json")
	info, err := os.Stat(sidecarPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("new sidecar permissions = %o, want 640", info.Mode().Perm())
	}

	staleReviewRevision := (*string)(nil)
	documentResult, err := store.CreateThread(context.Background(), CreateThreadInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: textResult.DocumentRevision,
		ExpectedReviewRevision:   staleReviewRevision,
		Anchor:                   Anchor{Type: AnchorDocument},
		MessageBody:              "Add an introduction.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if documentResult.Thread.Anchor.Type != AnchorDocument ||
		documentResult.Thread.Attachment.State != AttachmentDocument {
		t.Fatalf("document result = %+v", documentResult.Thread)
	}
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatal(err)
	}
	document, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Threads()) != 2 {
		t.Fatalf("merged thread count = %d, want 2", len(document.Threads()))
	}
	if documentResult.ReviewRevision != Revision(data) {
		t.Fatalf("result review revision does not match exact sidecar bytes")
	}
}

func TestStoreRebasesOnlyOneExactOccurrenceAfterDocumentChange(t *testing.T) {
	original := []byte("old target")
	tests := []struct {
		name       string
		current    string
		wantStart  uint64
		wantEnd    uint64
		wantErr    error
		documentAt bool
	}{
		{"unique move", "moved target here", 6, 12, nil, false},
		{"missing", "moved elsewhere", 0, 0, ErrDocumentChanged, false},
		{"ambiguous", "target and target", 0, 0, ErrDocumentChanged, false},
		{"overlapping ambiguous", "ttt", 0, 0, ErrDocumentChanged, false},
		{"document anchor tolerates change", "rewritten", 0, 0, nil, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expectedOriginal := original
			root := t.TempDir()
			writeFile(t, root, "README.md", []byte(test.current), 0o644)
			store := deterministicStore(t, openFilesystem(t, root))
			anchor := Anchor{
				Type:   AnchorText,
				Range:  &ByteRange{Start: 4, End: 10},
				Source: "target",
				Text:   "target",
			}
			if test.name == "overlapping ambiguous" {
				expectedOriginal = []byte("old tt")
				anchor.Range = &ByteRange{Start: 4, End: 6}
				anchor.Source = "tt"
				anchor.Text = "tt"
			}
			if test.documentAt {
				anchor = Anchor{Type: AnchorDocument}
			}
			result, err := store.CreateThread(context.Background(), CreateThreadInput{
				DocumentPath:             "README.md",
				ExpectedDocumentRevision: Revision(expectedOriginal),
				Anchor:                   anchor,
				MessageBody:              "Review this.",
			})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("CreateThread error = %v, want %v", err, test.wantErr)
				}
				var conflict *ConflictError
				if !errors.As(err, &conflict) ||
					conflict.Current.DocumentRevision != Revision([]byte(test.current)) {
					t.Fatalf("conflict = %+v", conflict)
				}
				if _, statErr := os.Stat(filepath.Join(root, "README.md.review.json")); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("conflicted creation wrote a sidecar: %v", statErr)
				}
				return
			}
			if test.documentAt {
				if result.Thread.Attachment.State != AttachmentDocument {
					t.Fatalf("document attachment = %+v", result.Thread.Attachment)
				}
				return
			}
			if result.Thread.Anchor.Range.Start != test.wantStart ||
				result.Thread.Anchor.Range.End != test.wantEnd ||
				result.Thread.Attachment.CurrentRange.Start != test.wantStart ||
				result.Thread.Attachment.CurrentRange.End != test.wantEnd {
				t.Fatalf("rebased result = %+v", result.Thread)
			}
		})
	}
}

func TestStoreRejectsInvalidCreateOperationsBeforeWriting(t *testing.T) {
	bodyLimit := strings.Repeat("x", int(limits.MaxPersistedMessageBodyBytes+1))
	sourceLimit := strings.Repeat("x", int(limits.MaxTextAnchorSourceBytes+1))
	valid := CreateThreadInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: strings.Repeat("a", 64),
		Anchor:                   Anchor{Type: AnchorDocument},
		MessageBody:              "Message.",
	}
	tests := []struct {
		name   string
		mutate func(*CreateThreadInput)
	}{
		{"traversal path", func(input *CreateThreadInput) { input.DocumentPath = "../README.md" }},
		{"non-Markdown path", func(input *CreateThreadInput) { input.DocumentPath = "README.txt" }},
		{"invalid document revision", func(input *CreateThreadInput) { input.ExpectedDocumentRevision = "ABC" }},
		{"invalid review revision", func(input *CreateThreadInput) {
			revision := strings.Repeat("A", 64)
			input.ExpectedReviewRevision = &revision
		}},
		{"empty message", func(input *CreateThreadInput) { input.MessageBody = "" }},
		{"oversized message", func(input *CreateThreadInput) { input.MessageBody = bodyLimit }},
		{"unknown anchor", func(input *CreateThreadInput) { input.Anchor = Anchor{Type: "line"} }},
		{"document text fields", func(input *CreateThreadInput) {
			input.Anchor = Anchor{Type: AnchorDocument, Text: "extra"}
		}},
		{"missing text range", func(input *CreateThreadInput) {
			input.Anchor = Anchor{Type: AnchorText, Source: "x", Text: "x"}
		}},
		{"empty visible text", func(input *CreateThreadInput) {
			input.Anchor = textAnchor(0, 1, "x")
			input.Anchor.Text = ""
		}},
		{"oversized source", func(input *CreateThreadInput) {
			input.Anchor = textAnchor(0, uint64(len(sourceLimit)), sourceLimit)
		}},
		{"reversed range", func(input *CreateThreadInput) {
			input.Anchor = textAnchor(2, 1, "x")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "README.md", []byte("x"), 0o644)
			store := deterministicStore(t, openFilesystem(t, root))
			input := valid
			test.mutate(&input)
			if _, err := store.CreateThread(context.Background(), input); !errors.Is(err, ErrInvalidOperation) {
				t.Fatalf("CreateThread error = %v, want ErrInvalidOperation", err)
			}
			if _, err := os.Stat(filepath.Join(root, "README.md.review.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid operation wrote a sidecar: %v", err)
			}
		})
	}
}

func TestStoreRejectsSameRevisionAnchorMismatch(t *testing.T) {
	markdown := []byte("one value")
	root := t.TempDir()
	writeFile(t, root, "README.md", markdown, 0o644)
	store := deterministicStore(t, openFilesystem(t, root))
	_, err := store.CreateThread(context.Background(), CreateThreadInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: Revision(markdown),
		Anchor:                   textAnchor(0, 3, "value"),
		MessageBody:              "Comment.",
	})
	if !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("CreateThread error = %v, want ErrInvalidOperation", err)
	}
}

func TestStoreConcurrentSameSidecarCreationsMerge(t *testing.T) {
	root := t.TempDir()
	markdown := []byte("# title\n")
	writeFile(t, root, "README.md", markdown, 0o644)
	store := deterministicStore(t, openFilesystem(t, root))

	const workers = 12
	start := make(chan struct{})
	results := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			<-start
			_, err := store.CreateThread(context.Background(), CreateThreadInput{
				DocumentPath:             "README.md",
				ExpectedDocumentRevision: Revision(markdown),
				Anchor:                   Anchor{Type: AnchorDocument},
				MessageBody:              fmt.Sprintf("Comment %d", worker),
			})
			results <- err
		}(worker)
	}
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "README.md.review.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Threads()) != workers {
		t.Fatalf("thread count = %d, want %d", len(document.Threads()), workers)
	}
}

func TestStoreDoesNotSerializeDifferentSidecars(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	fake := &fakeGateway{
		files: map[string][]byte{
			"a.md": []byte("a"),
			"b.md": []byte("b"),
		},
		mutate: func(
			ctx context.Context,
			relativePath string,
			options filesystem.MutationOptions,
			callback filesystem.MutationCallback,
		) ([]byte, filesystem.Durability, error) {
			entered <- relativePath
			select {
			case <-release:
			case <-ctx.Done():
				return nil, filesystem.DurabilityUnknown, ctx.Err()
			}
			updated, err := callback(nil, false)
			return updated, filesystem.DurabilityDurable, err
		},
	}
	store := deterministicStoreWithGateway(t, fake)
	results := make(chan error, 2)
	for _, documentPath := range []string{"a.md", "b.md"} {
		go func(path string) {
			_, err := store.CreateThread(context.Background(), CreateThreadInput{
				DocumentPath:             path,
				ExpectedDocumentRevision: Revision(fake.files[path]),
				Anchor:                   Anchor{Type: AnchorDocument},
				MessageBody:              "Comment.",
			})
			results <- err
		}(documentPath)
	}
	for count := 0; count < 2; count++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("different-sidecar mutations were globally serialized")
		}
	}
	close(release)
	for count := 0; count < 2; count++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreScopesIdenticalIDsToEachDerivedSidecar(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "first.md", []byte("first"), 0o644)
	writeFile(t, root, "second.md", []byte("second"), 0o644)
	gateway := openFilesystem(t, root)
	store, err := NewStore(gateway, StoreOptions{
		Now: func() time.Time {
			return time.Date(2026, 7, 28, 14, 30, 0, 0, time.UTC)
		},
		NewID: func(prefix string) (string, error) {
			switch prefix {
			case threadIDPrefix:
				return "thread_AAAAAAAAAAAAAAAAAAAAAA", nil
			case messageIDPrefix:
				return "message_BBBBBBBBBBBBBBBBBBBBBB", nil
			default:
				return "", fmt.Errorf("unexpected prefix %q", prefix)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, documentPath := range []string{"first.md", "second.md"} {
		markdown, err := os.ReadFile(filepath.Join(root, documentPath))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateThread(context.Background(), CreateThreadInput{
			DocumentPath:             documentPath,
			ExpectedDocumentRevision: Revision(markdown),
			Anchor:                   Anchor{Type: AnchorDocument},
			MessageBody:              "Review " + documentPath,
		}); err != nil {
			t.Fatalf("create %s: %v", documentPath, err)
		}
	}

	for _, documentPath := range []string{"first.md", "second.md"} {
		data, err := os.ReadFile(filepath.Join(root, documentPath+".review.json"))
		if err != nil {
			t.Fatal(err)
		}
		document, err := Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		threads := document.Threads()
		if len(threads) != 1 ||
			threads[0].ID != "thread_AAAAAAAAAAAAAAAAAAAAAA" ||
			threads[0].Messages[0].ID != "message_BBBBBBBBBBBBBBBBBBBBBB" ||
			threads[0].Messages[0].Body != "Review "+documentPath {
			t.Fatalf("%s sidecar contains %+v", documentPath, threads)
		}
	}
}

func TestStorePreservesExternalUnknownValuesDuringCreation(t *testing.T) {
	root := t.TempDir()
	markdown := []byte("# title\n")
	writeFile(t, root, "README.md", markdown, 0o644)
	fixture := losslessFixture(t)
	writeFile(t, root, "README.md.review.json", fixture, 0o640)
	store := deterministicStore(t, openFilesystem(t, root))

	if _, err := store.CreateThread(context.Background(), CreateThreadInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: Revision(markdown),
		ExpectedReviewRevision:   nil,
		Anchor:                   Anchor{Type: AnchorDocument},
		MessageBody:              "New feedback.",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(filepath.Join(root, "README.md.review.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(result, []byte("9007199254740993123456789")) {
		t.Fatal("arbitrary-precision unknown number was lost")
	}
	before, _ := parseJSON(fixture)
	after, _ := parseJSON(result)
	assertBytesEqual(
		t,
		"external root field",
		mustChild(t, before, "futureRoot").raw,
		mustChild(t, after, "futureRoot").raw,
	)
}

func TestStoreSurfacesAppliedUncertainDurability(t *testing.T) {
	markdown := []byte("# title\n")
	fake := &fakeGateway{
		files: map[string][]byte{"README.md": markdown},
		mutate: func(
			ctx context.Context,
			relativePath string,
			options filesystem.MutationOptions,
			callback filesystem.MutationCallback,
		) ([]byte, filesystem.Durability, error) {
			updated, err := callback(nil, false)
			return updated, filesystem.DurabilityUncertain, err
		},
	}
	store := deterministicStoreWithGateway(t, fake)
	result, err := store.CreateThread(context.Background(), CreateThreadInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: Revision(markdown),
		Anchor:                   Anchor{Type: AnchorDocument},
		MessageBody:              "Comment.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Durability != DurabilityUncertain {
		t.Fatalf("durability = %q, want uncertain", result.Durability)
	}
}

func TestStoreTranslatesFilesystemMutationOutcomes(t *testing.T) {
	markdown := []byte("# title\n")
	validSidecarBytes := []byte(`{"schemaVersion":1,"threads":[]}`)
	tests := []struct {
		name      string
		mutateErr error
		want      error
		conflict  bool
	}{
		{"contention", filesystem.ErrMutationConflict, ErrReviewChanged, true},
		{"unsafe", filesystem.ErrUnsafeMutationTarget, ErrUnsafe, false},
		{"too large", filesystem.ErrMutationTooLarge, ErrTooLarge, false},
		{"I/O", filesystem.ErrMutationIO, ErrUnavailable, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeGateway{
				files: map[string][]byte{
					"README.md":             markdown,
					"README.md.review.json": validSidecarBytes,
				},
				mutate: func(
					context.Context,
					string,
					filesystem.MutationOptions,
					filesystem.MutationCallback,
				) ([]byte, filesystem.Durability, error) {
					return nil, filesystem.DurabilityUnknown, test.mutateErr
				},
			}
			store := deterministicStoreWithGateway(t, fake)
			_, err := store.CreateThread(context.Background(), CreateThreadInput{
				DocumentPath:             "README.md",
				ExpectedDocumentRevision: Revision(markdown),
				Anchor:                   Anchor{Type: AnchorDocument},
				MessageBody:              "Comment.",
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("CreateThread error = %v, want %v", err, test.want)
			}
			if test.conflict {
				var conflict *ConflictError
				if !errors.As(err, &conflict) ||
					conflict.Current.DocumentRevision != Revision(markdown) ||
					conflict.Current.ReviewRevision == nil ||
					*conflict.Current.ReviewRevision != Revision(validSidecarBytes) {
					t.Fatalf("conflict = %+v", conflict)
				}
			}
		})
	}
}

func TestStoreRejectsUnsafeAndInvalidExistingSidecarsWithoutOverwrite(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, root string)
		want    error
	}{
		{
			name: "invalid",
			prepare: func(t *testing.T, root string) {
				writeFile(t, root, "README.md.review.json", []byte(`{invalid`), 0o640)
			},
			want: ErrInvalid,
		},
		{
			name: "unsupported",
			prepare: func(t *testing.T, root string) {
				writeFile(t, root, "README.md.review.json", []byte(`{"schemaVersion":9,"threads":[]}`), 0o640)
			},
			want: ErrUnsupportedSchema,
		},
		{
			name: "symlink",
			prepare: func(t *testing.T, root string) {
				outside := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(outside, []byte("outside"), 0o640); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "README.md.review.json")); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrUnsafe,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			markdown := []byte("# title\n")
			writeFile(t, root, "README.md", markdown, 0o644)
			test.prepare(t, root)
			target := filepath.Join(root, "README.md.review.json")
			before, _ := os.ReadFile(target)
			store := deterministicStore(t, openFilesystem(t, root))
			_, err := store.CreateThread(context.Background(), CreateThreadInput{
				DocumentPath:             "README.md",
				ExpectedDocumentRevision: Revision(markdown),
				Anchor:                   Anchor{Type: AnchorDocument},
				MessageBody:              "Comment.",
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("CreateThread error = %v, want %v", err, test.want)
			}
			after, _ := os.ReadFile(target)
			assertBytesEqual(t, "rejected sidecar", before, after)
		})
	}
}

func TestStoreRejectsOversizedResultWithoutChangingSidecar(t *testing.T) {
	root := t.TempDir()
	markdown := []byte("# title\n")
	writeFile(t, root, "README.md", markdown, 0o644)
	prefix := `{"schemaVersion":1,"threads":[],"padding":"`
	suffix := `"}`
	paddingSize := int(limits.MaxReviewSidecarBytes) - len(prefix) - len(suffix) - 16
	sidecar := []byte(prefix + strings.Repeat("x", paddingSize) + suffix)
	if int64(len(sidecar)) >= limits.MaxReviewSidecarBytes {
		t.Fatalf("fixture size = %d", len(sidecar))
	}
	writeFile(t, root, "README.md.review.json", sidecar, 0o640)
	store := deterministicStore(t, openFilesystem(t, root))

	_, err := store.CreateThread(context.Background(), CreateThreadInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: Revision(markdown),
		Anchor:                   Anchor{Type: AnchorDocument},
		MessageBody:              "This pushes the result over the limit.",
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("CreateThread error = %v, want ErrTooLarge", err)
	}
	result, readErr := os.ReadFile(filepath.Join(root, "README.md.review.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	assertBytesEqual(t, "sidecar after oversized mutation", sidecar, result)
}

func TestRandomIDUsesTypePrefixAnd128RandomBits(t *testing.T) {
	for _, prefix := range []string{threadIDPrefix, messageIDPrefix} {
		first, err := randomID(prefix)
		if err != nil {
			t.Fatal(err)
		}
		second, err := randomID(prefix)
		if err != nil {
			t.Fatal(err)
		}
		if first == second || !strings.HasPrefix(first, prefix) {
			t.Fatalf("IDs = %q, %q for prefix %q", first, second, prefix)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(first, prefix))
		if err != nil {
			t.Fatalf("decode ID %q: %v", first, err)
		}
		if len(decoded) != 16 {
			t.Fatalf("random ID bytes = %d, want 16", len(decoded))
		}
	}
}

type fakeGateway struct {
	mu     sync.Mutex
	files  map[string][]byte
	mutate func(
		ctx context.Context,
		relativePath string,
		options filesystem.MutationOptions,
		callback filesystem.MutationCallback,
	) ([]byte, filesystem.Durability, error)
}

func (fake *fakeGateway) ReadFile(relativePath string, maxBytes int64) ([]byte, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	data, exists := fake.files[relativePath]
	if !exists {
		return nil, os.ErrNotExist
	}
	if int64(len(data)) > maxBytes {
		return nil, filesystem.ErrTooLarge
	}
	return cloneBytes(data), nil
}

func (fake *fakeGateway) MutateFile(
	ctx context.Context,
	relativePath string,
	options filesystem.MutationOptions,
	callback filesystem.MutationCallback,
) ([]byte, filesystem.Durability, error) {
	if fake.mutate != nil {
		return fake.mutate(ctx, relativePath, options, callback)
	}
	fake.mu.Lock()
	current, exists := fake.files[relativePath]
	current = cloneBytes(current)
	fake.mu.Unlock()
	updated, err := callback(cloneBytes(current), exists)
	if err != nil {
		return nil, filesystem.DurabilityUnknown, err
	}
	fake.mu.Lock()
	fake.files[relativePath] = cloneBytes(updated)
	fake.mu.Unlock()
	return updated, filesystem.DurabilityDurable, nil
}

func deterministicStore(t *testing.T, gateway *filesystem.FS) *Store {
	t.Helper()
	store, err := NewStore(gateway, deterministicStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func deterministicStoreWithGateway(t *testing.T, gateway gateway) *Store {
	t.Helper()
	store, err := newStore(gateway, deterministicStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func deterministicStoreOptions() StoreOptions {
	var sequence atomic.Uint64
	return StoreOptions{
		Now: func() time.Time {
			return time.Date(2026, 7, 28, 14, 30, 0, 0, time.UTC)
		},
		NewID: func(prefix string) (string, error) {
			return fmt.Sprintf("%s%022d", prefix, sequence.Add(1)), nil
		},
	}
}

func openFilesystem(t *testing.T, root string) *filesystem.FS {
	t.Helper()
	gateway, err := filesystem.Open(root, filesystem.Auto)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gateway.Close(); err != nil {
			t.Error(err)
		}
	})
	return gateway
}

func writeFile(
	t *testing.T,
	root string,
	relativePath string,
	data []byte,
	permissions os.FileMode,
) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, permissions); err != nil {
		t.Fatal(err)
	}
}

func setUmask(t *testing.T, mask int) int {
	t.Helper()
	return unix.Umask(mask)
}
