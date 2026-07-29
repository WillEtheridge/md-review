package sidecar

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeStoreFixture(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "document.md.review.json")
	if err := os.WriteFile(path, losslessFixture(t), 0o640); err != nil {
		t.Fatal(err)
	}
	return path
}

func fingerprint(t *testing.T, data []byte, threadID string) string {
	t.Helper()
	document, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	result, err := document.ThreadFingerprint(threadID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCoordinatedBrowserMutationsMergeDifferentThreads(t *testing.T) {
	directory := t.TempDir()
	path := writeStoreFixture(t, directory)
	initial := losslessFixture(t)
	fingerprintA := fingerprint(t, initial, "thread_a")
	fingerprintB := fingerprint(t, initial, "thread_b")
	store := &Store{}

	var wait sync.WaitGroup
	wait.Add(2)
	errorsFromWorkers := make(chan error, 2)
	go func() {
		defer wait.Done()
		_, err := store.Mutate(path, fingerprintA, SetStatusOperation{
			ThreadID: "thread_a",
			Status:   "handled",
		})
		errorsFromWorkers <- err
	}()
	go func() {
		defer wait.Done()
		_, err := store.Mutate(path, fingerprintB, SetStatusOperation{
			ThreadID: "thread_b",
			Status:   "resolved",
		})
		errorsFromWorkers <- err
	}()
	wait.Wait()
	close(errorsFromWorkers)
	for err := range errorsFromWorkers {
		if err != nil {
			t.Fatal(err)
		}
	}

	result, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertThreadStatus(t, result, "thread_a", "handled")
	assertThreadStatus(t, result, "thread_b", "resolved")
	if !bytes.Contains(result, []byte("9007199254740993123456789")) {
		t.Fatal("unknown large integer was lost")
	}
}

func TestCoordinatedBrowserMutationsConflictOnSameThread(t *testing.T) {
	directory := t.TempDir()
	path := writeStoreFixture(t, directory)
	initial := losslessFixture(t)
	fingerprintA := fingerprint(t, initial, "thread_a")
	store := &Store{}

	var wait sync.WaitGroup
	wait.Add(2)
	errorsFromWorkers := make(chan error, 2)
	for _, status := range []string{"handled", "resolved"} {
		status := status
		go func() {
			defer wait.Done()
			_, err := store.Mutate(path, fingerprintA, SetStatusOperation{
				ThreadID: "thread_a",
				Status:   status,
			})
			errorsFromWorkers <- err
		}()
	}
	wait.Wait()
	close(errorsFromWorkers)

	successes := 0
	conflicts := 0
	for err := range errorsFromWorkers {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrTargetChanged):
			conflicts++
		default:
			t.Fatalf("unexpected mutation error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes = %d, conflicts = %d; want one of each", successes, conflicts)
	}
}

func TestExternalUnrelatedChangeIsObservedAndPreserved(t *testing.T) {
	directory := t.TempDir()
	path := writeStoreFixture(t, directory)
	initial := losslessFixture(t)
	fingerprintA := fingerprint(t, initial, "thread_a")
	attempts := 0
	store := &Store{Hooks: Hooks{
		BeforeFinalRead: func(attempt int) {
			attempts = attempt
			if attempt != 1 {
				return
			}
			external := bytes.Replace(
				initial,
				[]byte(`"id": "thread_b",
      "anchor": {"type":"document"},
      "status": "open"`),
				[]byte(`"id": "thread_b",
      "anchor": {"type":"document"},
      "status": "handled"`),
				1,
			)
			external = bytes.Replace(
				external,
				[]byte(`"object": {"b":2,"a":1}`),
				[]byte(`"object": {"b":2,"a":1,"agent":"kept"}`),
				1,
			)
			if err := os.WriteFile(path, external, 0o640); err != nil {
				panic(err)
			}
		},
	}}

	if _, err := store.Mutate(path, fingerprintA, SetStatusOperation{
		ThreadID: "thread_a",
		Status:   "handled",
	}); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("mutation completed after attempt %d, want 2", attempts)
	}
	result, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertThreadStatus(t, result, "thread_a", "handled")
	assertThreadStatus(t, result, "thread_b", "handled")
	if !bytes.Contains(result, []byte(`"agent":"kept"`)) {
		t.Fatal("external unknown field was not preserved")
	}
}

