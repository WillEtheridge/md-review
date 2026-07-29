//go:build linux

package containedfs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type ResolutionMode uint8

const (
	Auto ResolutionMode = iota
	Openat2Only
	OpenatFallback
)

type Hooks struct {
	BeforeOpen              func(relativePath string)
	BeforeFallbackComponent func(relativePathPrefix string)
}

type FS struct {
	rootFD int
	root   string
	mode   ResolutionMode
	Hooks  Hooks
}

type Warning struct {
	Path   string
	Reason string
}

type ScanResult struct {
	Markdown   []string
	IgnoreData map[string][]byte
	Warnings   []Warning
}

var (
	ErrInvalidPath = errors.New("invalid relative path")
	ErrNotRegular  = errors.New("not a regular file")
	ErrSymlink     = errors.New("symlink is not allowed")
	ErrTooLarge    = errors.New("file exceeds content limit")
)

func Open(root string, mode ResolutionMode) (*FS, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(
		canonical,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, err
	}
	result := &FS{rootFD: fd, root: canonical, mode: mode}
	if mode == Openat2Only {
		probe, probeErr := result.openat2(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY)
		if probeErr != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("openat2 unavailable: %w", probeErr)
		}
		unix.Close(probe)
	}
	return result, nil
}

func (filesystem *FS) Close() error {
	if filesystem.rootFD < 0 {
		return nil
	}
	err := unix.Close(filesystem.rootFD)
	filesystem.rootFD = -1
	return err
}

func (filesystem *FS) Root() string {
	return filesystem.root
}

func (filesystem *FS) ReadFile(relativePath string, maxBytes int64) ([]byte, error) {
	if err := validRelativePath(relativePath); err != nil {
		return nil, err
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
		unix.Close(fd)
		return nil, errors.New("could not wrap file descriptor")
	}
	defer file.Close()
	if err := requireRegular(fd); err != nil {
		return nil, err
	}
	return readBounded(file, maxBytes)
}

func (filesystem *FS) WriteFileAtomic(
	relativePath string,
	data []byte,
	permissions os.FileMode,
) error {
	if err := validRelativePath(relativePath); err != nil {
		return err
	}
	parentPath, base := path.Split(relativePath)
	parentPath = strings.TrimSuffix(parentPath, "/")
	parentFD := filesystem.rootFD
	ownedParent := false
	var err error
	if parentPath != "" {
		parentFD, err = filesystem.openRelative(
			parentPath,
			unix.O_RDONLY|unix.O_DIRECTORY,
			true,
		)
		if err != nil {
			return err
		}
		ownedParent = true
	}
	if ownedParent {
		defer unix.Close(parentFD)
	}

	var existing unix.Stat_t
	statErr := unix.Fstatat(parentFD, base, &existing, unix.AT_SYMLINK_NOFOLLOW)
	effectivePermissions := permissions.Perm()
	switch {
	case statErr == nil && existing.Mode&unix.S_IFMT == unix.S_IFLNK:
		return ErrSymlink
	case statErr == nil && existing.Mode&unix.S_IFMT != unix.S_IFREG:
		return ErrNotRegular
	case statErr == nil:
		effectivePermissions = os.FileMode(existing.Mode).Perm()
	case statErr != nil && !errors.Is(statErr, unix.ENOENT):
		return statErr
	}

	temporaryName, err := randomTemporaryName(base)
	if err != nil {
		return err
	}
	temporaryFD, err := unix.Openat(
		parentFD,
		temporaryName,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC,
		uint32(effectivePermissions),
	)
	if err != nil {
		return err
	}
	temporary := os.NewFile(uintptr(temporaryFD), temporaryName)
	if temporary == nil {
		unix.Close(temporaryFD)
		return errors.New("could not wrap temporary file descriptor")
	}
	renamed := false
	defer func() {
		temporary.Close()
		if !renamed {
			_ = unix.Unlinkat(parentFD, temporaryName, 0)
		}
	}()

	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(parentFD, temporaryName, parentFD, base); err != nil {
		return err
	}
	renamed = true
	if err := unix.Fsync(parentFD); err != nil {
		return err
	}
	return nil
}

func ResolveAsset(documentPath, reference string) (string, error) {
	if err := validRelativePath(documentPath); err != nil {
		return "", err
	}
	parsed, err := url.Parse(reference)
	if err != nil {
		return "", ErrInvalidPath
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidPath
	}
	if parsed.Path == "" || strings.HasPrefix(parsed.Path, "/") {
		return "", ErrInvalidPath
	}
	resolved := path.Clean(path.Join(path.Dir(documentPath), parsed.Path))
	if err := validRelativePath(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func (filesystem *FS) openRelative(
	relativePath string,
	flags int,
	directory bool,
) (int, error) {
	if err := validRelativePath(relativePath); err != nil && relativePath != "." {
		return -1, err
	}
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

func (filesystem *FS) openFallback(
	relativePath string,
	flags int,
	directory bool,
) (int, error) {
	components := strings.Split(relativePath, "/")
	current, err := unix.Dup(filesystem.rootFD)
	if err != nil {
		return -1, err
	}
	if relativePath == "." {
		return current, nil
	}

	for index, component := range components {
		if filesystem.Hooks.BeforeFallbackComponent != nil {
			filesystem.Hooks.BeforeFallbackComponent(
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
		unix.Close(current)
		if openErr != nil {
			return -1, classifyOpenError(openErr)
		}
		var stat unix.Stat_t
		if statErr := unix.Fstat(next, &stat); statErr != nil {
			unix.Close(next)
			return -1, statErr
		}
		if !final || directory {
			if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
				unix.Close(next)
				return -1, ErrNotRegular
			}
		}
		current = next
	}
	return current, nil
}

func (filesystem *FS) beforeOpen(relativePath string) {
	if filesystem.Hooks.BeforeOpen != nil {
		filesystem.Hooks.BeforeOpen(relativePath)
	}
}

func requireRegular(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return ErrNotRegular
	}
	return nil
}

func validRelativePath(relativePath string) error {
	if relativePath == "" ||
		strings.ContainsRune(relativePath, '\x00') ||
		strings.HasPrefix(relativePath, "/") ||
		path.Clean(relativePath) != relativePath ||
		relativePath == "." ||
		relativePath == ".." ||
		strings.HasPrefix(relativePath, "../") {
		return ErrInvalidPath
	}
	return nil
}

func classifyOpenError(err error) error {
	if errors.Is(err, unix.ELOOP) {
		return fmt.Errorf("%w: %v", ErrSymlink, err)
	}
	return err
}

func readBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

func randomTemporaryName(base string) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "." + base + ".tmp-" + hex.EncodeToString(random), nil
}

func sortedEntries(directory *os.File) ([]os.DirEntry, error) {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	return entries, nil
}
