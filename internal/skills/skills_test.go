package skills

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

var testSkillV1 = []byte("---\nname: mdreview\ndescription: test\n---\n\n# Test skill\n")
var testSkillV2 = []byte("---\nname: mdreview\ndescription: updated test\n---\n\n# Updated skill\n")

type sequenceReader struct {
	mutex sync.Mutex
	next  byte
}

func (reader *sequenceReader) Read(destination []byte) (int, error) {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	for index := range destination {
		reader.next++
		destination[index] = reader.next
	}
	return len(destination), nil
}

type testEnvironment struct {
	home   string
	data   string
	path   string
	random *sequenceReader
	now    time.Time
}

func newTestEnvironment(t *testing.T) *testEnvironment {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o750); err != nil {
		t.Fatalf("create fake home: %v", err)
	}
	return &testEnvironment{
		home:   home,
		data:   filepath.Join(root, "data"),
		random: &sequenceReader{},
		now:    time.Date(2026, 7, 29, 12, 34, 56, 0, time.FixedZone("test", 2*60*60)),
	}
}

func (environment *testEnvironment) manager(t *testing.T, skill []byte) *Manager {
	t.Helper()
	manager, err := New(Config{
		HomeDirectory:   environment.home,
		DataDirectory:   environment.data,
		PathEnvironment: environment.path,
		Skill:           skill,
		Now:             func() time.Time { return environment.now },
		Random:          environment.random,
	})
	if err != nil {
		t.Fatalf("construct skill manager: %v", err)
	}
	return manager
}

func TestNewValidatesInjectedEnvironment(t *testing.T) {
	environment := newTestEnvironment(t)
	valid := Config{
		HomeDirectory: environment.home,
		DataDirectory: environment.data,
		Skill:         testSkillV1,
		Now:           func() time.Time { return environment.now },
		Random:        environment.random,
	}
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "relative home", change: func(config *Config) { config.HomeDirectory = "home" }},
		{name: "relative data", change: func(config *Config) { config.DataDirectory = "data" }},
		{name: "missing skill", change: func(config *Config) { config.Skill = nil }},
		{name: "missing clock", change: func(config *Config) { config.Now = nil }},
		{name: "missing randomness", change: func(config *Config) { config.Random = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.change(&config)
			if _, err := New(config); err == nil {
				t.Fatal("New accepted invalid configuration")
			}
		})
	}
}

func TestNewFromEnvironmentUsesHomeDataAndPATH(t *testing.T) {
	environment := newTestEnvironment(t)
	t.Setenv("HOME", environment.home)
	t.Setenv("XDG_DATA_HOME", environment.data)
	t.Setenv("PATH", "/one:/two")
	manager, err := NewFromEnvironment(testSkillV1)
	if err != nil {
		t.Fatalf("construct real-environment manager: %v", err)
	}
	if manager.homeDirectory != environment.home ||
		manager.dataDirectory != environment.data ||
		manager.pathEnvironment != "/one:/two" {
		t.Fatalf(
			"environment manager = home %q, data %q, PATH %q",
			manager.homeDirectory,
			manager.dataDirectory,
			manager.pathEnvironment,
		)
	}
}

