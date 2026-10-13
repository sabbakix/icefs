// Package engine is the IceFs core: it scans the shares, freezes finished
// backup files, melts them when retention ends, snapshots .vbm files and
// watches disk space.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sabbakix/icefs/internal/config"
	"github.com/sabbakix/icefs/internal/freeze"
	"github.com/sabbakix/icefs/internal/procfd"
	"github.com/sabbakix/icefs/internal/store"
	"github.com/sabbakix/icefs/internal/veeam"
)

// MetaDir is the hidden folder at the root of each share where IceFs keeps
// its .vbm snapshots. Hide it from SMB clients with a Samba "veto files" rule.
const MetaDir = ".icefs"

// Disk is one statfs reading.
type Disk struct {
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

func (d Disk) FreePercent() float64 {
	if d.Total == 0 {
		return 0
	}
	return float64(d.Free) / float64(d.Total) * 100
}

type Engine struct {
	mu      sync.Mutex
	cfg     *config.Config
	cfgPath string
	st      *store.Store
	log     *slog.Logger
	disks   map[string]Disk
	lowWarn map[string]bool // low-space warning already logged

	// Swappable for tests.
	Freezer   freeze.Freezer
	OpenPaths func() (map[string]struct{}, error)
	Statfs    func(path string) (Disk, error)
	Now       func() time.Time
}

func New(cfg *config.Config, cfgPath string, st *store.Store, log *slog.Logger) *Engine {
	return &Engine{
		cfg:       cfg,
		cfgPath:   cfgPath,
		st:        st,
		log:       log,
		disks:     map[string]Disk{},
		lowWarn:   map[string]bool{},
		Freezer:   freeze.Linux{},
		OpenPaths: procfd.OpenPaths,
		Statfs:    statfs,
		Now:       time.Now,
	}
}

func statfs(path string) (Disk, error) {
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		return Disk{}, err
	}
	bs := uint64(s.Bsize)
	return Disk{Total: s.Blocks * bs, Free: s.Bavail * bs}, nil
}

// Run scans every ScanInterval until stop is closed.
func (e *Engine) Run(stop <-chan struct{}) {
	e.Scan()
	t := time.NewTicker(e.cfg.ScanInterval.Duration)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			e.Scan()
		}
	}
}

// Scan does one full pass over every share.
func (e *Engine) Scan() {
	e.mu.Lock()
	defer e.mu.Unlock()
	open, err := e.OpenPaths()
	if err != nil {
		// Without the open-file list we cannot tell whether Veeam is still
		// writing, so nothing gets frozen this round.
		e.log.Error("cannot list open files, skipping freezes this round", "err", err)
		open = nil
	}
	for i := range e.cfg.Shares {
		sh := e.cfg.Shares[i]
		if err := e.scanShare(sh, open); err != nil {
			e.log.Error("scan failed", "share", sh.Name, "err", err)
		}
	}
}

type seen struct {
	path string
	info fs.FileInfo
	kind veeam.Kind
}

func (e *Engine) scanShare(sh config.Share, open map[string]struct{}) error {
	now := e.Now()
	files, err := walkShare(sh.Path)
	if err != nil {
		return err
	}
	if err := e.syncRecords(sh, files, now); err != nil {
		return err
	}
	if open != nil {
		if err := e.freezeSettled(sh, files, open, now); err != nil {
			return err
		}
		if err := e.snapshotMetadata(sh, files, open, now); err != nil {
			return err
		}
	}
	if err := e.meltDue(sh, now); err != nil {
		return err
	}
	if err := e.pruneSnapshots(sh, now); err != nil {
		return err
	}
	return e.checkDisk(sh)
}

// walkShare lists the Veeam files under a share, skipping IceFs's own folder.
func walkShare(root string) ([]seen, error) {
	var out []seen
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil // unreadable subfolder: skip it, keep scanning
		}
		if d.IsDir() {
			if d.Name() == MetaDir && filepath.Dir(path) == root {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		k := veeam.KindOf(d.Name())
		if k == veeam.Other {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, seen{path: path, info: info, kind: k})
		return nil
	})
	return out, err
}

