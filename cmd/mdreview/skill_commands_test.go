package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"mdreview.dev/mdreview/internal/cli"
	"mdreview.dev/mdreview/internal/skills"
)

func TestSetupGathersAnswersBeforeInstalling(t *testing.T) {
	manager := &fakeSkillManager{
		detections: []skills.Detection{
			{Target: skills.TargetCodex, Detected: true},
			{Target: skills.TargetClaude},
			{Target: skills.TargetGemini, Detected: true},
		},
		snapshot: managedSkillSnapshot(),
	}
	var output bytes.Buffer
	err := runSkillManagementWith(
		context.Background(),
		cli.Options{Command: cli.Setup},
		strings.NewReader("later\nyes\nno\n"),
		&output,
		true,
		manager,
	)
	if err != nil {
		t.Fatalf("run setup: %v", err)
	}
	if manager.installCalls != 1 {
		t.Fatalf("Install calls = %d, want 1", manager.installCalls)
	}
	if len(manager.installRequests) != 1 ||
		manager.installRequests[0].Target != skills.TargetCodex {
		t.Fatalf("install requests = %v, want codex", manager.installRequests)
	}
	if manager.installRequests[0].AllowConflictBackup {
		t.Fatal("ordinary setup unexpectedly allowed a conflict backup")
	}
	for _, text := range []string{
		"Please answer yes or no.",
		"Install the mdReview skill for Codex?",
		"Install the mdReview skill for Gemini CLI?",
	} {
		if !strings.Contains(output.String(), text) {
			t.Errorf("setup output is missing %q:\n%s", text, output.String())
		}
	}
}

func TestSetupEOFBeforeFinalSelectionMakesNoMutation(t *testing.T) {
	manager := &fakeSkillManager{
		detections: []skills.Detection{
			{Target: skills.TargetCodex, Detected: true},
			{Target: skills.TargetGemini, Detected: true},
		},
	}
	err := runSkillManagementWith(
		context.Background(),
		cli.Options{Command: cli.Setup},
		strings.NewReader("yes\n"),
		&bytes.Buffer{},
		true,
		manager,
	)
	if err == nil || !strings.Contains(err.Error(), "input ended") {
		t.Fatalf("run setup error = %v, want interrupted input", err)
	}
	if manager.installCalls != 0 || manager.statusCalls != 0 {
		t.Fatalf(
			"interrupted setup calls = install %d, status %d; want no mutation phase",
			manager.installCalls,
			manager.statusCalls,
		)
	}
}

func TestSetupDeclineMakesNoMutation(t *testing.T) {
	manager := &fakeSkillManager{
		detections: []skills.Detection{
			{Target: skills.TargetCodex, Detected: true},
		},
	}
	var output bytes.Buffer
	err := runSkillManagementWith(
		context.Background(),
		cli.Options{Command: cli.Setup},
		strings.NewReader("\n"),
		&output,
		true,
		manager,
	)
	if err != nil {
		t.Fatalf("run setup: %v", err)
	}
	if manager.installCalls != 0 || manager.statusCalls != 0 {
		t.Fatal("declined setup entered the mutation phase")
	}
	if !strings.Contains(output.String(), "No changes made.") {
		t.Fatalf("decline output:\n%s", output.String())
	}
}

func TestSetupRequiresTerminalBeforeDetection(t *testing.T) {
	manager := &fakeSkillManager{}
	err := runSkillManagementWith(
		context.Background(),
		cli.Options{Command: cli.Setup},
		strings.NewReader("yes\n"),
		&bytes.Buffer{},
		false,
		manager,
	)
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("run setup error = %v, want terminal requirement", err)
	}
	if manager.detectCalls != 0 {
		t.Fatalf("Detect calls = %d, want 0", manager.detectCalls)
	}
}

func TestSetupConflictingTargetRequiresSecondConfirmation(t *testing.T) {
	snapshot := managedSkillSnapshot()
	snapshot.Targets[0].State = skills.StateModified
	manager := &fakeSkillManager{
		detections: []skills.Detection{
			{Target: skills.TargetCodex, Detected: true},
		},
		snapshot: snapshot,
	}
	var output bytes.Buffer
	err := runSkillManagementWith(
		context.Background(),
		cli.Options{Command: cli.Setup},
		strings.NewReader("yes\nyes\n"),
		&output,
		true,
		manager,
	)
	if err != nil {
		t.Fatalf("run setup: %v", err)
	}
	if manager.installCalls != 1 ||
		len(manager.installRequests) != 1 ||
		!manager.installRequests[0].AllowConflictBackup {
		t.Fatalf(
			"Install calls = %d, requests = %#v",
			manager.installCalls,
			manager.installRequests,
		)
	}
	if !strings.Contains(output.String(), "will be moved to a backup") {
		t.Fatalf("conflict output:\n%s", output.String())
	}
}

