// Package filesystem provides portable, bounded access beneath a workspace
// root. It rejects static traversal, symlink, and special-file cases. It does
// not claim race-free containment against another process running as the user.
package filesystem

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// DirectoryEntryKind classifies metadata observed without following symlinks.
type DirectoryEntryKind uint8

const (
	// DirectoryEntryRegular identifies a regular file that can be read.
	DirectoryEntryRegular DirectoryEntryKind = iota
	// DirectoryEntryDirectory identifies a directory that can be traversed.
	DirectoryEntryDirectory
	// DirectoryEntrySymlink identifies a symbolic link, which the gateway never follows.
	DirectoryEntrySymlink
	// DirectoryEntrySpecial identifies a device, socket, FIFO, or other non-regular entry.
	DirectoryEntrySpecial
	// DirectoryEntryUnavailable identifies an entry whose metadata could not be read.
	DirectoryEntryUnavailable
)

// MetadataSignature is a portable change hint. RelativePath is included so
// moving a same-size file with a preserved timestamp changes the signature.
type MetadataSignature struct {
	// RelativePath makes the signature sensitive to moves between same-sized
	// files with preserved timestamps.
	RelativePath string
	// Kind is observed without following symbolic links.
	Kind DirectoryEntryKind
	// SizeBytes is the metadata size at scan time.
	SizeBytes int64
	// ModificationUnixNano is the portable timestamp component of the hint.
	ModificationUnixNano int64
}

// DirectoryEntry describes one child without following symbolic links.
type DirectoryEntry struct {
	// Name is the single child component, never a slash-relative path.
	Name string
	// Kind classifies the child without following symbolic links.
	Kind DirectoryEntryKind
	// SizeBytes is populated for regular files and may be zero for other kinds.
	SizeBytes int64
	// Metadata is the signature used by workspace polling and ignore caching.
	Metadata MetadataSignature
	// Err preserves a metadata-read failure for unavailable entries.
	Err error
}

// Directory is a short-lived directory view supplied during a workspace scan.
type Directory struct {
	filesystem   *FS
	relativePath string
}

var (
	// ErrInvalidRelativePath reports a path that is not a canonical slash-relative identity.
	ErrInvalidRelativePath = errors.New("invalid relative path")
	// ErrNotRegular reports an attempt to read or replace a non-regular file.
	ErrNotRegular = errors.New("not a regular file")
	// ErrNotDirectory reports an attempt to traverse a non-directory path component.
	ErrNotDirectory = errors.New("not a directory")
	// ErrSymlink reports a path containing a symbolic link. Symlink traversal is never allowed.
	ErrSymlink = errors.New("symlink is not allowed")
	// ErrTooLarge reports content that exceeds the caller's explicit byte limit.
	ErrTooLarge = errors.New("file exceeds content limit")
	// ErrClosed reports use of a gateway after Close has begun or completed.
	ErrClosed = errors.New("filesystem gateway is closed")
)

type mutationHooks struct {
	afterOpenParent     func()
	writeTemporary      func(attempt int, file *os.File, data []byte) error
	beforeTemporarySync func(attempt int) error
	beforeFinalRead     func(attempt int)
	afterFinalCheck     func(attempt int)
}

type hooks struct {
	beforeOpen         func(relativePath string)
	afterOpenDirectory func(relativePath string)
	mutation           mutationHooks
}

// FS owns the canonical workspace path and coordinates Close with operations.
type FS struct {
	// mu keeps Close from racing operations and keeps all gateway calls inside
	// the lifetime of the canonical root.
	mu sync.RWMutex
	// closed is protected by mu and becomes permanent once Close is called.
	closed bool
	// root is canonical and absolute; callers never receive child OS paths.
	root string
	// hooks are test-only synchronization points and are nil in production.
	hooks hooks
}

