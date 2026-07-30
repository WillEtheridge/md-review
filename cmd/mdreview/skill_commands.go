package main

import (
	"context"
	"fmt"
	"io"

	"mdreview.dev/mdreview/internal/cli"
	"mdreview.dev/mdreview/internal/skillassets"
	"mdreview.dev/mdreview/internal/skills"
)

type skillManager interface {
	// The command layer depends on the small operation surface rather than the
	// concrete installer, which keeps terminal interaction and output testable.
	Status() (skills.Snapshot, error)
	Install([]skills.InstallRequest) (skills.Result, error)
	Uninstall([]skills.Target) (skills.Result, error)
}

func runSkillManagement(
	ctx context.Context,
	options cli.Options,
	input io.Reader,
	output io.Writer,
) error {
	skill, err := skillassets.ReadSkill()
	if err != nil {
		return fmt.Errorf("load canonical Agent Skill: %w", err)
	}
	manager, err := skills.NewFromEnvironment(skill)
	if err != nil {
		return fmt.Errorf("configure Agent Skill management: %w", err)
	}
	return runSkillManagementWith(
		ctx,
		options,
		input,
		output,
		isInteractiveTerminal(input, output),
		manager,
	)
}

func runSkillManagementWith(
	ctx context.Context,
	options cli.Options,
	input io.Reader,
	output io.Writer,
	inputIsTerminal bool,
	manager skillManager,
) error {
	// The non-interactive flag is passed through for setup validation; explicit
	// install and uninstall commands remain usable in scripts and tests.
	_ = inputIsTerminal
	switch options.Command {
	case cli.Setup:
		return runSetup(ctx, input, output, inputIsTerminal, manager)
	case cli.SkillStatus:
		snapshot, err := manager.Status()
		if err != nil {
			return fmt.Errorf("read Agent Skill status: %w", err)
		}
		return printSkillStatus(output, snapshot)
	case cli.SkillInstall:
		result, err := manager.Install(skillInstallRequests(options.Targets))
		if err != nil {
			return fmt.Errorf("install Agent Skill: %w", err)
		}
		return printSkillResult(output, result)
	case cli.SkillUninstall:
		result, err := manager.Uninstall(skillTargets(options.Targets))
		if err != nil {
			return fmt.Errorf("uninstall Agent Skill: %w", err)
		}
		return printSkillResult(output, result)
	default:
		return fmt.Errorf("unsupported Agent Skill command")
	}
}

func runSetup(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	inputIsTerminal bool,
	manager skillManager,
) error {
	_ = inputIsTerminal
	selected, err := selectSkillTargets(ctx, input, output, inputIsTerminal)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		_, err := fmt.Fprintln(output, "Setup cancelled. No changes made.")
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("setup cancelled before installation: %w", err)
	}

	requests := make([]skills.InstallRequest, 0, len(selected))
	for _, target := range selected {
		requests = append(requests, skills.InstallRequest{Target: target})
	}
	result, err := manager.Install(requests)
	if err != nil {
		return fmt.Errorf("install Agent Skill: %w", err)
	}
	return printSkillResult(output, result)
}

func printSkillStatus(output io.Writer, snapshot skills.Snapshot) error {
	if _, err := fmt.Fprintln(output, "mdReview Agent Skill (global user installation)"); err != nil {
		return err
	}
	for _, status := range snapshot.Targets {
		if _, err := fmt.Fprintf(
			output,
			"%s: %s (%s)\n",
			skillTargetName(status.Target),
			status.State,
			status.Path,
		); err != nil {
			return err
		}
	}
	return nil
}

func printSkillResult(output io.Writer, result skills.Result) error {
	if _, err := fmt.Fprintln(output, "Global user Agent Skill:"); err != nil {
		return err
	}
	for _, change := range result.Changes {
		if _, err := fmt.Fprintf(
			output,
			"%s: %s (%s)\n",
			skillTargetName(change.Target),
			change.Action,
			change.Path,
		); err != nil {
			return err
		}
	}
	return nil
}

func skillTargets(targets []cli.Target) []skills.Target {
	mapped := make([]skills.Target, 0, len(targets))
	for _, target := range targets {
		mapped = append(mapped, skills.Target(target))
	}
	return mapped
}

func skillInstallRequests(
	targets []cli.Target,
) []skills.InstallRequest {
	requests := make([]skills.InstallRequest, 0, len(targets))
	for _, target := range targets {
		requests = append(requests, skills.InstallRequest{Target: skills.Target(target)})
	}
	return requests
}

func skillTargetName(target skills.Target) string {
	switch target {
	case skills.TargetCodex:
		return "Codex"
	case skills.TargetClaude:
		return "Claude Code"
	case skills.TargetPi:
		return "Pi"
	default:
		return string(target)
	}
}
