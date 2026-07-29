package skills

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var errInjectedInterruption = errors.New("injected installer interruption")

func TestInterruptedReplacementRecoveryIsObservableAndUnambiguous(t *testing.T) {
	for _, failpoint := range []string{
		failAfterPendingRecord,
		failAfterBackupMove,
		failAfterEntryInstall,
	} {
		t.Run(failpoint, func(t *testing.T) {
			environment := newTestEnvironment(t)
			manager := environment.manager(t, testSkillV1)
			codex, _ := manager.definition(TargetCodex)
			writeUserDirectory(t, codex.entryPath, "user.txt", failpoint)
			manager.fail = func(point string) error {
				if point == failpoint {
					return errInjectedInterruption
				}
				return nil
			}
			if _, err := manager.Install(installRequests(true, TargetCodex)); !errors.Is(
				err,
				errInjectedInterruption,
			) {
				t.Fatalf("interrupted Install error = %v", err)
			}
			manager.fail = nil

			recordBefore, err := os.ReadFile(manager.recordPath())
			if err != nil {
				t.Fatalf("read pending record: %v", err)
			}
			snapshot, err := manager.Status()
			if err != nil {
				t.Fatalf("read pending status: %v", err)
			}
			if snapshot.Pending == nil ||
				snapshot.Pending.Target != TargetCodex ||
				snapshot.Targets[0].State != StatePending {
				t.Fatalf("pending snapshot = %+v", snapshot)
			}
			recordAfter, err := os.ReadFile(manager.recordPath())
			if err != nil {
				t.Fatalf("reread pending record: %v", err)
			}
			if !bytes.Equal(recordBefore, recordAfter) {
				t.Fatal("read-only Status recovered or rewrote pending state")
			}

			if _, err := manager.Install(installRequests(true, TargetCodex)); err != nil {
				t.Fatalf("recover interrupted Install: %v", err)
			}
			record, err := manager.loadRecord()
			if err != nil {
				t.Fatalf("load recovered record: %v", err)
			}
			if record.Pending != nil {
				t.Fatalf("recovered record still has pending state: %+v", record.Pending)
			}
			stored := record.Targets[string(TargetCodex)]
			if stored.BackupPath == "" {
				t.Fatal("recovered replacement did not retain a backup")
			}
			if content, err := os.ReadFile(filepath.Join(stored.BackupPath, "user.txt")); err != nil {
				t.Fatalf("read recovered backup: %v", err)
			} else if string(content) != failpoint {
				t.Fatalf("recovered backup content = %q", content)
			}
			if target, err := os.Readlink(codex.entryPath); err != nil {
				t.Fatalf("read recovered Codex link: %v", err)
			} else if target != manager.canonicalDirectory() {
				t.Fatalf("recovered Codex link = %q", target)
			}
		})
	}
}

func TestInterruptedReplacementWithUnexpectedTargetRemainsPending(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	codex, _ := manager.definition(TargetCodex)
	writeUserDirectory(t, codex.entryPath, "user.txt", "backup")
	manager.fail = func(point string) error {
		if point == failAfterBackupMove {
			return errInjectedInterruption
		}
		return nil
	}
	if _, err := manager.Install(installRequests(true, TargetCodex)); !errors.Is(
		err,
		errInjectedInterruption,
	) {
		t.Fatalf("interrupted Install error = %v", err)
	}
	manager.fail = nil
	if err := os.WriteFile(codex.entryPath, []byte("unexpected target\n"), 0o600); err != nil {
		t.Fatalf("write unexpected target: %v", err)
	}
	record, err := manager.loadRecord()
	if err != nil {
		t.Fatalf("load pending record: %v", err)
	}
	backup := record.Pending.BackupPath

	if _, err := manager.Install(installRequests(true, TargetCodex)); err == nil {
		t.Fatal("Install guessed through ambiguous pending state")
	} else {
		var pending *PendingConflictError
		if !errors.As(err, &pending) {
			t.Fatalf("ambiguous pending error = %T %v", err, err)
		}
	}
	if content, err := os.ReadFile(codex.entryPath); err != nil {
		t.Fatalf("read preserved unexpected target: %v", err)
	} else if string(content) != "unexpected target\n" {
		t.Fatalf("unexpected target = %q", content)
	}
	if content, err := os.ReadFile(filepath.Join(backup, "user.txt")); err != nil {
		t.Fatalf("read preserved pending backup: %v", err)
	} else if string(content) != "backup" {
		t.Fatalf("pending backup = %q", content)
	}
	record, err = manager.loadRecord()
	if err != nil {
		t.Fatalf("reload ambiguous pending record: %v", err)
	}
	if record.Pending == nil {
		t.Fatal("ambiguous pending state was discarded")
	}
}

