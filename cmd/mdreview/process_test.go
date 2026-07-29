//go:build linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCompiledCommandServesWithoutNodeAndReusesExistingInstance(t *testing.T) {
	binaryPath := buildCommand(t)
	workspaceRoot := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(workspaceRoot, "README.md"),
		[]byte("# Process integration\n"),
		0o600,
	); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(t.TempDir(), "outside-secret.md"),
		[]byte("outside secret"),
		0o600,
	); err != nil {
		t.Fatalf("write outside secret: %v", err)
	}

	runtimeParent := t.TempDir()
	port := availablePort(t)
	command := exec.Command(
		binaryPath,
		workspaceRoot,
		"--port",
		strconv.Itoa(port),
	)
	command.Env = commandEnvironment(runtimeParent)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("open stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start compiled command: %v", err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})

	output, instanceURL := waitForStartup(t, stdout)
	canonicalRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		t.Fatalf("canonicalise test root: %v", err)
	}
	for _, expected := range []string{
		"Directory: " + canonicalRoot,
		"Documents: 1",
		"Waiting for a browser connection. Press Ctrl+C to stop.",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("startup output does not contain %q:\n%s", expected, output)
		}
	}
	parsedURL, err := url.Parse(instanceURL)
	if err != nil {
		t.Fatalf("parse startup URL: %v", err)
	}
	if parsedURL.Port() != strconv.Itoa(port) {
		t.Fatalf("startup port = %q, want %d", parsedURL.Port(), port)
	}
	if parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		t.Fatalf("startup URL contains query or fragment: %q", instanceURL)
	}
	baseURL := "http://" + parsedURL.Host

	client := &http.Client{Timeout: 2 * time.Second}
	assertHTTPStatus(t, client, http.MethodGet, baseURL+"/", http.StatusOK)
	assertHTTPStatus(t, client, http.MethodGet, baseURL+"/api/state", http.StatusOK)
	assertHTTPStatus(
		t,
		client,
		http.MethodGet,
		baseURL+"/api/document?path=..%2Foutside-secret.md",
		http.StatusBadRequest,
	)

	stateRequest, err := http.NewRequest(http.MethodGet, baseURL+"/api/state", nil)
	if err != nil {
		t.Fatalf("create state request: %v", err)
	}
	stateResponse, err := client.Do(stateRequest)
	if err != nil {
		t.Fatalf("request state: %v", err)
	}
	defer stateResponse.Body.Close()
	var state struct {
		DocumentCount       int     `json:"documentCount"`
		InitialDocumentPath *string `json:"initialDocumentPath"`
	}
	if err := json.NewDecoder(stateResponse.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.DocumentCount != 1 || state.InitialDocumentPath == nil ||
		*state.InitialDocumentPath != "README.md" {
		t.Fatalf("state = %#v", state)
	}

	hostileHostRequest, err := http.NewRequest(http.MethodGet, baseURL+"/api/state", nil)
	if err != nil {
		t.Fatalf("create hostile Host request: %v", err)
	}
	hostileHostRequest.Host = "localhost:" + parsedURL.Port()
	hostileHostResponse, err := client.Do(hostileHostRequest)
	if err != nil {
		t.Fatalf("send hostile Host request: %v", err)
	}
	_ = hostileHostResponse.Body.Close()
	if hostileHostResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("hostile Host status = %d, want %d", hostileHostResponse.StatusCode, http.StatusBadRequest)
	}

	duplicate := exec.Command(binaryPath, workspaceRoot)
	duplicate.Env = commandEnvironment(runtimeParent)
	duplicateOutput, err := duplicate.CombinedOutput()
	if err != nil {
		t.Fatalf("run duplicate command: %v\n%s", err, duplicateOutput)
	}
	if !strings.Contains(string(duplicateOutput), instanceURL) ||
		!strings.Contains(string(duplicateOutput), "already serving") {
		t.Fatalf("duplicate output does not report the existing URL:\n%s", duplicateOutput)
	}

	managedDuplicate := exec.Command(binaryPath, workspaceRoot, "--managed-session")
	managedDuplicate.Env = commandEnvironment(runtimeParent)
	managedDuplicateOutput, err := managedDuplicate.CombinedOutput()
	if err != nil {
		t.Fatalf(
			"run detached managed duplicate: %v\n%s",
			err,
			managedDuplicateOutput,
		)
	}
	if !strings.Contains(string(managedDuplicateOutput), instanceURL) ||
		!strings.Contains(string(managedDuplicateOutput), "already serving") {
		t.Fatalf(
			"managed duplicate does not report the existing URL:\n%s",
			managedDuplicateOutput,
		)
	}

	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt compiled command: %v", err)
	}
	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("compiled command exit: %v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("compiled command did not stop after SIGINT")
	}

	runtimeEntries, err := os.ReadDir(filepath.Join(runtimeParent, "mdreview"))
	if err != nil {
		t.Fatalf("read runtime directory: %v", err)
	}
	var lockCount, stateCount int
	for _, entry := range runtimeEntries {
		switch filepath.Ext(entry.Name()) {
		case ".lock":
			lockCount++
		case ".json":
			stateCount++
		}
	}
	if lockCount != 1 || stateCount != 0 {
		t.Fatalf("runtime files after shutdown = %d locks, %d states", lockCount, stateCount)
	}
}

