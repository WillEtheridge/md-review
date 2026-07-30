// Package skills installs mdReview's instruction file into supported global
// user skill directories.
package skills

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Target identifies one global installation target.
type Target string

const (
	TargetCodex  Target = "codex"
	TargetClaude Target = "claude"
	TargetPi     Target = "pi"
)

// State reports only whether the target file is installed.
type State string

const (
	StateNotInstalled State = "not-installed"
	StateInstalled    State = "installed"
)

// Action describes one completed target operation.
type Action string

const (
	ActionInstalled Action = "installed"
	ActionRemoved   Action = "removed"
	ActionUnchanged Action = "unchanged"
)

// InstallRequest authorizes replacing exactly one target SKILL.md.
type InstallRequest struct {
	Target Target
}

// TargetStatus reports one global target.
type TargetStatus struct {
	Target Target
	Path   string
	State  State
}

// Snapshot is the complete read-only installer status.
type Snapshot struct {
	Targets []TargetStatus
}

// Change reports one target-level mutation outcome.
type Change struct {
	Target Target
	Action Action
	State  State
	Path   string
}

// Result reports completed target mutations.
type Result struct {
	Changes []Change
}

// Config supplies one isolated installer environment.
type Config struct {
	HomeDirectory string
	Skill         []byte
}

// Manager owns direct target file operations.
type Manager struct {
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
	errUnsafeLayout    = errors.New("unsafe skill target layout")
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

// Install atomically writes or replaces SKILL.md for explicit targets.
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
		if err := ensureSafeDirectory(manager.homeDirectory, definition.skillDirectory); err != nil {
			return result, fmt.Errorf("prepare %s skill directory: %w", request.Target, err)
		}
		if err := requireMissingOrRegular(definition.skillPath); err != nil {
			return result, fmt.Errorf("inspect %s skill target: %w", request.Target, err)
		}
		if err := atomicWrite(definition.skillPath, manager.skill); err != nil {
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
		state, err := manager.inspectTarget(definition)
		if err != nil {
			return result, fmt.Errorf("inspect %s skill: %w", target, err)
		}
		action := ActionUnchanged
		if state == StateInstalled {
			if err := os.Remove(definition.skillPath); err != nil {
				return result, fmt.Errorf("remove %s skill: %w", target, err)
			}
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
	if err := validateDirectoryChain(manager.homeDirectory, definition.skillDirectory); err != nil {
		return "", err
	}
	directoryInfo, err := os.Lstat(definition.skillDirectory)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return StateNotInstalled, nil
	case err != nil:
		return "", err
	case directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir():
		return "", errUnsafeLayout
	}
	info, err := os.Lstat(definition.skillPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return StateNotInstalled, nil
	case err != nil:
		return "", err
	case info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular():
		return "", errUnsafeLayout
	default:
		return StateInstalled, nil
	}
}

func ensureSafeDirectory(homeDirectory, targetDirectory string) error {
	relative, err := filepath.Rel(homeDirectory, targetDirectory)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errUnsafeLayout
	}
	current := homeDirectory
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			if err := os.Mkdir(current, 0o700); err != nil {
				if !errors.Is(err, os.ErrExist) {
					return err
				}
				info, inspectErr := os.Lstat(current)
				if inspectErr != nil ||
					info.Mode()&os.ModeSymlink != 0 ||
					!info.IsDir() {
					return errUnsafeLayout
				}
			}
		case statErr != nil:
			return statErr
		case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
			return errUnsafeLayout
		}
	}
	return nil
}

func validateDirectoryChain(homeDirectory, targetDirectory string) error {
	relative, err := filepath.Rel(homeDirectory, targetDirectory)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errUnsafeLayout
	}
	current := homeDirectory
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			return nil
		case statErr != nil:
			return statErr
		case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
			return errUnsafeLayout
		}
	}
	return nil
}

func requireMissingOrRegular(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular():
		return errUnsafeLayout
	default:
		return nil
	}
}

func atomicWrite(path string, content []byte) error {
	randomBytes := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, randomBytes); err != nil {
		return err
	}
	temporaryPath := filepath.Join(
		filepath.Dir(path),
		".mdreview-skill-"+hex.EncodeToString(randomBytes),
	)
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	closed := false
	cleanup := func() {
		if !closed {
			_ = file.Close()
		}
		_ = os.Remove(temporaryPath)
	}
	if _, err := file.Write(content); err != nil {
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		closed = true
		cleanup()
		return err
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