// syncRecords brings the database in line with what is on disk.
func (e *Engine) syncRecords(sh config.Share, files []seen, now time.Time) error {
	onDisk := make(map[string]bool, len(files))
	for _, f := range files {
		if f.kind == veeam.Metadata {
			continue
		}
		onDisk[f.path] = true
		rec, ok, err := e.st.GetFile(f.path)
		if err != nil {
			return err
		}
		size, mtime := f.info.Size(), f.info.ModTime().Unix()
		if !ok || rec.State == store.Deleted || rec.State == store.Missing {
			rec = store.File{Path: f.path, Share: sh.Name, Dir: filepath.Dir(f.path), Kind: f.kind, State: store.Pending}
		}
		rec.Size, rec.MTime, rec.LastSeen = size, mtime, now.Unix()
		if rec.State == store.Frozen {
			frozen, err := e.Freezer.IsFrozen(f.path)
			if err != nil {
				e.event(sh.Name, "check-failed", f.path, err.Error())
			} else if !frozen {
				// Someone with root cleared the flag before retention ended.
				if err := e.Freezer.Freeze(f.path); err != nil {
					e.event(sh.Name, "refreeze-failed", f.path, err.Error())
				} else {
					e.event(sh.Name, "refrozen", f.path, "immutable flag was cleared before retention ended")
				}
			}
		}
		if err := e.st.PutFile(rec); err != nil {
			return err
		}
	}
	live, err := e.st.LiveFiles(sh.Name)
	if err != nil {
		return err
	}
	for _, rec := range live {
		if onDisk[rec.Path] {
			continue
		}
		switch rec.State {
		case store.Melted:
			rec.State = store.Deleted
			e.event(sh.Name, "gone", rec.Path, "deleted outside IceFs after melting")
		case store.Pending, store.Frozen:
			rec.State = store.Missing
			e.event(sh.Name, "missing", rec.Path, "file disappeared while "+rec.State)
		default:
			continue
		}
		if err := e.st.PutFile(rec); err != nil {
			return err
		}
	}
	return nil
}

// settled reports whether Veeam is done with a file: untouched for the settle
// period and not open by any process.
func settled(f seen, sh config.Share, open map[string]struct{}, now time.Time) bool {
	if _, busy := open[f.path]; busy {
		return false
	}
	return now.Sub(f.info.ModTime()) >= sh.Settle()
}