func TestCompiledCommandRejectsUnavailableExplicitPort(t *testing.T) {
	binaryPath := buildCommand(t)
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy loopback port: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	command := exec.Command(
		binaryPath,
		t.TempDir(),
		"--port",
		strconv.Itoa(port),
	)
	command.Env = commandEnvironment(t.TempDir())
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("compiled command succeeded on occupied port:\n%s", output)
	}
	if !strings.Contains(string(output), fmt.Sprintf("port %d is unavailable", port)) {
		t.Fatalf("port conflict output:\n%s", output)
	}
}

func buildCommand(t *testing.T) string {
	t.Helper()
	binaryPath := filepath.Join(t.TempDir(), "mdreview")
	command := exec.Command("go", "build", "-trimpath", "-o", binaryPath, ".")
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build command: %v\n%s", err, output)
	}
	return binaryPath
}

func availablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("select available port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release selected port: %v", err)
	}
	return port
}

func commandEnvironment(runtimeParent string) []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PATH=") || strings.HasPrefix(entry, "XDG_RUNTIME_DIR=") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(
		environment,
		"PATH=/mdreview-test-no-executables",
		"XDG_RUNTIME_DIR="+runtimeParent,
	)
}

func waitForStartup(t *testing.T, stdout io.Reader) (string, string) {
	t.Helper()
	lines := make(chan string)
	continueReading := make(chan bool)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
			if !<-continueReading {
				return
			}
		}
	}()

	var output strings.Builder
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	instanceURL := ""
	for {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatalf("compiled command exited before readiness:\n%s", output.String())
			}
			output.WriteString(line)
			output.WriteByte('\n')
			if strings.HasPrefix(line, "URL:") {
				instanceURL = strings.TrimSpace(strings.TrimPrefix(line, "URL:"))
			}
			if strings.Contains(line, "Press Ctrl+C to stop.") {
				if instanceURL == "" {
					t.Fatalf("startup output has no URL:\n%s", output.String())
				}
				continueReading <- false
				return output.String(), instanceURL
			}
			continueReading <- true
		case <-timeout.C:
			t.Fatalf("timed out waiting for startup:\n%s", output.String())
		}
	}
}

func assertHTTPStatus(
	t *testing.T,
	client *http.Client,
	method string,
	target string,
	want int,
) {
	t.Helper()
	request, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request %s: %v", target, err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status for %s = %d, want %d\n%s", target, response.StatusCode, want, body)
	}
}
