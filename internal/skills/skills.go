// Package skills installs mdReview's instruction file into supported global
// user skill directories.
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Target identifies one global installation target.
type Target string

const (
	// TargetCodex is the global Codex skill destination.
	TargetCodex Target = "codex"
	// TargetClaude is the global Claude Code skill destination.
	TargetClaude Target = "claude"
	// TargetPi is the global Pi skill destination.
	TargetPi Target = "pi"
)

// State reports only whether the target file is installed.
type State string

const (
	// StateNotInstalled means the target SKILL.md does not exist.
	StateNotInstalled State = "not-installed"
	// StateInstalled means the target SKILL.md exists; its contents are not
	// compared because status is intentionally presence-only.
	StateInstalled State = "installed"
)

// Action describes one completed target operation.
type Action string

const (
	// ActionInstalled means the target file was written during this operation.
	ActionInstalled Action = "installed"
	// ActionRemoved means the target file was deleted during this operation.
	ActionRemoved Action = "removed"
	// ActionUnchanged means uninstall found no target file to remove.
	ActionUnchanged Action = "unchanged"
)

// InstallRequest authorizes replacing exactly one target SKILL.md.
type InstallRequest struct {
	// Target is intentionally explicit; installation never chooses a default
	// agent destination on the caller's behalf.
	Target Target
}

// TargetStatus reports one global target.
type TargetStatus struct {
	// Target identifies the configured global destination.
	Target Target
	// Path is the absolute user-facing destination path.
	Path string
	// State reports presence only and does not inspect file contents.
	State State
}

// Snapshot is the complete read-only installer status.
type Snapshot struct {
	// Targets is stable in the product's Codex, Claude Code, Pi order.
	Targets []TargetStatus
}

// Change reports one target-level mutation outcome.
type Change struct {
	// Target identifies the destination affected by this result.
	Target Target
	// Action distinguishes an actual write, removal, or no-op.
	Action Action
	// State is the resulting presence state after the action.
	State State
	// Path is the absolute destination path reported to the terminal.
	Path string
}

// Result reports completed target mutations.
type Result struct {
	// Changes contains completed operations in request order. A returned error
	// may accompany a partial prefix when a later target fails.
	Changes []Change
}

// Config supplies one isolated installer environment.
type Config struct {
	// HomeDirectory is the absolute current user's home; it is injected to keep
	// target path derivation deterministic and testable.
	HomeDirectory string
	// Skill is copied at construction so later caller mutations cannot alter an
	// installation in progress.
	Skill []byte
}

// Manager owns direct target file operations.
type Manager struct {
	// homeDirectory and skill are immutable after construction. No process-wide
	// registry or daemon coordinates installations between invocations.
	homeDirectory string
	skill         []byte
}

type targetDefinition struct {
	target         Target
	skillDirectory string
	skillPath      string
}

var (
	errNoTargets       = errors.New("at least one explicit skill target is required")
	errDuplicateTarget = errors.New("skill targets must not be repeated")
	errInvalidTarget   = errors.New("unsupported skill target")
)

// New validates an injected installer environment.
func New(config Config) (*Manager, error) {
	if !filepath.IsAbs(config.HomeDirectory) {
		return nil, errors.New("skill home directory must be absolute")
	}
	if len(config.Skill) == 0 {
		return nil, errors.New("skill bytes are required")
	}
	return &Manager{
		homeDirectory: filepath.Clean(config.HomeDirectory),
		skill:         append([]byte(nil), config.Skill...),
	}, nil
}

// NewFromEnvironment constructs a Manager for the current user.
func NewFromEnvironment(skill []byte) (*Manager, error) {
	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home directory: %w", err)
	}
	return New(Config{
		HomeDirectory: homeDirectory,
		Skill:         skill,
	})
}

// Status reports installed or not installed without mutating targets.
func (manager *Manager) Status() (Snapshot, error) {
	snapshot := Snapshot{Targets: make([]TargetStatus, 0, 3)}
	for _, definition := range manager.definitions() {
		state, err := manager.inspectTarget(definition)
		if err != nil {
			return Snapshot{}, fmt.Errorf("inspect %s skill: %w", definition.target, err)
		}
		snapshot.Targets = append(snapshot.Targets, TargetStatus{
			Target: definition.target,
			Path:   definition.skillPath,
			State:  state,
		})
	}
	return snapshot, nil
}

