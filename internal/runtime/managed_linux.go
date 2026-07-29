//go:build linux

package runtime

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

var (
	// ErrNoControllingTerminal reports that managed mode cannot observe terminal
	// ownership through the supplied descriptor.
	ErrNoControllingTerminal = errors.New("managed mode requires a controlling terminal")
	// ErrNotForeground reports that managed mode is not the terminal's
	// foreground process group.
	ErrNotForeground = errors.New("managed mode must be the terminal foreground process group")
	// ErrParentChanged reports that the direct parent no longer matches the PID
	// snapshotted by command startup.
	ErrParentChanged = errors.New("direct parent changed during managed-mode startup")
)

// ValidateSession verifies that terminalFD is a controlling terminal whose
// foreground process group contains mdReview. This is a startup observation;
// it cannot prove who owns the terminal master or infer an agent session.
func ValidateSession(terminalFD int) error {
	terminalGroup, err := unix.IoctlGetInt(terminalFD, unix.TIOCGPGRP)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoControllingTerminal, err)
	}
	return validateForegroundGroups(terminalGroup, unix.Getpgrp())
}

func validateForegroundGroups(terminalGroup, processGroup int) error {
	if terminalGroup != processGroup {
		return fmt.Errorf(
			"%w: terminal=%d process=%d",
			ErrNotForeground,
			terminalGroup,
			processGroup,
		)
	}
	return nil
}

// ArmParentDeath asks Linux to deliver signal when the snapshotted direct
// parent exits. The immediate parent check closes the race between the
// caller's snapshot and PR_SET_PDEATHSIG, but cannot detect reparenting that
// happened before that snapshot or establish an abstract agent lifetime.
func ArmParentDeath(expectedParent int, signal unix.Signal) error {
	if expectedParent <= 1 {
		return fmt.Errorf("%w: invalid expected parent %d", ErrParentChanged, expectedParent)
	}
	if err := unix.Prctl(
		unix.PR_SET_PDEATHSIG,
		uintptr(signal),
		0,
		0,
		0,
	); err != nil {
		return err
	}
	actualParent := unix.Getppid()
	if actualParent != expectedParent {
		_ = unix.Prctl(unix.PR_SET_PDEATHSIG, 0, 0, 0, 0)
		return fmt.Errorf(
			"%w: expected=%d actual=%d",
			ErrParentChanged,
			expectedParent,
			actualParent,
		)
	}
	return nil
}

// DisarmParentDeath clears a parent-death signal previously armed by
// ArmParentDeath.
func DisarmParentDeath() error {
	return unix.Prctl(unix.PR_SET_PDEATHSIG, 0, 0, 0, 0)
}
