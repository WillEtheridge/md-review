package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type entryInspection struct {
	exists  bool
	desired bool
	hash    string
	state   State
}

// Status reports the canonical asset and all host entries without creating a
// lock, directory, file, or recovery write.
func (manager *Manager) Status() (Snapshot, error) {
	record, err := manager.loadRecord()
	if err != nil {
		return Snapshot{}, err
	}
	canonical, err := manager.inspectCanonical(record)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{
		Canonical: canonical,
		Targets:   make([]TargetStatus, 0, len(supportedTargets())),
	}
	for _, definition := range manager.definitions() {
		var stored *targetRecord
		if record != nil {
			if entry, ok := record.Targets[string(definition.target)]; ok {
				entryCopy := entry
				stored = &entryCopy
			}
		}
		status, err := manager.inspectTargetStatus(definition, stored, canonical)
		if err != nil {
			return Snapshot{}, fmt.Errorf("inspect %s skill entry: %w", definition.target, err)
		}
		snapshot.Targets = append(snapshot.Targets, status)
	}
	if record != nil && record.Pending != nil {
		snapshot.Pending = &PendingStatus{
			Target:    record.Pending.Target,
			Operation: record.Pending.Operation,
			Phase:     record.Pending.Phase,
		}
		for index := range snapshot.Targets {
			if snapshot.Targets[index].Target == record.Pending.Target {
				snapshot.Targets[index].State = StatePending
			}
		}
	}
	return snapshot, nil
}

func (manager *Manager) inspectCanonical(record *ownershipRecord) (CanonicalStatus, error) {
	status := CanonicalStatus{
		Path:  manager.canonicalPath(),
		State: StateNotInstalled,
	}
	directoryInfo, err := os.Lstat(manager.canonicalDirectory())
	switch {
	case errors.Is(err, os.ErrNotExist):
		if record != nil {
			status.State = StateBroken
		}
		return status, nil
	case err != nil:
		return CanonicalStatus{}, err
	case !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0:
		status.State = StateConflicting
		if record != nil {
			status.State = StateModified
		}
		return status, nil
	}

	actualHash, skillInfo, err := hashFile(manager.canonicalPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
		if record == nil {
			status.State = StateConflicting
		} else {
			status.State = StateBroken
		}
		return status, nil
	case err != nil:
		status.State = StateConflicting
		if record != nil {
			status.State = StateModified
		}
		return status, nil
	}
	status.SHA256 = actualHash
	if record == nil {
		status.State = StateConflicting
		return status, nil
	}
	if actualHash != record.CanonicalSkillSHA256 {
		if actualHash == manager.skillHash {
			status.State = StateOutdated
		} else {
			status.State = StateModified
		}
		return status, nil
	}
	if actualHash != manager.skillHash {
		status.State = StateOutdated
		return status, nil
	}
	entries, err := os.ReadDir(manager.canonicalDirectory())
	if err != nil {
		return CanonicalStatus{}, err
	}
	if directoryInfo.Mode().Perm() != 0o700 ||
		skillInfo.Mode().Perm() != 0o600 ||
		len(entries) != 1 ||
		entries[0].Name() != "SKILL.md" {
		status.State = StateModified
		return status, nil
	}
	status.State = StateManaged
	return status, nil
}

func (manager *Manager) inspectTargetStatus(
	definition targetDefinition,
	stored *targetRecord,
	canonical CanonicalStatus,
) (TargetStatus, error) {
	inspection, err := manager.inspectEntry(definition)
	if err != nil {
		return TargetStatus{}, err
	}
	status := TargetStatus{
		Target: definition.target,
		Kind:   definition.kind,
		Path:   definition.entryPath,
		State:  inspection.state,
	}
	if stored == nil {
		if !inspection.exists {
			status.State = StateNotInstalled
		} else if inspection.state != StateBroken {
			status.State = StateConflicting
		}
		return status, nil
	}
	status.BackupPath = stored.BackupPath
	if !inspection.exists {
		status.State = StateBroken
		return status, nil
	}
	if !inspection.desired {
		if inspection.state == StateBroken {
			status.State = StateBroken
		} else {
			status.State = StateModified
		}
		return status, nil
	}
	if definition.kind == EntryCopy && inspection.hash != stored.InstalledSkillSHA256 {
		status.State = StateModified
		return status, nil
	}
	if definition.kind == EntryLink {
		switch canonical.State {
		case StateBroken:
			status.State = StateBroken
			return status, nil
		case StateModified, StateConflicting:
			status.State = StateModified
			return status, nil
		}
	}
	if stored.InstalledSkillSHA256 != manager.skillHash ||
		(definition.kind == EntryCopy && inspection.hash != manager.skillHash) ||
		canonical.State == StateOutdated {
		status.State = StateOutdated
		return status, nil
	}
	status.State = StateManaged
	return status, nil
}

func (manager *Manager) inspectEntry(definition targetDefinition) (entryInspection, error) {
	info, err := os.Lstat(definition.entryPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return entryInspection{state: StateNotInstalled}, nil
	case err != nil:
		return entryInspection{}, err
	}
	if definition.kind == EntryLink {
		if info.Mode()&os.ModeSymlink == 0 {
			return entryInspection{exists: true, state: StateConflicting}, nil
		}
		target, err := os.Readlink(definition.entryPath)
		if err != nil {
			return entryInspection{}, err
		}
		if target == manager.canonicalDirectory() && filepath.IsAbs(target) {
			return entryInspection{
				exists:  true,
				desired: true,
				hash:    manager.skillHash,
				state:   StateManaged,
			}, nil
		}
		if _, err := os.Stat(definition.entryPath); errors.Is(err, os.ErrNotExist) {
			return entryInspection{exists: true, state: StateBroken}, nil
		} else if err != nil {
			return entryInspection{}, err
		}
		return entryInspection{exists: true, state: StateConflicting}, nil
	}

	if info.Mode()&os.ModeSymlink != 0 {
		if _, err := os.Stat(definition.entryPath); errors.Is(err, os.ErrNotExist) {
			return entryInspection{exists: true, state: StateBroken}, nil
		} else if err != nil {
			return entryInspection{}, err
		}
		return entryInspection{exists: true, state: StateConflicting}, nil
	}
	if !info.IsDir() {
		return entryInspection{exists: true, state: StateConflicting}, nil
	}
	entries, err := os.ReadDir(definition.entryPath)
	if err != nil {
		return entryInspection{}, err
	}
	if info.Mode().Perm() != 0o700 ||
		len(entries) != 1 ||
		entries[0].Name() != "SKILL.md" {
		return entryInspection{exists: true, state: StateConflicting}, nil
	}
	hash, skillInfo, err := hashFile(filepath.Join(definition.entryPath, "SKILL.md"))
	if err != nil {
		return entryInspection{exists: true, state: StateConflicting}, nil
	}
	if skillInfo.Mode().Perm() != 0o600 {
		return entryInspection{exists: true, state: StateConflicting}, nil
	}
	return entryInspection{
		exists:  true,
		desired: true,
		hash:    hash,
		state:   StateManaged,
	}, nil
}
