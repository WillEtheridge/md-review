package skills

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	failAfterPendingRecord   = "after-pending-record"
	failAfterBackupMove      = "after-backup-move"
	failAfterEntryInstall    = "after-entry-install"
	failAfterUninstallRecord = "after-uninstall-record"
	failAfterEntryRemoval    = "after-entry-removal"
)

// Install writes or updates the canonical asset and only the explicitly
// requested targets. Each conflict is preserved as a sibling backup only when
// that target's request authorizes replacement.
func (manager *Manager) Install(
	requests []InstallRequest,
) (result Result, resultErr error) {
	if err := validateInstallRequests(requests); err != nil {
		return Result{}, err
	}
	lock, err := manager.acquireLock()
	if err != nil {
		return Result{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, releaseLock(lock))
	}()

	record, err := manager.loadRecord()
	if err != nil {
		return result, err
	}
	record, canonicalChanged, err := manager.ensureCanonical(record)
	if err != nil {
		return result, err
	}
	result.CanonicalChanged = canonicalChanged
	recovery, err := manager.recoverPending(record)
	if err != nil {
		return result, err
	}
	if recovery != nil {
		result.Changes = append(result.Changes, *recovery)
	}

	for _, request := range requests {
		change, err := manager.installTarget(
			record,
			request.Target,
			request.AllowConflictBackup,
		)
		if change != nil {
			result.Changes = append(result.Changes, *change)
		}
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// Uninstall removes only unchanged recorded entries for the explicitly named
// targets and restores their recorded backups when present.
func (manager *Manager) Uninstall(targets []Target) (result Result, resultErr error) {
	if err := validateTargets(targets); err != nil {
		return Result{}, err
	}
	lock, err := manager.acquireLock()
	if err != nil {
		return Result{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, releaseLock(lock))
	}()

	record, err := manager.loadRecord()
	if err != nil {
		return result, err
	}
	if record == nil {
		for _, target := range targets {
			definition, _ := manager.definition(target)
			result.Changes = append(result.Changes, Change{
				Target: target,
				Action: ActionPreserved,
				State:  StateNotInstalled,
				Path:   definition.entryPath,
			})
		}
		return result, nil
	}
	recovery, err := manager.recoverPending(record)
	if err != nil {
		return result, err
	}
	if recovery != nil {
		result.Changes = append(result.Changes, *recovery)
	}
	for _, target := range targets {
		change, err := manager.uninstallTarget(record, target)
		if change != nil {
			result.Changes = append(result.Changes, *change)
		}
		if err != nil {
			return result, err
		}
	}
	removed, err := manager.cleanupCanonical(record)
	if err != nil {
		return result, err
	}
	result.CanonicalRemoved = removed
	return result, nil
}

func (manager *Manager) ensureCanonical(
	record *ownershipRecord,
) (*ownershipRecord, bool, error) {
	createdRecord := record == nil
	if record == nil {
		record = newOwnershipRecord(manager)
	}
	directoryInfo, err := os.Lstat(manager.canonicalDirectory())
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := ensurePrivateDirectory(manager.canonicalDirectory()); err != nil {
			return nil, false, fmt.Errorf("create canonical skill directory: %w", err)
		}
	case err != nil:
		return nil, false, fmt.Errorf("inspect canonical skill directory: %w", err)
	case !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0:
		return nil, false, &CanonicalModifiedError{Path: manager.canonicalDirectory()}
	}

	canonicalChanged := false
	actualHash, skillInfo, err := hashFile(manager.canonicalPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := manager.atomicWrite(manager.canonicalPath(), manager.skill, 0o600); err != nil {
			return nil, false, fmt.Errorf("write missing canonical skill: %w", err)
		}
		canonicalChanged = true
	case err != nil:
		return nil, false, &CanonicalModifiedError{Path: manager.canonicalPath()}
	default:
		if createdRecord && actualHash != manager.skillHash {
			return nil, false, &CanonicalModifiedError{Path: manager.canonicalPath()}
		}
		if !createdRecord &&
			actualHash != record.CanonicalSkillSHA256 &&
			actualHash != manager.skillHash {
			return nil, false, &CanonicalModifiedError{Path: manager.canonicalPath()}
		}
		if actualHash == record.CanonicalSkillSHA256 &&
			actualHash != manager.skillHash {
			if err := manager.atomicWrite(manager.canonicalPath(), manager.skill, 0o600); err != nil {
				return nil, false, fmt.Errorf("update canonical skill: %w", err)
			}
			canonicalChanged = true
		} else if actualHash == manager.skillHash && skillInfo.Mode().Perm() != 0o600 {
			if err := manager.atomicWrite(manager.canonicalPath(), manager.skill, 0o600); err != nil {
				return nil, false, fmt.Errorf("restore canonical skill permissions: %w", err)
			}
			canonicalChanged = true
		}
	}

	recordDirty := createdRecord ||
		record.CanonicalDirectory != manager.canonicalDirectory() ||
		record.CanonicalSkillSHA256 != manager.skillHash
	record.CanonicalDirectory = manager.canonicalDirectory()
	record.CanonicalSkillSHA256 = manager.skillHash
	if record.Pending != nil && record.Pending.Operation == pendingReplace {
		record.Pending.InstalledSkillSHA256 = manager.skillHash
	}

	for targetName, stored := range record.Targets {
		definition, _ := manager.definition(Target(targetName))
		inspection, inspectErr := manager.inspectEntry(definition)
		if inspectErr != nil {
			return nil, false, fmt.Errorf("inspect recorded %s entry: %w", targetName, inspectErr)
		}
		updated := stored
		if definition.kind == EntryLink {
			if inspection.desired {
				updated.InstalledSkillSHA256 = manager.skillHash
				updated.State = StateManaged
			} else if inspection.state == StateBroken || !inspection.exists {
				updated.State = StateBroken
			} else {
				updated.State = StateModified
			}
		} else if inspection.desired &&
			(inspection.hash == stored.InstalledSkillSHA256 ||
				inspection.hash == manager.skillHash) {
			if inspection.hash != manager.skillHash {
				if err := manager.atomicWrite(
					filepath.Join(definition.entryPath, "SKILL.md"),
					manager.skill,
					0o600,
				); err != nil {
					return nil, false, fmt.Errorf("update recorded Claude skill copy: %w", err)
				}
				canonicalChanged = true
			}
			updated.InstalledSkillSHA256 = manager.skillHash
			updated.State = StateManaged
		} else if inspection.state == StateBroken || !inspection.exists {
			updated.State = StateBroken
		} else {
			updated.State = StateModified
		}
		if updated != stored {
			record.Targets[targetName] = updated
			recordDirty = true
		}
	}
	if recordDirty || canonicalChanged {
		if err := manager.writeRecord(record); err != nil {
			return nil, false, err
		}
	}
	return record, canonicalChanged, nil
}

func (manager *Manager) installTarget(
	record *ownershipRecord,
	target Target,
	allowConflictBackup bool,
) (*Change, error) {
	definition, _ := manager.definition(target)
	inspection, err := manager.inspectEntry(definition)
	if err != nil {
		return nil, fmt.Errorf("inspect %s skill entry: %w", target, err)
	}
	stored, isRecorded := record.Targets[string(target)]

	if !inspection.exists {
		if err := manager.installDesiredEntry(definition); err != nil {
			return nil, fmt.Errorf("install %s skill entry: %w", target, err)
		}
		record.Targets[string(target)] = manager.completedTargetRecord(definition, stored.BackupPath)
		if err := manager.writeRecord(record); err != nil {
			return nil, err
		}
		return &Change{
			Target: target,
			Action: ActionInstalled,
			State:  StateManaged,
			Path:   definition.entryPath,
		}, nil
	}

	if definition.kind == EntryLink && inspection.desired {
		action := ActionAdopted
		if isRecorded {
			action = ActionUnchanged
		}
		updated := manager.completedTargetRecord(definition, stored.BackupPath)
		if !isRecorded || updated != stored {
			record.Targets[string(target)] = updated
			if err := manager.writeRecord(record); err != nil {
				return nil, err
			}
			if isRecorded {
				action = ActionUpdated
			}
		}
		return &Change{
			Target:     target,
			Action:     action,
			State:      StateManaged,
			Path:       definition.entryPath,
			BackupPath: stored.BackupPath,
		}, nil
	}
	if definition.kind == EntryCopy &&
		isRecorded &&
		inspection.desired &&
		inspection.hash == stored.InstalledSkillSHA256 {
		updated := manager.completedTargetRecord(definition, stored.BackupPath)
		action := ActionUnchanged
		if updated != stored {
			record.Targets[string(target)] = updated
			if err := manager.writeRecord(record); err != nil {
				return nil, err
			}
			action = ActionUpdated
		}
		return &Change{
			Target:     target,
			Action:     action,
			State:      StateManaged,
			Path:       definition.entryPath,
			BackupPath: stored.BackupPath,
		}, nil
	}

	conflictState := StateConflicting
	if isRecorded {
		conflictState = StateModified
	}
	if inspection.state == StateBroken {
		conflictState = StateBroken
	}
	if !allowConflictBackup {
		return nil, &TargetConflictError{
			Target: target,
			Path:   definition.entryPath,
			State:  conflictState,
		}
	}
	if isRecorded && stored.BackupPath != "" {
		return nil, &PendingConflictError{
			Target: target,
			Reason: "the recorded entry already owns a backup",
		}
	}
	backupPath, err := manager.allocateBackupPath(definition.entryPath)
	if err != nil {
		return nil, err
	}
	record.Pending = &pendingRecord{
		Operation:                   pendingReplace,
		Phase:                       pendingPrepared,
		Target:                      target,
		Path:                        definition.entryPath,
		Kind:                        definition.kind,
		ExpectedCanonicalLinkTarget: manager.expectedLinkTarget(definition),
		InstalledSkillSHA256:        manager.skillHash,
		BackupPath:                  backupPath,
	}
	if err := manager.writeRecord(record); err != nil {
		return nil, err
	}
	if err := manager.hitFailpoint(failAfterPendingRecord); err != nil {
		return nil, err
	}
	if err := os.Rename(definition.entryPath, backupPath); err != nil {
		return nil, fmt.Errorf("move conflicting %s entry to backup: %w", target, err)
	}
	if err := syncDirectory(filepath.Dir(definition.entryPath)); err != nil {
		return nil, fmt.Errorf("sync %s backup directory: %w", target, err)
	}
	record.Pending.Phase = pendingMoved
	if err := manager.writeRecord(record); err != nil {
		return nil, err
	}
	if err := manager.hitFailpoint(failAfterBackupMove); err != nil {
		return nil, err
	}
	if err := manager.installDesiredEntry(definition); err != nil {
		return nil, fmt.Errorf("install replacement %s skill entry: %w", target, err)
	}
	record.Pending.Phase = pendingInstalled
	if err := manager.writeRecord(record); err != nil {
		return nil, err
	}
	if err := manager.hitFailpoint(failAfterEntryInstall); err != nil {
		return nil, err
	}
	record.Targets[string(target)] = manager.completedTargetRecord(definition, backupPath)
	record.Pending = nil
	if err := manager.writeRecord(record); err != nil {
		return nil, err
	}
	return &Change{
		Target:     target,
		Action:     ActionBackedUp,
		State:      StateManaged,
		Path:       definition.entryPath,
		BackupPath: backupPath,
	}, nil
}

func (manager *Manager) uninstallTarget(
	record *ownershipRecord,
	target Target,
) (*Change, error) {
	definition, _ := manager.definition(target)
	stored, isRecorded := record.Targets[string(target)]
	if !isRecorded {
		return &Change{
			Target: target,
			Action: ActionPreserved,
			State:  StateNotInstalled,
			Path:   definition.entryPath,
		}, nil
	}
	inspection, err := manager.inspectEntry(definition)
	if err != nil {
		return nil, fmt.Errorf("inspect %s skill entry: %w", target, err)
	}
	unchanged := inspection.desired
	if definition.kind == EntryCopy {
		unchanged = unchanged && inspection.hash == stored.InstalledSkillSHA256
	}
	if !unchanged {
		state := StateModified
		if !inspection.exists || inspection.state == StateBroken {
			state = StateBroken
		}
		return &Change{
			Target:     target,
			Action:     ActionPreserved,
			State:      state,
			Path:       definition.entryPath,
			BackupPath: stored.BackupPath,
		}, nil
	}
	if stored.BackupPath != "" {
		if _, err := os.Lstat(stored.BackupPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return &Change{
					Target:     target,
					Action:     ActionPreserved,
					State:      StateBroken,
					Path:       definition.entryPath,
					BackupPath: stored.BackupPath,
				}, nil
			}
			return nil, err
		}
	}

	record.Pending = &pendingRecord{
		Operation:                   pendingUninstall,
		Phase:                       pendingPrepared,
		Target:                      target,
		Path:                        definition.entryPath,
		Kind:                        definition.kind,
		ExpectedCanonicalLinkTarget: manager.expectedLinkTarget(definition),
		InstalledSkillSHA256:        stored.InstalledSkillSHA256,
		BackupPath:                  stored.BackupPath,
	}
	if err := manager.writeRecord(record); err != nil {
		return nil, err
	}
	if err := manager.hitFailpoint(failAfterUninstallRecord); err != nil {
		return nil, err
	}
	if err := manager.removeDesiredEntry(definition); err != nil {
		return nil, fmt.Errorf("remove managed %s skill entry: %w", target, err)
	}
	record.Pending.Phase = pendingRemoved
	if err := manager.writeRecord(record); err != nil {
		return nil, err
	}
	if err := manager.hitFailpoint(failAfterEntryRemoval); err != nil {
		return nil, err
	}
	action := ActionRemoved
	if stored.BackupPath != "" {
		if err := os.Rename(stored.BackupPath, definition.entryPath); err != nil {
			return nil, fmt.Errorf("restore %s skill backup: %w", target, err)
		}
		if err := syncDirectory(filepath.Dir(definition.entryPath)); err != nil {
			return nil, err
		}
		action = ActionRestored
	}
	delete(record.Targets, string(target))
	record.Pending = nil
	if err := manager.writeRecord(record); err != nil {
		return nil, err
	}
	return &Change{
		Target: target,
		Action: action,
		State:  StateNotInstalled,
		Path:   definition.entryPath,
	}, nil
}

