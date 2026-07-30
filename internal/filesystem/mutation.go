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
	"path/filepath"
)

// MutationOptions bounds replacement file content.
type MutationOptions struct {
	// MaxBytes is the maximum size of both the existing input and replacement
	// output. The limit is enforced with a bounded read, not metadata alone.
	MaxBytes int64
}

// MutationCallback derives complete replacement bytes from a snapshot of the
// current file. The callback must not mutate the filesystem itself; MutateFile
// performs the final unchanged check and atomic replacement around it.
type MutationCallback func(currentBytes []byte, exists bool) ([]byte, error)

var (
	// ErrMutationConflict means the target changed between the callback's input
	// snapshot and the final pre-rename check, so no replacement was applied.
	ErrMutationConflict = errors.New("file kept changing during mutation")
	// ErrUnsafeMutationTarget means the destination or one of its parents is a
	// symlink, special file, or otherwise outside the portable gateway contract.
	ErrUnsafeMutationTarget = errors.New("unsafe file mutation target")
	// ErrMutationTooLarge means the existing or emitted replacement exceeds the
	// configured mutation limit.
	ErrMutationTooLarge = errors.New("file mutation exceeds content limit")
	// ErrMutationIO wraps ordinary failures opening, writing, syncing, or
	// replacing the target.
	ErrMutationIO = errors.New("file mutation I/O failure")
	// ErrInvalidMutationOptions means the limit or replacement callback is not
	// usable for a bounded mutation.
	ErrInvalidMutationOptions = errors.New("invalid file mutation options")
)

type mutationState struct {
	// data is the exact bounded bytes used for the final conflict comparison.
	data []byte
	// exists distinguishes a missing target from an empty regular file.
	exists bool
	// permissions are copied to a replacement so a sidecar rewrite does not
	// silently change the existing file mode.
	permissions os.FileMode
}

// MutateFile derives and atomically installs a complete replacement for one
// slash-relative file. It reads the current file, invokes callback, writes a
// random temporary sibling with the original permissions, syncs and closes
// that sibling, re-reads the target, and renames only if the target is byte-for-
// byte unchanged. The final check protects cooperating callers; an external
// same-user writer can still replace the target after that check and before
// rename. There is deliberately no directory sync or power-loss durability
// classification.
func (filesystem *FS) MutateFile(
	ctx context.Context,
	relativePath string,
	options MutationOptions,
	callback MutationCallback,
) ([]byte, error) {
	err := validateMutationOptions(options, callback)
	if err != nil {
		return nil, err
	}
	if err := ValidateRelativePath(relativePath); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()
	if filesystem.closed {
		return nil, classifyMutationError("open parent directory", ErrClosed)
	}

	parentRelative := path.Dir(relativePath)
	if parentRelative == "." {
		parentRelative = ""
	}
	parentPath, err := filesystem.checkedDirectory(parentRelative)
	if err != nil {
		return nil, classifyMutationError("open parent directory", err)
	}
	base := path.Base(relativePath)
	if err := validatePathComponent(base); err != nil {
		return nil, err
	}
	targetPath := filepath.Join(parentPath, filepath.FromSlash(base))
	if hook := filesystem.hooks.mutation.afterOpenParent; hook != nil {
		hook()
	}

	before, err := filesystem.readMutationTarget(targetPath, relativePath, options.MaxBytes)
	if err != nil {
		return nil, classifyMutationError("read current file", err)
	}
	// Give the callback an owned copy: it may derive bytes freely, but cannot
	// mutate the snapshot that the final conflict check compares.
	updated, err := callback(append([]byte(nil), before.data...), before.exists)
	if err != nil {
		return nil, err
	}
	if int64(len(updated)) > options.MaxBytes {
		return nil, ErrMutationTooLarge
	}
	emitted := append([]byte(nil), updated...)

	temporary, temporaryPath, err := createMutationTemporary(parentPath, before)
	if err != nil {
		return nil, classifyMutationError("create temporary file", err)
	}
	temporaryClosed := false
	renamed := false
	cleanup := func() {
		if !temporaryClosed {
			_ = temporary.Close()
			temporaryClosed = true
		}
		if !renamed {
			_ = os.Remove(temporaryPath)
		}
	}

	if err := filesystem.writeMutationTemporary(1, temporary, emitted); err != nil {
		cleanup()
		return nil, classifyMutationError("write temporary file", err)
	}
	if hook := filesystem.hooks.mutation.beforeTemporarySync; hook != nil {
		if err := hook(1); err != nil {
			cleanup()
			return nil, classifyMutationError("sync temporary file", err)
		}
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return nil, classifyMutationError("sync temporary file", err)
	}
	if err := temporary.Close(); err != nil {
		temporaryClosed = true
		cleanup()
		return nil, classifyMutationError("close temporary file", err)
	}
	temporaryClosed = true

	if hook := filesystem.hooks.mutation.beforeFinalRead; hook != nil {
		hook(1)
	}
	current, err := filesystem.readMutationTarget(targetPath, relativePath, options.MaxBytes)
	if err != nil {
		cleanup()
		return nil, classifyMutationError("reread current file", err)
	}
	if current.exists != before.exists || !bytes.Equal(current.data, before.data) {
		cleanup()
		return nil, ErrMutationConflict
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return nil, err
	}

	// External writers do not share this protocol, so a replacement after this
	// exact check and before rename can still be overwritten. The local security
	// model does not claim to close that residual same-user race.
	if hook := filesystem.hooks.mutation.afterFinalCheck; hook != nil {
		hook(1)
	}
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		cleanup()
		return nil, classifyMutationError("replace destination", err)
	}
	renamed = true
	return emitted, nil
}

