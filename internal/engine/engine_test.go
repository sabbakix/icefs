package engine

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sabbakix/icefs/internal/config"
	"github.com/sabbakix/icefs/internal/freeze"
	"github.com/sabbakix/icefs/internal/store"
)

// fakeFreezer records the flag in memory and makes frozen files read-only
// for the test's own checks.
type fakeFreezer struct {
	mu     sync.Mutex
	frozen map[string]bool
}

func (f *fakeFreezer) Freeze(p string) error {
	if _, err := os.Stat(p); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frozen[p] = true
	return nil
}

func (f *fakeFreezer) Melt(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.frozen, p)
	return nil
}

func (f *fakeFreezer) IsFrozen(p string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frozen[p], nil
}

type harness struct {
	t     *testing.T
	root  string
	eng   *Engine
	st    *store.Store
	fz    freeze.Freezer
	now   time.Time
	open  map[string]struct{}
	disk  Disk
	start time.Time
}

const day = 24 * time.Hour

func newHarness(t *testing.T, real bool) *harness {
	t.Helper()
	root := t.TempDir()
	share := filepath.Join(root, "share")
	if err := os.MkdirAll(share, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config.toml")
	cfg := &config.Config{
		DBPath: filepath.Join(root, "state.db"),
		Shares: []config.Share{{Name: "s1", Path: share, RetentionDays: 15, SettleMinutes: 15, MinFreePercent: 15}},
	}
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &harness{t: t, root: share, st: st, open: map[string]struct{}{}, disk: Disk{Total: 1000, Free: 500}}
	h.start = time.Date(2026, 10, 1, 22, 0, 0, 0, time.UTC)
	h.now = h.start
	h.eng = New(cfg, cfgPath, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if real {
		h.fz = freeze.Linux{}
		// Frozen files would stop TempDir cleanup, so melt everything first.
		t.Cleanup(func() {
			filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					freeze.Linux{}.Melt(p)
				}
				return nil
			})
		})
	} else {
		h.fz = &fakeFreezer{frozen: map[string]bool{}}
	}
	h.eng.Freezer = h.fz
	h.eng.Now = func() time.Time { return h.now }
	h.eng.OpenPaths = func() (map[string]struct{}, error) { return h.open, nil }
	h.eng.Statfs = func(string) (Disk, error) { return h.disk, nil }
	return h
}

// write creates or rewrites a file under the share with the given mtime.
func (h *harness) write(rel, content string, mtime time.Time) string {
	h.t.Helper()
	p := filepath.Join(h.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		h.t.Fatal(err)
	}
	return p
}

func (h *harness) state(p string) store.File {
	h.t.Helper()
	f, ok, err := h.st.GetFile(p)
	if err != nil || !ok {
		h.t.Fatalf("no record for %s (err %v)", p, err)
	}
	return f
}

func (h *harness) frozen(p string) bool {
	h.t.Helper()
	ok, err := h.fz.IsFrozen(p)
	if err != nil {
		h.t.Fatal(err)
	}
	return ok
}

func (h *harness) at(d time.Duration) { h.now = h.start.Add(d) }

func TestFreezesOnlyAfterSettleAndWhenClosed(t *testing.T) {
	h := newHarness(t, false)
	vbk := h.write("Job/Job_D1.vbk", "full", h.start)
	vbm := h.write("Job/Job.vbm", "meta1", h.start)

	h.at(5 * time.Minute)
	h.eng.Scan()
	if h.frozen(vbk) || h.state(vbk).State != store.Pending {
		t.Fatal("froze a file that was written 5 minutes ago")
	}

	h.at(20 * time.Minute)
	h.open[vbk] = struct{}{}
	h.eng.Scan()
	if h.frozen(vbk) {
		t.Fatal("froze a file that Samba still has open")
	}

	delete(h.open, vbk)
	h.open[vbm] = struct{}{}
	h.eng.Scan()
	if h.frozen(vbk) {
		t.Fatal("froze a file while the folder's .vbm is still open")
	}

	delete(h.open, vbm)
	h.eng.Scan()
	if !h.frozen(vbk) || h.state(vbk).State != store.Frozen {
		t.Fatal("did not freeze a settled, closed full backup")
	}
}

