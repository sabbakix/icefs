// Package store keeps IceFs state in SQLite so a restart picks up where it left off.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/sabbakix/icefs/internal/veeam"
)

// File states.
const (
	Pending = "pending" // seen, waiting for the backup to finish
	Frozen  = "frozen"  // immutable flag set
	Melted  = "melted"  // retention over, flag cleared, can be deleted
	Deleted = "deleted" // removed by IceFs or by Veeam after melting
	Missing = "missing" // vanished while pending or frozen
)

type File struct {
	Path     string
	Share    string
	Dir      string
	Kind     veeam.Kind
	Size     int64
	MTime    int64 // unix seconds
	State    string
	Chain    string
	FrozenAt int64 // 0 when never frozen
	MeltsAt  int64 // 0 when not frozen
	LastSeen int64
}

type Snapshot struct {
	Path        string
	Share       string
	Source      string // the live .vbm it was copied from
	SourceMTime int64
	SHA256      string
	Size        int64
	FrozenAt    int64
	MeltsAt     int64
	State       string
}

type Event struct {
	ID     int64  `json:"id"`
	At     int64  `json:"at"`
	Share  string `json:"share"`
	Action string `json:"action"`
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS files (
	path      TEXT PRIMARY KEY,
	share     TEXT NOT NULL,
	dir       TEXT NOT NULL,
	kind      TEXT NOT NULL,
	size      INTEGER NOT NULL,
	mtime     INTEGER NOT NULL,
	state     TEXT NOT NULL,
	chain     TEXT NOT NULL DEFAULT '',
	frozen_at INTEGER NOT NULL DEFAULT 0,
	melts_at  INTEGER NOT NULL DEFAULT 0,
	last_seen INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS files_share ON files(share, state);
CREATE INDEX IF NOT EXISTS files_chain ON files(chain);
CREATE INDEX IF NOT EXISTS files_dir ON files(dir);
CREATE TABLE IF NOT EXISTS snapshots (
	path         TEXT PRIMARY KEY,
	share        TEXT NOT NULL,
	source       TEXT NOT NULL,
	source_mtime INTEGER NOT NULL,
	sha256       TEXT NOT NULL,
	size         INTEGER NOT NULL,
	frozen_at    INTEGER NOT NULL,
	melts_at     INTEGER NOT NULL,
	state        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS snapshots_source ON snapshots(source, frozen_at);
CREATE TABLE IF NOT EXISTS events (
	id     INTEGER PRIMARY KEY AUTOINCREMENT,
	at     INTEGER NOT NULL,
	share  TEXT NOT NULL,
	action TEXT NOT NULL,
	path   TEXT NOT NULL,
	detail TEXT NOT NULL
);
`

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const fileCols = `path, share, dir, kind, size, mtime, state, chain, frozen_at, melts_at, last_seen`

func scanFiles(rows *sql.Rows) ([]File, error) {
	defer rows.Close()
	var out []File
	for rows.Next() {
		var f File
		var kind string
		if err := rows.Scan(&f.Path, &f.Share, &f.Dir, &kind, &f.Size, &f.MTime, &f.State,
			&f.Chain, &f.FrozenAt, &f.MeltsAt, &f.LastSeen); err != nil {
			return nil, err
		}
		f.Kind = veeam.Kind(kind)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) queryFiles(where string, args ...any) ([]File, error) {
	rows, err := s.db.Query(`SELECT `+fileCols+` FROM files WHERE `+where+` ORDER BY mtime, path`, args...)
	if err != nil {
		return nil, err
	}
	return scanFiles(rows)
}

func (s *Store) GetFile(path string) (File, bool, error) {
	fs, err := s.queryFiles(`path = ?`, path)
	if err != nil || len(fs) == 0 {
		return File{}, false, err
	}
	return fs[0], true, nil
}

func (s *Store) PutFile(f File) error {
	_, err := s.db.Exec(`INSERT INTO files (`+fileCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET share=excluded.share, dir=excluded.dir, kind=excluded.kind,
		size=excluded.size, mtime=excluded.mtime, state=excluded.state, chain=excluded.chain,
		frozen_at=excluded.frozen_at, melts_at=excluded.melts_at, last_seen=excluded.last_seen`,
		f.Path, f.Share, f.Dir, string(f.Kind), f.Size, f.MTime, f.State, f.Chain, f.FrozenAt, f.MeltsAt, f.LastSeen)
	return err
}

// LiveFiles returns the share's files that still exist (any state but deleted).
func (s *Store) LiveFiles(share string) ([]File, error) {
	return s.queryFiles(`share = ? AND state != ?`, share, Deleted)
}

func (s *Store) LiveFilesInDir(dir string) ([]File, error) {
	return s.queryFiles(`dir = ? AND state != ?`, dir, Deleted)
}

func (s *Store) ChainFiles(chain string) ([]File, error) {
	return s.queryFiles(`chain = ? AND state != ?`, chain, Deleted)
}

// DueToMelt returns frozen files whose retention has ended.
func (s *Store) DueToMelt(share string, now int64) ([]File, error) {
	return s.queryFiles(`share = ? AND state = ? AND melts_at <= ?`, share, Frozen, now)
}

const snapCols = `path, share, source, source_mtime, sha256, size, frozen_at, melts_at, state`

func (s *Store) querySnapshots(where string, args ...any) ([]Snapshot, error) {
	rows, err := s.db.Query(`SELECT `+snapCols+` FROM snapshots WHERE `+where+` ORDER BY frozen_at, path`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		var n Snapshot
		if err := rows.Scan(&n.Path, &n.Share, &n.Source, &n.SourceMTime, &n.SHA256, &n.Size,
			&n.FrozenAt, &n.MeltsAt, &n.State); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// LatestSnapshot returns the newest snapshot taken of a live .vbm.
func (s *Store) LatestSnapshot(source string) (Snapshot, bool, error) {
	ns, err := s.querySnapshots(`source = ?`, source)
	if err != nil || len(ns) == 0 {
		return Snapshot{}, false, err
	}
	return ns[len(ns)-1], true, nil
}

func (s *Store) PutSnapshot(n Snapshot) error {
	_, err := s.db.Exec(`INSERT INTO snapshots (`+snapCols+`) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET state=excluded.state, melts_at=excluded.melts_at`,
		n.Path, n.Share, n.Source, n.SourceMTime, n.SHA256, n.Size, n.FrozenAt, n.MeltsAt, n.State)
	return err
}

func (s *Store) Snapshots(share string) ([]Snapshot, error) {
	return s.querySnapshots(`share = ? AND state != ?`, share, Deleted)
}

func (s *Store) SnapshotsDueToMelt(share string, now int64) ([]Snapshot, error) {
	return s.querySnapshots(`share = ? AND state = ? AND melts_at <= ?`, share, Frozen, now)
}

func (s *Store) AddEvent(e Event) error {
	_, err := s.db.Exec(`INSERT INTO events (at, share, action, path, detail) VALUES (?,?,?,?,?)`,
		e.At, e.Share, e.Action, e.Path, e.Detail)
	return err
}

// Events returns the newest events first.
func (s *Store) Events(limit int) ([]Event, error) {
	rows, err := s.db.Query(`SELECT id, at, share, action, path, detail FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.At, &e.Share, &e.Action, &e.Path, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
