package main

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const (
	webPlaceholderMarker = "MDREVIEW_PLACEHOLDER:WEB"
	webApplicationMarker = `<div id="app">`
	skillCanonicalMarker = "name: mdreview"
)

func TestRunPrintsVersionWithoutLoadingTheApplication(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--version"}, nil, &output); err != nil {
		t.Fatalf("run version: %v", err)
	}
	if got, want := output.String(), applicationVersion+"\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestEmbeddedApplicationLoadsBothAssetTrees(t *testing.T) {
	application, err := loadEmbeddedApplication()
	if err != nil {
		t.Fatalf("load embedded application: %v", err)
	}

	embeddedWebMarker(t, application.web)
	if !bytes.Contains(application.skill, []byte(skillCanonicalMarker)) {
		t.Fatalf("embedded Agent Skill does not contain %q", skillCanonicalMarker)
	}
	if bytes.Contains(application.skill, []byte("MDREVIEW_PLACEHOLDER:SKILL")) {
		t.Fatal("embedded Agent Skill still contains the release placeholder")
	}
}

func TestBuiltCommandContainsBothEmbeddedAssetTrees(t *testing.T) {
	binaryPath := filepath.Join(t.TempDir(), "mdreview")
	build := exec.Command("go", "build", "-trimpath", "-o", binaryPath, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build command binary: %v\n%s", err, output)
	}

	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read command binary: %v", err)
	}
	application, err := loadEmbeddedApplication()
	if err != nil {
		t.Fatalf("load embedded application: %v", err)
	}
	for _, marker := range []string{embeddedWebMarker(t, application.web), skillCanonicalMarker} {
		if !bytes.Contains(binary, []byte(marker)) {
			t.Errorf("built command does not contain embedded marker %q", marker)
		}
	}
}

func embeddedWebMarker(t *testing.T, assets fs.FS) string {
	t.Helper()

	index, indexErr := fs.ReadFile(assets, "index.html")
	if indexErr == nil {
		if !bytes.Contains(index, []byte(webApplicationMarker)) {
			t.Fatalf("embedded web application does not contain %q", webApplicationMarker)
		}
		return webApplicationMarker
	}
	if !os.IsNotExist(indexErr) {
		t.Fatalf("read embedded web application: %v", indexErr)
	}

	placeholder, placeholderErr := fs.ReadFile(assets, "placeholder.txt")
	if placeholderErr != nil {
		t.Fatalf("read embedded development scaffold: %v", placeholderErr)
	}
	if !bytes.Contains(placeholder, []byte(webPlaceholderMarker)) {
		t.Fatalf("embedded development scaffold does not contain %q", webPlaceholderMarker)
	}
	return webPlaceholderMarker
}
