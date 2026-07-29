//go:build linux

package filesystem

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"

	"golang.org/x/sys/unix"
)

// Durability describes the applied state of a successful file mutation.
type Durability uint8

const (
	// DurabilityUnknown indicates that no replacement was reported as applied.
	DurabilityUnknown Durability = iota

	// DurabilityDurable indicates that both file and containing directory synced.
	DurabilityDurable

	// DurabilityUncertain indicates that rename applied but directory sync failed.
	DurabilityUncertain
)

const (
	// DefaultMutationAttempts is used when MutationOptions.MaxAttempts is zero.
	DefaultMutationAttempts = 3

	// MaxMutationAttempts bounds semantic callback retries after observed changes.
	MaxMutationAttempts = 16
)

// MutationOptions bounds file content and semantic retry work.
type MutationOptions struct {
	// MaxBytes bounds both the current file and callback output.
	MaxBytes int64

	// MaxAttempts bounds callbacks and final state comparisons. Zero uses
	// DefaultMutationAttempts.
	MaxAttempts int
}

// MutationCallback derives complete replacement bytes from the latest observed
// target. It may run up to MutationOptions.MaxAttempts times and must not rely
// on single-call side effects. currentBytes is an independent copy and exists
// distinguishes a missing file from an existing empty file.
type MutationCallback func(
	currentBytes []byte,
	exists bool,
) (updatedBytes []byte, err error)

var (
	// ErrMutationConflict indicates that the target changed through every attempt.
	ErrMutationConflict = errors.New("file kept changing during mutation")

	// ErrUnsafeMutationTarget indicates a symlink or non-regular target or parent.
	ErrUnsafeMutationTarget = errors.New("unsafe file mutation target")

	// ErrMutationTooLarge indicates input or output beyond MutationOptions.MaxBytes.
	ErrMutationTooLarge = errors.New("file mutation exceeds content limit")

	// ErrMutationIO identifies an ordinary pre-apply filesystem operation failure.
	ErrMutationIO = errors.New("file mutation I/O failure")

	// ErrInvalidMutationOptions indicates unusable limits, attempts, or callback.
	ErrInvalidMutationOptions = errors.New("invalid file mutation options")
)

type mutationHooks struct {
	afterOpenParent     func()
	writeTemporary      func(attempt int, file *os.File, data []byte) error
	beforeTemporarySync func(attempt int) error
	beforeFinalRead     func(attempt int)
	afterFinalCheck     func(attempt int)
	beforeDirectorySync func(attempt int) error
}

type mutationState struct {
	data        []byte
	exists      bool
	permissions os.FileMode
}

