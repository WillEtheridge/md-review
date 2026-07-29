// Package runtime manages same-user ownership records for one canonical
// workspace. It deliberately does not own listeners or HTTP serving.
package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	runtimeDirectoryMode os.FileMode = 0o700
	recordFileMode       os.FileMode = 0o600
)

// Config controls Registry acquisition. Root must already be canonical and
// absolute; caller-owned CLI code is responsible for canonicalisation.
type Config struct {
	Root                  string
	XDGDirectory          string
	TemporaryDirectory    string
	EffectiveUID          int
	Now                   func() time.Time
	ProcessID             int
	ProcessStartTime      func(int) (string, error)
	Wait                  func(context.Context, time.Duration) error
	WaitInterval          time.Duration
	WaitTimeout           time.Duration
	VerifyReady           func(context.Context, ReadyState) error
	Nonce                 func() (string, error)
	BeforePublishStarting func() error
}

// ReadyState is the verified information an existing instance publishes for
// duplicate-invocation handoff. URL remains private to the per-user record.
type ReadyState struct {
	Root             string
	InstanceNonce    string
	PID              int
	ProcessStartTime string
	URL              string
}

// ExistingInstance identifies a running verified instance. It is returned
// instead of a Lease when another process holds the root lock.
type ExistingInstance struct {
	URL string
}

// Lease owns a root lock until Close. The stable lock file is intentionally
// retained; only this lease's nonce-matching state record may be removed.
type Lease struct {
	lockFile  *os.File
	statePath string
	state     stateRecord
	uid       int
}

type stateRecord struct {
	Status           string `json:"status"`
	Root             string `json:"root"`
	InstanceNonce    string `json:"instanceNonce"`
	PID              int    `json:"pid"`
	ProcessStartTime string `json:"processStartTime"`
	URL              string `json:"url,omitempty"`
}

// Acquire obtains the caller's root lock and publishes a starting record, or
// returns a cryptographically verified existing instance. It never permits a
// second writer merely because an existing state record is malformed or stale.
func Acquire(ctx context.Context, configured Config) (*Lease, *ExistingInstance, error) {
	config, err := normaliseConfig(configured)
	if err != nil {
		return nil, nil, err
	}

	directory, err := prepareRuntimeDirectory(config)
	if err != nil {
		return nil, nil, err
	}
	name := rootHash(config.Root)
	lockPath := filepath.Join(directory, name+".lock")
	statePath := filepath.Join(directory, name+".json")
	lockFile, err := openSecureLock(lockPath, config.EffectiveUID)
	if err != nil {
		return nil, nil, err
	}

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = lockFile.Close()
			return nil, nil, fmt.Errorf("acquire workspace runtime lock: %w", err)
		}
		_ = lockFile.Close()
		existing, waitErr := waitForReady(ctx, statePath, config)
		if waitErr != nil {
			return nil, nil, waitErr
		}
		return nil, existing, nil
	}

	nonce, err := config.Nonce()
	if err != nil {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
		return nil, nil, fmt.Errorf("generate runtime instance nonce: %w", err)
	}
	startTime, err := config.ProcessStartTime(config.ProcessID)
	if err != nil {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
		return nil, nil, fmt.Errorf("read process start time: %w", err)
	}
	record := stateRecord{
		Status:           "starting",
		Root:             config.Root,
		InstanceNonce:    nonce,
		PID:              config.ProcessID,
		ProcessStartTime: startTime,
	}
	if config.BeforePublishStarting != nil {
		if err := config.BeforePublishStarting(); err != nil {
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
			_ = lockFile.Close()
			return nil, nil, fmt.Errorf("prepare runtime starting state: %w", err)
		}
	}
	if err := writeState(statePath, record); err != nil {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
		return nil, nil, err
	}

	return &Lease{lockFile: lockFile, statePath: statePath, state: record, uid: config.EffectiveUID}, nil, nil
}

// PublishReady replaces the starting record after the caller has proved that
// its authenticated health endpoint is responding.
func (lease *Lease) PublishReady(url string) error {
	if lease == nil || lease.lockFile == nil {
		return fmt.Errorf("publish ready state without a runtime lease")
	}
	if url == "" {
		return fmt.Errorf("publish ready state without an instance URL")
	}
	lease.state.Status = "ready"
	lease.state.URL = url
	return writeState(lease.statePath, lease.state)
}

// InstanceNonce returns the immutable nonce that identifies this lease's state
// record and authenticated health response.
func (lease *Lease) InstanceNonce() string {
	if lease == nil {
		return ""
	}
	return lease.state.InstanceNonce
}

// Close removes only this lease's current state record and releases its lock.
// It never unlinks the stable lock inode, which avoids replacement races.
func (lease *Lease) Close() error {
	if lease == nil || lease.lockFile == nil {
		return nil
	}
	var closeError error
	if current, err := readState(lease.statePath, lease.uid); err == nil &&
		current.InstanceNonce == lease.state.InstanceNonce {
		if err := os.Remove(lease.statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			closeError = fmt.Errorf("remove runtime state: %w", err)
		}
	}
	if err := syscall.Flock(int(lease.lockFile.Fd()), syscall.LOCK_UN); err != nil && closeError == nil {
		closeError = fmt.Errorf("release workspace runtime lock: %w", err)
	}
	if err := lease.lockFile.Close(); err != nil && closeError == nil {
		closeError = fmt.Errorf("close workspace runtime lock: %w", err)
	}
	lease.lockFile = nil
	return closeError
}

