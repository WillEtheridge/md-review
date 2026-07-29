// Package skills manages mdReview's canonical Agent Skill and its explicitly
// selected host entries. Stored ownership metadata is never accepted as path
// authority; every path is re-derived from the current environment.
package skills

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Target identifies one supported agent host.
type Target string

const (
	TargetCodex  Target = "codex"
	TargetClaude Target = "claude"
	TargetGemini Target = "gemini"
)

// EntryKind identifies how a host receives the canonical skill.
type EntryKind string

const (
	EntryLink EntryKind = "link"
	EntryCopy EntryKind = "copy"
)

// State describes canonical or target ownership observed on disk.
type State string

const (
	StateNotInstalled State = "not-installed"
	StateManaged      State = "managed"
	StateOutdated     State = "outdated"
	StateModified     State = "modified"
	StateConflicting  State = "conflicting"
	StateBroken       State = "broken"
	StatePending      State = "pending"
)

// Action describes one completed or deliberately preserved mutation outcome.
type Action string

const (
	ActionInstalled Action = "installed"
	ActionUpdated   Action = "updated"
	ActionAdopted   Action = "adopted"
	ActionBackedUp  Action = "backed-up"
	ActionRemoved   Action = "removed"
	ActionRestored  Action = "restored"
	ActionPreserved Action = "preserved"
	ActionRecovered Action = "recovered"
	ActionUnchanged Action = "unchanged"
)

// Detection reports the read-only evidence used for setup suggestions.
type Detection struct {
	Target                 Target
	Detected               bool
	ExecutableFound        bool
	ConfigurationRootFound bool
}

// InstallRequest authorizes installation for exactly one target. Conflict
// backup permission never applies to another request in the same operation.
type InstallRequest struct {
	Target              Target
	AllowConflictBackup bool
}

// CanonicalStatus reports the canonical managed asset.
type CanonicalStatus struct {
	Path   string
	State  State
	SHA256 string
}

// TargetStatus reports one host entry without changing it.
type TargetStatus struct {
	Target     Target
	Kind       EntryKind
	Path       string
	State      State
	BackupPath string
}

// PendingStatus reports an interrupted operation that a later mutation may
// recover only after revalidating its derived paths and observed entries.
type PendingStatus struct {
	Target    Target
	Operation string
	Phase     string
}

// Snapshot is the complete read-only installer status.
type Snapshot struct {
	Canonical CanonicalStatus
	Targets   []TargetStatus
	Pending   *PendingStatus
}

// Change reports one target-level outcome from Install or Uninstall.
type Change struct {
	Target     Target
	Action     Action
	State      State
	Path       string
	BackupPath string
}

// Result reports successful mutations completed before any returned error.
type Result struct {
	CanonicalChanged bool
	CanonicalRemoved bool
	Changes          []Change
}

// Config supplies the environment and nondeterministic inputs used by a
// Manager. Tests provide isolated directories, a fixed clock, and deterministic
// randomness; production uses NewFromEnvironment.
type Config struct {
	HomeDirectory   string
	DataDirectory   string
	PathEnvironment string
	Skill           []byte
	Now             func() time.Time
	Random          io.Reader
}

// Manager owns skill detection, status, and mutation for one fixed environment.
type Manager struct {
	homeDirectory   string
	dataDirectory   string
	pathEnvironment string
	skill           []byte
	skillHash       string
	now             func() time.Time
	random          io.Reader
	fail            func(string) error
}

type targetDefinition struct {
	target            Target
	executable        string
	configurationRoot string
	entryPath         string
	kind              EntryKind
}

// TargetConflictError reports an entry that requires explicit backup
// authorization before installation.
type TargetConflictError struct {
	Target Target
	Path   string
	State  State
}

func (failure *TargetConflictError) Error() string {
	return fmt.Sprintf("%s skill entry %q is %s", failure.Target, failure.Path, failure.State)
}

// CanonicalModifiedError reports canonical content that no longer matches the
// hash last written by mdReview.
type CanonicalModifiedError struct {
	Path string
}

func (failure *CanonicalModifiedError) Error() string {
	return fmt.Sprintf("canonical skill %q was modified outside mdReview", failure.Path)
}

// UnsafeRecordError reports ownership metadata whose paths or values cannot be
// accepted safely.
type UnsafeRecordError struct {
	Reason string
}

func (failure *UnsafeRecordError) Error() string {
	return "unsafe skill ownership record: " + failure.Reason
}

// PendingConflictError reports an interrupted operation whose observed disk
// state is not unambiguous enough for automatic recovery.
type PendingConflictError struct {
	Target Target
	Reason string
}

func (failure *PendingConflictError) Error() string {
	return fmt.Sprintf("pending %s skill operation is ambiguous: %s", failure.Target, failure.Reason)
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
	if !filepath.IsAbs(config.DataDirectory) {
		return nil, errors.New("skill data directory must be absolute")
	}
	if len(config.Skill) == 0 {
		return nil, errors.New("canonical skill bytes are required")
	}
	if config.Now == nil {
		return nil, errors.New("skill clock is required")
	}
	if config.Random == nil {
		return nil, errors.New("skill randomness is required")
	}
	return &Manager{
		homeDirectory:   filepath.Clean(config.HomeDirectory),
		dataDirectory:   filepath.Clean(config.DataDirectory),
		pathEnvironment: config.PathEnvironment,
		skill:           append([]byte(nil), config.Skill...),
		skillHash:       hashBytes(config.Skill),
		now:             config.Now,
		random:          config.Random,
	}, nil
}

