//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCompiledCommandRejectsDetachedManagedOwnerAndCleansState(t *testing.T) {
	binaryPath := buildCommand(t)
	workspaceRoot := t.TempDir()
	runtimeParent := t.TempDir()
	command := exec.Command(
		binaryPath,
		workspaceRoot,
		"--port",
		strconv.Itoa(availablePort(t)),
		"--managed-session",
	)
	command.Env = commandEnvironment(runtimeParent)

	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("detached managed command unexpectedly succeeded:\n%s", output)
	}
	if !strings.Contains(
		string(output),
		"validate managed session: managed mode requires a controlling terminal",
	) {
		t.Fatalf("detached managed command output:\n%s", output)
	}
	if count := runtimeStateFileCount(t, runtimeParent); count != 0 {
		t.Fatalf("runtime state files after managed rejection = %d, want 0", count)
	}
}

func TestCompiledManagedCommandStopsOnPTYCloseAndCleansState(t *testing.T) {
	binaryPath := buildCommand(t)
	workspaceRoot := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(workspaceRoot, "README.md"),
		[]byte("# Managed process integration\n"),
		0o600,
	); err != nil {
		t.Fatalf("write README: %v", err)
	}

	runtimeParent := t.TempDir()
	command := exec.Command(
		binaryPath,
		workspaceRoot,
		"--port",
		strconv.Itoa(availablePort(t)),
		"--managed-session",
	)
	command.Env = commandEnvironment(runtimeParent)
	terminal, err := startCommandWithPTY(command)
	if err != nil {
		t.Fatalf("start managed command with PTY: %v", err)
	}
	waitDone := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = command.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() {
		_ = terminal.Close()
		select {
		case <-waitDone:
		default:
			_ = command.Process.Kill()
			<-waitDone
		}
	})

	output, instanceURL := waitForStartup(t, terminal)
	if instanceURL == "" {
		t.Fatalf("managed startup has no URL:\n%s", output)
	}
	if count := runtimeStateFileCount(t, runtimeParent); count != 1 {
		t.Fatalf("runtime state files while managed command is ready = %d, want 1", count)
	}

	if err := terminal.Close(); err != nil {
		t.Fatalf("close managed PTY: %v", err)
	}
	select {
	case <-waitDone:
		if waitErr != nil {
			t.Fatalf("managed command exit after PTY close: %v", waitErr)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("managed command survived controlling PTY closure")
	}

	if count := runtimeStateFileCount(t, runtimeParent); count != 0 {
		t.Fatalf("runtime state files after managed shutdown = %d, want 0", count)
	}
}

func startCommandWithPTY(command *exec.Cmd) (*os.File, error) {
	masterFD, err := unix.Open(
		"/dev/ptmx",
		unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open PTY master: %w", err)
	}
	master := os.NewFile(uintptr(masterFD), "/dev/ptmx")
	closeMaster := true
	defer func() {
		if closeMaster {
			_ = master.Close()
		}
	}()

	number, err := unix.IoctlGetInt(masterFD, unix.TIOCGPTN)
	if err != nil {
		return nil, fmt.Errorf("read PTY number: %w", err)
	}
	if err := unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, 0); err != nil {
		return nil, fmt.Errorf("unlock PTY: %w", err)
	}
	slavePath := "/dev/pts/" + strconv.Itoa(number)
	slaveFD, err := unix.Open(
		slavePath,
		unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open PTY slave: %w", err)
	}
	slave := os.NewFile(uintptr(slaveFD), slavePath)
	defer slave.Close()

	command.Stdin = slave
	command.Stdout = slave
	command.Stderr = slave
	command.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start command: %w", err)
	}
	closeMaster = false
	return master, nil
}

func runtimeStateFileCount(t *testing.T, runtimeParent string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(runtimeParent, "mdreview"))
	if err != nil {
		t.Fatalf("read runtime directory: %v", err)
	}
	count := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			count++
		}
	}
	return count
}
