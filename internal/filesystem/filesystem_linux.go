//go:build linux

// Package filesystem provides Linux descriptor-relative access to an opened
// workspace root.
//
// It owns operating-system paths and descriptors. Callers use canonical,
// slash-relative identifiers and cannot receive an ordinary path to reopen.
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

	"golang.org/x/sys/unix"
)

// ResolutionMode selects the contained kernel path-resolution implementation.
type ResolutionMode uint8

const (
	// Auto uses openat2 when supported and otherwise uses the verified fallback.
	Auto ResolutionMode = iota

	// Openat2Only requires openat2 and is used to exercise the modern path.
	Openat2Only

	// OpenatFallback forces component-by-component openat resolution.
	OpenatFallback
)

// DirectoryEntryKind classifies metadata observed without following symlinks.
type DirectoryEntryKind uint8

const (
	// DirectoryEntryRegular identifies a regular file.
	DirectoryEntryRegular DirectoryEntryKind = iota

	// DirectoryEntryDirectory identifies a directory.
	DirectoryEntryDirectory

	// DirectoryEntrySymlink identifies a rejected symbolic link.
	DirectoryEntrySymlink

	// DirectoryEntrySpecial identifies a non-regular, non-directory entry.
	DirectoryEntrySpecial

	// DirectoryEntryUnavailable identifies an entry whose metadata could not be read.
	DirectoryEntryUnavailable
)

// NanosecondTimestamp preserves a Linux timestamp without overflowing a
// combined int64 nanosecond count.
type NanosecondTimestamp struct {
	Seconds     int64
	Nanoseconds int64
}

// MetadataSignature is the comparable metadata observed for one directory
// entry without following it. It is a change hint only; callers must reopen
// content through FS before treating the entry as accessible.
type MetadataSignature struct {
	Kind             DirectoryEntryKind
	Device           uint64
	Inode            uint64
	SizeBytes        int64
	ModificationTime NanosecondTimestamp
	ChangeTime       NanosecondTimestamp
}

// DirectoryEntry describes one child observed relative to an opened directory.
type DirectoryEntry struct {
	// Name is the single path component returned by the directory.
	Name string

	// Kind describes the entry without following a symbolic link.
	Kind DirectoryEntryKind

	// SizeBytes is meaningful only for regular files.
	SizeBytes int64

	// Metadata is comparable scan-time state collected without following the
	// entry. An unavailable entry contains its kind with zero-valued stat data.
	Metadata MetadataSignature

	// Err records a metadata failure only when Kind is DirectoryEntryUnavailable.
	Err error
}

// Directory is a short-lived contained directory capability supplied during a
// descriptor-carried scan. It is valid only for the duration of its callback.
type Directory struct {
	filesystem   *FS
	fd           int
	relativePath string
}

var (
	// ErrInvalidRelativePath indicates a non-canonical or non-relative identifier.
	ErrInvalidRelativePath = errors.New("invalid relative path")

	// ErrInvalidResolutionMode indicates an unsupported ResolutionMode value.
	ErrInvalidResolutionMode = errors.New("invalid filesystem resolution mode")

	// ErrNotRegular indicates that a requested file is not a regular file.
	ErrNotRegular = errors.New("not a regular file")

	// ErrNotDirectory indicates that a requested directory is not a directory.
	ErrNotDirectory = errors.New("not a directory")

	// ErrSymlink indicates that descriptor-relative resolution rejected a symlink.
	ErrSymlink = errors.New("symlink is not allowed")

	// ErrTooLarge indicates that a bounded read exceeded its byte limit.
	ErrTooLarge = errors.New("file exceeds content limit")

	// ErrClosed indicates access through a gateway that has already been closed.
	ErrClosed = errors.New("filesystem gateway is closed")
)

type hooks struct {
	beforeOpen              func(relativePath string)
	beforeFallbackComponent func(relativePathPrefix string)
	afterOpenDirectory      func(relativePath string)
	mutation                mutationHooks
}

// FS owns the canonical workspace root descriptor.
//
// Close waits for active operations. Callers must not begin new operations
// after closing the gateway.
type FS struct {
	// mu keeps the root descriptor open for each complete operation and protects
	// closed. It prevents Close from racing descriptor reuse during an open.
	mu     sync.RWMutex
	closed bool

	rootFD int
	root   string
	mode   ResolutionMode
	hooks  hooks
}