func TestChainMeltsTogetherAfterNewestIncremental(t *testing.T) {
	h := newHarness(t, false)
	vbk := h.write("Job/Job_D1.vbk", "full", h.start)
	h.at(time.Hour)
	h.eng.Scan()

	// Daily incrementals for 5 days.
	var vibs []string
	for i := 1; i <= 5; i++ {
		ts := h.start.Add(time.Duration(i) * day)
		vibs = append(vibs, h.write(filepath.Join("Job", "Job_D"+string(rune('1'+i))+".vib"), "inc", ts))
		h.at(time.Duration(i)*day + time.Hour)
		h.eng.Scan()
	}
	for _, p := range append([]string{vbk}, vibs...) {
		if h.state(p).Chain != vbk {
			t.Fatalf("%s not in the full's chain: %q", p, h.state(p).Chain)
		}
	}
	// Day 16: the full is 15 days old but the newest incremental is not.
	h.at(16*day + 2*time.Hour)
	h.eng.Scan()
	if !h.frozen(vbk) || !h.frozen(vibs[0]) {
		t.Fatal("part of the chain melted while newer incrementals still depend on it")
	}
	// Retention after the last incremental froze (day 5 + 1h + 15d).
	h.at(20*day + 2*time.Hour)
	h.eng.Scan()
	for _, p := range append([]string{vbk}, vibs...) {
		if h.frozen(p) || h.state(p).State != store.Melted {
			t.Fatalf("%s still frozen after the whole chain expired", p)
		}
	}
}

func TestNewFullStartsNewChain(t *testing.T) {
	h := newHarness(t, false)
	a := h.write("Job/A.vbk", "full", h.start)
	ai := h.write("Job/A1.vib", "inc", h.start.Add(day))
	b := h.write("Job/B.vbk", "full", h.start.Add(7*day))
	bi := h.write("Job/B1.vib", "inc", h.start.Add(8*day))
	h.at(9 * day)
	h.eng.Scan()
	if h.state(ai).Chain != a || h.state(bi).Chain != b {
		t.Fatalf("chains wrong: %q %q", h.state(ai).Chain, h.state(bi).Chain)
	}
	cs, err := h.eng.Chains("s1")
	if err != nil || len(cs) != 2 {
		t.Fatalf("want 2 chains, got %v (err %v)", cs, err)
	}
	if !cs[0].Closed || cs[1].Closed {
		t.Fatal("only the older chain should be closed")
	}
}

func TestDeleteOnlyMeltedChains(t *testing.T) {
	h := newHarness(t, false)
	a := h.write("Job/A.vbk", "full", h.start)
	ai := h.write("Job/A1.vib", "inc", h.start.Add(day))
	h.at(2 * day)
	h.eng.Scan()

	if err := h.eng.DeleteChain("s1", a); !errors.Is(err, ErrNotMelted) {
		t.Fatalf("deleting a frozen chain: got %v, want ErrNotMelted", err)
	}
	h.at(18 * day)
	h.eng.Scan()
	if err := h.eng.DeleteChain("s1", a); err != nil {
		t.Fatalf("deleting a melted chain: %v", err)
	}
	for _, p := range []string{a, ai} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still on disk", p)
		}
	}
}

func TestAutoDeleteOnlyWhenEnabledAndLow(t *testing.T) {
	h := newHarness(t, false)
	a := h.write("Job/A.vbk", "full", h.start)
	b := h.write("Job/B.vbk", "full", h.start.Add(7*day))
	h.at(30 * day)
	h.eng.Scan() // both freeze now
	h.at(46 * day)
	h.disk = Disk{Total: 1000, Free: 100} // 10% free
	h.eng.Scan()                          // both melt; auto-delete is off
	if _, err := os.Stat(a); err != nil {
		t.Fatal("deleted a chain with auto-delete off")
	}

	if err := h.eng.UpdateShare("s1", 15, 15, 15, true); err != nil {
		t.Fatal(err)
	}
	h.eng.Statfs = func(string) (Disk, error) {
		if _, err := os.Stat(a); os.IsNotExist(err) {
			return Disk{Total: 1000, Free: 400}, nil
		}
		return Disk{Total: 1000, Free: 100}, nil
	}
	h.eng.Scan()
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Fatal("oldest melted chain not deleted when space was low")
	}
	if _, err := os.Stat(b); err != nil {
		t.Fatal("deleted the newest chain, which Veeam may still extend")
	}
	cfg, err := config.Load(h.eng.cfgPath)
	if err != nil || !cfg.Shares[0].AutoDelete {
		t.Fatalf("auto-delete not saved to config (err %v)", err)
	}
}

