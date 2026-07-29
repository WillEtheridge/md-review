package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestAcquirePublishesRestrictedStartingAndReadyRecords(t *testing.T) {
	temporaryDirectory := t.TempDir()
	config := testConfig(temporaryDirectory)
	lease, existing, err := Acquire(context.Background(), config)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if existing != nil {
		t.Fatal("Acquire() returned an existing instance, want a lease")
	}
	defer lease.Close()

	directory := filepath.Join(temporaryDirectory, "mdreview")
	assertMode(t, directory, runtimeDirectoryMode)
	statePath := filepath.Join(directory, rootHash(config.Root)+".json")
	assertMode(t, statePath, recordFileMode)
	starting, err := readState(statePath, config.EffectiveUID)
	if err != nil {
		t.Fatalf("read starting state: %v", err)
	}
	if starting.Status != "starting" || starting.URL != "" || starting.Root != config.Root {
		t.Fatalf("starting state = %#v", starting)
	}

	if err := lease.PublishReady("http://127.0.0.1:4242/"); err != nil {
		t.Fatalf("PublishReady() error = %v", err)
	}
	assertMode(t, statePath, recordFileMode)
	ready, err := readState(statePath, config.EffectiveUID)
	if err != nil {
		t.Fatalf("read ready state: %v", err)
	}
	if ready.Status != "ready" || ready.URL == "" {
		t.Fatalf("ready state = %#v", ready)
	}
	lockPath := filepath.Join(directory, rootHash(config.Root)+".lock")
	assertMode(t, lockPath, recordFileMode)
}

func TestAcquireReportsVerifiedExistingInstance(t *testing.T) {
	temporaryDirectory := t.TempDir()
	config := testConfig(temporaryDirectory)
	lease, _, err := Acquire(context.Background(), config)
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	defer lease.Close()
	const url = "http://127.0.0.1:4242/"
	if err := lease.PublishReady(url); err != nil {
		t.Fatalf("PublishReady() error = %v", err)
	}

	verified := false
	config.VerifyReady = func(_ context.Context, state ReadyState) error {
		verified = state.Root == config.Root && state.URL == url && state.InstanceNonce != ""
		return nil
	}
	secondLease, existing, err := Acquire(context.Background(), config)
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if secondLease != nil || existing == nil || existing.URL != url || !verified {
		t.Fatalf("second Acquire() = lease %v, existing %#v, verified %t", secondLease, existing, verified)
	}
}

func TestSecondAcquireWaitsForPublicationAfterOwnerTakesLock(t *testing.T) {
	temporaryDirectory := t.TempDir()
	ownerConfig := testConfig(temporaryDirectory)
	ownerLocked := make(chan struct{})
	releasePublication := make(chan struct{})
	ownerConfig.BeforePublishStarting = func() error {
		close(ownerLocked)
		<-releasePublication
		return nil
	}
	ownerResult := make(chan *Lease, 1)
	ownerErrors := make(chan error, 1)
	go func() {
		lease, _, err := Acquire(context.Background(), ownerConfig)
		if err != nil {
			ownerErrors <- err
			return
		}
		ownerResult <- lease
	}()
	<-ownerLocked

	readyPublished := make(chan struct{})
	secondWaitStarted := make(chan struct{})
	secondConfig := testConfig(temporaryDirectory)
	secondConfig.Wait = func(context.Context, time.Duration) error {
		close(secondWaitStarted)
		<-readyPublished
		return nil
	}
	verified := make(chan struct{})
	secondConfig.VerifyReady = func(context.Context, ReadyState) error {
		close(verified)
		return nil
	}
	secondResult := make(chan *ExistingInstance, 1)
	secondErrors := make(chan error, 1)
	go func() {
		_, existing, err := Acquire(context.Background(), secondConfig)
		if err != nil {
			secondErrors <- err
			return
		}
		secondResult <- existing
	}()
	<-secondWaitStarted

	close(releasePublication)
	var owner *Lease
	select {
	case err := <-ownerErrors:
		t.Fatalf("owner Acquire() error = %v", err)
	case owner = <-ownerResult:
	}
	defer owner.Close()
	const readyURL = "http://127.0.0.1:4242/"
	if err := owner.PublishReady(readyURL); err != nil {
		t.Fatalf("PublishReady() error = %v", err)
	}
	close(readyPublished)
	select {
	case err := <-secondErrors:
		t.Fatalf("second Acquire() error = %v", err)
	case existing := <-secondResult:
		if existing == nil || existing.URL != readyURL {
			t.Fatalf("existing instance = %#v", existing)
		}
	}
	<-verified
}

