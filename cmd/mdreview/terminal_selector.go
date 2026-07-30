package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/term"

	"mdreview.dev/mdreview/internal/skills"
)

const (
	clearLine  = "\x1b[2K\r"
	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
)

type fileDescriptor interface {
	Fd() uintptr
}

type selectorOption struct {
	target skills.Target
	label  string
}

type crlfWriter struct {
	output io.Writer
}

func (writer crlfWriter) Write(content []byte) (int, error) {
	converted := bytes.ReplaceAll(content, []byte{'\n'}, []byte{'\r', '\n'})
	if _, err := writer.output.Write(converted); err != nil {
		return 0, err
	}
	return len(content), nil
}

var selectorOptions = []selectorOption{
	{target: skills.TargetCodex, label: "Codex"},
	{target: skills.TargetClaude, label: "Claude Code"},
	{target: skills.TargetPi, label: "Pi"},
}

func isInteractiveTerminal(input io.Reader, output io.Writer) bool {
	inputFile, inputOK := input.(fileDescriptor)
	outputFile, outputOK := output.(fileDescriptor)
	return inputOK &&
		outputOK &&
		term.IsTerminal(int(inputFile.Fd())) &&
		term.IsTerminal(int(outputFile.Fd()))
}

func selectSkillTargets(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	inputIsTerminal bool,
) ([]skills.Target, error) {
	if !inputIsTerminal {
		return nil, errors.New(
			"setup requires an interactive terminal; use skill install with explicit --target values",
		)
	}

	if inputFile, ok := input.(fileDescriptor); ok {
		descriptor := int(inputFile.Fd())
		state, err := term.MakeRaw(descriptor)
		if err != nil {
			return nil, fmt.Errorf("start Agent Skill selector: %w", err)
		}
		defer term.Restore(descriptor, state)
		output = crlfWriter{output: output}
	}

	return runSkillTargetSelector(ctx, input, output)
}

func runSkillTargetSelector(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
) ([]skills.Target, error) {
	if _, err := fmt.Fprintln(output, `Select agents for the global mdReview skill.
Use Up and Down to move, Space to select, and Enter to confirm.
Press Ctrl+C to cancel.`); err != nil {
		return nil, fmt.Errorf("write Agent Skill selector instructions: %w", err)
	}
	if _, err := io.WriteString(output, hideCursor); err != nil {
		return nil, fmt.Errorf("hide terminal cursor: %w", err)
	}
	defer io.WriteString(output, showCursor)

	cursor := 0
	selected := make([]bool, len(selectorOptions))
	if err := renderSelector(output, cursor, selected, false); err != nil {
		return nil, err
	}

	inputBytes := make(chan byte, 1)
	inputErrors := make(chan error, 1)
	go readSelectorInput(input, inputBytes, inputErrors)

	escapeState := 0
	for {
		select {
		case <-ctx.Done():
			_ = clearSelector(output)
			return nil, fmt.Errorf("setup cancelled: %w", ctx.Err())
		case err := <-inputErrors:
			_ = clearSelector(output)
			if errors.Is(err, io.EOF) {
				return nil, errors.New("setup input ended before a selection")
			}
			return nil, fmt.Errorf("read Agent Skill selector input: %w", err)
		case value := <-inputBytes:
			switch escapeState {
			case 1:
				if value == '[' {
					escapeState = 2
				} else {
					escapeState = 0
				}
				continue
			case 2:
				switch value {
				case 'A':
					cursor = (cursor + len(selectorOptions) - 1) % len(selectorOptions)
				case 'B':
					cursor = (cursor + 1) % len(selectorOptions)
				}
				escapeState = 0
				if err := rerenderSelector(output, cursor, selected, false); err != nil {
					return nil, err
				}
				continue
			}

			switch value {
			case '\x1b':
				escapeState = 1
			case '\x03':
				if err := clearSelector(output); err != nil {
					return nil, err
				}
				return nil, nil
			case ' ':
				selected[cursor] = !selected[cursor]
				if err := rerenderSelector(output, cursor, selected, false); err != nil {
					return nil, err
				}
			case '\r', '\n':
				targets := selectedTargets(selected)
				if len(targets) == 0 {
					if err := rerenderSelector(output, cursor, selected, true); err != nil {
						return nil, err
					}
					continue
				}
				if err := clearSelector(output); err != nil {
					return nil, err
				}
				labels := make([]string, 0, len(targets))
				for index, option := range selectorOptions {
					if selected[index] {
						labels = append(labels, option.label)
					}
				}
				if _, err := fmt.Fprintf(
					output,
					"Selected: %s\n",
					strings.Join(labels, ", "),
				); err != nil {
					return nil, fmt.Errorf("write Agent Skill selection: %w", err)
				}
				return targets, nil
			}
		}
	}
}