func (manager *Manager) recoverPending(record *ownershipRecord) (*Change, error) {
	if record.Pending == nil {
		return nil, nil
	}
	pending := *record.Pending
	definition, _ := manager.definition(pending.Target)
	if pending.Operation == pendingReplace {
		return manager.recoverReplacement(record, definition, pending)
	}
	return manager.recoverUninstall(record, definition, pending)
}

func (manager *Manager) recoverReplacement(
	record *ownershipRecord,
	definition targetDefinition,
	pending pendingRecord,
) (*Change, error) {
	_, backupErr := os.Lstat(pending.BackupPath)
	backupExists := backupErr == nil
	if backupErr != nil && !errors.Is(backupErr, os.ErrNotExist) {
		return nil, backupErr
	}
	inspection, err := manager.inspectEntry(definition)
	if err != nil {
		return nil, err
	}
	if !backupExists {
		if pending.Phase == pendingPrepared && inspection.exists && !inspection.desired {
			record.Pending = nil
			if err := manager.writeRecord(record); err != nil {
				return nil, err
			}
			return &Change{
				Target: pending.Target,
				Action: ActionRecovered,
				State:  inspection.state,
				Path:   definition.entryPath,
			}, nil
		}
		return nil, &PendingConflictError{
			Target: pending.Target,
			Reason: "the recorded backup is missing",
		}
	}
	if !inspection.exists {
		if err := manager.installDesiredEntry(definition); err != nil {
			return nil, fmt.Errorf("recover %s replacement: %w", pending.Target, err)
		}
		inspection, err = manager.inspectEntry(definition)
		if err != nil {
			return nil, err
		}
	}
	if !inspection.desired ||
		(definition.kind == EntryCopy && inspection.hash != manager.skillHash) {
		return nil, &PendingConflictError{
			Target: pending.Target,
			Reason: "the target and backup no longer identify one recoverable replacement",
		}
	}
	record.Targets[string(pending.Target)] = manager.completedTargetRecord(
		definition,
		pending.BackupPath,
	)
	record.Pending = nil
	if err := manager.writeRecord(record); err != nil {
		return nil, err
	}
	return &Change{
		Target:     pending.Target,
		Action:     ActionRecovered,
		State:      StateManaged,
		Path:       definition.entryPath,
		BackupPath: pending.BackupPath,
	}, nil
}

