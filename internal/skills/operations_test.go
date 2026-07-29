package skills

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestInstallConflictRequiresAuthorizationAndPreservesBackup(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	codex, _ := manager.definition(TargetCodex)
	writeUserDirectory(t, codex.entryPath, "user.txt", "keep me")

	if _, err := manager.Install(installRequests(false, TargetCodex)); err == nil {
		t.Fatal("Install replaced a conflict without authorization")
	} else {
		var conflict *TargetConflictError
		if !errors.As(err, &conflict) || conflict.Target != TargetCodex {
			t.Fatalf("Install conflict error = %T %v", err, err)
		}
	}
	if content, err := os.ReadFile(filepath.Join(codex.entryPath, "user.txt")); err != nil {
		t.Fatalf("read preserved conflict: %v", err)
	} else if string(content) != "keep me" {
		t.Fatalf("preserved conflict = %q", content)
	}

	result, err := manager.Install(installRequests(true, TargetCodex))
	if err != nil {
		t.Fatalf("force install conflict: %v", err)
	}
	change := result.Changes[len(result.Changes)-1]
	if change.Action != ActionBackedUp || change.BackupPath == "" {
		t.Fatalf("force install change = %+v", change)
	}
	if linkTarget, err := os.Readlink(codex.entryPath); err != nil {
		t.Fatalf("read installed Codex link: %v", err)
	} else if linkTarget != manager.canonicalDirectory() {
		t.Fatalf("installed Codex link = %q", linkTarget)
	}
	if content, err := os.ReadFile(filepath.Join(change.BackupPath, "user.txt")); err != nil {
		t.Fatalf("read conflict backup: %v", err)
	} else if string(content) != "keep me" {
		t.Fatalf("backup content = %q", content)
	}
	if !strings.Contains(
		filepath.Base(change.BackupPath),
		"mdreview.mdreview-backup-20260729T103456Z-",
	) {
		t.Fatalf("backup name = %q", filepath.Base(change.BackupPath))
	}
}

func TestUninstallRestoresRecordedConflictBackupAndCleansCanonical(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	codex, _ := manager.definition(TargetCodex)
	writeUserDirectory(t, codex.entryPath, "user.txt", "restore me")
	installed, err := manager.Install(installRequests(true, TargetCodex))
	if err != nil {
		t.Fatalf("force install: %v", err)
	}
	backupPath := installed.Changes[len(installed.Changes)-1].BackupPath

	result, err := manager.Uninstall([]Target{TargetCodex})
	if err != nil {
		t.Fatalf("uninstall restored target: %v", err)
	}
	if len(result.Changes) != 1 || result.Changes[0].Action != ActionRestored {
		t.Fatalf("uninstall changes = %+v", result.Changes)
	}
	if !result.CanonicalRemoved {
		t.Fatal("uninstall did not remove unchanged unused canonical skill")
	}
	if content, err := os.ReadFile(filepath.Join(codex.entryPath, "user.txt")); err != nil {
		t.Fatalf("read restored user entry: %v", err)
	} else if string(content) != "restore me" {
		t.Fatalf("restored user entry = %q", content)
	}
	if _, err := os.Lstat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup still exists after restoration: %v", err)
	}
	if _, err := os.Lstat(manager.canonicalDirectory()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canonical directory still exists: %v", err)
	}
	if _, err := os.Lstat(manager.recordPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ownership record still exists: %v", err)
	}
	if _, err := os.Lstat(manager.lockPath()); err != nil {
		t.Fatalf("stable lock was removed: %v", err)
	}
}

func TestUninstallRestoresRecordedClaudeDirectoryBackup(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	claude, _ := manager.definition(TargetClaude)
	writeUserDirectory(t, claude.entryPath, "user.txt", "Claude user directory")
	if _, err := manager.Install(installRequests(true, TargetClaude)); err != nil {
		t.Fatalf("force install Claude copy: %v", err)
	}
	result, err := manager.Uninstall([]Target{TargetClaude})
	if err != nil {
		t.Fatalf("uninstall Claude copy: %v", err)
	}
	if len(result.Changes) != 1 || result.Changes[0].Action != ActionRestored {
		t.Fatalf("Claude uninstall changes = %+v", result.Changes)
	}
	if content, err := os.ReadFile(filepath.Join(claude.entryPath, "user.txt")); err != nil {
		t.Fatalf("read restored Claude directory: %v", err)
	} else if string(content) != "Claude user directory" {
		t.Fatalf("restored Claude content = %q", content)
	}
	if _, err := os.Lstat(filepath.Join(claude.entryPath, "SKILL.md")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("managed Claude copy survived restoration: %v", err)
	}
}