func TestAcquireRejectsInsecureFallbackDirectory(t *testing.T) {
	temporaryDirectory := t.TempDir()
	config := testConfig(temporaryDirectory)
	config.XDGDirectory = ""
	t.Setenv("XDG_RUNTIME_DIR", "")
	directory := filepath.Join(temporaryDirectory, "mdreview-"+itoa(config.EffectiveUID))
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatalf("create insecure fallback directory: %v", err)
	}
	if _, _, err := Acquire(context.Background(), config); err == nil {
		t.Fatal("Acquire() error = nil, want insecure fallback rejection")
	}
}

func TestAcquireUsesSecureFallbackWithoutXDGRuntimeDirectory(t *testing.T) {
	temporaryDirectory := t.TempDir()
	config := testConfig(temporaryDirectory)
	config.XDGDirectory = ""
	t.Setenv("XDG_RUNTIME_DIR", "")

	lease, existing, err := Acquire(context.Background(), config)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if existing != nil {
		t.Fatalf("Acquire() existing = %#v, want a new lease", existing)
	}
	defer lease.Close()

	directory := filepath.Join(
		temporaryDirectory,
		"mdreview-"+itoa(config.EffectiveUID),
	)
	assertMode(t, directory, runtimeDirectoryMode)
	lockPath := filepath.Join(directory, rootHash(config.Root)+".lock")
	statePath := filepath.Join(directory, rootHash(config.Root)+".json")
	assertMode(t, lockPath, recordFileMode)
	assertMode(t, statePath, recordFileMode)

	const readyURL = "http://127.0.0.1:4242/"
	if err := lease.PublishReady(readyURL); err != nil {
		t.Fatalf("PublishReady() error = %v", err)
	}
	assertMode(t, statePath, recordFileMode)
	ready, err := readState(statePath, config.EffectiveUID)
	if err != nil {
		t.Fatalf("read ready fallback state: %v", err)
	}
	if ready.Status != "ready" || ready.URL != readyURL {
		t.Fatalf("ready fallback state = %#v", ready)
	}
}

func TestAcquireRejectsFallbackDirectoryOwnedByAnotherUser(t *testing.T) {
	temporaryDirectory := t.TempDir()
	config := testConfig(temporaryDirectory)
	config.XDGDirectory = ""
	config.EffectiveUID = os.Geteuid() + 1
	t.Setenv("XDG_RUNTIME_DIR", "")
	directory := filepath.Join(temporaryDirectory, "mdreview-"+itoa(config.EffectiveUID))
	if err := os.Mkdir(directory, runtimeDirectoryMode); err != nil {
		t.Fatalf("create fallback directory: %v", err)
	}
	if _, _, err := Acquire(context.Background(), config); err == nil {
		t.Fatal("Acquire() error = nil, want fallback owner rejection")
	}
}

func TestAcquireRejectsRuntimeDirectoryThatIsNotExactly0700(t *testing.T) {
	temporaryDirectory := t.TempDir()
	config := testConfig(temporaryDirectory)
	directory := filepath.Join(temporaryDirectory, "mdreview")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatalf("create insufficiently restricted runtime directory: %v", err)
	}
	if _, _, err := Acquire(context.Background(), config); err == nil {
		t.Fatal("Acquire() error = nil, want runtime directory rejection")
	}
}

