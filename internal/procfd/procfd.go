// Package procfd lists files that any process has open, like fuser does.
package procfd

import (
	"os"
	"path/filepath"
)

// OpenPaths returns the set of paths open by any process on the system.
// Processes that exit or deny access during the scan are skipped.
func OpenPaths() (map[string]struct{}, error) {
	return openPaths("/proc")
}

func openPaths(procRoot string) (map[string]struct{}, error) {
	procs, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	open := make(map[string]struct{})
	for _, p := range procs {
		if !p.IsDir() || !isPID(p.Name()) {
			continue
		}
		fdDir := filepath.Join(procRoot, p.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || len(target) == 0 || target[0] != '/' {
				continue
			}
			open[target] = struct{}{}
		}
	}
	return open, nil
}

func isPID(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
