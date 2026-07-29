//go:build linux

package runtime

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestValidateSessionRejectsPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if err := ValidateSession(int(reader.Fd())); !errors.Is(err, ErrNoControllingTerminal) {
		t.Fatalf("ValidateSession(pipe) = %v", err)
	}
}

func TestForegroundGroupValidation(t *testing.T) {
	if err := validateForegroundGroups(42, 42); err != nil {
		t.Fatal(err)
	}
	if err := validateForegroundGroups(41, 42); !errors.Is(err, ErrNotForeground) {
		t.Fatalf("mismatched groups error = %v", err)
	}
}

func TestArmParentDeathDetectsStartupParentMismatchAndDisarms(t *testing.T) {
	err := ArmParentDeath(unix.Getppid()+1, unix.SIGTERM)
	if !errors.Is(err, ErrParentChanged) {
		t.Fatalf("ArmParentDeath mismatch error = %v", err)
	}
	parentDeathSignal, err := currentParentDeathSignal()
	if err != nil {
		t.Fatalf("read parent-death signal: %v", err)
	}
	if parentDeathSignal != 0 {
		t.Fatalf("parent-death signal after mismatch = %d, want disarmed", parentDeathSignal)
	}
}

func TestManagedPTYHelper(t *testing.T) {
	if os.Getenv("MDREVIEW_MANAGED_PTY_HELPER") != "1" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, unix.SIGHUP, unix.SIGTERM, unix.SIGINT)
	if err := ValidateSession(int(os.Stdin.Fd())); err != nil {
		fmt.Fprintf(os.Stdout, "ERROR %v\n", err)
		os.Exit(41)
	}
	expectedParent := unix.Getppid()
	if err := ArmParentDeath(expectedParent, unix.SIGTERM); err != nil {
		fmt.Fprintf(os.Stdout, "ERROR %v\n", err)
		os.Exit(42)
	}
	fmt.Fprintf(os.Stdout, "READY pid=%d parent=%d\n", os.Getpid(), expectedParent)
	<-signals
	os.Exit(0)
}

func TestManagedModeExitsWhenControllingPTYCloses(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestManagedPTYHelper$")
	command.Env = append(os.Environ(), "MDREVIEW_MANAGED_PTY_HELPER=1")
	terminal, err := startWithPTY(command)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(terminal)
	ready := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		if !strings.Contains(line, "READY") {
			t.Fatalf("helper did not become ready: %q", line)
		}
	case <-time.After(3 * time.Second):
		_ = terminal.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal("timed out waiting for managed PTY helper")
	}

	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("managed helper exit = %v", err)
		}
	case <-time.After(3 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("managed helper survived controlling PTY closure")
	}
}

func TestManagedModeRejectsDetachedLaunch(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestManagedPTYHelper$")
	command.Env = append(os.Environ(), "MDREVIEW_MANAGED_PTY_HELPER=1")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("detached managed helper unexpectedly succeeded")
	}
	if !strings.Contains(string(output), ErrNoControllingTerminal.Error()) {
		t.Fatalf("detached output = %q", output)
	}
}

func TestParentDeathChild(t *testing.T) {
	if os.Getenv("MDREVIEW_PDEATH_CHILD") != "1" {
		return
	}
	readyPath := os.Getenv("MDREVIEW_PDEATH_READY")
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, unix.SIGHUP, unix.SIGTERM, unix.SIGINT)
	expectedParent := unix.Getppid()
	if err := ArmParentDeath(expectedParent, unix.SIGTERM); err != nil {
		os.Exit(51)
	}
	if err := os.WriteFile(
		readyPath,
		[]byte(strconv.Itoa(os.Getpid())),
		0o600,
	); err != nil {
		os.Exit(52)
	}
	<-signals
	os.Exit(0)
}