// NewFromEnvironment constructs a Manager from the current home, XDG data, and
// PATH environment. The supplied bytes remain the only canonical skill source.
func NewFromEnvironment(skill []byte) (*Manager, error) {
	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home directory: %w", err)
	}
	dataDirectory := os.Getenv("XDG_DATA_HOME")
	if dataDirectory == "" {
		dataDirectory = filepath.Join(homeDirectory, ".local", "share")
	}
	return New(Config{
		HomeDirectory:   homeDirectory,
		DataDirectory:   dataDirectory,
		PathEnvironment: os.Getenv("PATH"),
		Skill:           skill,
		Now:             time.Now,
		Random:          rand.Reader,
	})
}

// Detect returns read-only executable and host-configuration evidence for all
// supported targets.
func (manager *Manager) Detect() ([]Detection, error) {
	detections := make([]Detection, 0, len(supportedTargets()))
	for _, definition := range manager.definitions() {
		executableFound, err := manager.executableExists(definition.executable)
		if err != nil {
			return nil, fmt.Errorf("detect %s executable: %w", definition.target, err)
		}
		configurationRootFound, err := directoryExists(definition.configurationRoot)
		if err != nil {
			return nil, fmt.Errorf("detect %s configuration root: %w", definition.target, err)
		}
		detections = append(detections, Detection{
			Target:                 definition.target,
			Detected:               executableFound || configurationRootFound,
			ExecutableFound:        executableFound,
			ConfigurationRootFound: configurationRootFound,
		})
	}
	return detections, nil
}

func (manager *Manager) definitions() []targetDefinition {
	return []targetDefinition{
		{
			target:            TargetCodex,
			executable:        "codex",
			configurationRoot: filepath.Join(manager.homeDirectory, ".codex"),
			entryPath:         filepath.Join(manager.homeDirectory, ".agents", "skills", "mdreview"),
			kind:              EntryLink,
		},
		{
			target:            TargetClaude,
			executable:        "claude",
			configurationRoot: filepath.Join(manager.homeDirectory, ".claude"),
			entryPath:         filepath.Join(manager.homeDirectory, ".claude", "skills", "mdreview"),
			kind:              EntryCopy,
		},
		{
			target:            TargetGemini,
			executable:        "gemini",
			configurationRoot: filepath.Join(manager.homeDirectory, ".gemini"),
			entryPath:         filepath.Join(manager.homeDirectory, ".gemini", "skills", "mdreview"),
			kind:              EntryLink,
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

func supportedTargets() []Target {
	return []Target{TargetCodex, TargetClaude, TargetGemini}
}

func validateTargets(targets []Target) error {
	if len(targets) == 0 {
		return errNoTargets
	}
	seen := make(map[Target]struct{}, len(targets))
	for _, target := range targets {
		switch target {
		case TargetCodex, TargetClaude, TargetGemini:
		default:
			return fmt.Errorf("%w: %q", errInvalidTarget, target)
		}
		if _, duplicate := seen[target]; duplicate {
			return fmt.Errorf("%w: %q", errDuplicateTarget, target)
		}
		seen[target] = struct{}{}
	}
	return nil
}

func validateInstallRequests(requests []InstallRequest) error {
	if len(requests) == 0 {
		return errNoTargets
	}
	seen := make(map[Target]struct{}, len(requests))
	for _, request := range requests {
		switch request.Target {
		case TargetCodex, TargetClaude, TargetGemini:
		default:
			return fmt.Errorf("%w: %q", errInvalidTarget, request.Target)
		}
		if _, duplicate := seen[request.Target]; duplicate {
			return fmt.Errorf("%w: %q", errDuplicateTarget, request.Target)
		}
		seen[request.Target] = struct{}{}
	}
	return nil
}

func (manager *Manager) canonicalDirectory() string {
	return filepath.Join(manager.dataDirectory, "mdreview", "skills", "mdreview")
}

func (manager *Manager) canonicalPath() string {
	return filepath.Join(manager.canonicalDirectory(), "SKILL.md")
}

func (manager *Manager) recordPath() string {
	return filepath.Join(manager.dataDirectory, "mdreview", "skill-installation.json")
}

func (manager *Manager) lockPath() string {
	return filepath.Join(manager.dataDirectory, "mdreview", "skill-installation.lock")
}

func (manager *Manager) executableExists(name string) (bool, error) {
	for _, directory := range filepath.SplitList(manager.pathEnvironment) {
		if directory == "" {
			continue
		}
		path := filepath.Join(directory, name)
		info, err := os.Stat(path)
		switch {
		case err == nil:
			if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
				return true, nil
			}
		case errors.Is(err, os.ErrNotExist), errors.Is(err, os.ErrPermission):
			continue
		default:
			return false, err
		}
	}
	return false, nil
}

func directoryExists(path string) (bool, error) {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		return info.IsDir(), nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

func hashBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("%x", sum[:])
}

func hashFile(path string) (string, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", info, errors.New("path is not a regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", info, err
	}
	return hashBytes(content), info, nil
}

func isSafeHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
