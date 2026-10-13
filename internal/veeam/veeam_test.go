package veeam

import (
	"testing"
	"time"
)

func TestAssignChains(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	files := []File{
		{Path: "/s/j/B1.vib", Kind: Incremental, MTime: t0.Add(8 * 24 * time.Hour)},
		{Path: "/s/j/A.vbk", Kind: Full, MTime: t0},
		{Path: "/s/j/A1.vib", Kind: Incremental, MTime: t0.Add(24 * time.Hour)},
		{Path: "/s/j/B.vbk", Kind: Full, MTime: t0.Add(7 * 24 * time.Hour)},
		{Path: "/s/j/X.vib", Kind: Incremental, MTime: t0.Add(-time.Hour)},
	}
	got := AssignChains(files)
	want := map[string]string{
		"/s/j/A.vbk":  "/s/j/A.vbk",
		"/s/j/A1.vib": "/s/j/A.vbk",
		"/s/j/B.vbk":  "/s/j/B.vbk",
		"/s/j/B1.vib": "/s/j/B.vbk",
		"/s/j/X.vib":  OrphanPrefix + "/s/j",
	}
	for p, c := range want {
		if got[p] != c {
			t.Errorf("%s: chain %q, want %q", p, got[p], c)
		}
	}
}

func TestKindOf(t *testing.T) {
	for name, k := range map[string]Kind{"a.VBK": Full, "b.vib": Incremental, "c.vbm": Metadata, "d.txt": Other} {
		if KindOf(name) != k {
			t.Errorf("%s: got %q", name, KindOf(name))
		}
	}
}
