//go:build linux

package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"mdreview.dev/mdreview/internal/filesystem"
)

func TestStoreReadCalculatesExactRawTargetFingerprints(t *testing.T) {
	root := t.TempDir()
	markdown := []byte("# title\n")
	writeFile(t, root, "README.md", markdown, 0o644)
	sidecar := []byte(`{
  "schemaVersion": 1,
  "threads": [
    { "id": "thread /opaque?", "anchor": {"type":"document"}, "status": "open",
      "messages": [
        { "id": "message #opaque", "author": {"type":"human","name":"Reviewer"},
          "body": "Original.", "createdAt": "2026-07-28T12:00:00Z" }
      ],
      "future": 9007199254740993123456789 }
  ]
}`)
	writeFile(t, root, "README.md.review.json", sidecar, 0o640)

	snapshot, err := deterministicStore(t, openFilesystem(t, root)).
		Read(context.Background(), "README.md")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseJSON(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	threadNode := mustChild(t, parsed, "threads").items[0]
	messageNode := mustChild(t, threadNode, "messages").items[0]
	if snapshot.Targets.Threads["thread /opaque?"] != Revision(threadNode.raw) {
		t.Fatalf("thread fingerprint = %q, want exact raw object hash", snapshot.Targets.Threads["thread /opaque?"])
	}
	if snapshot.Targets.Messages["message #opaque"] != Revision(messageNode.raw) {
		t.Fatalf("message fingerprint = %q, want exact raw object hash", snapshot.Targets.Messages["message #opaque"])
	}
	if bytes.Contains(sidecar, []byte(`"targets"`)) {
		t.Fatal("transport fingerprints appeared in persisted fixture")
	}
}

func TestStoreReplyLifecycleAndExactResponseFingerprints(t *testing.T) {
	tests := []struct {
		status ThreadStatus
		want   ThreadStatus
	}{
		{StatusOpen, StatusOpen},
		{StatusHandled, StatusOpen},
		{StatusResolved, StatusOpen},
	}
	for _, test := range tests {
		t.Run(string(test.status), func(t *testing.T) {
			root, store, snapshot := setupM3Store(
				t,
				m3Sidecar(m3Thread(
					"thread_existing",
					test.status,
					m3Message("message_existing", "human", "Original."),
				)),
			)
			result, err := store.Reply(context.Background(), ReplyInput{
				DocumentPath:             "README.md",
				ExpectedDocumentRevision: snapshot.DocumentRevision,
				ExpectedReviewRevision:   *snapshot.ReviewRevision,
				ThreadID:                 "thread_existing",
				TargetFingerprint:        snapshot.Targets.Threads["thread_existing"],
				MessageBody:              "Human reply.",
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Thread.Status != test.want || len(result.Thread.Messages) != 2 {
				t.Fatalf("reply result = %+v", result.Thread)
			}
			reply := result.Thread.Messages[1]
			if reply.Author != (Author{Type: "human", Name: "Reviewer"}) ||
				reply.Body != "Human reply." ||
				reply.CreatedAt != "2026-07-28T14:30:00Z" {
				t.Fatalf("reply = %+v", reply)
			}
			assertResultTargetsMatchEmittedThread(
				t,
				fileBytes(t, filepath.Join(root, "README.md.review.json")),
				result.Thread.ID,
				result.Targets,
			)
			if bytes.Contains(
				fileBytes(t, filepath.Join(root, "README.md.review.json")),
				[]byte(`"targets"`),
			) {
				t.Fatal("transport target fingerprints were persisted")
			}
		})
	}
}

func TestStoreChangeStatusTransitionMatrix(t *testing.T) {
	statuses := []ThreadStatus{StatusOpen, StatusHandled, StatusResolved}
	for _, current := range statuses {
		for _, requested := range statuses {
			name := fmt.Sprintf("%s_to_%s", current, requested)
			t.Run(name, func(t *testing.T) {
				root, store, snapshot := setupM3Store(
					t,
					m3Sidecar(m3Thread(
						"thread_existing",
						current,
						m3Message("message_existing", "human", "Original."),
					)),
				)
				before := fileBytes(t, filepath.Join(root, "README.md.review.json"))
				result, err := store.ChangeStatus(context.Background(), ChangeStatusInput{
					DocumentPath:             "README.md",
					ExpectedDocumentRevision: snapshot.DocumentRevision,
					ExpectedReviewRevision:   *snapshot.ReviewRevision,
					ThreadID:                 "thread_existing",
					TargetFingerprint:        snapshot.Targets.Threads["thread_existing"],
					Status:                   requested,
				})
				allowed := (requested == StatusResolved &&
					(current == StatusOpen || current == StatusHandled)) ||
					(requested == StatusOpen && current == StatusResolved)
				if !allowed {
					if !errors.Is(err, ErrInvalidOperation) {
						t.Fatalf("ChangeStatus error = %v, want ErrInvalidOperation", err)
					}
					assertBytesEqual(
						t,
						"sidecar after rejected transition",
						before,
						fileBytes(t, filepath.Join(root, "README.md.review.json")),
					)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if result.Thread.Status != requested {
					t.Fatalf("status = %q, want %q", result.Thread.Status, requested)
				}
			})
		}
	}
}

func TestStoreEditMessageEnforcesCurrentAuthorAndPreservesIdentity(t *testing.T) {
	tests := []struct {
		authorType string
		allowed    bool
	}{
		{"human", true},
		{"agent", false},
	}
	for _, test := range tests {
		t.Run(test.authorType, func(t *testing.T) {
			authorName := "Codex"
			if test.authorType == "human" {
				authorName = "External Reviewer"
			}
			root, store, snapshot := setupM3Store(
				t,
				m3Sidecar(m3Thread(
					"thread_existing",
					StatusHandled,
					m3MessageNamed(
						"message_existing",
						test.authorType,
						authorName,
						"Original.",
					),
				)),
			)
			before := fileBytes(t, filepath.Join(root, "README.md.review.json"))
			result, err := store.EditMessage(context.Background(), EditMessageInput{
				DocumentPath:             "README.md",
				ExpectedDocumentRevision: snapshot.DocumentRevision,
				ExpectedReviewRevision:   *snapshot.ReviewRevision,
				MessageID:                "message_existing",
				TargetFingerprint:        snapshot.Targets.Messages["message_existing"],
				MessageBody:              "Edited body.",
			})
			if !test.allowed {
				if !errors.Is(err, ErrInvalidOperation) {
					t.Fatalf("EditMessage error = %v, want ErrInvalidOperation", err)
				}
				assertBytesEqual(
					t,
					"sidecar after rejected agent edit",
					before,
					fileBytes(t, filepath.Join(root, "README.md.review.json")),
				)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			message := result.Thread.Messages[0]
			if message.ID != "message_existing" ||
				message.Author.Type != "human" ||
				message.Author.Name != "External Reviewer" ||
				message.CreatedAt != "2026-07-28T12:00:00Z" ||
				message.EditedAt == nil ||
				*message.EditedAt != "2026-07-28T14:30:00Z" ||
				message.Body != "Edited body." ||
				result.Thread.Status != StatusHandled {
				t.Fatalf("edited result = %+v", result.Thread)
			}
		})
	}
}

func TestStoreDeleteThreadRequiresExactlyOneMessage(t *testing.T) {
	tests := []struct {
		name     string
		messages string
		allowed  bool
	}{
		{
			name:     "unreplied human thread",
			messages: m3Message("message_existing", "human", "Original."),
			allowed:  true,
		},
		{
			name:     "structurally valid direct-agent thread",
			messages: m3Message("message_existing", "agent", "Direct."),
			allowed:  true,
		},
		{
			name: "replied thread",
			messages: m3Message("message_existing", "human", "Original.") + "," +
				m3Message("message_reply", "agent", "Changed."),
			allowed: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, store, snapshot := setupM3Store(
				t,
				m3Sidecar(m3Thread("thread_existing", StatusOpen, test.messages)),
			)
			before := fileBytes(t, filepath.Join(root, "README.md.review.json"))
			result, err := store.DeleteThread(context.Background(), DeleteThreadInput{
				DocumentPath:             "README.md",
				ExpectedDocumentRevision: snapshot.DocumentRevision,
				ExpectedReviewRevision:   *snapshot.ReviewRevision,
				ThreadID:                 "thread_existing",
				TargetFingerprint:        snapshot.Targets.Threads["thread_existing"],
			})
			if !test.allowed {
				if !errors.Is(err, ErrInvalidOperation) {
					t.Fatalf("DeleteThread error = %v, want ErrInvalidOperation", err)
				}
				assertBytesEqual(
					t,
					"sidecar after rejected deletion",
					before,
					fileBytes(t, filepath.Join(root, "README.md.review.json")),
				)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.DeletedThreadID != "thread_existing" {
				t.Fatalf("deleted ID = %q", result.DeletedThreadID)
			}
			document, err := Decode(fileBytes(t, filepath.Join(root, "README.md.review.json")))
			if err != nil {
				t.Fatal(err)
			}
			if len(document.Threads()) != 0 {
				t.Fatalf("remaining threads = %+v", document.Threads())
			}
		})
	}
}

func TestStoreTargetConflictsIncludeCurrentExactState(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, root string)
		wantFP bool
	}{
		{
			name: "target changed",
			change: func(t *testing.T, root string) {
				writeFile(
					t,
					root,
					"README.md.review.json",
					[]byte(m3Sidecar(m3Thread(
						"thread_existing",
						StatusHandled,
						m3Message("message_existing", "human", "Original."),
					))),
					0o640,
				)
			},
			wantFP: true,
		},
		{
			name: "target formatting changed",
			change: func(t *testing.T, root string) {
				writeFile(
					t,
					root,
					"README.md.review.json",
					[]byte(`{"schemaVersion":1,"threads":[{
  "id":"thread_existing",
  "anchor":{"type":"document"},
  "status":"open",
  "messages":[{"id":"message_existing","author":{"type":"human","name":"Reviewer"},"body":"Original.","createdAt":"2026-07-28T12:00:00Z"}]
}]}`),
					0o640,
				)
			},
			wantFP: true,
		},
		{
			name: "target removed",
			change: func(t *testing.T, root string) {
				writeFile(t, root, "README.md.review.json", []byte(m3Sidecar("")), 0o640)
			},
			wantFP: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, store, snapshot := setupM3Store(
				t,
				m3Sidecar(m3Thread(
					"thread_existing",
					StatusOpen,
					m3Message("message_existing", "human", "Original."),
				)),
			)
			test.change(t, root)
			currentBytes := fileBytes(t, filepath.Join(root, "README.md.review.json"))
			_, err := store.Reply(context.Background(), ReplyInput{
				DocumentPath:             "README.md",
				ExpectedDocumentRevision: snapshot.DocumentRevision,
				ExpectedReviewRevision:   *snapshot.ReviewRevision,
				ThreadID:                 "thread_existing",
				TargetFingerprint:        snapshot.Targets.Threads["thread_existing"],
				MessageBody:              "Stale reply.",
			})
			if !errors.Is(err, ErrTargetChanged) {
				t.Fatalf("Reply error = %v, want ErrTargetChanged", err)
			}
			var conflict *TargetChangedError
			if !errors.As(err, &conflict) {
				t.Fatalf("Reply error type = %T, want *TargetChangedError", err)
			}
			if conflict.Current.DocumentRevision != snapshot.DocumentRevision ||
				conflict.Current.ReviewRevision == nil ||
				*conflict.Current.ReviewRevision != Revision(currentBytes) {
				t.Fatalf("conflict current = %+v", conflict.Current)
			}
			if (conflict.Current.TargetFingerprint != nil) != test.wantFP {
				t.Fatalf("target fingerprint = %v, want present %t", conflict.Current.TargetFingerprint, test.wantFP)
			}
			assertBytesEqual(
				t,
				"sidecar after target conflict",
				currentBytes,
				fileBytes(t, filepath.Join(root, "README.md.review.json")),
			)
		})
	}
}

func TestStoreMergesUnrelatedChangeAndPreservesUnknownLexemes(t *testing.T) {
	initial := `{"schemaVersion":1,"threads":[` +
		m3Thread(
			"thread_target",
			StatusOpen,
			m3MessageWithFuture("message_target", "human", "Original.", "1e+09"),
		) + `,` +
		m3Thread(
			"thread_other",
			StatusOpen,
			m3Message("message_other", "human", "Other."),
		) +
		`],"futureRoot":9007199254740993123456789}`
	root, store, snapshot := setupM3Store(t, initial)

	external := `{"schemaVersion":1,"threads":[` +
		m3Thread(
			"thread_target",
			StatusHandled,
			m3MessageWithFuture("message_target", "human", "Original.", "1e+09"),
		) + `,` +
		m3Thread(
			"thread_other",
			StatusHandled,
			m3Message("message_other", "human", "Externally changed."),
		) +
		`],"futureRoot":9007199254740993123456789}`
	writeFile(t, root, "README.md.review.json", []byte(external), 0o640)
	result, err := store.EditMessage(context.Background(), EditMessageInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: snapshot.DocumentRevision,
		ExpectedReviewRevision:   *snapshot.ReviewRevision,
		MessageID:                "message_target",
		TargetFingerprint:        snapshot.Targets.Messages["message_target"],
		MessageBody:              "Edited target.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Thread.Messages[0].Body != "Edited target." {
		t.Fatalf("target result = %+v", result.Thread)
	}
	if result.Thread.Status != StatusHandled {
		t.Fatalf("unrelated current thread status = %q, want handled", result.Thread.Status)
	}
	output := fileBytes(t, filepath.Join(root, "README.md.review.json"))
	if !bytes.Contains(output, []byte("Externally changed.")) ||
		!bytes.Contains(output, []byte("9007199254740993123456789")) ||
		!bytes.Contains(output, []byte("1e+09")) {
		t.Fatalf("unrelated or unknown data was not preserved:\n%s", output)
	}
}

func TestStoreLeavesStructurallyValidDirectWriterLifecycleAsWritten(t *testing.T) {
	root, store, snapshot := setupM3Store(
		t,
		m3Sidecar(m3Thread(
			"direct thread",
			StatusHandled,
			m3Message("direct agent message", "agent", "Marked handled without a reply."),
		)),
	)
	if snapshot.Threads[0].Status != StatusHandled ||
		snapshot.Threads[0].Messages[0].Author.Type != "agent" ||
		len(snapshot.Threads[0].Messages) != 1 {
		t.Fatalf("Read reinterpreted direct data: %+v", snapshot.Threads[0])
	}
	before := fileBytes(t, filepath.Join(root, "README.md.review.json"))
	if _, err := store.EditMessage(context.Background(), EditMessageInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: snapshot.DocumentRevision,
		ExpectedReviewRevision:   *snapshot.ReviewRevision,
		MessageID:                "direct agent message",
		TargetFingerprint:        snapshot.Targets.Messages["direct agent message"],
		MessageBody:              "Browser rewrite.",
	}); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("EditMessage error = %v, want ErrInvalidOperation", err)
	}
	assertBytesEqual(
		t,
		"direct data after rejected agent edit",
		before,
		fileBytes(t, filepath.Join(root, "README.md.review.json")),
	)

	resolved, err := store.ChangeStatus(context.Background(), ChangeStatusInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: snapshot.DocumentRevision,
		ExpectedReviewRevision:   *snapshot.ReviewRevision,
		ThreadID:                 "direct thread",
		TargetFingerprint:        snapshot.Targets.Threads["direct thread"],
		Status:                   StatusResolved,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Thread.Status != StatusResolved ||
		len(resolved.Thread.Messages) != 1 ||
		resolved.Thread.Messages[0].Author.Type != "agent" {
		t.Fatalf("status operation repaired direct data: %+v", resolved.Thread)
	}
}

func TestStoreWorkflowMutationsIgnoreDocumentChangesAndKeepAttachmentIndependent(t *testing.T) {
	sidecar := `{"schemaVersion":1,"threads":[{"id":"thread_text",` +
		`"anchor":{"type":"text","range":{"start":2,"end":7},"source":"title","text":"title"},` +
		`"status":"open","messages":[` +
		m3Message("message_existing", "human", "Original.") + `]}]}`
	root, store, snapshot := setupM3Store(t, sidecar)
	writeFile(t, root, "README.md", []byte("# replaced\n"), 0o644)

	result, err := store.ChangeStatus(context.Background(), ChangeStatusInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: snapshot.DocumentRevision,
		ExpectedReviewRevision:   *snapshot.ReviewRevision,
		ThreadID:                 "thread_text",
		TargetFingerprint:        snapshot.Targets.Threads["thread_text"],
		Status:                   StatusResolved,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DocumentRevision != Revision([]byte("# replaced\n")) ||
		result.Thread.Status != StatusResolved ||
		result.Thread.Attachment.State != AttachmentDetached {
		t.Fatalf("changed-document result = %+v", result)
	}
	document, err := Decode(fileBytes(t, filepath.Join(root, "README.md.review.json")))
	if err != nil {
		t.Fatal(err)
	}
	if document.Threads()[0].Anchor.Source != "title" {
		t.Fatal("status operation changed the immutable anchor")
	}
}

func TestStoreMessageTargetConflictUsesCurrentMessageFingerprint(t *testing.T) {
	root, store, snapshot := setupM3Store(
		t,
		m3Sidecar(m3Thread(
			"thread_existing",
			StatusOpen,
			m3Message("message_existing", "human", "Original."),
		)),
	)
	changed := m3Sidecar(m3Thread(
		"thread_existing",
		StatusOpen,
		m3Message("message_existing", "human", "Externally edited."),
	))
	writeFile(t, root, "README.md.review.json", []byte(changed), 0o640)
	currentDocument, err := Decode([]byte(changed))
	if err != nil {
		t.Fatal(err)
	}
	currentFingerprint, _, ok := currentDocument.messageFingerprint("message_existing")
	if !ok {
		t.Fatal("changed fixture message is missing")
	}

	_, err = store.EditMessage(context.Background(), EditMessageInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: snapshot.DocumentRevision,
		ExpectedReviewRevision:   *snapshot.ReviewRevision,
		MessageID:                "message_existing",
		TargetFingerprint:        snapshot.Targets.Messages["message_existing"],
		MessageBody:              "Browser edit.",
	})
	var conflict *TargetChangedError
	if !errors.As(err, &conflict) ||
		conflict.Current.TargetFingerprint == nil ||
		*conflict.Current.TargetFingerprint != currentFingerprint {
		t.Fatalf("message conflict = %#v, error %v", conflict, err)
	}
}

func TestStoreCreateResponseTargetsMatchExactEmittedBytes(t *testing.T) {
	root := t.TempDir()
	markdown := []byte("# title\n")
	writeFile(t, root, "README.md", markdown, 0o644)
	store := deterministicStore(t, openFilesystem(t, root))
	result, err := store.CreateThread(context.Background(), CreateThreadInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: Revision(markdown),
		Anchor:                   Anchor{Type: AnchorDocument},
		MessageBody:              "Initial.",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertResultTargetsMatchEmittedThread(
		t,
		fileBytes(t, filepath.Join(root, "README.md.review.json")),
		result.Thread.ID,
		result.Targets,
	)
}

func TestStoreScopesOpaqueSameIDsToDerivedSidecars(t *testing.T) {
	root := t.TempDir()
	store := deterministicStore(t, openFilesystem(t, root))
	threadID := "thread /?#[] opaque"
	messageID := "message /?#[] opaque"
	for _, path := range []string{"first.md", "second.md"} {
		writeFile(t, root, path, []byte(path), 0o644)
		writeFile(
			t,
			root,
			path+".review.json",
			[]byte(m3Sidecar(m3Thread(
				threadID,
				StatusOpen,
				m3Message(messageID, "human", path),
			))),
			0o640,
		)
	}

	for _, path := range []string{"first.md", "second.md"} {
		snapshot, err := store.Read(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		result, err := store.ChangeStatus(context.Background(), ChangeStatusInput{
			DocumentPath:             path,
			ExpectedDocumentRevision: snapshot.DocumentRevision,
			ExpectedReviewRevision:   *snapshot.ReviewRevision,
			ThreadID:                 threadID,
			TargetFingerprint:        snapshot.Targets.Threads[threadID],
			Status:                   StatusResolved,
		})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if result.Thread.Messages[0].Body != path {
			t.Fatalf("%s returned wrong sidecar thread: %+v", path, result.Thread)
		}
	}
}

func TestStoreConcurrentUnrelatedTargetsMergeAndSameTargetConflicts(t *testing.T) {
	t.Run("unrelated targets", func(t *testing.T) {
		root, store, snapshot := setupM3Store(
			t,
			m3Sidecar(
				m3Thread(
					"thread_one",
					StatusOpen,
					m3Message("message_one", "human", "One."),
				)+","+
					m3Thread(
						"thread_two",
						StatusOpen,
						m3Message("message_two", "human", "Two."),
					),
			),
		)
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, threadID := range []string{"thread_one", "thread_two"} {
			go func(id string) {
				<-start
				_, err := store.Reply(context.Background(), ReplyInput{
					DocumentPath:             "README.md",
					ExpectedDocumentRevision: snapshot.DocumentRevision,
					ExpectedReviewRevision:   *snapshot.ReviewRevision,
					ThreadID:                 id,
					TargetFingerprint:        snapshot.Targets.Threads[id],
					MessageBody:              "Reply to " + id,
				})
				results <- err
			}(threadID)
		}
		close(start)
		for count := 0; count < 2; count++ {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		document, err := Decode(fileBytes(t, filepath.Join(root, "README.md.review.json")))
		if err != nil {
			t.Fatal(err)
		}
		for _, thread := range document.Threads() {
			if len(thread.Messages) != 2 {
				t.Fatalf("thread %q messages = %d, want 2", thread.ID, len(thread.Messages))
			}
		}
	})

	t.Run("same target", func(t *testing.T) {
		_, store, snapshot := setupM3Store(
			t,
			m3Sidecar(m3Thread(
				"thread_existing",
				StatusOpen,
				m3Message("message_existing", "human", "Original."),
			)),
		)
		start := make(chan struct{})
		results := make(chan error, 2)
		for worker := 0; worker < 2; worker++ {
			go func(worker int) {
				<-start
				_, err := store.Reply(context.Background(), ReplyInput{
					DocumentPath:             "README.md",
					ExpectedDocumentRevision: snapshot.DocumentRevision,
					ExpectedReviewRevision:   *snapshot.ReviewRevision,
					ThreadID:                 "thread_existing",
					TargetFingerprint:        snapshot.Targets.Threads["thread_existing"],
					MessageBody:              fmt.Sprintf("Reply %d", worker),
				})
				results <- err
			}(worker)
		}
		close(start)
		var successes, conflicts int
		for count := 0; count < 2; count++ {
			err := <-results
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrTargetChanged):
				conflicts++
			default:
				t.Fatalf("unexpected concurrent result: %v", err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("successes = %d, conflicts = %d", successes, conflicts)
		}
	})
}

func TestStoreM3SurfacesMutationContention(t *testing.T) {
	markdown := []byte("# title\n")
	sidecar := []byte(m3Sidecar(m3Thread(
		"thread_existing",
		StatusOpen,
		m3Message("message_existing", "human", "Original."),
	)))
	document, err := Decode(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, _ := document.threadFingerprint("thread_existing")
	input := ChangeStatusInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: Revision(markdown),
		ExpectedReviewRevision:   Revision(sidecar),
		ThreadID:                 "thread_existing",
		TargetFingerprint:        fingerprint,
		Status:                   StatusResolved,
	}

	t.Run("bounded contention", func(t *testing.T) {
		fake := &fakeGateway{
			files: map[string][]byte{
				"README.md":             markdown,
				"README.md.review.json": sidecar,
			},
			mutate: func(
				context.Context,
				string,
				filesystem.MutationOptions,
				filesystem.MutationCallback,
			) ([]byte, error) {
				return nil, filesystem.ErrMutationConflict
			},
		}
		_, err := deterministicStoreWithGateway(t, fake).
			ChangeStatus(context.Background(), input)
		if !errors.Is(err, ErrReviewChanged) {
			t.Fatalf("ChangeStatus error = %v, want ErrReviewChanged", err)
		}
	})
}

func TestStoreM3SemanticRetriesUseLatestTargetAndUnrelatedData(t *testing.T) {
	markdown := []byte("# title\n")
	initial := []byte(m3Sidecar(
		m3Thread(
			"thread_target",
			StatusOpen,
			m3Message("message_target", "human", "Target."),
		) + "," +
			m3Thread(
				"thread_other",
				StatusOpen,
				m3Message("message_other", "human", "Other."),
			),
	))
	decoded, err := Decode(initial)
	if err != nil {
		t.Fatal(err)
	}
	targetFingerprint, _ := decoded.threadFingerprint("thread_target")
	input := ReplyInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: Revision(markdown),
		ExpectedReviewRevision:   Revision(initial),
		ThreadID:                 "thread_target",
		TargetFingerprint:        targetFingerprint,
		MessageBody:              "Retried reply.",
	}
	tests := []struct {
		name       string
		second     []byte
		wantTarget bool
	}{
		{
			name: "unrelated external change",
			second: []byte(m3Sidecar(
				m3Thread(
					"thread_target",
					StatusOpen,
					m3Message("message_target", "human", "Target."),
				) + "," +
					m3Thread(
						"thread_other",
						StatusHandled,
						m3Message("message_other", "human", "Externally changed."),
					),
			)),
			wantTarget: false,
		},
		{
			name: "target external change",
			second: []byte(m3Sidecar(
				m3Thread(
					"thread_target",
					StatusHandled,
					m3Message("message_target", "human", "Target."),
				) + "," +
					m3Thread(
						"thread_other",
						StatusOpen,
						m3Message("message_other", "human", "Other."),
					),
			)),
			wantTarget: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var emitted []byte
			fake := &fakeGateway{
				files: map[string][]byte{
					"README.md":             markdown,
					"README.md.review.json": test.second,
				},
				mutate: func(
					ctx context.Context,
					relativePath string,
					options filesystem.MutationOptions,
					callback filesystem.MutationCallback,
				) ([]byte, error) {
					if _, firstErr := callback(initial, true); firstErr != nil {
						return nil, firstErr
					}
					updated, secondErr := callback(test.second, true)
					emitted = cloneBytes(updated)
					return updated, secondErr
				},
			}
			result, err := deterministicStoreWithGateway(t, fake).
				Reply(context.Background(), input)
			if test.wantTarget {
				if !errors.Is(err, ErrTargetChanged) {
					t.Fatalf("Reply error = %v, want ErrTargetChanged", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Thread.Messages) != 2 {
				t.Fatalf("target messages = %d, want 2", len(result.Thread.Messages))
			}
			if !bytes.Contains(emitted, []byte("Externally changed.")) {
				t.Fatalf("retried mutation lost unrelated latest bytes:\n%s", emitted)
			}
		})
	}
}

func TestPublishedM3ContractFixturesDecodeIntoReviewCoreTypes(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "web", "src", "testdata", "contracts", "m3")
	var snapshot Snapshot
	decodeFixture(t, filepath.Join(fixtureRoot, "review.json"), &snapshot)
	if snapshot.Path != "README.md" ||
		len(snapshot.Targets.Threads) != 1 ||
		len(snapshot.Targets.Messages) != 1 {
		t.Fatalf("M3 snapshot fixture = %+v", snapshot)
	}

	var mutation MutationResult
	decodeFixture(t, filepath.Join(fixtureRoot, "mutation-response.json"), &mutation)
	if mutation.Thread.ID != "thread_existing" ||
		len(mutation.Targets.Threads) != 1 ||
		len(mutation.Targets.Messages) != 2 {
		t.Fatalf("M3 mutation fixture = %+v", mutation)
	}

	var deletion DeleteThreadResult
	decodeFixture(t, filepath.Join(fixtureRoot, "delete-response.json"), &deletion)
	if deletion.DeletedThreadID != "thread_existing" {
		t.Fatalf("M3 delete fixture = %+v", deletion)
	}
}

func TestStoreM3RejectsInvalidInputsBeforeMutation(t *testing.T) {
	_, store, snapshot := setupM3Store(
		t,
		m3Sidecar(m3Thread(
			"thread_existing",
			StatusOpen,
			m3Message("message_existing", "human", "Original."),
		)),
	)
	valid := ReplyInput{
		DocumentPath:             "README.md",
		ExpectedDocumentRevision: snapshot.DocumentRevision,
		ExpectedReviewRevision:   *snapshot.ReviewRevision,
		ThreadID:                 "thread_existing",
		TargetFingerprint:        snapshot.Targets.Threads["thread_existing"],
		MessageBody:              "Reply.",
	}
	tests := []struct {
		name   string
		mutate func(*ReplyInput)
	}{
		{"invalid path", func(input *ReplyInput) { input.DocumentPath = "../README.md" }},
		{"invalid document revision", func(input *ReplyInput) { input.ExpectedDocumentRevision = "no" }},
		{"invalid review revision", func(input *ReplyInput) { input.ExpectedReviewRevision = "no" }},
		{"empty target ID", func(input *ReplyInput) { input.ThreadID = "" }},
		{"invalid target fingerprint", func(input *ReplyInput) { input.TargetFingerprint = "no" }},
		{"empty body", func(input *ReplyInput) { input.MessageBody = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if _, err := store.Reply(context.Background(), input); !errors.Is(err, ErrInvalidOperation) {
				t.Fatalf("Reply error = %v, want ErrInvalidOperation", err)
			}
		})
	}
}

func setupM3Store(t *testing.T, sidecar string) (string, *Store, Snapshot) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "README.md", []byte("# title\n"), 0o644)
	writeFile(t, root, "README.md.review.json", []byte(sidecar), 0o640)
	store := deterministicStore(t, openFilesystem(t, root))
	snapshot, err := store.Read(context.Background(), "README.md")
	if err != nil {
		t.Fatal(err)
	}
	return root, store, snapshot
}

func m3Sidecar(threads string) string {
	return `{"schemaVersion":1,"threads":[` + threads + `]}`
}

func m3Thread(id string, status ThreadStatus, messages string) string {
	return `{"id":` + quotedTestString(id) +
		`,"anchor":{"type":"document"},"status":` + quotedTestString(string(status)) +
		`,"messages":[` + messages + `]}`
}

func m3Message(id, authorType, body string) string {
	return m3MessageNamed(id, authorType, "Reviewer", body)
}

func m3MessageNamed(id, authorType, authorName, body string) string {
	return `{"id":` + quotedTestString(id) +
		`,"author":{"type":` + quotedTestString(authorType) +
		`,"name":` + quotedTestString(authorName) + `},` +
		`"body":` + quotedTestString(body) +
		`,"createdAt":"2026-07-28T12:00:00Z"}`
}

func m3MessageWithFuture(id, authorType, body, futureNumber string) string {
	return `{"id":` + quotedTestString(id) +
		`,"author":{"type":` + quotedTestString(authorType) + `,"name":"Reviewer"},` +
		`"body":` + quotedTestString(body) +
		`,"createdAt":"2026-07-28T12:00:00Z","futureNumber":` + futureNumber + `}`
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeFixture(t *testing.T, path string, target any) {
	t.Helper()
	data := fileBytes(t, path)
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func assertResultTargetsMatchEmittedThread(
	t *testing.T,
	emitted []byte,
	threadID string,
	actual TargetFingerprints,
) {
	t.Helper()
	document, err := Decode(emitted)
	if err != nil {
		t.Fatal(err)
	}
	expected, ok := document.targetsForThread(threadID)
	if !ok {
		t.Fatalf("emitted thread %q is missing", threadID)
	}
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	actualJSON, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expectedJSON, actualJSON) {
		t.Fatalf("targets = %s, want %s", actualJSON, expectedJSON)
	}
}