func TestExternalTargetChangeReturnsConflict(t *testing.T) {
	directory := t.TempDir()
	path := writeStoreFixture(t, directory)
	initial := losslessFixture(t)
	fingerprintA := fingerprint(t, initial, "thread_a")
	store := &Store{Hooks: Hooks{
		BeforeFinalRead: func(attempt int) {
			if attempt != 1 {
				return
			}
			external := bytes.Replace(
				initial,
				[]byte(`"status": "open"`),
				[]byte(`"status": "resolved"`),
				1,
			)
			if err := os.WriteFile(path, external, 0o640); err != nil {
				panic(err)
			}
		},
	}}

	_, err := store.Mutate(path, fingerprintA, SetStatusOperation{
		ThreadID: "thread_a",
		Status:   "handled",
	})
	if !errors.Is(err, ErrTargetChanged) {
		t.Fatalf("mutation error = %v, want ErrTargetChanged", err)
	}
	result, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	assertThreadStatus(t, result, "thread_a", "resolved")
}

func TestDocumentedFinalCheckToRenameRaceIsObservable(t *testing.T) {
	directory := t.TempDir()
	path := writeStoreFixture(t, directory)
	initial := losslessFixture(t)
	fingerprintA := fingerprint(t, initial, "thread_a")
	externalMarker := []byte(`"externalAfterCheck":true`)
	store := &Store{Hooks: Hooks{
		AfterFinalRead: func(attempt int) {
			external := bytes.Replace(
				initial,
				[]byte(`"futureRoot": {`),
				append([]byte(`"externalAfterCheck":true,"futureRoot": {`), nil...),
				1,
			)
			if err := os.WriteFile(path, external, 0o640); err != nil {
				panic(err)
			}
		},
	}}

	if _, err := store.Mutate(path, fingerprintA, SetStatusOperation{
		ThreadID: "thread_a",
		Status:   "handled",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(result, externalMarker) {
		t.Fatal("external after-check marker unexpectedly survived the documented race")
	}
	assertThreadStatus(t, result, "thread_a", "handled")
}

func TestTempSyncFailureLeavesOriginalAndCleansTemporary(t *testing.T) {
	directory := t.TempDir()
	path := writeStoreFixture(t, directory)
	initial, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{Hooks: Hooks{
		BeforeTempSync: func(attempt int) error {
			return errors.New("injected file sync failure")
		},
	}}

	_, err = store.Mutate(
		path,
		fingerprint(t, initial, "thread_a"),
		SetStatusOperation{ThreadID: "thread_a", Status: "handled"},
	)
	if err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("mutation error = %v", err)
	}
	result, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	assertBytesEqual(t, "original after sync failure", initial, result)
	entries, readDirErr := os.ReadDir(directory)
	if readDirErr != nil {
		t.Fatal(readDirErr)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files remain: %v", entries)
	}
}

func TestDirectorySyncFailureReportsAppliedUncertainWrite(t *testing.T) {
	directory := t.TempDir()
	path := writeStoreFixture(t, directory)
	initial := losslessFixture(t)
	store := &Store{Hooks: Hooks{
		BeforeDirSync: func() error {
			return errors.New("injected directory sync failure")
		},
	}}

	_, err := store.Mutate(
		path,
		fingerprint(t, initial, "thread_a"),
		SetStatusOperation{ThreadID: "thread_a", Status: "handled"},
	)
	if !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("mutation error = %v, want ErrDurabilityUncertain", err)
	}
	result, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	assertThreadStatus(t, result, "thread_a", "handled")
}

func TestStorePreservesExistingPermissions(t *testing.T) {
	directory := t.TempDir()
	path := writeStoreFixture(t, directory)
	initial := losslessFixture(t)
	if _, err := (&Store{}).Mutate(
		path,
		fingerprint(t, initial, "thread_a"),
		SetStatusOperation{ThreadID: "thread_a", Status: "handled"},
	); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("permissions = %o, want 640", got)
	}
}

func assertThreadStatus(t *testing.T, data []byte, threadID, expected string) {
	t.Helper()
	root, err := parseJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	threads, _ := root.get("threads")
	for _, thread := range threads.items {
		id, idErr := requiredString(thread, "id")
		if idErr != nil {
			t.Fatal(idErr)
		}
		if id != threadID {
			continue
		}
		status, statusErr := requiredString(thread, "status")
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		if status != expected {
			t.Fatalf("thread %s status = %s, want %s", threadID, status, expected)
		}
		return
	}
	t.Fatalf("thread %s not found", threadID)
}
