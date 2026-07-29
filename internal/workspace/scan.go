package workspace

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"

	"mdreview.dev/mdreview/internal/filesystem"
	"mdreview.dev/mdreview/internal/gatee"
	"mdreview.dev/mdreview/internal/limits"
)

type cachedIgnoreFile struct {
	signature filesystem.MetadataSignature
	rules     ignoreRules
	warning   *Warning
}

type ignoreFileCache map[string]cachedIgnoreFile

type scanResult struct {
	snapshot    Snapshot
	documents   map[string]indexedDocument
	ignoreFiles ignoreFileCache
}

type scanFunction func(
	ctx context.Context,
	gateway *filesystem.FS,
	previousIgnoreFiles ignoreFileCache,
) (scanResult, error)

type ignoreFileReader func(
	directory *filesystem.Directory,
	name string,
	maxBytes int64,
) ([]byte, error)

type scanner struct {
	documents           map[string]indexedDocument
	warnings            []Warning
	previousIgnoreFiles ignoreFileCache
	ignoreFiles         ignoreFileCache
	readIgnoreFile      ignoreFileReader
}

func scanWorkspace(
	ctx context.Context,
	gateway *filesystem.FS,
	previousIgnoreFiles ignoreFileCache,
) (scanResult, error) {
	return scanWorkspaceWithIgnoreReader(
		ctx,
		gateway,
		previousIgnoreFiles,
		func(
			directory *filesystem.Directory,
			name string,
			maxBytes int64,
		) ([]byte, error) {
			return directory.ReadFile(name, maxBytes)
		},
	)
}

func scanWorkspaceWithMeasurements(counters *gatee.Counters) scanFunction {
	return func(
		ctx context.Context,
		gateway *filesystem.FS,
		previousIgnoreFiles ignoreFileCache,
	) (scanResult, error) {
		return scanWorkspaceWithIgnoreReader(
			ctx,
			gateway,
			previousIgnoreFiles,
			func(
				directory *filesystem.Directory,
				name string,
				maxBytes int64,
			) ([]byte, error) {
				data, err := directory.ReadFile(name, maxBytes)
				counters.RecordGitignoreContentRead(len(data))
				return data, err
			},
		)
	}
}

func recordCompletedScans(scan scanFunction, counters *gatee.Counters) scanFunction {
	return func(
		ctx context.Context,
		gateway *filesystem.FS,
		previousIgnoreFiles ignoreFileCache,
	) (scanResult, error) {
		result, err := scan(ctx, gateway, previousIgnoreFiles)
		if err == nil {
			counters.RecordCompleteWorkspaceScan()
		}
		return result, err
	}
}

func scanWorkspaceWithIgnoreReader(
	ctx context.Context,
	gateway *filesystem.FS,
	previousIgnoreFiles ignoreFileCache,
	readIgnoreFile ignoreFileReader,
) (scanResult, error) {
	currentScanner := &scanner{
		documents:           make(map[string]indexedDocument),
		previousIgnoreFiles: previousIgnoreFiles,
		ignoreFiles:         make(ignoreFileCache),
		readIgnoreFile:      readIgnoreFile,
	}
	var navigation []NavigationEntry
	err := gateway.WithRootDirectory(func(root *filesystem.Directory) error {
		var scanErr error
		navigation, scanErr = currentScanner.scanDirectory(
			ctx,
			root,
			newIgnoreMatcher(),
		)
		return scanErr
	})
	if err != nil {
		return scanResult{}, err
	}

	var initialDocumentPath *string
	if _, exists := currentScanner.documents["README.md"]; exists {
		initial := "README.md"
		initialDocumentPath = &initial
	} else if first, exists := firstDocumentPath(navigation); exists {
		initialDocumentPath = &first
	}
	return scanResult{
		snapshot: Snapshot{
			DocumentCount:       len(currentScanner.documents),
			InitialDocumentPath: initialDocumentPath,
			Navigation:          navigation,
			Warnings:            currentScanner.warnings,
		},
		documents:   currentScanner.documents,
		ignoreFiles: currentScanner.ignoreFiles,
	}, nil
}