func TestInstallAdoptsExactUnrecordedLinkButNotUnrecordedClaudeCopy(t *testing.T) {
	t.Run("link", func(t *testing.T) {
		environment := newTestEnvironment(t)
		manager := environment.manager(t, testSkillV1)
		codex, _ := manager.definition(TargetCodex)
		if err := os.MkdirAll(filepath.Dir(codex.entryPath), 0o700); err != nil {
			t.Fatalf("create Codex parent: %v", err)
		}
		if err := os.Symlink(manager.canonicalDirectory(), codex.entryPath); err != nil {
			t.Fatalf("create exact unrecorded link: %v", err)
		}
		result, err := manager.Install(installRequests(false, TargetCodex))
		if err != nil {
			t.Fatalf("adopt exact link: %v", err)
		}
		if result.Changes[len(result.Changes)-1].Action != ActionAdopted {
			t.Fatalf("adoption changes = %+v", result.Changes)
		}
		record, err := manager.loadRecord()
		if err != nil {
			t.Fatalf("load adopted record: %v", err)
		}
		if _, ok := record.Targets[string(TargetCodex)]; !ok {
			t.Fatal("adopted link was not recorded")
		}
	})

	t.Run("Claude copy", func(t *testing.T) {
		environment := newTestEnvironment(t)
		manager := environment.manager(t, testSkillV1)
		claude, _ := manager.definition(TargetClaude)
		writeUserDirectory(t, claude.entryPath, "SKILL.md", string(testSkillV1))
		assertMode(t, claude.entryPath, 0o700)
		assertMode(t, filepath.Join(claude.entryPath, "SKILL.md"), 0o600)

		if _, err := manager.Install(installRequests(false, TargetClaude)); err == nil {
			t.Fatal("Install adopted an unrecorded Claude directory")
		}
		result, err := manager.Install(installRequests(true, TargetClaude))
		if err != nil {
			t.Fatalf("back up unrecorded Claude directory: %v", err)
		}
		change := result.Changes[len(result.Changes)-1]
		if change.Action != ActionBackedUp {
			t.Fatalf("Claude replacement change = %+v", change)
		}
		if content, err := os.ReadFile(filepath.Join(change.BackupPath, "SKILL.md")); err != nil {
			t.Fatalf("read Claude backup: %v", err)
		} else if !bytes.Equal(content, testSkillV1) {
			t.Fatalf("Claude backup = %q", content)
		}
	})
}

func TestInstallPreservesBrokenLinkUntilForceBacksItUp(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	gemini, _ := manager.definition(TargetGemini)
	if err := os.MkdirAll(filepath.Dir(gemini.entryPath), 0o700); err != nil {
		t.Fatalf("create Gemini parent: %v", err)
	}
	const missingTarget = "/definitely/missing/mdreview-skill"
	if err := os.Symlink(missingTarget, gemini.entryPath); err != nil {
		t.Fatalf("create broken Gemini link: %v", err)
	}
	if _, err := manager.Install(installRequests(false, TargetGemini)); err == nil {
		t.Fatal("Install replaced a broken link without force")
	}
	if target, err := os.Readlink(gemini.entryPath); err != nil || target != missingTarget {
		t.Fatalf("broken link changed to %q, error %v", target, err)
	}
	result, err := manager.Install(installRequests(true, TargetGemini))
	if err != nil {
		t.Fatalf("force install broken link: %v", err)
	}
	backup := result.Changes[len(result.Changes)-1].BackupPath
	if target, err := os.Readlink(backup); err != nil || target != missingTarget {
		t.Fatalf("broken-link backup = %q, error %v", target, err)
	}
}

