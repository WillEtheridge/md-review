package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const recordSchemaVersion = 1

const (
	pendingReplace   = "replace"
	pendingUninstall = "uninstall"

	pendingPrepared  = "prepared"
	pendingMoved     = "moved"
	pendingRemoved   = "removed"
	pendingInstalled = "installed"
)

type ownershipRecord struct {
	SchemaVersion        int                     `json:"schemaVersion"`
	CanonicalDirectory   string                  `json:"canonicalDirectory"`
	CanonicalSkillSHA256 string                  `json:"canonicalSkillSha256"`
	Targets              map[string]targetRecord `json:"targets"`
	Pending              *pendingRecord          `json:"pending,omitempty"`
}

type targetRecord struct {
	Path                        string    `json:"path"`
	Kind                        EntryKind `json:"kind"`
	ExpectedCanonicalLinkTarget string    `json:"expectedCanonicalLinkTarget,omitempty"`
	InstalledSkillSHA256        string    `json:"installedSkillSha256"`
	State                       State     `json:"state"`
	BackupPath                  string    `json:"backupPath,omitempty"`
}

type pendingRecord struct {
	Operation                   string    `json:"operation"`
	Phase                       string    `json:"phase"`
	Target                      Target    `json:"target"`
	Path                        string    `json:"path"`
	Kind                        EntryKind `json:"kind"`
	ExpectedCanonicalLinkTarget string    `json:"expectedCanonicalLinkTarget,omitempty"`
	InstalledSkillSHA256        string    `json:"installedSkillSha256"`
	BackupPath                  string    `json:"backupPath,omitempty"`
}

func newOwnershipRecord(manager *Manager) *ownershipRecord {
	return &ownershipRecord{
		SchemaVersion:        recordSchemaVersion,
		CanonicalDirectory:   manager.canonicalDirectory(),
		CanonicalSkillSHA256: manager.skillHash,
		Targets:              make(map[string]targetRecord),
	}
}