func (manager *Manager) recoverUninstall(
	record *ownershipRecord,
	definition targetDefinition,
	pending pendingRecord,
) (*Change, error) {
	inspection, err := manager.inspectEntry(definition)
	if err != nil {
		return nil, err
	}
	if pending.Phase == pendingPrepared && inspection.desired {
		record.Pending = nil
		if err := manager.writeRecord(record); err != nil {
			return nil, err
		}
		return &Change{
			Target: pending.Target,
			Action: ActionRecovered,
			State:  StateManaged,
			Path:   definition.entryPath,
		}, nil
	}
	if inspection.exists {
		return nil, &PendingConflictError{
			Target: pending.Target,
			Reason: "the target changed after managed removal",
		}
	}
	action := ActionRemoved
	if pending.BackupPath != "" {
		if _, err := os.Lstat(pending.BackupPath); err != nil {
			return nil, &PendingConflictError{
				Target: pending.Target,
				Reason: "the backup needed for restoration is missing",
			}
		}
		if err := os.Rename(pending.BackupPath, definition.entryPath); err != nil {
			return nil, err
		}
		if err := syncDirectory(filepath.Dir(definition.entryPath)); err != nil {
			return nil, err
		}
		action = ActionRestored
	}
	delete(record.Targets, string(pending.Target))
	record.Pending = nil
	if err := manager.writeRecord(record); err != nil {
		return nil, err
	}
	return &Change{
		Target: pending.Target,
		Action: action,
		State:  StateNotInstalled,
		Path:   definition.entryPath,
	}, nil
}