func TestCanonicalModificationBlocksEveryInstallIncludingForce(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	if _, err := manager.Install(installRequests(false, TargetCodex)); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	userCanonical := []byte("user canonical modification\n")
	if err := os.WriteFile(manager.canonicalPath(), userCanonical, 0o600); err != nil {
		t.Fatalf("modify canonical skill: %v", err)
	}
	if _, err := manager.Install(installRequests(true, TargetGemini)); err == nil {
		t.Fatal("force Install overwrote modified canonical content")
	} else {
		var modified *CanonicalModifiedError
		if !errors.As(err, &modified) {
			t.Fatalf("modified canonical error = %T %v", err, err)
		}
	}
	if content, err := os.ReadFile(manager.canonicalPath()); err != nil {
		t.Fatalf("read modified canonical: %v", err)
	} else if !bytes.Equal(content, userCanonical) {
		t.Fatalf("modified canonical was overwritten with %q", content)
	}
	gemini, _ := manager.definition(TargetGemini)
	if _, err := os.Lstat(gemini.entryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked install created Gemini entry: %v", err)
	}
}

func TestMultiTargetInstallKeepsEarlierSuccessAccuratelyRecorded(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	claude, _ := manager.definition(TargetClaude)
	writeUserDirectory(t, claude.entryPath, "user.txt", "conflict")

	result, err := manager.Install(installRequests(false, TargetCodex, TargetClaude, TargetGemini))
	if err == nil {
		t.Fatal("multi-target Install did not report Claude conflict")
	}
	if len(result.Changes) != 1 || result.Changes[0].Target != TargetCodex {
		t.Fatalf("partial Install changes = %+v", result.Changes)
	}
	record, loadErr := manager.loadRecord()
	if loadErr != nil {
		t.Fatalf("load partial ownership record: %v", loadErr)
	}
	if _, ok := record.Targets[string(TargetCodex)]; !ok {
		t.Fatal("earlier Codex success was not recorded")
	}
	if _, ok := record.Targets[string(TargetClaude)]; ok {
		t.Fatal("conflicting Claude target was recorded")
	}
	if _, ok := record.Targets[string(TargetGemini)]; ok {
		t.Fatal("later Gemini target was processed after error")
	}
	if content, readErr := os.ReadFile(filepath.Join(claude.entryPath, "user.txt")); readErr != nil {
		t.Fatalf("read preserved Claude conflict: %v", readErr)
	} else if string(content) != "conflict" {
		t.Fatalf("Claude conflict = %q", content)
	}
}

func TestInstallConflictAuthorizationIsScopedToItsExactTarget(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	codex, _ := manager.definition(TargetCodex)
	claude, _ := manager.definition(TargetClaude)
	writeUserDirectory(t, codex.entryPath, "codex.txt", "authorized conflict")
	createdClaudeConflict := false
	manager.fail = func(point string) error {
		if point == failAfterEntryInstall && !createdClaudeConflict {
			writeUserDirectory(t, claude.entryPath, "claude.txt", "new conflict")
			createdClaudeConflict = true
		}
		return nil
	}

	result, err := manager.Install([]InstallRequest{
		{Target: TargetCodex, AllowConflictBackup: true},
		{Target: TargetClaude, AllowConflictBackup: false},
	})
	manager.fail = nil
	if err == nil {
		t.Fatal("Install replaced a newly conflicting unauthorized target")
	}
	var conflict *TargetConflictError
	if !errors.As(err, &conflict) || conflict.Target != TargetClaude {
		t.Fatalf("second-target conflict error = %T %v", err, err)
	}
	if !createdClaudeConflict {
		t.Fatal("test did not create the second-target conflict after installation began")
	}
	if len(result.Changes) != 1 ||
		result.Changes[0].Target != TargetCodex ||
		result.Changes[0].Action != ActionBackedUp {
		t.Fatalf("per-target authorization changes = %+v", result.Changes)
	}
	if content, err := os.ReadFile(filepath.Join(claude.entryPath, "claude.txt")); err != nil {
		t.Fatalf("read preserved unauthorized Claude conflict: %v", err)
	} else if string(content) != "new conflict" {
		t.Fatalf("unauthorized Claude conflict = %q", content)
	}
	if backups, err := filepath.Glob(claude.entryPath + ".mdreview-backup-*"); err != nil {
		t.Fatalf("glob Claude backups: %v", err)
	} else if len(backups) != 0 {
		t.Fatalf("unauthorized Claude conflict was moved to backups: %v", backups)
	}
	record, err := manager.loadRecord()
	if err != nil {
		t.Fatalf("load per-target authorization record: %v", err)
	}
	if _, ok := record.Targets[string(TargetClaude)]; ok {
		t.Fatal("unauthorized conflicting Claude target was recorded")
	}
}