func normaliseConfig(config Config) (Config, error) {
	if !filepath.IsAbs(config.Root) {
		return Config{}, fmt.Errorf("runtime root must be an absolute canonical path")
	}
	if config.XDGDirectory == "" {
		config.XDGDirectory = os.Getenv("XDG_RUNTIME_DIR")
	}
	if config.TemporaryDirectory == "" {
		config.TemporaryDirectory = os.TempDir()
	}
	if config.EffectiveUID == 0 {
		config.EffectiveUID = os.Geteuid()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.ProcessID == 0 {
		config.ProcessID = os.Getpid()
	}
	if config.ProcessStartTime == nil {
		config.ProcessStartTime = linuxProcessStartTime
	}
	if config.Wait == nil {
		config.Wait = waitContext
	}
	if config.WaitInterval <= 0 {
		config.WaitInterval = 50 * time.Millisecond
	}
	if config.WaitTimeout <= 0 {
		config.WaitTimeout = 3 * time.Second
	}
	if config.VerifyReady == nil {
		return Config{}, fmt.Errorf("runtime ready-state verifier is required")
	}
	if config.Nonce == nil {
		config.Nonce = randomNonce
	}
	return config, nil
}

func prepareRuntimeDirectory(config Config) (string, error) {
	if config.XDGDirectory != "" {
		return createAndValidateDirectory(filepath.Join(config.XDGDirectory, "mdreview"), config.EffectiveUID)
	}
	return createAndValidateDirectory(
		filepath.Join(config.TemporaryDirectory, "mdreview-"+strconv.Itoa(config.EffectiveUID)),
		config.EffectiveUID,
	)
}

func createAndValidateDirectory(path string, expectedUID int) (string, error) {
	if err := os.Mkdir(path, runtimeDirectoryMode); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create runtime directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect runtime directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("runtime directory is not a real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != expectedUID {
		return "", fmt.Errorf("runtime directory is not owned by the effective user")
	}
	if info.Mode().Perm() != runtimeDirectoryMode {
		return "", fmt.Errorf("runtime directory permissions must be exactly 0700")
	}
	return path, nil
}

func openSecureLock(path string, expectedUID int) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, uint32(recordFileMode))
	if err != nil {
		return nil, fmt.Errorf("open runtime lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect runtime lock: %w", err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG || int(stat.Uid) != expectedUID {
		_ = file.Close()
		return nil, fmt.Errorf("runtime lock is not a user-owned regular file")
	}
	if os.FileMode(stat.Mode).Perm() != recordFileMode {
		_ = file.Close()
		return nil, fmt.Errorf("runtime lock permissions are broader than 0600")
	}
	return file, nil
}

func waitForReady(ctx context.Context, statePath string, config Config) (*ExistingInstance, error) {
	deadline := config.Now().Add(config.WaitTimeout)
	for {
		if record, err := readState(statePath, config.EffectiveUID); err == nil && record.Status == "ready" &&
			record.Root == config.Root && record.URL != "" && record.InstanceNonce != "" {
			ready := ReadyState{
				Root:             record.Root,
				InstanceNonce:    record.InstanceNonce,
				PID:              record.PID,
				ProcessStartTime: record.ProcessStartTime,
				URL:              record.URL,
			}
			if err := config.VerifyReady(ctx, ready); err == nil {
				return &ExistingInstance{URL: record.URL}, nil
			}
		}
		if !config.Now().Before(deadline) {
			return nil, fmt.Errorf("existing mdReview instance did not publish a verified ready state")
		}
		if err := config.Wait(ctx, config.WaitInterval); err != nil {
			return nil, fmt.Errorf("wait for existing mdReview instance: %w", err)
		}
	}
}

func writeState(path string, record stateRecord) error {
	contents, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode runtime state: %w", err)
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".mdreview-state-*")
	if err != nil {
		return fmt.Errorf("create runtime state temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(recordFileMode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set runtime state permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write runtime state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync runtime state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close runtime state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish runtime state: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open runtime directory for sync: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("sync runtime directory: %w", err)
	}
	return nil
}

func readState(path string, expectedUID int) (stateRecord, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return stateRecord{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return stateRecord{}, fmt.Errorf("inspect runtime state: %w", err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG || int(stat.Uid) != expectedUID {
		return stateRecord{}, fmt.Errorf("runtime state is not a user-owned regular file")
	}
	if os.FileMode(stat.Mode).Perm() != recordFileMode {
		return stateRecord{}, fmt.Errorf("runtime state permissions are not 0600")
	}
	contents, err := io.ReadAll(io.LimitReader(file, 64*1024))
	if err != nil {
		return stateRecord{}, err
	}
	var record stateRecord
	if err := json.Unmarshal(contents, &record); err != nil {
		return stateRecord{}, fmt.Errorf("decode runtime state: %w", err)
	}
	return record, nil
}

func rootHash(root string) string {
	digest := sha256.Sum256([]byte(root))
	return hex.EncodeToString(digest[:])
}

func randomNonce() (string, error) {
	bytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func linuxProcessStartTime(processID int) (string, error) {
	contents, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(processID), "stat"))
	if err != nil {
		return "", err
	}
	closeParenthesis := strings.LastIndexByte(string(contents), ')')
	if closeParenthesis < 0 {
		return "", fmt.Errorf("malformed /proc process stat")
	}
	fields := strings.Fields(string(contents[closeParenthesis+1:]))
	const startTimeIndexAfterCommand = 19
	if len(fields) <= startTimeIndexAfterCommand {
		return "", fmt.Errorf("missing process start time")
	}
	return fields[startTimeIndexAfterCommand], nil
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
