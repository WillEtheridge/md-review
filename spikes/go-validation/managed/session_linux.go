//go:build linux

package managed

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

var (
	ErrNoControllingTerminal = errors.New("managed mode requires a controlling terminal")
	ErrNotForeground         = errors.New("managed mode must be the terminal foreground process group")
	ErrParentChanged         = errors.New("direct parent changed during managed-mode startup")
)

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

func DisarmParentDeath() error {
	return unix.Prctl(unix.PR_SET_PDEATHSIG, 0, 0, 0, 0)
}