// MutateFile applies callback to the latest bounded regular-file bytes and
// atomically replaces relativePath from its contained parent directory.
//
// A DurabilityUncertain result has already been renamed into place and returns
// nil error; callers must report that applied state and must not blindly retry.
// All errors are pre-rename outcomes and return DurabilityUnknown.
func (filesystem *FS) MutateFile(
	ctx context.Context,
	relativePath string,
	options MutationOptions,
	callback MutationCallback,
) ([]byte, Durability, error) {
	attempts, err := validateMutationOptions(options, callback)
	if err != nil {
		return nil, DurabilityUnknown, err
	}
	if err := ValidateRelativePath(relativePath); err != nil {
		return nil, DurabilityUnknown, err
	}
	if err := ctx.Err(); err != nil {
		return nil, DurabilityUnknown, err
	}

	// The read lock deliberately spans the semantic callback and all filesystem
	// work so Close keeps its documented guarantee of waiting for active
	// operations. It does not serialise mutations: concurrent callers all hold
	// read locks and destination changes are handled by the bounded retry loop.
	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()
	if filesystem.closed {
		return nil, DurabilityUnknown, classifyMutationError(
			"open parent directory",
			ErrClosed,
		)
	}

	parentFD, base, err := filesystem.openMutationParentLocked(relativePath)
	if err != nil {
		return nil, DurabilityUnknown, classifyMutationError("open parent directory", err)
	}
	defer unix.Close(parentFD)
	if hook := filesystem.hooks.mutation.afterOpenParent; hook != nil {
		hook()
	}

	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, DurabilityUnknown, err
		}
		before, err := filesystem.readMutationTarget(
			parentFD,
			base,
			relativePath,
			options.MaxBytes,
		)
		if err != nil {
			return nil, DurabilityUnknown, classifyMutationError("read current file", err)
		}

		callbackInput := append([]byte(nil), before.data...)
		updated, err := callback(callbackInput, before.exists)
		if err != nil {
			return nil, DurabilityUnknown, err
		}
		if int64(len(updated)) > options.MaxBytes {
			return nil, DurabilityUnknown, ErrMutationTooLarge
		}
		emitted := append([]byte(nil), updated...)
		if err := ctx.Err(); err != nil {
			return nil, DurabilityUnknown, err
		}

		temporary, temporaryName, err := filesystem.createMutationTemporary(
			parentFD,
			before,
		)
		if err != nil {
			return nil, DurabilityUnknown, classifyMutationError("create temporary file", err)
		}
		temporaryClosed := false
		renamed := false
		cleanup := func() {
			if !temporaryClosed {
				_ = temporary.Close()
				temporaryClosed = true
			}
			if !renamed {
				_ = unix.Unlinkat(parentFD, temporaryName, 0)
			}
		}

		if err := filesystem.writeMutationTemporary(attempt, temporary, emitted); err != nil {
			cleanup()
			return nil, DurabilityUnknown, classifyMutationError("write temporary file", err)
		}
		if hook := filesystem.hooks.mutation.beforeTemporarySync; hook != nil {
			if err := hook(attempt); err != nil {
				cleanup()
				return nil, DurabilityUnknown, classifyMutationError("sync temporary file", err)
			}
		}
		if err := temporary.Sync(); err != nil {
			cleanup()
			return nil, DurabilityUnknown, classifyMutationError("sync temporary file", err)
		}
		if err := temporary.Close(); err != nil {
			temporaryClosed = true
			cleanup()
			return nil, DurabilityUnknown, classifyMutationError("close temporary file", err)
		}
		temporaryClosed = true

		if err := ctx.Err(); err != nil {
			cleanup()
			return nil, DurabilityUnknown, err
		}
		if hook := filesystem.hooks.mutation.beforeFinalRead; hook != nil {
			hook(attempt)
		}
		current, err := filesystem.readMutationTarget(
			parentFD,
			base,
			relativePath,
			options.MaxBytes,
		)
		if err != nil {
			cleanup()
			return nil, DurabilityUnknown, classifyMutationError("reread current file", err)
		}
		if current.exists != before.exists || !bytes.Equal(current.data, before.data) {
			cleanup()
			continue
		}
		if err := ctx.Err(); err != nil {
			cleanup()
			return nil, DurabilityUnknown, err
		}

		// The exact reread detects changes observable before this point, and the
		// rename remains descriptor-relative. External writers do not share this
		// protocol, so a content or file-type replacement after this check and
		// before rename can still be overwritten. The hook makes that residual
		// race observable in tests; it does not close the race.
		if hook := filesystem.hooks.mutation.afterFinalCheck; hook != nil {
			hook(attempt)
		}
		if err := unix.Renameat(parentFD, temporaryName, parentFD, base); err != nil {
			cleanup()
			return nil, DurabilityUnknown, classifyMutationError("replace destination", err)
		}
		renamed = true

		// Rename has applied before this sync. A failure here is not retryable:
		// the emitted bytes are visible, but their crash durability is uncertain.
		if hook := filesystem.hooks.mutation.beforeDirectorySync; hook != nil {
			if err := hook(attempt); err != nil {
				return emitted, DurabilityUncertain, nil
			}
		}
		if err := unix.Fsync(parentFD); err != nil {
			return emitted, DurabilityUncertain, nil
		}
		return emitted, DurabilityDurable, nil
	}

	return nil, DurabilityUnknown, fmt.Errorf(
		"%w after %d attempts",
		ErrMutationConflict,
		attempts,
	)
}

func validateMutationOptions(
	options MutationOptions,
	callback MutationCallback,
) (int, error) {
	if callback == nil ||
		options.MaxBytes < 0 ||
		options.MaxBytes == math.MaxInt64 ||
		options.MaxAttempts < 0 ||
		options.MaxAttempts > MaxMutationAttempts {
		return 0, ErrInvalidMutationOptions
	}
	attempts := options.MaxAttempts
	if attempts == 0 {
		attempts = DefaultMutationAttempts
	}
	return attempts, nil
}