func (e *Engine) freezeSettled(sh config.Share, files []seen, open map[string]struct{}, now time.Time) error {
	// A folder counts as done only when its .vbm has settled too: Veeam
	// rewrites the .vbm at the end of every job.
	dirBusy := map[string]bool{}
	for _, f := range files {
		if f.kind == veeam.Metadata && !settled(f, sh, open, now) {
			dirBusy[filepath.Dir(f.path)] = true
		}
	}
	live, err := e.st.LiveFiles(sh.Name) // ordered by mtime, so a full freezes before its incrementals
	if err != nil {
		return err
	}
	byPath := make(map[string]seen, len(files))
	for _, f := range files {
		byPath[f.path] = f
	}
	for _, rec := range live {
		if rec.State != store.Pending {
			continue
		}
		f, ok := byPath[rec.Path]
		if !ok || dirBusy[rec.Dir] || !settled(f, sh, open, now) {
			continue
		}
		if err := e.Freezer.Freeze(rec.Path); err != nil {
			e.event(sh.Name, "freeze-failed", rec.Path, err.Error())
			continue
		}
		chain, err := e.chainFor(rec)
		if err != nil {
			return err
		}
		rec.State = store.Frozen
		rec.Chain = chain
		rec.FrozenAt = now.Unix()
		rec.MeltsAt = now.Add(sh.Retention()).Unix()
		if err := e.st.PutFile(rec); err != nil {
			return err
		}
		e.event(sh.Name, "frozen", rec.Path, "")
		if err := e.extendChain(sh, chain); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) chainFor(rec store.File) (string, error) {
	dirFiles, err := e.st.LiveFilesInDir(rec.Dir)
	if err != nil {
		return "", err
	}
	vf := make([]veeam.File, 0, len(dirFiles))
	for _, d := range dirFiles {
		vf = append(vf, veeam.File{Path: d.Path, Kind: d.Kind, MTime: time.Unix(d.MTime, 0)})
	}
	return veeam.AssignChains(vf)[rec.Path], nil
}

// extendChain makes every file in a chain melt together, retention after the
// newest file was frozen. In forward incremental each .vib needs the .vbk and
// every earlier .vib, so no part of a chain may melt before the rest.
// Melts are only ever pushed later, never earlier.
func (e *Engine) extendChain(sh config.Share, chain string) error {
	members, err := e.st.ChainFiles(chain)
	if err != nil {
		return err
	}
	var newest int64
	for _, m := range members {
		if m.FrozenAt > newest {
			newest = m.FrozenAt
		}
	}
	meltsAt := newest + int64(sh.Retention()/time.Second)
	for _, m := range members {
		switch m.State {
		case store.Frozen:
			if m.MeltsAt >= meltsAt {
				continue
			}
		case store.Melted:
			// The chain grew after it melted: freeze the old parts again.
			if err := e.Freezer.Freeze(m.Path); err != nil {
				e.event(sh.Name, "refreeze-failed", m.Path, err.Error())
				continue
			}
			m.State = store.Frozen
			e.event(sh.Name, "refrozen", m.Path, "chain received a new incremental")
		default:
			continue
		}
		m.MeltsAt = meltsAt
		if err := e.st.PutFile(m); err != nil {
			return err
		}
	}
	return nil
}

// meltDue clears the immutable flag on chains whose retention has ended.
// A chain with a member still pending is being written to and waits.
func (e *Engine) meltDue(sh config.Share, now time.Time) error {
	due, err := e.st.DueToMelt(sh.Name, now.Unix())
	if err != nil {
		return err
	}
	checked := map[string]bool{}
	for _, rec := range due {
		if checked[rec.Chain] {
			continue
		}
		checked[rec.Chain] = true
		members, err := e.st.ChainFiles(rec.Chain)
		if err != nil {
			return err
		}
		ready := true
		for _, m := range members {
			if m.State == store.Pending || (m.State == store.Frozen && m.MeltsAt > now.Unix()) {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		for _, m := range members {
			if m.State != store.Frozen {
				continue
			}
			if err := e.Freezer.Melt(m.Path); err != nil {
				e.event(sh.Name, "melt-failed", m.Path, err.Error())
				continue
			}
			m.State = store.Melted
			if err := e.st.PutFile(m); err != nil {
				return err
			}
			e.event(sh.Name, "melted", m.Path, "")
		}
	}
	return nil
}

func (e *Engine) checkDisk(sh config.Share) error {
	d, err := e.Statfs(sh.Path)
	if err != nil {
		return fmt.Errorf("statfs: %w", err)
	}
	e.disks[sh.Name] = d
	if d.FreePercent() > sh.MinFreePercent {
		e.lowWarn[sh.Name] = false
		return nil
	}
	if sh.AutoDelete {
		if err := e.autoDelete(sh, d); err != nil {
			return err
		}
		d = e.disks[sh.Name]
	}
	if d.FreePercent() <= sh.MinFreePercent && !e.lowWarn[sh.Name] {
		e.lowWarn[sh.Name] = true
		e.event(sh.Name, "low-space", sh.Path, fmt.Sprintf("%.1f%% free, threshold %.0f%%", d.FreePercent(), sh.MinFreePercent))
	}
	return nil
}

// autoDelete removes melted, closed chains oldest first until free space is
// above the share's threshold. Frozen files are never touched.
func (e *Engine) autoDelete(sh config.Share, d Disk) error {
	chains, err := e.chains(sh)
	if err != nil {
		return err
	}
	for _, c := range chains {
		if d.FreePercent() > sh.MinFreePercent {
			return nil
		}
		if c.State != StateMelted || !c.Closed {
			continue
		}
		if err := e.deleteChain(sh, c.ID, "auto-delete: free space at or below threshold"); err != nil {
			return err
		}
		if d, err = e.Statfs(sh.Path); err != nil {
			return err
		}
		e.disks[sh.Name] = d
	}
	return nil
}

var ErrNotMelted = errors.New("chain still has frozen or pending files")

// deleteChain removes every file of a fully melted chain, newest first.
func (e *Engine) deleteChain(sh config.Share, chain, reason string) error {
	members, err := e.st.ChainFiles(chain)
	if err != nil {
		return err
	}
	if len(members) == 0 {
		return fmt.Errorf("chain %q not found", chain)
	}
	for _, m := range members {
		if m.State != store.Melted {
			return ErrNotMelted
		}
		if frozen, err := e.Freezer.IsFrozen(m.Path); err == nil && frozen {
			return fmt.Errorf("%s still has the immutable flag", m.Path)
		}
	}
	for i := len(members) - 1; i >= 0; i-- {
		m := members[i]
		if err := os.Remove(m.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			e.event(sh.Name, "delete-failed", m.Path, err.Error())
			return err
		}
		m.State = store.Deleted
		if err := e.st.PutFile(m); err != nil {
			return err
		}
		e.event(sh.Name, "deleted", m.Path, reason)
	}
	return nil
}

func (e *Engine) event(share, action, path, detail string) {
	lvl := slog.LevelInfo
	switch action {
	case "freeze-failed", "melt-failed", "refreeze-failed", "delete-failed", "check-failed", "missing", "refrozen", "low-space":
		lvl = slog.LevelWarn
	}
	e.log.Log(context.Background(), lvl, action, "share", share, "path", path, "detail", detail)
	if err := e.st.AddEvent(store.Event{At: e.Now().Unix(), Share: share, Action: action, Path: path, Detail: detail}); err != nil {
		e.log.Error("cannot record event", "err", err)
	}
}
