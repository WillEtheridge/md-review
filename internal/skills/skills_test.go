package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallStatusAndUninstallGlobalTargets(t *testing.T) {
	home := t.TempDir()
	manager, err := New(Config{
		HomeDirectory: home,
		Skill:         []byte("---\nname: mdreview\n---\n"),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = manager.Install([]InstallRequest{
		{Target: TargetCodex},
		{Target: TargetClaude},
		{Target: TargetPi},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		filepath.Join(home, ".codex", "skills", "mdreview", "SKILL.md"),
		filepath.Join(home, ".claude", "skills", "mdreview", "SKILL.md"),
		filepath.Join(home, ".pi", "agent", "skills", "mdreview", "SKILL.md"),
	} {
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("installed target %q: %v", target, err)
		}
	}
	snapshot, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Targets) != 3 ||
		snapshot.Targets[0].State != StateInstalled ||
		snapshot.Targets[1].State != StateInstalled ||
		snapshot.Targets[2].State != StateInstalled {
		t.Fatalf("status = %#v", snapshot)
	}

	if _, err := manager.Uninstall([]Target{TargetCodex, TargetClaude, TargetPi}); err != nil {
		t.Fatal(err)
	}
	for _, status := range snapshot.Targets {
		if _, err := os.Stat(status.Path); !os.IsNotExist(err) {
			t.Fatalf("target still exists %q: %v", status.Path, err)
		}
	}
}

func TestExplicitInstallReplacesAndUninstallPreservesUnrelatedFile(t *testing.T) {
	home := t.TempDir()
	manager, err := New(Config{HomeDirectory: home, Skill: []byte("new")})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(home, ".codex", "skills", "mdreview")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(directory, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Install([]InstallRequest{{Target: TargetCodex}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(skillPath)
	if err != nil || string(data) != "new" {
		t.Fatalf("replacement = %q, %v", data, err)
	}
	if _, err := manager.Uninstall([]Target{TargetCodex}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "notes.txt")); err != nil {
		t.Fatalf("unrelated file was not preserved: %v", err)
	}
}

func TestInstallUsesConfiguredSymlinkedLayout(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, ".codex")); err != nil {
		t.Fatal(err)
	}
	manager, err := New(Config{HomeDirectory: home, Skill: []byte("skill")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install([]InstallRequest{{Target: TargetCodex}}); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	installedPath := filepath.Join(outside, "skills", "mdreview", "SKILL.md")
	content, err := os.ReadFile(installedPath)
	if err != nil || string(content) != "skill" {
		t.Fatalf("installed skill = %q, %v", content, err)
	}
}
