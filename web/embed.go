// Package web provides the browser assets compiled into the mdReview binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var embeddedAssets embed.FS

// Assets returns the embedded production asset tree rooted at web/dist.
func Assets() (fs.FS, error) {
	return fs.Sub(embeddedAssets, "dist")
}