// Open canonicalises root and opens its directory descriptor. Openat2Only
// returns an error when the modern resolver is unavailable.
func Open(root string, mode ResolutionMode) (*FS, error) {
	if mode > OpenatFallback {
		return nil, ErrInvalidResolutionMode
	}

	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("make workspace root absolute: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("canonicalise workspace root: %w", err)
	}
	rootFD, err := unix.Open(
		canonical,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}
	var rootStat unix.Stat_t
	if err := unix.Fstat(rootFD, &rootStat); err != nil {
		_ = unix.Close(rootFD)
		return nil, fmt.Errorf("inspect workspace root: %w", err)
	}
	if rootStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(rootFD)
		return nil, ErrNotDirectory
	}

	filesystem := &FS{
		rootFD: rootFD,
		root:   canonical,
		mode:   mode,
	}
	if mode == Openat2Only {
		probe, probeErr := filesystem.openat2(rootFD, ".", unix.O_RDONLY|unix.O_DIRECTORY)
		if probeErr != nil {
			_ = unix.Close(rootFD)
			return nil, fmt.Errorf("openat2 unavailable: %w", probeErr)
		}
		_ = unix.Close(probe)
	}
	return filesystem, nil
}

// Close releases the workspace root descriptor after active operations finish.
// Repeated calls return nil.
func (filesystem *FS) Close() error {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()

	if filesystem.closed {
		return nil
	}
	filesystem.closed = true
	if err := unix.Close(filesystem.rootFD); err != nil {
		return fmt.Errorf("close workspace root: %w", err)
	}
	return nil
}

// Root returns the canonical absolute root used for display and instance identity.
func (filesystem *FS) Root() string {
	return filesystem.root
}

// ValidateRelativePath rejects empty, absolute, and non-canonical identifiers.
func ValidateRelativePath(relativePath string) error {
	if relativePath == "" ||
		strings.ContainsRune(relativePath, '\x00') ||
		strings.HasPrefix(relativePath, "/") ||
		path.Clean(relativePath) != relativePath ||
		relativePath == "." ||
		relativePath == ".." ||
		strings.HasPrefix(relativePath, "../") {
		return ErrInvalidRelativePath
	}
	return nil
}

// ReadFile reads a regular file through the contained gateway. It checks
// metadata before allocation and independently reads at most maxBytes plus one.
func (filesystem *FS) ReadFile(relativePath string, maxBytes int64) ([]byte, error) {
	if err := ValidateRelativePath(relativePath); err != nil {
		return nil, err
	}
	if maxBytes < 0 || maxBytes == math.MaxInt64 {
		return nil, ErrTooLarge
	}

	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()
	if filesystem.closed {
		return nil, ErrClosed
	}

	filesystem.beforeOpen(relativePath)
	fd, err := filesystem.openRelative(
		relativePath,
		unix.O_RDONLY|unix.O_NONBLOCK,
		false,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), relativePath)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("wrap file descriptor")
	}
	defer file.Close()

	sizeBytes, err := requireRegular(fd)
	if err != nil {
		return nil, err
	}
	if sizeBytes > maxBytes {
		return nil, ErrTooLarge
	}
	return readBounded(file, maxBytes)
}

// ReadDirectory returns child metadata without following or opening symlinks.
// An empty relativeDirectory addresses the already opened workspace root.
func (filesystem *FS) ReadDirectory(relativeDirectory string) ([]DirectoryEntry, error) {
	if relativeDirectory != "" {
		if err := ValidateRelativePath(relativeDirectory); err != nil {
			return nil, err
		}
	}

	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()
	if filesystem.closed {
		return nil, ErrClosed
	}

	var (
		directoryFD int
		err         error
	)
	if relativeDirectory == "" {
		directoryFD, err = duplicateCloseOnExec(filesystem.rootFD)
	} else {
		filesystem.beforeOpen(relativeDirectory)
		directoryFD, err = filesystem.openRelative(
			relativeDirectory,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK,
			true,
		)
	}
	if err != nil {
		return nil, err
	}
	directory := &Directory{
		filesystem:   filesystem,
		fd:           directoryFD,
		relativePath: relativeDirectory,
	}
	defer unix.Close(directoryFD)
	return directory.ReadEntries()
}

