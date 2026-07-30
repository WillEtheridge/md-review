package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mdreview.dev/mdreview/internal/cli"
)

func TestListenLoopbackUsesExplicitPortWithoutFallback(t *testing.T) {
	var addresses []string
	wantError := errors.New("occupied")
	_, err := listenLoopbackWith(43123, true, func(address string) (net.Listener, error) {
		addresses = append(addresses, address)
		return nil, wantError
	})
	if !errors.Is(err, wantError) {
		t.Fatalf("listenLoopbackWith() error = %v, want occupied error", err)
	}
	if len(addresses) != 1 || addresses[0] != "127.0.0.1:43123" {
		t.Fatalf("listen addresses = %#v", addresses)
	}
}

func TestListenLoopbackFallsBackFromDefaultToAutomaticPort(t *testing.T) {
	fallback := &stubListener{address: stubAddress("127.0.0.1:49152")}
	var addresses []string
	got, err := listenLoopbackWith(0, false, func(address string) (net.Listener, error) {
		addresses = append(addresses, address)
		if address == "127.0.0.1:4242" {
			return nil, errors.New("occupied")
		}
		return fallback, nil
	})
	if err != nil {
		t.Fatalf("listenLoopbackWith() error = %v", err)
	}
	if got != fallback {
		t.Fatalf("listener = %T, want fallback listener", got)
	}
	want := []string{"127.0.0.1:4242", "127.0.0.1:0"}
	if len(addresses) != len(want) || addresses[0] != want[0] || addresses[1] != want[1] {
		t.Fatalf("listen addresses = %#v, want %#v", addresses, want)
	}
}

func TestListenLoopbackPrefersDefaultPort(t *testing.T) {
	preferred := &stubListener{address: stubAddress("127.0.0.1:4242")}
	var addresses []string
	got, err := listenLoopbackWith(0, false, func(address string) (net.Listener, error) {
		addresses = append(addresses, address)
		return preferred, nil
	})
	if err != nil {
		t.Fatalf("listenLoopbackWith() error = %v", err)
	}
	if got != preferred {
		t.Fatalf("listener = %T, want preferred listener", got)
	}
	if len(addresses) != 1 || addresses[0] != "127.0.0.1:4242" {
		t.Fatalf("listen addresses = %#v", addresses)
	}
}

func TestLoopbackURL(t *testing.T) {
	got := loopbackURL("127.0.0.1:4242")
	if got != "http://127.0.0.1:4242/" {
		t.Fatalf("loopbackURL() = %q", got)
	}
}

func TestStartupOutputIncludesAgreedFieldsWithoutOpeningBrowser(t *testing.T) {
	var output bytes.Buffer
	printStartedInstance(
		&output,
		"/canonical/workspace",
		14,
		"http://127.0.0.1:4243/",
	)
	for _, expected := range []string{
		"mdReview",
		"Directory: /canonical/workspace",
		"Documents: 14",
		"URL:       http://127.0.0.1:4243/",
		"Press Ctrl+C to stop.",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("startup output does not contain %q:\n%s", expected, output.String())
		}
	}
}

func TestSameWorkspaceInstancesRunAndStopIndependently(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	type instance struct {
		cancel context.CancelFunc
		done   chan error
		output *notifyingBuffer
		port   int
	}
	start := func() instance {
		port := availableLoopbackPort(t)
		ctx, cancel := context.WithCancel(context.Background())
		output := newNotifyingBuffer()
		done := make(chan error, 1)
		go func() {
			done <- runServe(ctx, cli.Options{
				Command:      cli.Serve,
				Directory:    root,
				Port:         uint16(port),
				PortExplicit: true,
			}, output)
		}()
		waitForOutput(t, output, "Waiting for a browser connection")
		return instance{cancel: cancel, done: done, output: output, port: port}
	}

	first := start()
	second := start()
	if first.port == second.port {
		t.Fatal("instances unexpectedly share a port")
	}

	first.cancel()
	if err := <-first.done; err != nil {
		t.Fatalf("stop first instance: %v", err)
	}
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/state", second.port))
	if err != nil {
		t.Fatalf("second instance stopped with first: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("second instance status = %d", response.StatusCode)
	}
	second.cancel()
	if err := <-second.done; err != nil {
		t.Fatalf("stop second instance: %v", err)
	}
}

type notifyingBuffer struct {
	mu      sync.Mutex
	content strings.Builder
	changed chan struct{}
}

func newNotifyingBuffer() *notifyingBuffer {
	return &notifyingBuffer{changed: make(chan struct{}, 1)}
}

func (buffer *notifyingBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	written, err := buffer.content.Write(data)
	buffer.mu.Unlock()
	select {
	case buffer.changed <- struct{}{}:
	default:
	}
	return written, err
}

func (buffer *notifyingBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.content.String()
}

func waitForOutput(t *testing.T, output *notifyingBuffer, text string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		if strings.Contains(output.String(), text) {
			return
		}
		select {
		case <-output.changed:
		case <-timer.C:
			t.Fatalf("timed out waiting for %q in %q", text, output.String())
		}
	}
}

func availableLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

type stubListener struct {
	address net.Addr
}

func (*stubListener) Accept() (net.Conn, error) {
	return nil, errors.New("not implemented")
}

func (*stubListener) Close() error {
	return nil
}

func (listener *stubListener) Addr() net.Addr {
	return listener.address
}

type stubAddress string

func (address stubAddress) Network() string {
	return "tcp"
}

func (address stubAddress) String() string {
	return string(address)
}