func (scanner *scanner) scanDirectory(
	ctx context.Context,
	directory *filesystem.Directory,
	inherited ignoreMatcher,
) ([]NavigationEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := directory.ReadEntries()
	if err != nil {
		return nil, err
	}

	relativeDirectory := directory.Path()
	matcher := scanner.matcherForDirectory(directory, entries, inherited)
	entriesByName := make(map[string]filesystem.DirectoryEntry, len(entries))
	for _, entry := range entries {
		entriesByName[entry.Name] = entry
	}

	navigation := make([]NavigationEntry, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.Name == ".git" ||
			entry.Name == ".gitignore" ||
			strings.HasSuffix(entry.Name, ".md.review.json") {
			continue
		}

		relativePath := entry.Name
		if relativeDirectory != "" {
			relativePath = path.Join(relativeDirectory, entry.Name)
		}
		isDirectory := entry.Kind == filesystem.DirectoryEntryDirectory
		if matcher.Match(relativePath, isDirectory) {
			continue
		}

		switch entry.Kind {
		case filesystem.DirectoryEntryDirectory:
			var children []NavigationEntry
			err := directory.OpenDirectory(entry.Name, func(child *filesystem.Directory) error {
				var scanErr error
				children, scanErr = scanner.scanDirectory(ctx, child, matcher)
				return scanErr
			})
			if err != nil {
				if errors.Is(err, context.Canceled) ||
					errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
				scanner.warn(
					relativePath,
					WarningCodeDirectoryUnreadable,
					"This directory could not be read and was skipped.",
				)
				continue
			}
			if len(children) > 0 {
				navigation = append(navigation, NavigationEntry{
					Kind:     EntryKindDirectory,
					Name:     entry.Name,
					Path:     relativePath,
					Children: children,
				})
			}
		case filesystem.DirectoryEntryRegular:
			if !strings.HasSuffix(entry.Name, ".md") {
				continue
			}
			availability := AvailabilityReady
			if entry.SizeBytes > limits.MaxMarkdownDocumentBytes {
				availability = AvailabilityTooLarge
			}

			var reviewMetadata *filesystem.MetadataSignature
			var reviewMetadataRevision *string
			if sidecarEntry, exists := entriesByName[entry.Name+".review.json"]; exists {
				signature := sidecarEntry.Metadata
				reviewMetadata = &signature
				revision := metadataRevision(signature)
				reviewMetadataRevision = &revision
				if sidecarEntry.Kind != filesystem.DirectoryEntryRegular {
					sidecarPath := relativePath + ".review.json"
					scanner.warnUnsafeEntry(sidecarPath, sidecarEntry.Kind)
				}
			}

			scanner.documents[relativePath] = indexedDocument{
				sizeBytes:      entry.SizeBytes,
				availability:   availability,
				metadata:       entry.Metadata,
				reviewMetadata: reviewMetadata,
			}
			navigation = append(navigation, NavigationEntry{
				Kind:                     EntryKindDocument,
				Name:                     entry.Name,
				Path:                     relativePath,
				SizeBytes:                entry.SizeBytes,
				Availability:             availability,
				DocumentMetadataRevision: metadataRevision(entry.Metadata),
				ReviewMetadataRevision:   reviewMetadataRevision,
			})
		case filesystem.DirectoryEntrySymlink,
			filesystem.DirectoryEntrySpecial,
			filesystem.DirectoryEntryUnavailable:
			scanner.warnUnsafeEntry(relativePath, entry.Kind)
		}
	}

	sortNavigation(navigation)
	return navigation, nil
}

