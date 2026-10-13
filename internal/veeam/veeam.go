// Package veeam knows the Veeam backup file types and how they form chains.
package veeam

import (
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Kind string

const (
	Full        Kind = "vbk" // full backup, starts a chain
	Incremental Kind = "vib" // incremental, needs the full and every earlier incremental
	Metadata    Kind = "vbm" // job metadata, rewritten on every run
	Other       Kind = ""
)

func KindOf(name string) Kind {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".vbk":
		return Full
	case ".vib":
		return Incremental
	case ".vbm":
		return Metadata
	}
	return Other
}

// File is the minimum the chain logic needs to know about a backup file.
type File struct {
	Path  string
	Kind  Kind
	MTime time.Time
}

// OrphanPrefix marks incrementals that have no full before them in the folder.
const OrphanPrefix = "orphan:"

// AssignChains returns, for each file in one folder, the chain it belongs to.
// A chain is named by the path of its .vbk. In forward incremental, a .vib
// belongs to the newest .vbk written before it.
func AssignChains(files []File) map[string]string {
	sorted := append([]File(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].MTime.Equal(sorted[j].MTime) {
			// A full and an incremental with the same mtime: the full comes first.
			return sorted[i].Kind == Full && sorted[j].Kind != Full
		}
		return sorted[i].MTime.Before(sorted[j].MTime)
	})
	chains := make(map[string]string, len(files))
	current := ""
	for _, f := range sorted {
		switch f.Kind {
		case Full:
			current = f.Path
			chains[f.Path] = current
		case Incremental:
			if current == "" {
				chains[f.Path] = OrphanPrefix + filepath.Dir(f.Path)
			} else {
				chains[f.Path] = current
			}
		}
	}
	return chains
}