func TestModifiedRecordedEntriesArePreservedByUninstall(t *testing.T) {
	tests := []struct {
		name   string
		target Target
		modify func(*testing.T, targetDefinition)
	}{
		{
			name:   "retargeted link",
			target: TargetCodex,
			modify: func(t *testing.T, definition targetDefinition) {
				t.Helper()
				if err := os.Remove(definition.entryPath); err != nil {
					t.Fatalf("remove managed link: %v", err)
				}
				if err := os.Symlink(filepath.Dir(definition.entryPath), definition.entryPath); err != nil {
					t.Fatalf("retarget managed link: %v", err)
				}
			},
		},
		{
			name:   "modified Claude copy",
			target: TargetClaude,
			modify: func(t *testing.T, definition targetDefinition) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(definition.entryPath, "SKILL.md"),
					[]byte("user change\n"),
					0o600,
				); err != nil {
					t.Fatalf("modify Claude copy: %v", err)
				}
			},
		},
		{
			name:   "unexpected Claude entry",
			target: TargetClaude,
			modify: func(t *testing.T, definition targetDefinition) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(definition.entryPath, "notes.txt"),
					[]byte("user file\n"),
					0o600,
				); err != nil {
					t.Fatalf("add Claude entry: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := newTestEnvironment(t)
			manager := environment.manager(t, testSkillV1)
			if _, err := manager.Install(installRequests(false, test.target)); err != nil {
				t.Fatalf("initial install: %v", err)
			}
			definition, _ := manager.definition(test.target)
			test.modify(t, definition)

			result, err := manager.Uninstall([]Target{test.target})
			if err != nil {
				t.Fatalf("preserving uninstall: %v", err)
			}
			if len(result.Changes) != 1 ||
				result.Changes[0].Action != ActionPreserved ||
				result.Changes[0].State != StateModified {
				t.Fatalf("preserving uninstall changes = %+v", result.Changes)
			}
			if _, err := os.Lstat(definition.entryPath); err != nil {
				t.Fatalf("modified target was removed: %v", err)
			}
			record, err := manager.loadRecord()
			if err != nil {
				t.Fatalf("load preserved record: %v", err)
			}
			if _, ok := record.Targets[string(test.target)]; !ok {
				t.Fatal("modified target ownership was forgotten")
			}
		})
	}
}

func TestUninstallNeverRemovesUnrecordedEntry(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	gemini, _ := manager.definition(TargetGemini)
	writeUserDirectory(t, gemini.entryPath, "user.txt", "unowned")
	result, err := manager.Uninstall([]Target{TargetGemini})
	if err != nil {
		t.Fatalf("uninstall unowned target: %v", err)
	}
	if len(result.Changes) != 1 || result.Changes[0].Action != ActionPreserved {
		t.Fatalf("unowned uninstall changes = %+v", result.Changes)
	}
	if content, err := os.ReadFile(filepath.Join(gemini.entryPath, "user.txt")); err != nil {
		t.Fatalf("unowned entry was removed: %v", err)
	} else if string(content) != "unowned" {
		t.Fatalf("unowned content = %q", content)
	}
}

