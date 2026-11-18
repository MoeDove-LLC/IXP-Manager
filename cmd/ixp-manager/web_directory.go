package main

import (
	"os"
	"path/filepath"
)

// A portable release keeps the built frontend beside the manager binary. Source
// checkouts retain the existing working-directory default for local development.
func defaultWebDirectory() string {
	executable, err := os.Executable()
	if err != nil {
		return "./web/dist"
	}
	bundled := filepath.Join(filepath.Dir(executable), "web")
	if index, err := os.Stat(filepath.Join(bundled, "index.html")); err == nil && index.Mode().IsRegular() {
		return bundled
	}
	return "./web/dist"
}
