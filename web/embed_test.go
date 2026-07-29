package web

import (
	"bytes"
	"errors"
	"io/fs"
	"testing"
)

func TestAssetsContainsDevelopmentScaffoldOrBuiltApplication(t *testing.T) {
	assets, err := Assets()
	if err != nil {
		t.Fatalf("open embedded asset tree: %v", err)
	}

	entries, err := fs.ReadDir(assets, ".")
	if err != nil {
		t.Fatalf("read embedded asset root: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded asset root is empty")
	}

	placeholder, placeholderErr := fs.ReadFile(assets, "placeholder.txt")
	index, indexErr := fs.ReadFile(assets, "index.html")
	if placeholderErr != nil && !isMissing(placeholderErr) {
		t.Fatalf("read embedded development placeholder: %v", placeholderErr)
	}
	if indexErr != nil && !isMissing(indexErr) {
		t.Fatalf("read embedded application entry: %v", indexErr)
	}
	if placeholderErr != nil && indexErr != nil {
		t.Fatal("embedded assets contain neither the development scaffold nor a built application")
	}
	if placeholderErr == nil && !bytes.Contains(placeholder, []byte("MDREVIEW_PLACEHOLDER:WEB")) {
		t.Fatal("embedded development assets are missing the release-rejection marker")
	}
	if indexErr == nil && !bytes.Contains(index, []byte(`id="app"`)) {
		t.Fatal("embedded application entry does not contain its mount point")
	}
}

func isMissing(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