func (manager *Manager) loadRecord() (*ownershipRecord, error) {
	info, err := os.Lstat(manager.recordPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("inspect skill ownership record: %w", err)
	case !info.Mode().IsRegular():
		return nil, &UnsafeRecordError{Reason: "record is not a regular file"}
	case info.Mode().Perm() != 0o600:
		return nil, &UnsafeRecordError{Reason: "record is not mode 0600"}
	}
	content, err := os.ReadFile(manager.recordPath())
	if err != nil {
		return nil, fmt.Errorf("read skill ownership record: %w", err)
	}
	if err := rejectDuplicateJSONMembers(content); err != nil {
		return nil, &UnsafeRecordError{Reason: err.Error()}
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var record ownershipRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, &UnsafeRecordError{Reason: "record JSON is invalid: " + err.Error()}
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, &UnsafeRecordError{Reason: err.Error()}
	}
	if err := manager.validateRecord(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (manager *Manager) validateRecord(record *ownershipRecord) error {
	unsafe := func(format string, arguments ...any) error {
		return &UnsafeRecordError{Reason: fmt.Sprintf(format, arguments...)}
	}
	if record.SchemaVersion != recordSchemaVersion {
		return unsafe("schemaVersion is %d, want %d", record.SchemaVersion, recordSchemaVersion)
	}
	if record.CanonicalDirectory != manager.canonicalDirectory() {
		return unsafe("canonical directory does not match the current data directory")
	}
	if !isSafeHash(record.CanonicalSkillSHA256) {
		return unsafe("canonical skill hash is invalid")
	}
	if record.Targets == nil {
		return unsafe("targets object is missing")
	}
	for targetName, entry := range record.Targets {
		target := Target(targetName)
		definition, ok := manager.definition(target)
		if !ok {
			return unsafe("target %q is unsupported", targetName)
		}
		if entry.Path != definition.entryPath {
			return unsafe("target %q path does not match the current home directory", target)
		}
		if entry.Kind != definition.kind {
			return unsafe("target %q kind is %q, want %q", target, entry.Kind, definition.kind)
		}
		if !manager.validExpectedLink(definition, entry.ExpectedCanonicalLinkTarget) {
			return unsafe("target %q canonical link target is invalid", target)
		}
		if !isSafeHash(entry.InstalledSkillSHA256) {
			return unsafe("target %q installed hash is invalid", target)
		}
		switch entry.State {
		case StateManaged, StateOutdated, StateModified, StateBroken:
		default:
			return unsafe("target %q state %q is invalid", target, entry.State)
		}
		if entry.BackupPath != "" && !safeBackupPath(definition.entryPath, entry.BackupPath) {
			return unsafe("target %q backup path is outside its derived sibling namespace", target)
		}
	}
	if record.Pending != nil {
		pending := record.Pending
		definition, ok := manager.definition(pending.Target)
		if !ok {
			return unsafe("pending target %q is unsupported", pending.Target)
		}
		if pending.Path != definition.entryPath || pending.Kind != definition.kind {
			return unsafe("pending target path or kind does not match the current environment")
		}
		if !manager.validExpectedLink(definition, pending.ExpectedCanonicalLinkTarget) {
			return unsafe("pending canonical link target is invalid")
		}
		if !isSafeHash(pending.InstalledSkillSHA256) {
			return unsafe("pending installed hash is invalid")
		}
		switch pending.Operation {
		case pendingReplace:
			if pending.BackupPath == "" || !safeBackupPath(definition.entryPath, pending.BackupPath) {
				return unsafe("pending replacement backup path is invalid")
			}
			switch pending.Phase {
			case pendingPrepared, pendingMoved, pendingInstalled:
			default:
				return unsafe("pending replacement phase %q is invalid", pending.Phase)
			}
		case pendingUninstall:
			if pending.BackupPath != "" && !safeBackupPath(definition.entryPath, pending.BackupPath) {
				return unsafe("pending uninstall backup path is invalid")
			}
			switch pending.Phase {
			case pendingPrepared, pendingRemoved:
			default:
				return unsafe("pending uninstall phase %q is invalid", pending.Phase)
			}
		default:
			return unsafe("pending operation %q is invalid", pending.Operation)
		}
	}
	return nil
}

func (manager *Manager) validExpectedLink(
	definition targetDefinition,
	expected string,
) bool {
	if definition.kind == EntryLink {
		return expected == manager.canonicalDirectory()
	}
	return expected == ""
}

func safeBackupPath(targetPath string, backupPath string) bool {
	if !filepath.IsAbs(backupPath) ||
		filepath.Dir(backupPath) != filepath.Dir(targetPath) {
		return false
	}
	prefix := filepath.Base(targetPath) + ".mdreview-backup-"
	return filepath.Base(backupPath) != prefix &&
		len(filepath.Base(backupPath)) > len(prefix) &&
		filepath.Base(backupPath)[:len(prefix)] == prefix
}

func (manager *Manager) writeRecord(record *ownershipRecord) error {
	if err := manager.validateRecord(record); err != nil {
		return err
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode skill ownership record: %w", err)
	}
	content = append(content, '\n')
	if err := manager.atomicWrite(manager.recordPath(), content, 0o600); err != nil {
		return fmt.Errorf("write skill ownership record: %w", err)
	}
	return nil
}

func (manager *Manager) acquireLock() (*os.File, error) {
	parent := filepath.Dir(manager.lockPath())
	if err := ensurePrivateDirectory(parent); err != nil {
		return nil, fmt.Errorf("create skill data directory: %w", err)
	}
	flags := syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	descriptor, err := syscall.Open(manager.lockPath(), flags|syscall.O_CREAT|syscall.O_EXCL, 0o600)
	created := err == nil
	if errors.Is(err, syscall.EEXIST) {
		descriptor, err = syscall.Open(manager.lockPath(), flags, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open skill installation lock: %w", err)
	}
	lock := os.NewFile(uintptr(descriptor), manager.lockPath())
	if created {
		if err := lock.Chmod(0o600); err != nil {
			_ = lock.Close()
			return nil, fmt.Errorf("set skill installation lock permissions: %w", err)
		}
	}
	info, err := lock.Stat()
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("inspect skill installation lock: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = lock.Close()
		return nil, errors.New("skill installation lock is not a mode-0600 regular file")
	}
	if err := syscall.Flock(descriptor, syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("lock skill installation state: %w", err)
	}
	return lock, nil
}

func releaseLock(lock *os.File) error {
	descriptor := int(lock.Fd())
	unlockErr := syscall.Flock(descriptor, syscall.LOCK_UN)
	closeErr := lock.Close()
	return errors.Join(unlockErr, closeErr)
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%q is not a directory", path)
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return fmt.Errorf("cannot create filesystem root %q", path)
	}
	if err := ensurePrivateDirectory(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%q was replaced while creating directories", path)
		}
		return nil
	}
	return os.Chmod(path, 0o700)
}

func (manager *Manager) atomicWrite(path string, content []byte, mode os.FileMode) error {
	parent := filepath.Dir(path)
	if err := ensurePrivateDirectory(parent); err != nil {
		return err
	}
	temporaryPath, err := manager.unusedSibling(path, ".tmp-")
	if err != nil {
		return err
	}
	descriptor, err := syscall.Open(
		temporaryPath,
		syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		uint32(mode.Perm()),
	)
	if err != nil {
		return err
	}
	temporary := os.NewFile(uintptr(descriptor), temporaryPath)
	renamed := false
	defer func() {
		_ = temporary.Close()
		if !renamed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	renamed = true
	return syncDirectory(parent)
}

func (manager *Manager) unusedSibling(path string, marker string) (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		random := make([]byte, 8)
		if _, err := io.ReadFull(manager.random, random); err != nil {
			return "", fmt.Errorf("generate private temporary name: %w", err)
		}
		candidate := filepath.Join(
			filepath.Dir(path),
			fmt.Sprintf(".%s%s%x", filepath.Base(path), marker, random),
		)
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate an unused sibling path")
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("record JSON has invalid trailing data: %w", err)
	}
	return errors.New("record JSON contains more than one value")
}

func rejectDuplicateJSONMembers(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return fmt.Errorf("record JSON is invalid: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object member name is not a string")
			}
			if _, duplicate := keys[key]; duplicate {
				return fmt.Errorf("duplicate object member %q", key)
			}
			keys[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("object has an invalid closing delimiter")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("array has an invalid closing delimiter")
		}
	default:
		return errors.New("unexpected JSON closing delimiter")
	}
	return nil
}
