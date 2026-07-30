package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"mdreview.dev/mdreview/internal/skills"
)

type setupTestManager struct {
	installRequests []skills.InstallRequest
	statusCalls     int
}

func (manager *setupTestManager) Status() (skills.Snapshot, error) {
	manager.statusCalls++
	return skills.Snapshot{}, nil
}

func (manager *setupTestManager) Install(
	requests []skills.InstallRequest,
) (skills.Result, error) {
	manager.installRequests = append([]skills.InstallRequest(nil), requests...)
	result := skills.Result{Changes: make([]skills.Change, 0, len(requests))}
	for _, request := range requests {
		result.Changes = append(result.Changes, skills.Change{
			Target: request.Target,
			Action: skills.ActionInstalled,
			State:  skills.StateInstalled,
			Path:   "/test/" + string(request.Target) + "/SKILL.md",
		})
	}
	return result, nil
}

func (*setupTestManager) Uninstall([]skills.Target) (skills.Result, error) {
	return skills.Result{}, nil
}

func TestSetupInstallsSelectedTargetsInOneStep(t *testing.T) {
	manager := &setupTestManager{}
	var output bytes.Buffer

	err := runSetup(
		context.Background(),
		strings.NewReader(" \x1b[B \x1b[B \r"),
		&output,
		true,
		manager,
	)
	if err != nil {
		t.Fatal(err)
	}

	want := []skills.InstallRequest{
		{Target: skills.TargetCodex},
		{Target: skills.TargetClaude},
		{Target: skills.TargetPi},
	}
	if !reflect.DeepEqual(manager.installRequests, want) {
		t.Fatalf("install requests = %#v, want %#v", manager.installRequests, want)
	}
	if manager.statusCalls != 0 {
		t.Fatalf("status calls = %d, want 0", manager.statusCalls)
	}
	for _, text := range []string{
		"[ ] Codex",
		"[ ] Claude Code",
		"[ ] Pi",
		"Selected: Codex, Claude Code, Pi",
		"Codex: installed",
		"Claude Code: installed",
		"Pi: installed",
	} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("output does not contain %q:\n%s", text, output.String())
		}
	}
}

func TestSetupRequiresSelectionBeforeConfirmation(t *testing.T) {
	manager := &setupTestManager{}
	var output bytes.Buffer

	err := runSetup(
		context.Background(),
		strings.NewReader("\r \r"),
		&output,
		true,
		manager,
	)
	if err != nil {
		t.Fatal(err)
	}

	want := []skills.InstallRequest{{Target: skills.TargetCodex}}
	if !reflect.DeepEqual(manager.installRequests, want) {
		t.Fatalf("install requests = %#v, want %#v", manager.installRequests, want)
	}
	if !strings.Contains(
		output.String(),
		"Select at least one agent.",
	) {
		t.Fatalf("missing empty-selection guidance:\n%s", output.String())
	}
}

func TestSetupCancellationMakesNoChanges(t *testing.T) {
	manager := &setupTestManager{}
	var output bytes.Buffer

	err := runSetup(
		context.Background(),
		strings.NewReader("\x03"),
		&output,
		true,
		manager,
	)
	if err != nil {
		t.Fatal(err)
	}
	if manager.installRequests != nil {
		t.Fatalf("install requests = %#v, want none", manager.installRequests)
	}
	if !strings.Contains(output.String(), "Setup cancelled. No changes made.") {
		t.Fatalf("missing cancellation result:\n%s", output.String())
	}
}

func TestSetupRejectsNonInteractiveInput(t *testing.T) {
	manager := &setupTestManager{}
	var output bytes.Buffer

	err := runSetup(
		context.Background(),
		strings.NewReader(""),
		&output,
		false,
		manager,
	)
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("error = %v, want interactive terminal requirement", err)
	}
	if manager.installRequests != nil {
		t.Fatalf("install requests = %#v, want none", manager.installRequests)
	}
}