// WithRootDirectory invokes visit with a duplicate of the opened root
// descriptor. The Directory and every child capability become invalid when
// their callback returns.
func (filesystem *FS) WithRootDirectory(visit func(*Directory) error) error {
	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()
	if filesystem.closed {
		return ErrClosed
	}

	rootFD, err := duplicateCloseOnExec(filesystem.rootFD)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	return visit(&Directory{
		filesystem: filesystem,
		fd:         rootFD,
	})
}

// Path returns the slash-relative directory identity, or an empty string for root.
func (directory *Directory) Path() string {
	return directory.relativePath
}

// ReadEntries returns metadata for children of this already opened directory.
func (directory *Directory) ReadEntries() ([]DirectoryEntry, error) {
	// Opening "." creates an independent directory stream while retaining the
	// already-contained descriptor as the resolution root. dup would share the
	// directory offset and make a second enumeration incorrectly appear empty.
	duplicateFD, err := directory.filesystem.openAt(
		directory.fd,
		".",
		directory.relativePath,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK,
		true,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicateFD), directory.relativePath)
	if file == nil {
		_ = unix.Close(duplicateFD)
		return nil, errors.New("wrap directory descriptor")
	}

	children, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("enumerate directory: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close directory enumeration descriptor: %w", closeErr)
	}

	entries := make([]DirectoryEntry, 0, len(children))
	for _, child := range children {
		name := child.Name()
		var stat unix.Stat_t
		if err := unix.Fstatat(directory.fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			entries = append(entries, DirectoryEntry{
				Name: name,
				Kind: DirectoryEntryUnavailable,
				Metadata: MetadataSignature{
					Kind: DirectoryEntryUnavailable,
				},
				Err: err,
			})
			continue
		}

		entry := DirectoryEntry{
			Name: name,
			Metadata: MetadataSignature{
				Device:    uint64(stat.Dev),
				Inode:     stat.Ino,
				SizeBytes: stat.Size,
				ModificationTime: NanosecondTimestamp{
					Seconds:     stat.Mtim.Sec,
					Nanoseconds: stat.Mtim.Nsec,
				},
				ChangeTime: NanosecondTimestamp{
					Seconds:     stat.Ctim.Sec,
					Nanoseconds: stat.Ctim.Nsec,
				},
			},
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			entry.Kind = DirectoryEntryRegular
			entry.SizeBytes = stat.Size
		case unix.S_IFDIR:
			entry.Kind = DirectoryEntryDirectory
		case unix.S_IFLNK:
			entry.Kind = DirectoryEntrySymlink
		default:
			entry.Kind = DirectoryEntrySpecial
		}
		entry.Metadata.Kind = entry.Kind
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

// ReadFile opens and boundedly reads one regular child of this directory.
func (directory *Directory) ReadFile(name string, maxBytes int64) ([]byte, error) {
	if err := validatePathComponent(name); err != nil {
		return nil, err
	}
	if maxBytes < 0 || maxBytes == math.MaxInt64 {
		return nil, ErrTooLarge
	}

	relativePath := joinRelative(directory.relativePath, name)
	directory.filesystem.beforeOpen(relativePath)
	fd, err := directory.filesystem.openAt(
		directory.fd,
		name,
		relativePath,
		unix.O_RDONLY|unix.O_NONBLOCK,
		false,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), relativePath)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("wrap file descriptor")
	}
	defer file.Close()

	sizeBytes, err := requireRegular(fd)
	if err != nil {
		return nil, err
	}
	if sizeBytes > maxBytes {
		return nil, ErrTooLarge
	}
	return readBounded(file, maxBytes)
}

// OpenDirectory opens one child descriptor-relative and invokes visit while
// that verified directory descriptor remains open.
func (directory *Directory) OpenDirectory(
	name string,
	visit func(*Directory) error,
) error {
	if err := validatePathComponent(name); err != nil {
		return err
	}
	relativePath := joinRelative(directory.relativePath, name)
	directory.filesystem.beforeOpen(relativePath)
	childFD, err := directory.filesystem.openAt(
		directory.fd,
		name,
		relativePath,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK,
		true,
	)
	if err != nil {
		return err
	}
	defer unix.Close(childFD)
	if directory.filesystem.hooks.afterOpenDirectory != nil {
		directory.filesystem.hooks.afterOpenDirectory(relativePath)
	}
	return visit(&Directory{
		filesystem:   directory.filesystem,
		fd:           childFD,
		relativePath: relativePath,
	})
}