func TestParentDeathLauncher(t *testing.T) {
	if os.Getenv("MDREVIEW_PDEATH_LAUNCHER") != "1" {
		return
	}
	readyPath := os.Getenv("MDREVIEW_PDEATH_READY")
	child := exec.Command(os.Args[0], "-test.run=^TestParentDeathChild$")
	child.Env = append(
		os.Environ(),
		"MDREVIEW_PDEATH_CHILD=1",
		"MDREVIEW_PDEATH_READY="+readyPath,
	)
	if err := child.Start(); err != nil {
		os.Exit(61)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(readyPath); err == nil {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = child.Process.Kill()
	os.Exit(62)
}

func TestManagedModeExitsWhenDirectParentDies(t *testing.T) {
	directory := t.TempDir()
	readyPath := filepath.Join(directory, "child.pid")
	launcher := exec.Command(os.Args[0], "-test.run=^TestParentDeathLauncher$")
	launcher.Env = append(
		os.Environ(),
		"MDREVIEW_PDEATH_LAUNCHER=1",
		"MDREVIEW_PDEATH_READY="+readyPath,
	)
	if output, err := launcher.CombinedOutput(); err != nil {
		t.Fatalf("launcher failed: %v\n%s", err, output)
	}
	rawPID, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(rawPID))
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state, stateErr := processState(pid)
		if errors.Is(stateErr, os.ErrNotExist) || state == "Z" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("managed child %d survived direct parent death", pid)
}

func TestOrdinaryChild(t *testing.T) {
	if os.Getenv("MDREVIEW_ORDINARY_CHILD") != "1" {
		return
	}
	readyPath := os.Getenv("MDREVIEW_ORDINARY_READY")
	if err := os.WriteFile(
		readyPath,
		[]byte(strconv.Itoa(os.Getpid())),
		0o600,
	); err != nil {
		os.Exit(71)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestOrdinaryLauncher(t *testing.T) {
	if os.Getenv("MDREVIEW_ORDINARY_LAUNCHER") != "1" {
		return
	}
	readyPath := os.Getenv("MDREVIEW_ORDINARY_READY")
	child := exec.Command(os.Args[0], "-test.run=^TestOrdinaryChild$")
	child.Env = append(
		os.Environ(),
		"MDREVIEW_ORDINARY_CHILD=1",
		"MDREVIEW_ORDINARY_READY="+readyPath,
	)
	if err := child.Start(); err != nil {
		os.Exit(72)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(readyPath); err == nil {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = child.Process.Kill()
	os.Exit(73)
}

func TestOrdinaryModeIsNotArmedToParentDeath(t *testing.T) {
	directory := t.TempDir()
	readyPath := filepath.Join(directory, "ordinary.pid")
	launcher := exec.Command(os.Args[0], "-test.run=^TestOrdinaryLauncher$")
	launcher.Env = append(
		os.Environ(),
		"MDREVIEW_ORDINARY_LAUNCHER=1",
		"MDREVIEW_ORDINARY_READY="+readyPath,
	)
	if output, err := launcher.CombinedOutput(); err != nil {
		t.Fatalf("ordinary launcher failed: %v\n%s", err, output)
	}
	rawPID, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(rawPID))
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)
	state, err := processState(pid)
	if err != nil || state == "Z" {
		t.Fatalf("ordinary child did not survive parent death: state=%q err=%v", state, err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
}

func startWithPTY(command *exec.Cmd) (*os.File, error) {
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
		return nil, fmt.Errorf("start command with PTY: %w", err)
	}
	closeMaster = false
	return master, nil
}

func currentParentDeathSignal() (unix.Signal, error) {
	var parentDeathSignal int
	if err := unix.Prctl(
		unix.PR_GET_PDEATHSIG,
		uintptr(unsafe.Pointer(&parentDeathSignal)),
		0,
		0,
		0,
	); err != nil {
		return 0, err
	}
	return unix.Signal(parentDeathSignal), nil
}

func processState(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	closing := strings.LastIndex(string(data), ")")
	if closing < 0 || closing+2 >= len(data) {
		return "", errors.New("invalid proc stat")
	}
	fields := strings.Fields(string(data[closing+2:]))
	if len(fields) == 0 {
		return "", errors.New("missing proc state")
	}
	return fields[0], nil
}