// Open canonicalises root and verifies that it is a real directory. The
// canonical root is the only operating-system path retained by FS; callers
// subsequently address entries by slash-relative identities.
func Open(root string) (*FS, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("make workspace root absolute: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("canonicalise workspace root: %w", err)
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return nil, fmt.Errorf("inspect workspace root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSymlink
	}
	if !info.IsDir() {
		return nil, ErrNotDirectory
	}
	return &FS{root: filepath.Clean(canonical)}, nil
}

// Close prevents future operations and waits for in-flight operations to
// release their read lock. Repeated calls return nil.
func (filesystem *FS) Close() error {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()
	filesystem.closed = true
	return nil
}

// Root returns the canonical absolute workspace root. Higher layers should
// use this only for display; they must not reconstruct child paths themselves.
func (filesystem *FS) Root() string {
	return filesystem.root
}

// ValidateRelativePath rejects empty, absolute, traversal-containing, and
// platform-dependent paths. A valid value is a slash-separated identity, not
// an operating-system path and not a promise that the entry currently exists.
func ValidateRelativePath(relativePath string) error {
	if relativePath == "" ||
		strings.ContainsRune(relativePath, '\x00') ||
		strings.HasPrefix(relativePath, "/") ||
		path.Clean(relativePath) != relativePath ||
		relativePath == "." ||
		relativePath == ".." ||
		strings.HasPrefix(relativePath, "../") ||
		strings.Contains(relativePath, `\`) {
		return ErrInvalidRelativePath
	}
	return nil
}

// ReadFile boundedly reads one non-symlink regular file.
func (filesystem *FS) ReadFile(relativePath string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 || maxBytes == math.MaxInt64 {
		return nil, ErrTooLarge
	}
	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()
	if filesystem.closed {
		return nil, ErrClosed
	}
	fullPath, err := filesystem.checkedPath(relativePath, false)
	if err != nil {
		return nil, err
	}
	filesystem.beforeOpen(relativePath)
	file, err := os.Open(fullPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	if info.Size() > maxBytes {
		return nil, ErrTooLarge
	}
	return readBounded(file, maxBytes)
}

// ReadDirectory returns child metadata without following symlinks.
func (filesystem *FS) ReadDirectory(relativeDirectory string) ([]DirectoryEntry, error) {
	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()
	if filesystem.closed {
		return nil, ErrClosed
	}
	return filesystem.readDirectory(relativeDirectory)
}

// WithRootDirectory invokes visit with the workspace root directory view.
func (filesystem *FS) WithRootDirectory(visit func(*Directory) error) error {
	if visit == nil {
		return errors.New("directory visitor is required")
	}
	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()
	if filesystem.closed {
		return ErrClosed
	}
	if _, err := filesystem.checkedDirectory(""); err != nil {
		return err
	}
	return visit(&Directory{filesystem: filesystem})
}

// Path returns the slash-relative directory identity, empty for the root.
func (directory *Directory) Path() string {
	return directory.relativePath
}

// ReadEntries returns metadata for this directory's children.
func (directory *Directory) ReadEntries() ([]DirectoryEntry, error) {
	return directory.filesystem.readDirectory(directory.relativePath)
}

// ReadFile boundedly reads one regular child.
func (directory *Directory) ReadFile(name string, maxBytes int64) ([]byte, error) {
	if err := validatePathComponent(name); err != nil {
		return nil, err
	}
	return directory.filesystem.readFileLocked(
		joinRelative(directory.relativePath, name),
		maxBytes,
	)
}

// OpenDirectory validates one child directory and invokes visit.
func (directory *Directory) OpenDirectory(name string, visit func(*Directory) error) error {
	if err := validatePathComponent(name); err != nil {
		return err
	}
	if visit == nil {
		return errors.New("directory visitor is required")
	}
	relativePath := joinRelative(directory.relativePath, name)
	if _, err := directory.filesystem.checkedDirectory(relativePath); err != nil {
		return err
	}
	directory.filesystem.beforeOpen(relativePath)
	if hook := directory.filesystem.hooks.afterOpenDirectory; hook != nil {
		hook(relativePath)
	}
	return visit(&Directory{filesystem: directory.filesystem, relativePath: relativePath})
}

func (filesystem *FS) readFileLocked(relativePath string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 || maxBytes == math.MaxInt64 {
		return nil, ErrTooLarge
	}
	fullPath, err := filesystem.checkedPath(relativePath, false)
	if err != nil {
		return nil, err
	}
	filesystem.beforeOpen(relativePath)
	file, err := os.Open(fullPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	if info.Size() > maxBytes {
		return nil, ErrTooLarge
	}
	return readBounded(file, maxBytes)
}

func (filesystem *FS) readDirectory(relativeDirectory string) ([]DirectoryEntry, error) {
	fullPath, err := filesystem.checkedDirectory(relativeDirectory)
	if err != nil {
		return nil, err
	}
	children, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, fmt.Errorf("enumerate directory: %w", err)
	}
	entries := make([]DirectoryEntry, 0, len(children))
	for _, child := range children {
		relativePath := joinRelative(relativeDirectory, child.Name())
		info, infoErr := os.Lstat(filepath.Join(fullPath, child.Name()))
		if infoErr != nil {
			entries = append(entries, DirectoryEntry{
				Name: child.Name(),
				Kind: DirectoryEntryUnavailable,
				Metadata: MetadataSignature{
					RelativePath: relativePath,
					Kind:         DirectoryEntryUnavailable,
				},
				Err: infoErr,
			})
			continue
		}
		kind := classifyMode(info.Mode())
		entry := DirectoryEntry{
			Name: child.Name(),
			Kind: kind,
			Metadata: MetadataSignature{
				RelativePath:         relativePath,
				Kind:                 kind,
				SizeBytes:            info.Size(),
				ModificationUnixNano: info.ModTime().UnixNano(),
			},
		}
		if kind == DirectoryEntryRegular {
			entry.SizeBytes = info.Size()
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(first, second int) bool {
		firstFolded := strings.ToLower(entries[first].Name)
		secondFolded := strings.ToLower(entries[second].Name)
		if firstFolded != secondFolded {
			return firstFolded < secondFolded
		}
		return entries[first].Name < entries[second].Name
	})
	return entries, nil
}

func classifyMode(mode os.FileMode) DirectoryEntryKind {
	switch {
	case mode&os.ModeSymlink != 0:
		return DirectoryEntrySymlink
	case mode.IsRegular():
		return DirectoryEntryRegular
	case mode.IsDir():
		return DirectoryEntryDirectory
	default:
		return DirectoryEntrySpecial
	}
}

func (filesystem *FS) checkedDirectory(relativePath string) (string, error) {
	if relativePath == "" {
		info, err := os.Lstat(filesystem.root)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", ErrSymlink
		}
		if !info.IsDir() {
			return "", ErrNotDirectory
		}
		return filesystem.root, nil
	}
	return filesystem.checkedPath(relativePath, true)
}

func (filesystem *FS) checkedPath(relativePath string, wantDirectory bool) (string, error) {
	// Walk every component with Lstat rather than evaluating the final path in
	// one call. This rejects symlinks in parents as well as at the destination.
	if err := ValidateRelativePath(relativePath); err != nil {
		return "", err
	}
	components := strings.Split(relativePath, "/")
	current := filesystem.root
	for index, component := range components {
		current = filepath.Join(current, filepath.FromSlash(component))
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", ErrSymlink
		}
		final := index == len(components)-1
		if !final && !info.IsDir() {
			return "", ErrNotDirectory
		}
		if final && wantDirectory && !info.IsDir() {
			return "", ErrNotDirectory
		}
	}
	return current, nil
}

func (filesystem *FS) beforeOpen(relativePath string) {
	if hook := filesystem.hooks.beforeOpen; hook != nil {
		hook(relativePath)
	}
}

func readBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	// Read one byte beyond the limit so a stream with missing or stale metadata
	// cannot silently truncate into an apparently valid result.
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

func validatePathComponent(name string) error {
	if name == "" ||
		name == "." ||
		name == ".." ||
		strings.ContainsRune(name, '\x00') ||
		strings.ContainsAny(name, `/\`) {
		return ErrInvalidRelativePath
	}
	return nil
}

func joinRelative(relativeDirectory, name string) string {
	if relativeDirectory == "" {
		return name
	}
	return path.Join(relativeDirectory, name)
}