func (manager *Manager) installDesiredEntry(definition targetDefinition) error {
	parent := filepath.Dir(definition.entryPath)
	if err := ensurePrivateDirectory(parent); err != nil {
		return err
	}
	if definition.kind == EntryLink {
		temporaryPath, err := manager.unusedSibling(definition.entryPath, ".link-")
		if err != nil {
			return err
		}
		if err := os.Symlink(manager.canonicalDirectory(), temporaryPath); err != nil {
			return err
		}
		renamed := false
		defer func() {
			if !renamed {
				_ = os.Remove(temporaryPath)
			}
		}()
		if err := os.Rename(temporaryPath, definition.entryPath); err != nil {
			return err
		}
		renamed = true
		return syncDirectory(parent)
	}

	temporaryPath, err := manager.unusedSibling(definition.entryPath, ".copy-")
	if err != nil {
		return err
	}
	if err := os.Mkdir(temporaryPath, 0o700); err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(filepath.Join(temporaryPath, "SKILL.md"))
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := os.Chmod(temporaryPath, 0o700); err != nil {
		return err
	}
	if err := manager.atomicWrite(
		filepath.Join(temporaryPath, "SKILL.md"),
		manager.skill,
		0o600,
	); err != nil {
		return err
	}
	if err := syncDirectory(temporaryPath); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, definition.entryPath); err != nil {
		return err
	}
	renamed = true
	return syncDirectory(parent)
}