func (filesystem *FS) openMutationParentLocked(relativePath string) (int, string, error) {
	parentPath := path.Dir(relativePath)
	base := path.Base(relativePath)

	if parentPath == "." {
		parentFD, err := duplicateCloseOnExec(filesystem.rootFD)
		return parentFD, base, err
	}

	filesystem.beforeOpen(parentPath)
	parentFD, err := filesystem.openRelative(
		parentPath,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK,
		true,
	)
	if err != nil {
		return -1, "", err
	}
	return parentFD, base, nil
}

func (filesystem *FS) readMutationTarget(
	parentFD int,
	base string,
	relativePath string,
	maxBytes int64,
) (mutationState, error) {
	var metadata unix.Stat_t
	if err := unix.Fstatat(parentFD, base, &metadata, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return mutationState{}, nil
		}
		return mutationState{}, err
	}
	switch metadata.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return mutationState{}, ErrSymlink
	case unix.S_IFREG:
	default:
		return mutationState{}, ErrNotRegular
	}

	filesystem.beforeOpen(relativePath)
	fd, err := filesystem.openAt(
		parentFD,
		base,
		relativePath,
		unix.O_RDONLY|unix.O_NONBLOCK,
		false,
	)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return mutationState{}, nil
		}
		return mutationState{}, filesystem.classifyChangedMutationTarget(parentFD, base, err)
	}
	file := os.NewFile(uintptr(fd), relativePath)
	if file == nil {
		_ = unix.Close(fd)
		return mutationState{}, errors.New("wrap mutation target descriptor")
	}
	defer file.Close()

	stat, err := statRegular(fd)
	if err != nil {
		return mutationState{}, err
	}
	if stat.Size > maxBytes {
		return mutationState{}, ErrTooLarge
	}
	data, err := readBounded(file, maxBytes)
	if err != nil {
		return mutationState{}, err
	}
	return mutationState{
		data:        data,
		exists:      true,
		permissions: os.FileMode(stat.Mode).Perm(),
	}, nil
}

func (filesystem *FS) classifyChangedMutationTarget(
	parentFD int,
	base string,
	openErr error,
) error {
	var metadata unix.Stat_t
	if err := unix.Fstatat(parentFD, base, &metadata, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		switch metadata.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			return ErrSymlink
		case unix.S_IFREG:
		default:
			return ErrNotRegular
		}
	}
	return openErr
}

func (filesystem *FS) createMutationTemporary(
	parentFD int,
	before mutationState,
) (*os.File, string, error) {
	const collisionAttempts = 16
	for attempt := 0; attempt < collisionAttempts; attempt++ {
		randomBytes := make([]byte, 12)
		if _, err := rand.Read(randomBytes); err != nil {
			return nil, "", err
		}
		name := ".mdreview-tmp-" + hex.EncodeToString(randomBytes)
		fd, err := unix.Openat(
			parentFD,
			name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0o666,
		)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", err
		}

		if before.exists {
			if err := unix.Fchmod(fd, uint32(before.permissions.Perm())); err != nil {
				_ = unix.Close(fd)
				_ = unix.Unlinkat(parentFD, name, 0)
				return nil, "", err
			}
		}
		file := os.NewFile(uintptr(fd), name)
		if file == nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(parentFD, name, 0)
			return nil, "", errors.New("wrap temporary file descriptor")
		}
		return file, name, nil
	}
	return nil, "", errors.New("temporary name collisions exhausted")
}

func (filesystem *FS) writeMutationTemporary(
	attempt int,
	file *os.File,
	data []byte,
) error {
	if hook := filesystem.hooks.mutation.writeTemporary; hook != nil {
		return hook(attempt, file, data)
	}
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func statRegular(fd int) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return unix.Stat_t{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return unix.Stat_t{}, ErrNotRegular
	}
	return stat, nil
}

func classifyMutationError(operation string, err error) error {
	switch {
	case errors.Is(err, ErrSymlink),
		errors.Is(err, ErrNotRegular),
		errors.Is(err, ErrNotDirectory):
		return fmt.Errorf("%w: %s: %w", ErrUnsafeMutationTarget, operation, err)
	case errors.Is(err, ErrTooLarge):
		return fmt.Errorf("%w: %s: %w", ErrMutationTooLarge, operation, err)
	default:
		return fmt.Errorf("%w: %s: %w", ErrMutationIO, operation, err)
	}
}