func TestReadStateRejectsInsecureOrSymlinkedRecords(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	if err := os.WriteFile(path, []byte(`{"status":"ready"}`), 0o644); err != nil {
		t.Fatalf("write insecure state: %v", err)
	}
	if _, err := readState(path, os.Geteuid()); err == nil {
		t.Fatal("readState() accepted a state record broader than 0600")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove insecure state: %v", err)
	}
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{"status":"ready"}`), recordFileMode); err != nil {
		t.Fatalf("write target state: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("create state symlink: %v", err)
	}
	if _, err := readState(path, os.Geteuid()); err == nil {
		t.Fatal("readState() accepted a symlink")
	}
}

func TestCloseRetainsStableLockAndDoesNotRemoveReplacementState(t *testing.T) {
	temporaryDirectory := t.TempDir()
	config := testConfig(temporaryDirectory)
	lease, _, err := Acquire(context.Background(), config)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	directory := filepath.Join(temporaryDirectory, "mdreview")
	lockPath := filepath.Join(directory, rootHash(config.Root)+".lock")
	if err := writeState(lease.statePath, stateRecord{InstanceNonce: "replacement"}); err != nil {
		t.Fatalf("replace state: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("stable lock was removed: %v", err)
	}
	if replacement, err := readState(lease.statePath, config.EffectiveUID); err != nil || replacement.InstanceNonce != "replacement" {
		t.Fatalf("replacement state after Close() = %#v, %v", replacement, err)
	}
}

func TestAcquireTimesOutForPartialOrUnverifiedState(t *testing.T) {
	temporaryDirectory := t.TempDir()
	config := testConfig(temporaryDirectory)
	lease, _, err := Acquire(context.Background(), config)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer lease.Close()
	if err := os.WriteFile(lease.statePath, []byte(`{"status":`), recordFileMode); err != nil {
		t.Fatalf("write partial state: %v", err)
	}

	now := time.Unix(0, 0)
	config.Now = func() time.Time { return now }
	config.WaitTimeout = 2 * time.Millisecond
	config.WaitInterval = time.Millisecond
	config.Wait = func(_ context.Context, duration time.Duration) error {
		now = now.Add(duration)
		return nil
	}
	config.VerifyReady = func(context.Context, ReadyState) error {
		return errors.New("unverified")
	}
	if _, _, err := Acquire(context.Background(), config); err == nil {
		t.Fatal("Acquire() error = nil, want bounded readiness failure")
	}
}

func TestAcquireReplacesStaleReadyRecordAfterTakingLock(t *testing.T) {
	temporaryDirectory := t.TempDir()
	config := testConfig(temporaryDirectory)
	directory, err := prepareRuntimeDirectory(config)
	if err != nil {
		t.Fatalf("prepare runtime directory: %v", err)
	}
	statePath := filepath.Join(directory, rootHash(config.Root)+".json")
	if err := writeState(statePath, stateRecord{
		Status: "ready", Root: config.Root, InstanceNonce: "stale", PID: 1, ProcessStartTime: "old", URL: "http://127.0.0.1:1/",
	}); err != nil {
		t.Fatalf("write stale state: %v", err)
	}
	lease, existing, err := Acquire(context.Background(), config)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer lease.Close()
	if existing != nil {
		t.Fatalf("Acquire() existing = %#v, want nil", existing)
	}
	replaced, err := readState(statePath, config.EffectiveUID)
	if err != nil {
		t.Fatalf("read replacement state: %v", err)
	}
	if replaced.Status != "starting" || replaced.InstanceNonce != "nonce-for-test" {
		t.Fatalf("replacement state = %#v", replaced)
	}
}

func testConfig(temporaryDirectory string) Config {
	return Config{
		Root:               "/canonical/workspace",
		XDGDirectory:       temporaryDirectory,
		TemporaryDirectory: temporaryDirectory,
		EffectiveUID:       os.Geteuid(),
		ProcessID:          1234,
		ProcessStartTime: func(int) (string, error) {
			return "42", nil
		},
		VerifyReady: func(context.Context, ReadyState) error { return nil },
		Nonce:       func() (string, error) { return "nonce-for-test", nil },
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s permissions = %04o, want %04o", path, got, want)
	}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