func TestSnapshotsMetadata(t *testing.T) {
	h := newHarness(t, false)
	vbm := h.write("Job/Job.vbm", "meta1", h.start)
	h.at(time.Hour)
	h.eng.Scan()
	h.at(2 * time.Hour)
	h.eng.Scan() // unchanged: no second copy
	snaps, _ := h.st.Snapshots("s1")
	if len(snaps) != 1 || !h.frozen(snaps[0].Path) {
		t.Fatalf("want one frozen snapshot, got %+v", snaps)
	}
	if got, _ := os.ReadFile(snaps[0].Path); string(got) != "meta1" {
		t.Fatalf("snapshot content %q", got)
	}

	h.write("Job/Job.vbm", "meta2", h.start.Add(day))
	h.at(day + time.Hour)
	h.eng.Scan()
	snaps, _ = h.st.Snapshots("s1")
	if len(snaps) != 2 {
		t.Fatalf("want 2 snapshots after the .vbm changed, got %d", len(snaps))
	}
	if h.frozen(vbm) {
		t.Fatal("the live .vbm must never be frozen")
	}

	h.at(16*day + 2*time.Hour)
	h.eng.Scan()
	snaps, _ = h.st.Snapshots("s1")
	if len(snaps) != 1 {
		t.Fatalf("want the expired snapshot pruned, have %d", len(snaps))
	}
}

func TestRefreezesClearedFlag(t *testing.T) {
	h := newHarness(t, false)
	vbk := h.write("Job/A.vbk", "full", h.start)
	h.at(time.Hour)
	h.eng.Scan()
	h.fz.Melt(vbk) // someone ran chattr -i
	h.eng.Scan()
	if !h.frozen(vbk) {
		t.Fatal("cleared flag was not restored")
	}
}

func TestLongerRetentionExtendsFrozenChains(t *testing.T) {
	h := newHarness(t, false)
	vbk := h.write("Job/A.vbk", "full", h.start)
	h.at(time.Hour)
	h.eng.Scan()
	before := h.state(vbk).MeltsAt
	if err := h.eng.UpdateShare("s1", 30, 15, 15, false); err != nil {
		t.Fatal(err)
	}
	if got := h.state(vbk).MeltsAt; got != before+15*86400 {
		t.Fatalf("melts_at %d, want %d", got, before+15*86400)
	}
	if err := h.eng.UpdateShare("s1", 5, 15, 15, false); err != nil {
		t.Fatal(err)
	}
	if got := h.state(vbk).MeltsAt; got != before+15*86400 {
		t.Fatal("a shorter retention shortened an existing lock")
	}
}

// TestRealImmutableFlag runs against the kernel. It needs root and a
// filesystem with the immutable attribute, so it skips elsewhere.
func TestRealImmutableFlag(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	h := newHarness(t, true)
	probe := h.write("probe", "x", h.start)
	if err := (freeze.Linux{}).Freeze(probe); err != nil {
		t.Skipf("filesystem does not support the immutable flag: %v", err)
	}
	freeze.Linux{}.Melt(probe)
	os.Remove(probe)

	vbk := h.write("Job/A.vbk", "full", h.start)
	h.at(time.Hour)
	h.eng.Scan()
	if err := os.Remove(vbk); err == nil {
		t.Fatal("removed a frozen file")
	}
	if err := os.WriteFile(vbk, []byte("ransom"), 0o644); err == nil {
		t.Fatal("overwrote a frozen file")
	}
	h.at(16 * day)
	h.eng.Scan()
	if err := h.eng.DeleteChain("s1", vbk); err != nil {
		t.Fatalf("delete after melt: %v", err)
	}
}