func (manager *Manager) removeDesiredEntry(definition targetDefinition) error {
	if definition.kind == EntryLink {
		if err := os.Remove(definition.entryPath); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(definition.entryPath))
	}
	if err := os.Remove(filepath.Join(definition.entryPath, "SKILL.md")); err != nil {
		return err
	}
	if err := os.Remove(definition.entryPath); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(definition.entryPath))
}

func (manager *Manager) cleanupCanonical(record *ownershipRecord) (bool, error) {
	if len(record.Targets) != 0 || record.Pending != nil {
		return false, nil
	}
	directoryInfo, err := os.Lstat(manager.canonicalDirectory())
	if errors.Is(err, os.ErrNotExist) {
		return manager.removeRecord()
	}
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return false, err
	}
	entries, err := os.ReadDir(manager.canonicalDirectory())
	if err != nil {
		return false, err
	}
	if len(entries) != 1 || entries[0].Name() != "SKILL.md" {
		return false, nil
	}
	hash, info, err := hashFile(manager.canonicalPath())
	if err != nil ||
		hash != record.CanonicalSkillSHA256 ||
		info.Mode().Perm() != 0o600 {
		return false, err
	}
	if err := os.Remove(manager.canonicalPath()); err != nil {
		return false, err
	}
	if err := os.Remove(manager.canonicalDirectory()); err != nil {
		return false, err
	}
	if err := syncDirectory(filepath.Dir(manager.canonicalDirectory())); err != nil {
		return false, err
	}
	removed, err := manager.removeRecord()
	return removed, err
}

func (manager *Manager) removeRecord() (bool, error) {
	if err := os.Remove(manager.recordPath()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	if err := syncDirectory(filepath.Dir(manager.recordPath())); err != nil {
		return false, err
	}
	return true, nil
}

func (manager *Manager) completedTargetRecord(
	definition targetDefinition,
	backupPath string,
) targetRecord {
	return targetRecord{
		Path:                        definition.entryPath,
		Kind:                        definition.kind,
		ExpectedCanonicalLinkTarget: manager.expectedLinkTarget(definition),
		InstalledSkillSHA256:        manager.skillHash,
		State:                       StateManaged,
		BackupPath:                  backupPath,
	}
}

func (manager *Manager) expectedLinkTarget(definition targetDefinition) string {
	if definition.kind == EntryLink {
		return manager.canonicalDirectory()
	}
	return ""
}

func (manager *Manager) allocateBackupPath(targetPath string) (string, error) {
	timestamp := manager.now().UTC().Format("20060102T150405Z")
	for attempt := 0; attempt < 32; attempt++ {
		random := make([]byte, 6)
		if _, err := io.ReadFull(manager.random, random); err != nil {
			return "", fmt.Errorf("generate skill backup name: %w", err)
		}
		candidate := filepath.Join(
			filepath.Dir(targetPath),
			fmt.Sprintf(
				"%s.mdreview-backup-%s-%x",
				filepath.Base(targetPath),
				timestamp,
				random,
			),
		)
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate an unused skill backup path")
}

func (manager *Manager) hitFailpoint(name string) error {
	if manager.fail == nil {
		return nil
	}
	return manager.fail(name)
}