func TestUninstallPreservesManagedEntryWhenRecordedBackupIsMissing(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	codex, _ := manager.definition(TargetCodex)
	writeUserDirectory(t, codex.entryPath, "user.txt", "backup")
	installed, err := manager.Install(installRequests(true, TargetCodex))
	if err != nil {
		t.Fatalf("force install: %v", err)
	}
	backup := installed.Changes[len(installed.Changes)-1].BackupPath
	if err := os.Remove(filepath.Join(backup, "user.txt")); err != nil {
		t.Fatalf("remove backup content: %v", err)
	}
	if err := os.Remove(backup); err != nil {
		t.Fatalf("remove backup directory: %v", err)
	}

	result, err := manager.Uninstall([]Target{TargetCodex})
	if err != nil {
		t.Fatalf("uninstall with missing backup: %v", err)
	}
	if result.Changes[0].Action != ActionPreserved ||
		result.Changes[0].State != StateBroken {
		t.Fatalf("missing-backup uninstall = %+v", result.Changes)
	}
	if _, err := os.Readlink(codex.entryPath); err != nil {
		t.Fatalf("managed link was removed despite missing backup: %v", err)
	}
}

func TestCanonicalCleanupRetainsModifiedOrUnexpectedContent(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, *Manager)
	}{
		{
			name: "modified skill",
			change: func(t *testing.T, manager *Manager) {
				t.Helper()
				if err := os.WriteFile(manager.canonicalPath(), []byte("modified\n"), 0o600); err != nil {
					t.Fatalf("modify canonical: %v", err)
				}
			},
		},
		{
			name: "unexpected entry",
			change: func(t *testing.T, manager *Manager) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(manager.canonicalDirectory(), "user.txt"),
					[]byte("keep\n"),
					0o600,
				); err != nil {
					t.Fatalf("add canonical entry: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := newTestEnvironment(t)
			manager := environment.manager(t, testSkillV1)
			if _, err := manager.Install(installRequests(false, TargetCodex)); err != nil {
				t.Fatalf("install target: %v", err)
			}
			test.change(t, manager)
			result, err := manager.Uninstall([]Target{TargetCodex})
			if err != nil {
				t.Fatalf("uninstall with retained canonical: %v", err)
			}
			if result.CanonicalRemoved {
				t.Fatal("unsafe canonical content was removed")
			}
			if _, err := os.Lstat(manager.canonicalDirectory()); err != nil {
				t.Fatalf("retained canonical directory is missing: %v", err)
			}
			if _, err := os.Lstat(manager.recordPath()); err != nil {
				t.Fatalf("ownership record for retained canonical is missing: %v", err)
			}
		})
	}
}

func TestExistingParentModesAreNotChanged(t *testing.T) {
	environment := newTestEnvironment(t)
	claudeRoot := filepath.Join(environment.home, ".claude")
	if err := os.Mkdir(claudeRoot, 0o755); err != nil {
		t.Fatalf("create existing Claude root: %v", err)
	}
	manager := environment.manager(t, testSkillV1)
	if _, err := manager.Install(installRequests(false, TargetClaude)); err != nil {
		t.Fatalf("install Claude copy: %v", err)
	}
	assertMode(t, environment.home, 0o750)
	assertMode(t, claudeRoot, 0o755)
	assertMode(t, filepath.Join(claudeRoot, "skills"), 0o700)
}

func TestConcurrentInstallersSerializeWithoutLosingTargetRecords(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	var wait sync.WaitGroup
	wait.Add(2)
	errorsByTarget := make(chan error, 2)
	for _, target := range []Target{TargetCodex, TargetGemini} {
		target := target
		go func() {
			defer wait.Done()
			_, err := manager.Install(installRequests(false, target))
			errorsByTarget <- err
		}()
	}
	wait.Wait()
	close(errorsByTarget)
	for err := range errorsByTarget {
		if err != nil {
			t.Errorf("concurrent Install: %v", err)
		}
	}
	record, err := manager.loadRecord()
	if err != nil {
		t.Fatalf("load concurrent ownership record: %v", err)
	}
	for _, target := range []Target{TargetCodex, TargetGemini} {
		if _, ok := record.Targets[string(target)]; !ok {
			t.Errorf("concurrent record is missing %s", target)
		}
	}
}

func writeUserDirectory(
	t *testing.T,
	directory string,
	name string,
	content string,
) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create user directory %q: %v", directory, err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatalf("set user directory mode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write user entry: %v", err)
	}
}
