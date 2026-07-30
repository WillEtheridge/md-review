// Package cli parses command-line inputs before lifecycle code acquires any
// workspace or runtime resources.
package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// Command identifies one complete mdReview command form.
type Command uint8

const (
	// Serve starts the foreground Markdown review service.
	Serve Command = iota
	// Version prints the installed mdReview release version.
	Version
	// Setup interactively selects supported Agent Skill targets.
	Setup
	// SkillStatus inspects every supported global target.
	SkillStatus
	// SkillInstall installs the skill for explicitly named targets.
	SkillInstall
	// SkillUninstall removes explicitly selected target files.
	SkillUninstall
)

// Target identifies one supported global Agent Skill location.
type Target string

const (
	TargetCodex  Target = "codex"
	TargetClaude Target = "claude"
	TargetPi     Target = "pi"
)

// Options is one validated mdReview command-line configuration.
type Options struct {
	Command      Command
	Directory    string
	Port         uint16
	PortExplicit bool
	Targets      []Target
}

// Parse converts command-line arguments to Options. workingDirectory is called
// only for the serve command, so version and management commands remain usable
// even when the process current directory has disappeared.
func Parse(
	arguments []string,
	workingDirectory func() (string, error),
) (Options, error) {
	if len(arguments) > 0 {
		switch arguments[0] {
		case "--version":
			if len(arguments) != 1 {
				return Options{}, fmt.Errorf("--version does not accept arguments")
			}
			return Options{Command: Version}, nil
		case "setup":
			if len(arguments) != 1 {
				return Options{}, fmt.Errorf("setup does not accept arguments")
			}
			return Options{Command: Setup}, nil
		case "skill":
			return parseSkill(arguments[1:])
		}
	}

	if workingDirectory == nil {
		return Options{}, fmt.Errorf("determine the working directory before parsing serve arguments")
	}
	directory, err := workingDirectory()
	if err != nil {
		return Options{}, fmt.Errorf("determine current directory: %w", err)
	}
	if directory == "" {
		return Options{}, fmt.Errorf("determine the working directory before parsing serve arguments")
	}

	options := Options{Command: Serve, Directory: directory}
	directorySet := false

	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "--port":
			if options.PortExplicit {
				return Options{}, fmt.Errorf("--port may be specified only once")
			}
			if index+1 >= len(arguments) {
				return Options{}, fmt.Errorf("--port requires a value between 1 and 65535")
			}
			port, portErr := parsePort(arguments[index+1])
			if portErr != nil {
				return Options{}, portErr
			}
			options.Port = port
			options.PortExplicit = true
			index++
		case strings.HasPrefix(argument, "--port="):
			if options.PortExplicit {
				return Options{}, fmt.Errorf("--port may be specified only once")
			}
			port, portErr := parsePort(argument[len("--port="):])
			if portErr != nil {
				return Options{}, portErr
			}
			options.Port = port
			options.PortExplicit = true
		case len(argument) > 0 && argument[0] == '-':
			return Options{}, fmt.Errorf("unknown option %q", argument)
		case directorySet:
			return Options{}, fmt.Errorf("expected at most one directory argument")
		default:
			options.Directory = argument
			directorySet = true
		}
	}

	return options, nil
}

func parseSkill(arguments []string) (Options, error) {
	if len(arguments) == 0 {
		return Options{}, fmt.Errorf("skill requires status, install, or uninstall")
	}

	switch arguments[0] {
	case "status":
		if len(arguments) != 1 {
			return Options{}, fmt.Errorf("skill status does not accept arguments")
		}
		return Options{Command: SkillStatus}, nil
	case "install":
		return parseSkillMutation(SkillInstall, arguments[1:])
	case "uninstall":
		return parseSkillMutation(SkillUninstall, arguments[1:])
	default:
		return Options{}, fmt.Errorf(
			"unknown skill command %q; use status, install, or uninstall",
			arguments[0],
		)
	}
}

func parseSkillMutation(command Command, arguments []string) (Options, error) {
	options := Options{Command: command}
	seenTargets := make(map[Target]struct{})

	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "--target":
			if index+1 >= len(arguments) {
				return Options{}, fmt.Errorf("--target requires codex, claude, or pi")
			}
			target, err := parseTarget(arguments[index+1])
			if err != nil {
				return Options{}, err
			}
			if err := addTarget(&options, seenTargets, target); err != nil {
				return Options{}, err
			}
			index++
		case strings.HasPrefix(argument, "--target="):
			target, err := parseTarget(argument[len("--target="):])
			if err != nil {
				return Options{}, err
			}
			if err := addTarget(&options, seenTargets, target); err != nil {
				return Options{}, err
			}
		case len(argument) > 0 && argument[0] == '-':
			return Options{}, fmt.Errorf("unknown option %q", argument)
		default:
			return Options{}, fmt.Errorf("%s does not accept positional arguments", commandName(command))
		}
	}

	if len(options.Targets) == 0 {
		return Options{}, fmt.Errorf("%s requires at least one explicit --target", commandName(command))
	}
	return options, nil
}

func parseTarget(value string) (Target, error) {
	target := Target(value)
	switch target {
	case TargetCodex, TargetClaude, TargetPi:
		return target, nil
	default:
		return "", fmt.Errorf("--target must be codex, claude, or pi")
	}
}

func addTarget(
	options *Options,
	seen map[Target]struct{},
	target Target,
) error {
	if _, exists := seen[target]; exists {
		return fmt.Errorf("--target %s may be specified only once", target)
	}
	seen[target] = struct{}{}
	options.Targets = append(options.Targets, target)
	return nil
}

func commandName(command Command) string {
	if command == SkillInstall {
		return "skill install"
	}
	return "skill uninstall"
}

func parsePort(value string) (uint16, error) {
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("--port must be an integer between 1 and 65535")
	}
	return uint16(port), nil
}
