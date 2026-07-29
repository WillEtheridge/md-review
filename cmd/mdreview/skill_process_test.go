//go:build linux

package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompiledSkillManagementInstallsStatusAndUninstallsSelectedTargets(t *testing.T) {
	binaryPath := buildCommand(t)
	homeDirectory := t.TempDir()
	dataDirectory := t.TempDir()
	environment := skillCommandEnvironment(homeDirectory, dataDirectory)

	install := exec.Command(
		binaryPath,
		"skill",
		"install",
		"--target",
		"codex",
		"--target",
		"claude",
		"--target",
		"gemini",
	)
	install.Env = environment
	if output, err := install.CombinedOutput(); err != nil {
		t.Fatalf("install skills: %v\n%s", err, output)
	}

	canonicalDirectory := filepath.Join(
		dataDirectory,
		"mdreview",
		"skills",
		"mdreview",
	)
	for target, path := range map[string]string{
		"Codex":  filepath.Join(homeDirectory, ".agents", "skills", "mdreview"),
		"Gemini": filepath.Join(homeDirectory, ".gemini", "skills", "mdreview"),
	} {
		link, err := os.Readlink(path)
		if err != nil {
			t.Fatalf("read %s skill link: %v", target, err)
		}
		if link != canonicalDirectory {
			t.Fatalf("%s skill link = %q, want %q", target, link, canonicalDirectory)
		}
	}

	claudeDirectory := filepath.Join(homeDirectory, ".claude", "skills", "mdreview")
	claudeInfo, err := os.Lstat(claudeDirectory)
	if err != nil {
		t.Fatalf("inspect Claude skill copy: %v", err)
	}
	if !claudeInfo.IsDir() || claudeInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("Claude skill entry mode = %v, want copied directory", claudeInfo.Mode())
	}
	claudeSkill, err := os.ReadFile(filepath.Join(claudeDirectory, "SKILL.md"))
	if err != nil {
		t.Fatalf("read Claude skill copy: %v", err)
	}
	if !bytes.Contains(claudeSkill, []byte("name: mdreview")) {
		t.Fatal("Claude skill copy is not the embedded canonical skill")
	}

	status := exec.Command(binaryPath, "skill", "status")
	status.Env = environment
	statusOutput, err := status.CombinedOutput()
	if err != nil {
		t.Fatalf("read skill status: %v\n%s", err, statusOutput)
	}
	for _, expected := range []string{
		"Canonical: managed",
		"Codex: managed",
		"Claude Code: managed",
		"Gemini CLI: managed",
	} {
		if !strings.Contains(string(statusOutput), expected) {
			t.Errorf("status output is missing %q:\n%s", expected, statusOutput)
		}
	}

	uninstall := exec.Command(
		binaryPath,
		"skill",
		"uninstall",
		"--target=codex",
		"--target=claude",
		"--target=gemini",
	)
	uninstall.Env = environment
	if output, err := uninstall.CombinedOutput(); err != nil {
		t.Fatalf("uninstall skills: %v\n%s", err, output)
	}
	for _, path := range []string{
		filepath.Join(homeDirectory, ".agents", "skills", "mdreview"),
		claudeDirectory,
		filepath.Join(homeDirectory, ".gemini", "skills", "mdreview"),
		canonicalDirectory,
	} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("path %q remains after uninstall: %v", path, err)
		}
	}
}

func TestCompiledSetupDeclineMakesNoFilesystemChanges(t *testing.T) {
	binaryPath := buildCommand(t)
	homeDirectory := t.TempDir()
	dataDirectory := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(filepath.Join(homeDirectory, ".codex"), 0o700); err != nil {
		t.Fatalf("create detected Codex configuration: %v", err)
	}

	command := exec.Command(binaryPath, "setup")
	command.Env = skillCommandEnvironment(homeDirectory, dataDirectory)
	terminal, err := startCommandWithPTY(command)
	if err != nil {
		t.Fatalf("start setup with PTY: %v", err)
	}
	if _, err := terminal.Write([]byte("no\n")); err != nil {
		t.Fatalf("answer setup prompt: %v", err)
	}
	waitErr := make(chan error, 1)
	go func() {
		waitErr <- command.Wait()
	}()
	select {
	case err := <-waitErr:
		if err != nil {
			t.Fatalf("declined setup exit: %v", err)
		}
	case <-testProcessTimeout():
		_ = command.Process.Kill()
		<-waitErr
		t.Fatal("declined setup did not exit")
	}
	_ = terminal.Close()

	if _, err := os.Lstat(dataDirectory); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("declined setup created data path: %v", err)
	}
	if _, err := os.Lstat(
		filepath.Join(homeDirectory, ".agents", "skills", "mdreview"),
	); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("declined setup created a target: %v", err)
	}
}

func TestCompiledSetupStopsOnInterruptBeforeMutation(t *testing.T) {
	binaryPath := buildCommand(t)
	homeDirectory := t.TempDir()
	dataDirectory := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(filepath.Join(homeDirectory, ".codex"), 0o700); err != nil {
		t.Fatalf("create detected Codex configuration: %v", err)
	}

	command := exec.Command(binaryPath, "setup")
	command.Env = skillCommandEnvironment(homeDirectory, dataDirectory)
	terminal, err := startCommandWithPTY(command)
	if err != nil {
		t.Fatalf("start setup with PTY: %v", err)
	}
	t.Cleanup(func() {
		_ = terminal.Close()
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	waitForPTYText(t, terminal, "Install the mdReview skill for Codex? [y/N] ")

	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt setup: %v", err)
	}
	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()
	select {
	case err := <-wait:
		if err == nil {
			t.Fatal("interrupted setup unexpectedly reported success")
		}
	case <-testProcessTimeout():
		t.Fatal("interrupted setup remained blocked on terminal input")
	}
	if _, err := os.Lstat(dataDirectory); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("interrupted setup created data path: %v", err)
	}
}

func waitForPTYText(t *testing.T, terminal *os.File, expected string) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		var output strings.Builder
		buffer := make([]byte, 1)
		for {
			count, err := terminal.Read(buffer)
			if count > 0 {
				output.Write(buffer[:count])
				if strings.Contains(output.String(), expected) {
					result <- nil
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					result <- errors.New("PTY closed before expected prompt")
				} else {
					result <- err
				}
				return
			}
		}
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("wait for setup prompt: %v", err)
		}
	case <-testProcessTimeout():
		t.Fatalf("timed out waiting for setup prompt %q", expected)
	}
}

func skillCommandEnvironment(homeDirectory, dataDirectory string) []string {
	environment := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "HOME=") ||
			strings.HasPrefix(entry, "XDG_DATA_HOME=") ||
			strings.HasPrefix(entry, "PATH=") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(
		environment,
		"HOME="+homeDirectory,
		"XDG_DATA_HOME="+dataDirectory,
		"PATH=/mdreview-test-no-executables",
	)
}

func testProcessTimeout() <-chan time.Time {
	return time.After(5 * time.Second)
}