func TestDetectUsesExecutablesAndHostConfigurationRootsWithoutMutation(t *testing.T) {
	environment := newTestEnvironment(t)
	binaryDirectory := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(binaryDirectory, 0o700); err != nil {
		t.Fatalf("create fake binary directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binaryDirectory, "codex"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write fake Codex executable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binaryDirectory, "gemini"), []byte("not executable\n"), 0o600); err != nil {
		t.Fatalf("write non-executable Gemini file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(environment.home, ".claude"), 0o755); err != nil {
		t.Fatalf("create Claude configuration root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(environment.home, ".agents", "skills"), 0o755); err != nil {
		t.Fatalf("create shared agent skill root: %v", err)
	}
	environment.path = binaryDirectory
	manager := environment.manager(t, testSkillV1)

	detections, err := manager.Detect()
	if err != nil {
		t.Fatalf("detect hosts: %v", err)
	}
	byTarget := make(map[Target]Detection)
	for _, detection := range detections {
		byTarget[detection.Target] = detection
	}
	if !byTarget[TargetCodex].Detected ||
		!byTarget[TargetCodex].ExecutableFound ||
		byTarget[TargetCodex].ConfigurationRootFound {
		t.Fatalf("Codex detection = %+v", byTarget[TargetCodex])
	}
	if !byTarget[TargetClaude].Detected ||
		byTarget[TargetClaude].ExecutableFound ||
		!byTarget[TargetClaude].ConfigurationRootFound {
		t.Fatalf("Claude detection = %+v", byTarget[TargetClaude])
	}
	if byTarget[TargetGemini].Detected {
		t.Fatalf("Gemini detection = %+v, shared .agents root must not detect Gemini", byTarget[TargetGemini])
	}
	if _, err := os.Lstat(environment.data); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only detection created data directory: %v", err)
	}
}

func TestStatusIsReadOnlyWhenNothingIsInstalled(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	snapshot, err := manager.Status()
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if snapshot.Canonical.State != StateNotInstalled {
		t.Fatalf("canonical state = %q, want not-installed", snapshot.Canonical.State)
	}
	for _, target := range snapshot.Targets {
		if target.State != StateNotInstalled {
			t.Errorf("%s state = %q, want not-installed", target.Target, target.State)
		}
	}
	if _, err := os.Lstat(environment.data); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only status created data directory: %v", err)
	}
}

func TestInstallCreatesCanonicalLinksAndClaudeCopyWithPrivatePermissions(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	result, err := manager.Install(installRequests(false, supportedTargets()...))
	if err != nil {
		t.Fatalf("install all targets: %v", err)
	}
	if !result.CanonicalChanged {
		t.Fatal("initial install did not report canonical creation")
	}
	if content, err := os.ReadFile(manager.canonicalPath()); err != nil {
		t.Fatalf("read canonical skill: %v", err)
	} else if !bytes.Equal(content, testSkillV1) {
		t.Fatalf("canonical skill = %q", content)
	}
	assertMode(t, manager.canonicalPath(), 0o600)
	assertMode(t, manager.recordPath(), 0o600)
	assertMode(t, manager.lockPath(), 0o600)
	for _, directory := range []string{
		environment.data,
		filepath.Join(environment.data, "mdreview"),
		filepath.Join(environment.data, "mdreview", "skills"),
		manager.canonicalDirectory(),
	} {
		assertMode(t, directory, 0o700)
	}

	for _, target := range []Target{TargetCodex, TargetGemini} {
		definition, _ := manager.definition(target)
		info, err := os.Lstat(definition.entryPath)
		if err != nil {
			t.Fatalf("inspect %s link: %v", target, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s entry mode = %v, want directory symlink", target, info.Mode())
		}
		linkTarget, err := os.Readlink(definition.entryPath)
		if err != nil {
			t.Fatalf("read %s link: %v", target, err)
		}
		if linkTarget != manager.canonicalDirectory() || !filepath.IsAbs(linkTarget) {
			t.Fatalf("%s link target = %q, want absolute %q", target, linkTarget, manager.canonicalDirectory())
		}
	}
	claude, _ := manager.definition(TargetClaude)
	claudeInfo, err := os.Lstat(claude.entryPath)
	if err != nil {
		t.Fatalf("inspect Claude copy: %v", err)
	}
	if !claudeInfo.IsDir() || claudeInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("Claude entry mode = %v, want copied directory", claudeInfo.Mode())
	}
	assertMode(t, claude.entryPath, 0o700)
	assertMode(t, filepath.Join(claude.entryPath, "SKILL.md"), 0o600)
	if content, err := os.ReadFile(filepath.Join(claude.entryPath, "SKILL.md")); err != nil {
		t.Fatalf("read Claude copy: %v", err)
	} else if !bytes.Equal(content, testSkillV1) {
		t.Fatalf("Claude copy = %q", content)
	}

	record, err := manager.loadRecord()
	if err != nil {
		t.Fatalf("load ownership record: %v", err)
	}
	if record.Targets[string(TargetCodex)].Kind != EntryLink ||
		record.Targets[string(TargetClaude)].Kind != EntryCopy ||
		record.Targets[string(TargetGemini)].Kind != EntryLink {
		t.Fatalf("record target kinds = %+v", record.Targets)
	}
	snapshot, err := manager.Status()
	if err != nil {
		t.Fatalf("read installed status: %v", err)
	}
	if snapshot.Canonical.State != StateManaged {
		t.Fatalf("canonical state = %q, want managed", snapshot.Canonical.State)
	}
	for _, target := range snapshot.Targets {
		if target.State != StateManaged {
			t.Errorf("%s state = %q, want managed", target.Target, target.State)
		}
	}
}

func TestInstallIsIdempotentWithoutEntryChurn(t *testing.T) {
	environment := newTestEnvironment(t)
	manager := environment.manager(t, testSkillV1)
	if _, err := manager.Install(installRequests(false, supportedTargets()...)); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	before := make(map[Target]uint64)
	for _, definition := range manager.definitions() {
		before[definition.target] = inode(t, definition.entryPath)
	}
	result, err := manager.Install(installRequests(false, supportedTargets()...))
	if err != nil {
		t.Fatalf("idempotent install: %v", err)
	}
	if result.CanonicalChanged {
		t.Fatal("idempotent install rewrote canonical content")
	}
	for _, change := range result.Changes {
		if change.Action != ActionUnchanged {
			t.Errorf("%s action = %q, want unchanged", change.Target, change.Action)
		}
	}
	for _, definition := range manager.definitions() {
		if got := inode(t, definition.entryPath); got != before[definition.target] {
			t.Errorf("%s inode changed from %d to %d", definition.target, before[definition.target], got)
		}
	}
}

func TestCanonicalUpdateAdvancesLinksAndRecordedClaudeCopy(t *testing.T) {
	environment := newTestEnvironment(t)
	first := environment.manager(t, testSkillV1)
	if _, err := first.Install(installRequests(false, supportedTargets()...)); err != nil {
		t.Fatalf("install version one: %v", err)
	}
	claude, _ := first.definition(TargetClaude)
	claudeInode := inode(t, claude.entryPath)
	second := environment.manager(t, testSkillV2)
	result, err := second.Install(installRequests(false, TargetCodex))
	if err != nil {
		t.Fatalf("update canonical skill: %v", err)
	}
	if !result.CanonicalChanged {
		t.Fatal("canonical update was not reported")
	}
	if content, err := os.ReadFile(second.canonicalPath()); err != nil {
		t.Fatalf("read updated canonical: %v", err)
	} else if !bytes.Equal(content, testSkillV2) {
		t.Fatalf("updated canonical = %q", content)
	}
	if content, err := os.ReadFile(filepath.Join(claude.entryPath, "SKILL.md")); err != nil {
		t.Fatalf("read updated Claude copy: %v", err)
	} else if !bytes.Equal(content, testSkillV2) {
		t.Fatalf("updated Claude copy = %q", content)
	}
	if inode(t, claude.entryPath) != claudeInode {
		t.Fatal("canonical update churned the recorded Claude directory")
	}
	snapshot, err := second.Status()
	if err != nil {
		t.Fatalf("read updated status: %v", err)
	}
	for _, target := range snapshot.Targets {
		if target.State != StateManaged {
			t.Errorf("%s state after update = %q, want managed", target.Target, target.State)
		}
	}
}

func TestStatusReportsOutdatedCanonicalLinksAndCopyWithoutMutation(t *testing.T) {
	environment := newTestEnvironment(t)
	first := environment.manager(t, testSkillV1)
	if _, err := first.Install(installRequests(false, supportedTargets()...)); err != nil {
		t.Fatalf("install version one: %v", err)
	}
	recordBefore, err := os.ReadFile(first.recordPath())
	if err != nil {
		t.Fatalf("read version-one record: %v", err)
	}
	second := environment.manager(t, testSkillV2)
	snapshot, err := second.Status()
	if err != nil {
		t.Fatalf("read version-two status: %v", err)
	}
	if snapshot.Canonical.State != StateOutdated {
		t.Fatalf("canonical state = %q, want outdated", snapshot.Canonical.State)
	}
	for _, target := range snapshot.Targets {
		if target.State != StateOutdated {
			t.Errorf("%s state = %q, want outdated", target.Target, target.State)
		}
	}
	recordAfter, err := os.ReadFile(first.recordPath())
	if err != nil {
		t.Fatalf("reread version-one record: %v", err)
	}
	if !bytes.Equal(recordBefore, recordAfter) {
		t.Fatal("read-only outdated status rewrote the ownership record")
	}
}

func TestMutationsRejectEmptyDuplicateAndUnknownTargetsBeforeMutation(t *testing.T) {
	tests := []struct {
		name    string
		targets []Target
	}{
		{name: "empty"},
		{name: "duplicate", targets: []Target{TargetCodex, TargetCodex}},
		{name: "unknown", targets: []Target{"other"}},
	}
	for _, test := range tests {
		for _, operation := range []string{"install", "uninstall"} {
			t.Run(test.name+"/"+operation, func(t *testing.T) {
				environment := newTestEnvironment(t)
				manager := environment.manager(t, testSkillV1)
				var err error
				if operation == "install" {
					_, err = manager.Install(installRequests(false, test.targets...))
				} else {
					_, err = manager.Uninstall(test.targets)
				}
				if err == nil {
					t.Fatalf("%s accepted invalid targets", operation)
				}
				if _, err := os.Lstat(environment.data); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid %s mutated data path: %v", operation, err)
				}
			})
		}
	}
}

func installRequests(allowConflictBackup bool, targets ...Target) []InstallRequest {
	requests := make([]InstallRequest, 0, len(targets))
	for _, target := range targets {
		requests = append(requests, InstallRequest{
			Target:              target,
			AllowConflictBackup: allowConflictBackup,
		})
	}
	return requests
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect mode for %q: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%q mode = %#o, want %#o", path, got, want)
	}
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect inode for %q: %v", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat payload for %q has type %T", path, info.Sys())
	}
	return stat.Ino
}