func TestDirectSkillCommandsMapTargetsAndForce(t *testing.T) {
	manager := &fakeSkillManager{}
	err := runSkillManagementWith(
		context.Background(),
		cli.Options{
			Command: cli.SkillInstall,
			Targets: []cli.Target{cli.TargetClaude, cli.TargetGemini},
			Force:   true,
		},
		nil,
		&bytes.Buffer{},
		false,
		manager,
	)
	if err != nil {
		t.Fatalf("run direct install: %v", err)
	}
	if manager.installCalls != 1 {
		t.Fatalf(
			"Install calls = %d",
			manager.installCalls,
		)
	}
	if len(manager.installRequests) != 2 ||
		manager.installRequests[0].Target != skills.TargetClaude ||
		manager.installRequests[1].Target != skills.TargetGemini ||
		!manager.installRequests[0].AllowConflictBackup ||
		!manager.installRequests[1].AllowConflictBackup {
		t.Fatalf("install requests = %#v", manager.installRequests)
	}

	err = runSkillManagementWith(
		context.Background(),
		cli.Options{
			Command: cli.SkillUninstall,
			Targets: []cli.Target{cli.TargetClaude},
		},
		nil,
		&bytes.Buffer{},
		false,
		manager,
	)
	if err != nil {
		t.Fatalf("run direct uninstall: %v", err)
	}
	if manager.uninstallCalls != 1 ||
		len(manager.uninstalledTargets) != 1 ||
		manager.uninstalledTargets[0] != skills.TargetClaude {
		t.Fatalf("uninstalled targets = %v", manager.uninstalledTargets)
	}
}

func TestSkillStatusIsReadOnly(t *testing.T) {
	manager := &fakeSkillManager{snapshot: managedSkillSnapshot()}
	var output bytes.Buffer
	err := runSkillManagementWith(
		context.Background(),
		cli.Options{Command: cli.SkillStatus},
		nil,
		&output,
		false,
		manager,
	)
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	if manager.statusCalls != 1 ||
		manager.installCalls != 0 ||
		manager.uninstallCalls != 0 {
		t.Fatalf(
			"status calls = %d, install = %d, uninstall = %d",
			manager.statusCalls,
			manager.installCalls,
			manager.uninstallCalls,
		)
	}
	for _, text := range []string{"Canonical: managed", "Codex: managed"} {
		if !strings.Contains(output.String(), text) {
			t.Errorf("status output is missing %q:\n%s", text, output.String())
		}
	}
}

type fakeSkillManager struct {
	detections         []skills.Detection
	snapshot           skills.Snapshot
	detectErr          error
	statusErr          error
	installErr         error
	uninstallErr       error
	detectCalls        int
	statusCalls        int
	installCalls       int
	uninstallCalls     int
	installRequests    []skills.InstallRequest
	uninstalledTargets []skills.Target
}

func (manager *fakeSkillManager) Detect() ([]skills.Detection, error) {
	manager.detectCalls++
	return manager.detections, manager.detectErr
}

func (manager *fakeSkillManager) Status() (skills.Snapshot, error) {
	manager.statusCalls++
	return manager.snapshot, manager.statusErr
}

func (manager *fakeSkillManager) Install(
	requests []skills.InstallRequest,
) (skills.Result, error) {
	manager.installCalls++
	manager.installRequests = append([]skills.InstallRequest(nil), requests...)
	return skills.Result{}, manager.installErr
}

func (manager *fakeSkillManager) Uninstall(
	targets []skills.Target,
) (skills.Result, error) {
	manager.uninstallCalls++
	manager.uninstalledTargets = append([]skills.Target(nil), targets...)
	return skills.Result{}, manager.uninstallErr
}

func managedSkillSnapshot() skills.Snapshot {
	return skills.Snapshot{
		Canonical: skills.CanonicalStatus{
			Path:  "/data/mdreview/skills/mdreview/SKILL.md",
			State: skills.StateManaged,
		},
		Targets: []skills.TargetStatus{
			{
				Target: skills.TargetCodex,
				Path:   "/home/user/.agents/skills/mdreview",
				State:  skills.StateManaged,
			},
			{
				Target: skills.TargetClaude,
				Path:   "/home/user/.claude/skills/mdreview",
				State:  skills.StateNotInstalled,
			},
			{
				Target: skills.TargetGemini,
				Path:   "/home/user/.gemini/skills/mdreview",
				State:  skills.StateNotInstalled,
			},
		},
	}
}

var _ skillManager = (*fakeSkillManager)(nil)
