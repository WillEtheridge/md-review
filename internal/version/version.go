// Package version exposes the product version embedded in every build.
package version

import (
	_ "embed"
	"strings"
)

//go:embed version.txt
var embedded string

// Current is the version used by the CLI and release tooling.
var Current = strings.TrimSpace(embedded)
