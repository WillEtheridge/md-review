package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mdruntime "mdreview.dev/mdreview/internal/runtime"
)

func TestCanonicalDirectoryResolvesRootSymlink(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(root, link); err != nil {
		t.Fatalf("create root symlink: %v", err)
	}

	got, err := canonicalDirectory(link)
	if err != nil {
		t.Fatalf("canonicalDirectory() error = %v", err)
	}
	if got != root {
		t.Fatalf("canonicalDirectory() = %q, want %q", got, root)
	}
}

func TestCanonicalDirectoryRejectsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(file, []byte("# document\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := canonicalDirectory(file); err == nil {
		t.Fatal("canonicalDirectory() error = nil, want a directory error")
	}
}

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

func TestWaitUntilReadyReturnsServerFailure(t *testing.T) {
	serveErrors := make(chan error, 1)
	serveErrors <- errors.New("listener failed")
	err := waitUntilReady(
		t.Context(),
		func(_ context.Context, _ mdruntime.ReadyState) error {
			return errors.New("not ready")
		},
		mdruntime.ReadyState{},
		serveErrors,
	)
	if err == nil || !strings.Contains(err.Error(), "listener failed") {
		t.Fatalf("waitUntilReady() error = %v", err)
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