func readSelectorInput(
	input io.Reader,
	values chan<- byte,
	failures chan<- error,
) {
	var buffer [1]byte
	for {
		_, err := io.ReadFull(input, buffer[:])
		if err != nil {
			failures <- err
			return
		}
		values <- buffer[0]
	}
}

func selectedTargets(selected []bool) []skills.Target {
	targets := make([]skills.Target, 0, len(selected))
	for index, option := range selectorOptions {
		if selected[index] {
			targets = append(targets, option.target)
		}
	}
	return targets
}

func renderSelector(
	output io.Writer,
	cursor int,
	selected []bool,
	showSelectionError bool,
) error {
	for index, option := range selectorOptions {
		pointer := "  "
		if index == cursor {
			pointer = "> "
		}
		checkbox := "[ ]"
		if selected[index] {
			checkbox = "[x]"
		}
		if _, err := fmt.Fprintf(output, "%s%s %s\n", pointer, checkbox, option.label); err != nil {
			return fmt.Errorf("write Agent Skill selector: %w", err)
		}
	}
	if showSelectionError {
		if _, err := fmt.Fprintln(output, "Select at least one agent."); err != nil {
			return fmt.Errorf("write Agent Skill selector guidance: %w", err)
		}
	} else if _, err := fmt.Fprintln(output); err != nil {
		return fmt.Errorf("write Agent Skill selector spacing: %w", err)
	}
	return nil
}

func rerenderSelector(
	output io.Writer,
	cursor int,
	selected []bool,
	showSelectionError bool,
) error {
	if _, err := io.WriteString(output, "\x1b[4A"); err != nil {
		return fmt.Errorf("move Agent Skill selector cursor: %w", err)
	}
	for range 4 {
		if _, err := io.WriteString(output, clearLine); err != nil {
			return fmt.Errorf("clear Agent Skill selector: %w", err)
		}
		if _, err := io.WriteString(output, "\x1b[1B"); err != nil {
			return fmt.Errorf("move Agent Skill selector cursor: %w", err)
		}
	}
	if _, err := io.WriteString(output, "\x1b[4A"); err != nil {
		return fmt.Errorf("move Agent Skill selector cursor: %w", err)
	}
	return renderSelector(output, cursor, selected, showSelectionError)
}

func clearSelector(output io.Writer) error {
	if _, err := io.WriteString(output, "\x1b[4A"); err != nil {
		return fmt.Errorf("move Agent Skill selector cursor: %w", err)
	}
	for range 4 {
		if _, err := io.WriteString(output, clearLine); err != nil {
			return fmt.Errorf("clear Agent Skill selector: %w", err)
		}
		if _, err := io.WriteString(output, "\x1b[1B"); err != nil {
			return fmt.Errorf("move Agent Skill selector cursor: %w", err)
		}
	}
	if _, err := io.WriteString(output, "\x1b[4A"); err != nil {
		return fmt.Errorf("move Agent Skill selector cursor: %w", err)
	}
	return nil
}
