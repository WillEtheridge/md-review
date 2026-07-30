package filesystem

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// WithRegularFile opens one validated regular file for the duration of visit.
// The path is revalidated immediately before the ordinary portable open. A
// same-user process can still race those operations; that actor is trusted by
// mdReview's local security model.
func (filesystem *FS) WithRegularFile(
	relativePath string,
	maxBytes int64,
	visit func(io.Reader, int64) error,
) error {
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
	fullPath, err := filesystem.checkedPath(relativePath, false)
	if err != nil {
		return err
	}
	filesystem.beforeOpen(relativePath)
	file, err := os.Open(fullPath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrNotRegular
	}
	if info.Size() > maxBytes {
		return ErrTooLarge
	}
	if err := visit(file, info.Size()); err != nil {
		return fmt.Errorf("visit regular file: %w", err)
	}
	return nil
}