func (filesystem *FS) openRelative(
	relativePath string,
	flags int,
	directory bool,
) (int, error) {
	if err := ValidateRelativePath(relativePath); err != nil && relativePath != "." {
		return -1, err
	}

	// openat2 binds all component resolution to rootFD in one syscall. The
	// fallback below retains the same containment property by carrying only
	// verified directory descriptors between components; neither path performs
	// a pathname-based reopen after validation. This guarantee begins at the
	// acquired root descriptor and prevents later component replacement from
	// resolving above it; it does not freeze content or coordinate in-root writers.
	if filesystem.mode != OpenatFallback {
		fd, err := filesystem.openat2(filesystem.rootFD, relativePath, flags)
		if err == nil {
			return fd, nil
		}
		if filesystem.mode == Openat2Only ||
			(!errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL)) {
			return -1, classifyOpenError(err)
		}
	}
	return filesystem.openFallback(relativePath, flags, directory)
}

func (filesystem *FS) openat2(directoryFD int, relativePath string, flags int) (int, error) {
	how := &unix.OpenHow{
		Flags: uint64(flags | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH |
			unix.RESOLVE_NO_SYMLINKS |
			unix.RESOLVE_NO_MAGICLINKS,
	}
	return unix.Openat2(directoryFD, relativePath, how)
}

func (filesystem *FS) openAt(
	directoryFD int,
	name string,
	relativePath string,
	flags int,
	directory bool,
) (int, error) {
	if filesystem.mode != OpenatFallback {
		fd, err := filesystem.openat2(directoryFD, name, flags)
		if err == nil {
			return fd, nil
		}
		if filesystem.mode == Openat2Only ||
			(!errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL)) {
			return -1, classifyOpenError(err)
		}
	}

	if filesystem.hooks.beforeFallbackComponent != nil {
		filesystem.hooks.beforeFallbackComponent(relativePath)
	}
	openFlags := flags | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if directory {
		openFlags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(directoryFD, name, openFlags, 0)
	if err != nil {
		return -1, classifyOpenError(err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if directory && stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		return -1, ErrNotDirectory
	}
	return fd, nil
}

func (filesystem *FS) openFallback(
	relativePath string,
	flags int,
	directory bool,
) (int, error) {
	current, err := duplicateCloseOnExec(filesystem.rootFD)
	if err != nil {
		return -1, err
	}
	if relativePath == "." {
		return current, nil
	}

	components := strings.Split(relativePath, "/")
	for index, component := range components {
		if filesystem.hooks.beforeFallbackComponent != nil {
			filesystem.hooks.beforeFallbackComponent(
				strings.Join(components[:index+1], "/"),
			)
		}

		final := index == len(components)-1
		openFlags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if final {
			openFlags = flags | unix.O_NOFOLLOW | unix.O_CLOEXEC
			if directory {
				openFlags |= unix.O_DIRECTORY
			}
		}
		next, openErr := unix.Openat(current, component, openFlags, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return -1, classifyOpenError(openErr)
		}

		var stat unix.Stat_t
		if statErr := unix.Fstat(next, &stat); statErr != nil {
			_ = unix.Close(next)
			return -1, statErr
		}
		if (!final || directory) && stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			_ = unix.Close(next)
			return -1, ErrNotDirectory
		}
		current = next
	}
	return current, nil
}

func (filesystem *FS) beforeOpen(relativePath string) {
	if filesystem.hooks.beforeOpen != nil {
		filesystem.hooks.beforeOpen(relativePath)
	}
}

func requireRegular(fd int) (int64, error) {
	stat, err := statRegular(fd)
	if err != nil {
		return 0, err
	}
	return stat.Size, nil
}

func classifyOpenError(err error) error {
	if errors.Is(err, unix.ELOOP) {
		return fmt.Errorf("%w: %v", ErrSymlink, err)
	}
	if errors.Is(err, unix.ENOTDIR) {
		return fmt.Errorf("%w: %v", ErrNotDirectory, err)
	}
	return err
}

func readBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

func duplicateCloseOnExec(fd int) (int, error) {
	duplicate, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("duplicate directory descriptor: %w", err)
	}
	return duplicate, nil
}

func validatePathComponent(name string) error {
	if name == "" ||
		name == "." ||
		name == ".." ||
		strings.ContainsRune(name, '\x00') ||
		strings.ContainsRune(name, '/') {
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
