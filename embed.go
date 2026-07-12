// Package embedded exposes the web templates, static assets, and default
// configuration that are compiled into the timemon binary.
package embedded

import (
	"embed"
	"io/fs"
)

//go:embed web/templates web/static defaults.json
var FS embed.FS

// Templates returns an fs.FS rooted at web/templates.
func Templates() fs.FS {
	sub, err := fs.Sub(FS, "web/templates")
	if err != nil {
		panic(err)
	}
	return sub
}

// Static returns an fs.FS rooted at web/static.
func Static() fs.FS {
	sub, err := fs.Sub(FS, "web/static")
	if err != nil {
		panic(err)
	}
	return sub
}

// DefaultsJSON returns the raw contents of defaults.json.
func DefaultsJSON() []byte {
	b, err := FS.ReadFile("defaults.json")
	if err != nil {
		panic(err)
	}
	return b
}
