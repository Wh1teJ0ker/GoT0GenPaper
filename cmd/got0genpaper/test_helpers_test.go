package main

import (
	"path/filepath"
	"runtime"
)

// repoPath makes tests independent of the package working directory. Go runs
// this package from cmd/got0genpaper, while bundled data lives at repository root.
func repoPath(parts ...string) string {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	return filepath.Join(append([]string{root}, parts...)...)
}
