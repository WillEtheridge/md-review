//go:build linux

package filesystem

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"golang.org/x/sys/unix"
)

type scopedRegularReader struct {
	file *os.File
}

func (reader scopedRegularReader) Read(buffer []byte) (int, error) {
	return reader.file.Read(buffer)
}

// WithRegularFile opens one contained regular file and keeps its descriptor
// valid only for visit. The callback receives the descriptor's initial size,
// while callers remain responsible for a limit-plus-one streaming bound
// because a regular file can grow after inspection.
func (filesystem *FS) WithRegularFile(
	relativePath string,
	maxBytes int64,
	visit func(io.Reader, int64) error,
) error {
	if err := ValidateRelativePath(relativePath); err != nil {
		return err
	}
	if maxBytes < 0 || maxBytes == math.MaxInt64 {
		return ErrTooLarge
	}
	if visit == nil {
		return errors.New("regular file visitor is required")
	}

	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()
	if filesystem.closed {
		return ErrClosed
	}

	filesystem.beforeOpen(relativePath)
	fd, err := filesystem.openRelative(
		relativePath,
		unix.O_RDONLY|unix.O_NONBLOCK,
		false,
	)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), relativePath)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("wrap file descriptor")
	}
	defer file.Close()

	sizeBytes, err := requireRegular(fd)
	if err != nil {
		return err
	}
	if sizeBytes > maxBytes {
		return ErrTooLarge
	}
	if err := visit(scopedRegularReader{file: file}, sizeBytes); err != nil {
		return fmt.Errorf("visit regular file: %w", err)
	}
	return nil
}