func TestInterruptedUninstallRestoresBackupOnNextMutation(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	codex, _ := manager.definition(TargetCodex)
	writeUserDirectory(t, codex.entryPath, "user.txt", "restore after interruption")
	if _, err := manager.Install(installRequests(true, TargetCodex)); err != nil {
		t.Fatalf("force install: %v", err)
	}
	manager.fail = func(point string) error {
		if point == failAfterEntryRemoval {
			return errInjectedInterruption
		}
		return nil
	}
	if _, err := manager.Uninstall([]Target{TargetCodex}); !errors.Is(
		err,
		errInjectedInterruption,
	) {
		t.Fatalf("interrupted Uninstall error = %v", err)
	}
	manager.fail = nil
	if _, err := os.Lstat(codex.entryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target exists at removed barrier: %v", err)
	}

	result, err := manager.Uninstall([]Target{TargetCodex})
	if err != nil {
		t.Fatalf("recover interrupted Uninstall: %v", err)
	}
	if len(result.Changes) < 1 || result.Changes[0].Action != ActionRestored {
		t.Fatalf("recovered Uninstall changes = %+v", result.Changes)
	}
	if content, err := os.ReadFile(filepath.Join(codex.entryPath, "user.txt")); err != nil {
		t.Fatalf("read restored user entry: %v", err)
	} else if string(content) != "restore after interruption" {
		t.Fatalf("restored user entry = %q", content)
	}
}

func TestUnsafeOwnershipRecordsNeverGrantPathAuthority(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, *Manager, []byte) []byte
	}{
		{
			name: "unsupported schema",
			change: func(_ *testing.T, _ *Manager, record []byte) []byte {
				return bytes.Replace(record, []byte(`"schemaVersion": 1`), []byte(`"schemaVersion": 2`), 1)
			},
		},
		{
			name: "unknown field",
			change: func(_ *testing.T, _ *Manager, record []byte) []byte {
				return bytes.Replace(
					record,
					[]byte(`"schemaVersion": 1,`),
					[]byte(`"schemaVersion": 1, "unknown": true,`),
					1,
				)
			},
		},
		{
			name: "duplicate nested member",
			change: func(t *testing.T, manager *Manager, record []byte) []byte {
				t.Helper()
				definition, _ := manager.definition(TargetCodex)
				needle := []byte(`"path": "` + definition.entryPath + `"`)
				replacement := []byte(
					`"path": "` + definition.entryPath + `", "path": "` + definition.entryPath + `"`,
				)
				if !bytes.Contains(record, needle) {
					t.Fatalf("record has no target path %q", definition.entryPath)
				}
				return bytes.Replace(record, needle, replacement, 1)
			},
		},
		{
			name: "arbitrary stored target path",
			change: func(t *testing.T, manager *Manager, record []byte) []byte {
				t.Helper()
				definition, _ := manager.definition(TargetCodex)
				return bytes.Replace(
					record,
					[]byte(definition.entryPath),
					[]byte(filepath.Join(t.TempDir(), "outside")),
					1,
				)
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
			record, err := os.ReadFile(manager.recordPath())
			if err != nil {
				t.Fatalf("read ownership record: %v", err)
			}
			changed := test.change(t, manager, record)
			if err := os.WriteFile(manager.recordPath(), changed, 0o600); err != nil {
				t.Fatalf("write adversarial ownership record: %v", err)
			}
			if _, err := manager.Status(); err == nil {
				t.Fatal("Status accepted adversarial ownership record")
			} else {
				var unsafe *UnsafeRecordError
				if !errors.As(err, &unsafe) {
					t.Fatalf("unsafe record error = %T %v", err, err)
				}
			}
			if _, err := manager.Uninstall([]Target{TargetCodex}); err == nil {
				t.Fatal("Uninstall accepted adversarial ownership record")
			}
			definition, _ := manager.definition(TargetCodex)
			if target, err := os.Readlink(definition.entryPath); err != nil {
				t.Fatalf("managed target changed after unsafe record: %v", err)
			} else if target != manager.canonicalDirectory() {
				t.Fatalf("managed target changed to %q", target)
			}
			after, err := os.ReadFile(manager.recordPath())
			if err != nil {
				t.Fatalf("reread adversarial ownership record: %v", err)
			}
			if !bytes.Equal(after, changed) {
				t.Fatal("adversarial ownership record was rewritten")
			}
		})
	}
}