func validateMutationOptions(options MutationOptions, callback MutationCallback) error {
	if callback == nil ||
		options.MaxBytes < 0 ||
		options.MaxBytes == math.MaxInt64 {
		return ErrInvalidMutationOptions
	}
	return nil
}

func (filesystem *FS) readMutationTarget(
	targetPath string,
	relativePath string,
	maxBytes int64,
) (mutationState, error) {
	// Lstat establishes the safety and size preconditions without following a
	// symlink; Stat on the opened descriptor repeats the regular-file check after
	// the open race window.
	info, err := os.Lstat(targetPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return mutationState{}, nil
	case err != nil:
		return mutationState{}, err
	case info.Mode()&os.ModeSymlink != 0:
		return mutationState{}, ErrSymlink
	case !info.Mode().IsRegular():
		return mutationState{}, ErrNotRegular
	case info.Size() > maxBytes:
		return mutationState{}, ErrTooLarge
	}

	filesystem.beforeOpen(relativePath)
	file, err := os.Open(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return mutationState{}, nil
		}
		return mutationState{}, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return mutationState{}, err
	}
	if !openedInfo.Mode().IsRegular() {
		return mutationState{}, ErrNotRegular
	}
	data, err := readBounded(file, maxBytes)
	if err != nil {
		return mutationState{}, err
	}
	return mutationState{
		data:        data,
		exists:      true,
		permissions: openedInfo.Mode().Perm(),
	}, nil
}

func createMutationTemporary(
	parentPath string,
	before mutationState,
) (*os.File, string, error) {
	const collisionAttempts = 16
	for attempt := 0; attempt < collisionAttempts; attempt++ {
		randomBytes := make([]byte, 12)
		if _, err := rand.Read(randomBytes); err != nil {
			return nil, "", err
		}
		temporaryPath := filepath.Join(
			parentPath,
			".mdreview-tmp-"+hex.EncodeToString(randomBytes),
		)
		file, err := os.OpenFile(
			temporaryPath,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0o666,
		)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		if before.exists {
			if err := file.Chmod(before.permissions); err != nil {
				_ = file.Close()
				_ = os.Remove(temporaryPath)
				return nil, "", err
			}
		}
		return file, temporaryPath, nil
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
