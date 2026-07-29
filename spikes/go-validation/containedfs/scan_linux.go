//go:build linux

package containedfs

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

const MaxIgnoreBytes = 1 << 20

func (filesystem *FS) Scan() (ScanResult, error) {
	result := ScanResult{
		IgnoreData: make(map[string][]byte),
	}
	root, err := unix.Dup(filesystem.rootFD)
	if err != nil {
		return result, err
	}
	if err := filesystem.scanDirectory(root, "", &result); err != nil {
		return result, err
	}
	return result, nil
}

func (filesystem *FS) scanDirectory(
	directoryFD int,
	relativeDirectory string,
	result *ScanResult,
) error {
	directory := os.NewFile(uintptr(directoryFD), relativeDirectory)
	if directory == nil {
		unix.Close(directoryFD)
		return errors.New("could not wrap directory descriptor")
	}
	entries, err := sortedEntries(directory)
	closeErr := directory.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}

	for _, entry := range entries {
		name := entry.Name()
		if name == "." || name == ".." || name == ".git" {
			continue
		}
		relativePath := name
		if relativeDirectory != "" {
			relativePath = path.Join(relativeDirectory, name)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			result.Warnings = append(result.Warnings, Warning{
				Path:   relativePath,
				Reason: "symlink rejected",
			})
			continue
		}

		if name == ".gitignore" {
			filesystem.readIgnore(relativePath, result)
			continue
		}

		filesystem.beforeOpen(relativePath)
		fd, openErr := filesystem.openRelative(
			relativePath,
			unix.O_RDONLY|unix.O_NONBLOCK,
			false,
		)
		if openErr != nil {
			result.Warnings = append(result.Warnings, Warning{
				Path:   relativePath,
				Reason: openErr.Error(),
			})
			continue
		}
		var stat unix.Stat_t
		if statErr := unix.Fstat(fd, &stat); statErr != nil {
			unix.Close(fd)
			return statErr
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if recurseErr := filesystem.scanDirectory(fd, relativePath, result); recurseErr != nil {
				return recurseErr
			}
		case unix.S_IFREG:
			unix.Close(fd)
			if strings.HasSuffix(strings.ToLower(name), ".md") {
				result.Markdown = append(result.Markdown, relativePath)
			}
		default:
			unix.Close(fd)
			result.Warnings = append(result.Warnings, Warning{
				Path:   relativePath,
				Reason: ErrNotRegular.Error(),
			})
		}
	}
	return nil
}

func (filesystem *FS) readIgnore(relativePath string, result *ScanResult) {
	data, err := filesystem.ReadFile(relativePath, MaxIgnoreBytes)
	if err != nil {
		result.Warnings = append(result.Warnings, Warning{
			Path:   relativePath,
			Reason: fmt.Sprintf("ignored unsafe .gitignore: %v", err),
		})
		return
	}
	result.IgnoreData[relativePath] = data
}
