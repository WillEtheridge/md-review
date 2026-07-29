package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"mdreview.dev/mdreview/internal/cli"
	"mdreview.dev/mdreview/internal/skillassets"
	"mdreview.dev/mdreview/internal/skills"

	"golang.org/x/sys/unix"
)

type skillManager interface {
	Detect() ([]skills.Detection, error)
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
		isTerminalReader(input),
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
		result, err := manager.Install(skillInstallRequests(options.Targets, options.Force))
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
	if !inputIsTerminal {
		return errors.New("setup requires an interactive terminal; use skill install with explicit --target values")
	}
	detections, err := manager.Detect()
	if err != nil {
		return fmt.Errorf("detect installed agents: %w", err)
	}

	reader := bufio.NewReader(input)
	selected := make([]skills.Target, 0, len(detections))
	for _, detection := range detections {
		if !detection.Detected {
			continue
		}
		accepted, promptErr := promptDecision(
			ctx,
			reader,
			output,
			fmt.Sprintf(
				"Install the mdReview skill for %s? [y/N] ",
				skillTargetName(detection.Target),
			),
		)
		if promptErr != nil {
			return promptErr
		}
		if accepted {
			selected = append(selected, detection.Target)
		}
	}
	if len(selected) == 0 {
		_, err := fmt.Fprintln(output, "No Agent Skill targets selected. No changes made.")
		return err
	}

	snapshot, err := manager.Status()
	if err != nil {
		return fmt.Errorf("inspect selected Agent Skill targets: %w", err)
	}
	statuses := make(map[skills.Target]skills.TargetStatus, len(snapshot.Targets))
	for _, status := range snapshot.Targets {
		statuses[status.Target] = status
	}

	requests := make([]skills.InstallRequest, 0, len(selected))
	for _, target := range selected {
		status := statuses[target]
		if !requiresConflictBackup(status.State) {
			requests = append(requests, skills.InstallRequest{Target: target})
			continue
		}
		accepted, promptErr := promptDecision(
			ctx,
			reader,
			output,
			fmt.Sprintf(
				"The existing %s skill entry will be moved to a backup. Continue? [y/N] ",
				skillTargetName(target),
			),
		)
		if promptErr != nil {
			return promptErr
		}
		if accepted {
			requests = append(requests, skills.InstallRequest{
				Target:              target,
				AllowConflictBackup: true,
			})
		}
	}
	if len(requests) == 0 {
		_, err := fmt.Fprintln(output, "No Agent Skill targets confirmed. No changes made.")
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("setup cancelled before installation: %w", err)
	}

	result, err := manager.Install(requests)
	if err != nil {
		return fmt.Errorf("install Agent Skill: %w", err)
	}
	return printSkillResult(output, result)
}

func promptDecision(
	ctx context.Context,
	input *bufio.Reader,
	output io.Writer,
	prompt string,
) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("setup cancelled: %w", err)
		}
		if _, err := io.WriteString(output, prompt); err != nil {
			return false, fmt.Errorf("write setup prompt: %w", err)
		}
		answer, err := readSetupLine(ctx, input)
		if err != nil {
			if cancellationErr := ctx.Err(); cancellationErr != nil {
				return false, fmt.Errorf("setup cancelled: %w", cancellationErr)
			}
			return false, fmt.Errorf("setup input ended before a decision: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		default:
			if _, err := fmt.Fprintln(output, "Please answer yes or no."); err != nil {
				return false, fmt.Errorf("write setup guidance: %w", err)
			}
		}
	}
}

func readSetupLine(
	ctx context.Context,
	input *bufio.Reader,
) (string, error) {
	type result struct {
		answer string
		err    error
	}
	results := make(chan result, 1)
	go func() {
		answer, err := input.ReadString('\n')
		results <- result{answer: answer, err: err}
	}()

	select {
	case <-ctx.Done():
		// A terminal read cannot be portably cancelled from another goroutine.
		// This unexported command path returns immediately and main exits the
		// process, which owns and terminates the sole pending read.
		return "", ctx.Err()
	case read := <-results:
		return read.answer, read.err
	}
}

func printSkillStatus(output io.Writer, snapshot skills.Snapshot) error {
	if _, err := fmt.Fprintf(
		output,
		"mdReview Agent Skill\n\nCanonical: %s\nPath:      %s\n",
		snapshot.Canonical.State,
		snapshot.Canonical.Path,
	); err != nil {
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
	if snapshot.Pending != nil {
		_, err := fmt.Fprintf(
			output,
			"Pending: %s %s (%s)\n",
			snapshot.Pending.Target,
			snapshot.Pending.Operation,
			snapshot.Pending.Phase,
		)
		return err
	}
	return nil
}

func printSkillResult(output io.Writer, result skills.Result) error {
	if result.CanonicalChanged {
		if _, err := fmt.Fprintln(output, "Canonical Agent Skill updated."); err != nil {
			return err
		}
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
		if change.BackupPath != "" {
			if _, err := fmt.Fprintf(output, "Backup: %s\n", change.BackupPath); err != nil {
				return err
			}
		}
	}
	if result.CanonicalRemoved {
		_, err := fmt.Fprintln(output, "Canonical Agent Skill removed.")
		return err
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
	allowConflictBackup bool,
) []skills.InstallRequest {
	requests := make([]skills.InstallRequest, 0, len(targets))
	for _, target := range targets {
		requests = append(requests, skills.InstallRequest{
			Target:              skills.Target(target),
			AllowConflictBackup: allowConflictBackup,
		})
	}
	return requests
}

func skillTargetName(target skills.Target) string {
	switch target {
	case skills.TargetCodex:
		return "Codex"
	case skills.TargetClaude:
		return "Claude Code"
	case skills.TargetGemini:
		return "Gemini CLI"
	default:
		return string(target)
	}
}

func requiresConflictBackup(state skills.State) bool {
	switch state {
	case skills.StateModified, skills.StateConflicting, skills.StateBroken:
		return true
	default:
		return false
	}
}

func isTerminalReader(input io.Reader) bool {
	file, ok := input.(*os.File)
	if !ok {
		return false
	}
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	return err == nil
}
