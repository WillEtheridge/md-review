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
	if textResult.DocumentRevision != Revision([]byte("# mdReview\n")) ||
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

	currentReviewRevision := textResult.ReviewRevision
	documentResult, err := store.CreateThread(context.Background(), CreateThreadInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: textResult.DocumentRevision,
		ExpectedReviewRevision:   &currentReviewRevision,
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
		t.Fatalf("thread count = %d, want 2", len(document.Threads()))
	}
	if documentResult.ReviewRevision != Revision(data) {
		t.Fatalf("result review revision does not match exact sidecar bytes")
	}
}

func TestStoreRejectsAllStaleDocumentRevisions(t *testing.T) {
	root := t.TempDir()
	current := []byte("moved target here")
	writeFile(t, root, "README.md", current, 0o644)
	store := deterministicStore(t, openFilesystem(t, root))
	_, err := store.CreateThread(context.Background(), CreateThreadInput{
		DocumentPath: "README.md", ExpectedDocumentRevision: Revision([]byte("old target")),
		Anchor: Anchor{Type: AnchorDocument}, MessageBody: "Review this.",
	})
	if !errors.Is(err, ErrDocumentChanged) {
		t.Fatalf("CreateThread error = %v", err)
	}
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.Current.DocumentRevision != Revision(current) {
		t.Fatalf("conflict = %+v", conflict)
	}
	if _, statErr := os.Stat(filepath.Join(root, "README.md.review.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("conflicted creation wrote a sidecar: %v", statErr)
	}
}

func TestStoreWorkflowMutationsRequireWholeRevisions(t *testing.T) {
	root := t.TempDir()
	markdown := []byte("# title\n")
	sidecar := []byte(validSidecar(`{"id":"thread_existing","anchor":{"type":"document"},"status":"open","messages":[` + validMessage("message_existing") + `]}`))
	writeFile(t, root, "README.md", markdown, 0o644)
	writeFile(t, root, "README.md.review.json", sidecar, 0o640)
	store := deterministicStore(t, openFilesystem(t, root))
	documentRevision, reviewRevision := Revision(markdown), Revision(sidecar)
	if _, err := store.Reply(context.Background(), ReplyInput{DocumentPath: "README.md", ExpectedDocumentRevision: documentRevision, ExpectedReviewRevision: reviewRevision, ThreadID: "thread_existing", MessageBody: "Reply."}); err != nil {
		t.Fatalf("Reply() = %v", err)
	}

	for name, mutate := range map[string]func() error{
		"reply": func() error {
			_, err := store.Reply(context.Background(), ReplyInput{DocumentPath: "README.md", ExpectedDocumentRevision: documentRevision, ExpectedReviewRevision: reviewRevision, ThreadID: "thread_existing", MessageBody: "Reply again."})
			return err
		},
		"edit": func() error {
			_, err := store.EditMessage(context.Background(), EditMessageInput{DocumentPath: "README.md", ExpectedDocumentRevision: documentRevision, ExpectedReviewRevision: reviewRevision, MessageID: "message_existing", MessageBody: "Edited."})
			return err
		},
		"status": func() error {
			_, err := store.ChangeStatus(context.Background(), ChangeStatusInput{DocumentPath: "README.md", ExpectedDocumentRevision: documentRevision, ExpectedReviewRevision: reviewRevision, ThreadID: "thread_existing", Status: StatusResolved})
			return err
		},
		"delete": func() error {
			_, err := store.DeleteThread(context.Background(), DeleteThreadInput{DocumentPath: "README.md", ExpectedDocumentRevision: documentRevision, ExpectedReviewRevision: reviewRevision, ThreadID: "thread_existing"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := mutate(); !errors.Is(err, ErrReviewChanged) {
				t.Fatalf("mutation error = %v, want ErrReviewChanged", err)
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

func TestStoreConcurrentSameSidecarCreationsConflict(t *testing.T) {
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
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrReviewChanged) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful creations = %d, want 1", successes)
	}
	data, err := os.ReadFile(filepath.Join(root, "README.md.review.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Threads()) != 1 {
		t.Fatalf("thread count = %d, want 1", len(document.Threads()))
	}
}

func TestStoreSerializesAllMutations(t *testing.T) {
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
		) ([]byte, error) {
			entered <- relativePath
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			updated, err := callback(nil, false)
			return updated, err
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
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first mutation did not enter")
	}
	select {
	case <-entered:
		t.Fatal("mutations were not globally serialized")
	case <-time.After(50 * time.Millisecond):
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
		ExpectedReviewRevision:   ptr(Revision(fixture)),
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
				) ([]byte, error) {
					return nil, test.mutateErr
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
			reviewRevision := Revision(before)
			store := deterministicStore(t, openFilesystem(t, root))
			_, err := store.CreateThread(context.Background(), CreateThreadInput{
				DocumentPath:             "README.md",
				ExpectedDocumentRevision: Revision(markdown),
				ExpectedReviewRevision:   &reviewRevision,
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
	reviewRevision := Revision(sidecar)

	_, err := store.CreateThread(context.Background(), CreateThreadInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: Revision(markdown),
		ExpectedReviewRevision:   &reviewRevision,
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
	) ([]byte, error)
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
) ([]byte, error) {
	if fake.mutate != nil {
		return fake.mutate(ctx, relativePath, options, callback)
	}
	fake.mu.Lock()
	current, exists := fake.files[relativePath]
	current = cloneBytes(current)
	fake.mu.Unlock()
	updated, err := callback(cloneBytes(current), exists)
	if err != nil {
		return nil, err
	}
	fake.mu.Lock()
	fake.files[relativePath] = cloneBytes(updated)
	fake.mu.Unlock()
	return updated, nil
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

func ptr(value string) *string { return &value }

func openFilesystem(t *testing.T, root string) *filesystem.FS {
	t.Helper()
	gateway, err := filesystem.Open(root)
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