func TestRecordAndLockSymlinksAreRejectedWithoutFollowing(t *testing.T) {
	t.Run("record", func(t *testing.T) {
		environment := newTestEnvironment(t)
		manager := environment.manager(t, testSkillV1)
		if err := os.MkdirAll(filepath.Dir(manager.recordPath()), 0o700); err != nil {
			t.Fatalf("create record parent: %v", err)
		}
		outside := filepath.Join(t.TempDir(), "outside-record")
		original := []byte("outside record\n")
		if err := os.WriteFile(outside, original, 0o600); err != nil {
			t.Fatalf("write outside record: %v", err)
		}
		if err := os.Symlink(outside, manager.recordPath()); err != nil {
			t.Fatalf("link ownership record: %v", err)
		}
		if _, err := manager.Status(); err == nil {
			t.Fatal("Status followed ownership-record symlink")
		}
		if content, err := os.ReadFile(outside); err != nil {
			t.Fatalf("read outside record: %v", err)
		} else if !bytes.Equal(content, original) {
			t.Fatalf("outside record changed to %q", content)
		}
	})

	t.Run("lock", func(t *testing.T) {
		environment := newTestEnvironment(t)
		manager := environment.manager(t, testSkillV1)
		if err := os.MkdirAll(filepath.Dir(manager.lockPath()), 0o700); err != nil {
			t.Fatalf("create lock parent: %v", err)
		}
		outside := filepath.Join(t.TempDir(), "outside-lock")
		original := []byte("outside lock\n")
		if err := os.WriteFile(outside, original, 0o600); err != nil {
			t.Fatalf("write outside lock: %v", err)
		}
		if err := os.Symlink(outside, manager.lockPath()); err != nil {
			t.Fatalf("link installation lock: %v", err)
		}
		if _, err := manager.Install(installRequests(false, TargetCodex)); err == nil {
			t.Fatal("Install followed installation-lock symlink")
		}
		if content, err := os.ReadFile(outside); err != nil {
			t.Fatalf("read outside lock: %v", err)
		} else if !bytes.Equal(content, original) {
			t.Fatalf("outside lock changed to %q", content)
		}
	})
}

func TestBroadenedOwnershipRecordModeIsRejectedWithoutRepair(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	if _, err := manager.Install(installRequests(false, TargetCodex)); err != nil {
		t.Fatalf("install target: %v", err)
	}
	if err := os.Chmod(manager.recordPath(), 0o644); err != nil {
		t.Fatalf("broaden ownership record mode: %v", err)
	}
	if _, err := manager.Status(); err == nil {
		t.Fatal("Status accepted a mode-0644 ownership record")
	}
	if _, err := manager.Install(installRequests(false, TargetGemini)); err == nil {
		t.Fatal("Install silently repaired a mode-0644 ownership record")
	}
	assertMode(t, manager.recordPath(), 0o644)
	gemini, _ := manager.definition(TargetGemini)
	if _, err := os.Lstat(gemini.entryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe-record Install created Gemini entry: %v", err)
	}
}

func TestAtomicNameFailureDoesNotCreateTargetOrRecord(t *testing.T) {
	environment := newTestEnvironment(t)
	manager, err := New(Config{
		HomeDirectory:   environment.home,
		DataDirectory:   environment.data,
		PathEnvironment: "",
		Skill:           testSkillV1,
		Now:             func() time.Time { return environment.now },
		Random:          errorReader{},
	})
	if err != nil {
		t.Fatalf("construct failing manager: %v", err)
	}
	if _, err := manager.Install(installRequests(false, TargetCodex)); err == nil {
		t.Fatal("Install succeeded without temporary-name randomness")
	}
	codex, _ := manager.definition(TargetCodex)
	if _, err := os.Lstat(codex.entryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed Install created target: %v", err)
	}
	if _, err := os.Lstat(manager.recordPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed Install created ownership record: %v", err)
	}
	if _, err := os.Lstat(manager.canonicalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed Install published canonical content: %v", err)
	}
}

func TestRecordedModifiedClaudeCopyRequiresForceAndIsBackedUp(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	if _, err := manager.Install(installRequests(false, TargetClaude)); err != nil {
		t.Fatalf("install Claude copy: %v", err)
	}
	claude, _ := manager.definition(TargetClaude)
	modified := []byte("user-modified Claude skill\n")
	if err := os.WriteFile(filepath.Join(claude.entryPath, "SKILL.md"), modified, 0o600); err != nil {
		t.Fatalf("modify Claude copy: %v", err)
	}
	if _, err := manager.Install(installRequests(false, TargetClaude)); err == nil {
		t.Fatal("Install replaced a modified recorded Claude copy without force")
	}
	result, err := manager.Install(installRequests(true, TargetClaude))
	if err != nil {
		t.Fatalf("force replace modified Claude copy: %v", err)
	}
	backup := result.Changes[len(result.Changes)-1].BackupPath
	if content, err := os.ReadFile(filepath.Join(backup, "SKILL.md")); err != nil {
		t.Fatalf("read modified Claude backup: %v", err)
	} else if !bytes.Equal(content, modified) {
		t.Fatalf("modified Claude backup = %q", content)
	}
	if content, err := os.ReadFile(filepath.Join(claude.entryPath, "SKILL.md")); err != nil {
		t.Fatalf("read replacement Claude copy: %v", err)
	} else if !bytes.Equal(content, testSkillV1) {
		t.Fatalf("replacement Claude copy = %q", content)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
