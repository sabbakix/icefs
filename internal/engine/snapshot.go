package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sabbakix/icefs/internal/config"
	"github.com/sabbakix/icefs/internal/store"
	"github.com/sabbakix/icefs/internal/veeam"
)

// snapshotMetadata keeps a frozen, timestamped copy of every settled .vbm
// whose content changed since the last copy. The live .vbm is never frozen
// because Veeam rewrites it on every run.
func (e *Engine) snapshotMetadata(sh config.Share, files []seen, open map[string]struct{}, now time.Time) error {
	for _, f := range files {
		if f.kind != veeam.Metadata || !settled(f, sh, open, now) {
			continue
		}
		mtime := f.info.ModTime().Unix()
		last, ok, err := e.st.LatestSnapshot(f.path)
		if err != nil {
			return err
		}
		if ok && last.SourceMTime == mtime {
			continue
		}
		sum, err := hashFile(f.path)
		if err != nil {
			e.event(sh.Name, "snapshot-failed", f.path, err.Error())
			continue
		}
		if ok && last.SHA256 == sum {
			continue
		}
		dest, err := snapshotPath(sh.Path, f.path, now)
		if err != nil {
			return err
		}
		size, err := copyFile(f.path, dest)
		if err != nil {
			e.event(sh.Name, "snapshot-failed", f.path, err.Error())
			continue
		}
		if err := e.Freezer.Freeze(dest); err != nil {
			e.event(sh.Name, "freeze-failed", dest, err.Error())
			continue
		}
		snap := store.Snapshot{
			Path: dest, Share: sh.Name, Source: f.path, SourceMTime: mtime, SHA256: sum, Size: size,
			FrozenAt: now.Unix(), MeltsAt: now.Add(sh.Retention()).Unix(), State: store.Frozen,
		}
		if err := e.st.PutSnapshot(snap); err != nil {
			return err
		}
		e.event(sh.Name, "snapshot", dest, "copy of "+f.path)
	}
	return nil
}

// pruneSnapshots melts and removes older snapshots past retention. Snapshots belong
// to IceFs, so IceFs cleans them up; Veeam does not know they exist.
func (e *Engine) pruneSnapshots(sh config.Share, now time.Time) error {
	due, err := e.st.SnapshotsDueToMelt(sh.Name, now.Unix())
	if err != nil {
		return err
	}
	for _, n := range due {
		// The newest copy of each .vbm is always kept: it describes the
		// chain as it stands now.
		if latest, ok, err := e.st.LatestSnapshot(n.Source); err != nil {
			return err
		} else if ok && latest.Path == n.Path {
			continue
		}
		if err := e.Freezer.Melt(n.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			e.event(sh.Name, "melt-failed", n.Path, err.Error())
			continue
		}
		if err := os.Remove(n.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			e.event(sh.Name, "delete-failed", n.Path, err.Error())
			continue
		}
		n.State = store.Deleted
		if err := e.st.PutSnapshot(n); err != nil {
			return err
		}
		e.event(sh.Name, "snapshot-pruned", n.Path, "")
	}
	return nil
}

// snapshotPath maps <share>/<dir>/Job.vbm to
// <share>/.icefs/vbm/<dir>/Job.20261013-221500.vbm.
func snapshotPath(shareRoot, source string, now time.Time) (string, error) {
	rel, err := filepath.Rel(shareRoot, filepath.Dir(source))
	if err != nil {
		return "", err
	}
	base := filepath.Base(source)
	name := strings.TrimSuffix(base, filepath.Ext(base)) + "." + now.Format("20060102-150405") + filepath.Ext(base)
	return filepath.Join(shareRoot, MetaDir, "vbm", rel, name), nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFile writes src to dest through a temp file and fsyncs it.
func copyFile(src, dest string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return 0, err
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	tmp := dest + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o400)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return n, os.Rename(tmp, dest)
}