// Install writes or replaces SKILL.md for explicit targets.
func (manager *Manager) Install(requests []InstallRequest) (Result, error) {
	targets := make([]Target, 0, len(requests))
	for _, request := range requests {
		targets = append(targets, request.Target)
	}
	if err := validateTargets(targets); err != nil {
		return Result{}, err
	}

	result := Result{Changes: make([]Change, 0, len(requests))}
	for _, request := range requests {
		definition, _ := manager.definition(request.Target)
		if err := os.MkdirAll(definition.skillDirectory, 0o700); err != nil {
			return result, fmt.Errorf("prepare %s skill directory: %w", request.Target, err)
		}
		if err := os.WriteFile(definition.skillPath, manager.skill, 0o600); err != nil {
			return result, fmt.Errorf("write %s skill: %w", request.Target, err)
		}
		result.Changes = append(result.Changes, Change{
			Target: request.Target,
			Action: ActionInstalled,
			State:  StateInstalled,
			Path:   definition.skillPath,
		})
	}
	return result, nil
}

// Uninstall removes explicit target files and empty mdreview directories.
func (manager *Manager) Uninstall(targets []Target) (Result, error) {
	if err := validateTargets(targets); err != nil {
		return Result{}, err
	}
	result := Result{Changes: make([]Change, 0, len(targets))}
	for _, target := range targets {
		definition, _ := manager.definition(target)
		action := ActionUnchanged
		if err := os.Remove(definition.skillPath); err == nil {
			action = ActionRemoved
			entries, err := os.ReadDir(definition.skillDirectory)
			if err != nil {
				return result, fmt.Errorf("inspect %s skill directory: %w", target, err)
			}
			if len(entries) == 0 {
				if err := os.Remove(definition.skillDirectory); err != nil {
					return result, fmt.Errorf("remove empty %s skill directory: %w", target, err)
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("remove %s skill: %w", target, err)
		}
		result.Changes = append(result.Changes, Change{
			Target: target,
			Action: action,
			State:  StateNotInstalled,
			Path:   definition.skillPath,
		})
	}
	return result, nil
}

func (manager *Manager) definitions() []targetDefinition {
	codexDirectory := filepath.Join(manager.homeDirectory, ".codex", "skills", "mdreview")
	claudeDirectory := filepath.Join(manager.homeDirectory, ".claude", "skills", "mdreview")
	piDirectory := filepath.Join(manager.homeDirectory, ".pi", "agent", "skills", "mdreview")
	return []targetDefinition{
		{
			target:         TargetCodex,
			skillDirectory: codexDirectory,
			skillPath:      filepath.Join(codexDirectory, "SKILL.md"),
		},
		{
			target:         TargetClaude,
			skillDirectory: claudeDirectory,
			skillPath:      filepath.Join(claudeDirectory, "SKILL.md"),
		},
		{
			target:         TargetPi,
			skillDirectory: piDirectory,
			skillPath:      filepath.Join(piDirectory, "SKILL.md"),
		},
	}
}

func (manager *Manager) definition(target Target) (targetDefinition, bool) {
	for _, definition := range manager.definitions() {
		if definition.target == target {
			return definition, true
		}
	}
	return targetDefinition{}, false
}

func validateTargets(targets []Target) error {
	if len(targets) == 0 {
		return errNoTargets
	}
	seen := make(map[Target]struct{}, len(targets))
	for _, target := range targets {
		if target != TargetCodex && target != TargetClaude && target != TargetPi {
			return errInvalidTarget
		}
		if _, exists := seen[target]; exists {
			return errDuplicateTarget
		}
		seen[target] = struct{}{}
	}
	return nil
}

func (manager *Manager) inspectTarget(definition targetDefinition) (State, error) {
	_, err := os.Stat(definition.skillPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return StateNotInstalled, nil
	case err != nil:
		return "", err
	default:
		return StateInstalled, nil
	}
}