func (scanner *scanner) matcherForDirectory(
	directory *filesystem.Directory,
	entries []filesystem.DirectoryEntry,
	inherited ignoreMatcher,
) ignoreMatcher {
	relativeDirectory := directory.Path()
	for _, entry := range entries {
		if entry.Name != ".gitignore" {
			continue
		}
		relativePath := ".gitignore"
		if relativeDirectory != "" {
			relativePath = path.Join(relativeDirectory, ".gitignore")
		}

		if cached, exists := scanner.previousIgnoreFiles[relativePath]; exists &&
			cached.signature == entry.Metadata {
			scanner.ignoreFiles[relativePath] = cached
			if cached.warning != nil {
				scanner.warnings = append(scanner.warnings, *cached.warning)
				return inherited
			}
			return inherited.WithRules(cached.rules)
		}

		switch {
		case entry.Kind != filesystem.DirectoryEntryRegular:
			return scanner.cacheIgnoreWarning(
				relativePath,
				entry.Metadata,
				WarningCodeIgnoreFileUnsafe,
				"This ignore file is not a safe regular file and was skipped.",
				inherited,
			)
		case entry.SizeBytes > limits.MaxGitignoreFileBytes:
			return scanner.cacheIgnoreWarning(
				relativePath,
				entry.Metadata,
				WarningCodeIgnoreFileTooLarge,
				"This ignore file exceeds 1 MiB and was skipped.",
				inherited,
			)
		}

		data, err := scanner.readIgnoreFile(
			directory,
			".gitignore",
			limits.MaxGitignoreFileBytes,
		)
		if err != nil {
			code := WarningCodeIgnoreFileUnsafe
			message := "This ignore file could not be read safely and was skipped."
			if errors.Is(err, filesystem.ErrTooLarge) {
				code = WarningCodeIgnoreFileTooLarge
				message = "This ignore file exceeds 1 MiB and was skipped."
			}
			return scanner.cacheIgnoreWarning(
				relativePath,
				entry.Metadata,
				code,
				message,
				inherited,
			)
		}

		rules := parseIgnoreRules(relativeDirectory, data)
		scanner.ignoreFiles[relativePath] = cachedIgnoreFile{
			signature: entry.Metadata,
			rules:     rules,
		}
		return inherited.WithRules(rules)
	}
	return inherited
}

func (scanner *scanner) cacheIgnoreWarning(
	relativePath string,
	signature filesystem.MetadataSignature,
	code string,
	message string,
	inherited ignoreMatcher,
) ignoreMatcher {
	warning := Warning{Path: relativePath, Code: code, Message: message}
	scanner.warnings = append(scanner.warnings, warning)
	scanner.ignoreFiles[relativePath] = cachedIgnoreFile{
		signature: signature,
		warning:   &warning,
	}
	return inherited
}

func (scanner *scanner) warnUnsafeEntry(
	relativePath string,
	kind filesystem.DirectoryEntryKind,
) {
	switch kind {
	case filesystem.DirectoryEntrySymlink:
		scanner.warn(
			relativePath,
			WarningCodeEntryUnsafe,
			"This symbolic link was skipped.",
		)
	case filesystem.DirectoryEntrySpecial:
		scanner.warn(
			relativePath,
			WarningCodeEntryUnsafe,
			"This special filesystem entry was skipped.",
		)
	case filesystem.DirectoryEntryUnavailable:
		scanner.warn(
			relativePath,
			WarningCodeEntryUnsafe,
			"This entry could not be inspected safely and was skipped.",
		)
	}
}

func (scanner *scanner) warn(relativePath, code, message string) {
	scanner.warnings = append(scanner.warnings, Warning{
		Path:    relativePath,
		Code:    code,
		Message: message,
	})
}

func sortNavigation(entries []NavigationEntry) {
	sort.Slice(entries, func(first, second int) bool {
		if entries[first].Kind != entries[second].Kind {
			return entries[first].Kind == EntryKindDirectory
		}
		firstFolded := strings.ToLower(entries[first].Name)
		secondFolded := strings.ToLower(entries[second].Name)
		if firstFolded != secondFolded {
			return firstFolded < secondFolded
		}
		return entries[first].Name < entries[second].Name
	})
}

func firstDocumentPath(entries []NavigationEntry) (string, bool) {
	for _, entry := range entries {
		if entry.Kind == EntryKindDocument {
			return entry.Path, true
		}
		if first, exists := firstDocumentPath(entry.Children); exists {
			return first, true
		}
	}
	return "", false
}
