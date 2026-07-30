package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"

	"mdreview.dev/mdreview/internal/filesystem"
	"mdreview.dev/mdreview/internal/limits"
)

var (
	// ErrAssetNotFound hides whether an image reference was absent, escaping,
	// unsafe, or scoped from a document outside the current index.
	ErrAssetNotFound = errors.New("asset was not found")

	// ErrAssetTooLarge indicates a regular image beyond the fixed read bound.
	ErrAssetTooLarge = errors.New("asset exceeds content limit")

	// ErrAssetUnavailable indicates an ordinary contained asset read failure.
	ErrAssetUnavailable = errors.New("asset is unavailable")
)

type assetVisitorFailure struct {
	err error
}

func (failure assetVisitorFailure) Error() string {
	return failure.err.Error()
}

func (failure assetVisitorFailure) Unwrap() error {
	return failure.err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

// ReadAsset resolves one relative image from a currently indexed Markdown
// identity and keeps the validated file handle scoped to visit. The visitor
// must apply content-type validation and a limit-plus-one streaming bound.
func (service *Service) ReadAsset(
	ctx context.Context,
	documentPath string,
	reference string,
	visit func(io.Reader, int64) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if visit == nil {
		return errors.New("asset visitor is required")
	}
	if err := filesystem.ValidateRelativePath(documentPath); err != nil {
		return ErrAssetNotFound
	}

	service.stateMu.RLock()
	_, indexed := service.documents[documentPath]
	service.stateMu.RUnlock()
	if !indexed {
		return ErrAssetNotFound
	}

	assetPath, ok := resolveAssetPath(documentPath, reference)
	if !ok {
		return ErrAssetNotFound
	}
	err := service.gateway.WithRegularFile(
		assetPath,
		limits.MaxImageAssetBytes,
		func(reader io.Reader, sizeBytes int64) error {
			if err := ctx.Err(); err != nil {
				return assetVisitorFailure{err: err}
			}
			if err := visit(contextReader{ctx: ctx, reader: reader}, sizeBytes); err != nil {
				return assetVisitorFailure{err: err}
			}
			return nil
		},
	)
	if err == nil {
		return nil
	}
	var visitorFailure assetVisitorFailure
	if errors.As(err, &visitorFailure) {
		return visitorFailure.err
	}
	switch {
	case errors.Is(err, filesystem.ErrTooLarge):
		return ErrAssetTooLarge
	case errors.Is(err, os.ErrNotExist),
		errors.Is(err, filesystem.ErrInvalidRelativePath),
		errors.Is(err, filesystem.ErrSymlink),
		errors.Is(err, filesystem.ErrNotRegular),
		errors.Is(err, filesystem.ErrNotDirectory):
		return ErrAssetNotFound
	default:
		return fmt.Errorf("%w: %v", ErrAssetUnavailable, err)
	}
}

func resolveAssetPath(documentPath string, reference string) (string, bool) {
	if reference == "" ||
		strings.ContainsRune(reference, '\x00') ||
		strings.Contains(reference, "\\") ||
		strings.HasPrefix(reference, "//") {
		return "", false
	}
	parsed, err := url.Parse(reference)
	if err != nil ||
		parsed.Scheme != "" ||
		parsed.Host != "" ||
		parsed.User != nil ||
		parsed.Opaque != "" ||
		parsed.RawQuery != "" ||
		parsed.ForceQuery ||
		parsed.Fragment != "" {
		return "", false
	}
	decodedPath := parsed.Path
	if decodedPath == "" ||
		path.IsAbs(decodedPath) ||
		strings.ContainsRune(decodedPath, '\x00') ||
		strings.Contains(decodedPath, "\\") {
		return "", false
	}

	resolved := path.Clean(path.Join(path.Dir(documentPath), decodedPath))
	if err := filesystem.ValidateRelativePath(resolved); err != nil {
		return "", false
	}
	return resolved, true
}
