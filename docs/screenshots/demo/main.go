// Command demo serves the icefsd API with made-up shares and chains, so the
// README screenshots can be taken without a real Veeam repository:
//
//	go run ./docs/screenshots/demo -socket /tmp/icefs-demo.sock &
//	go run ./cmd/icefs -socket /tmp/icefs-demo.sock
//
// Nothing on disk is touched; deletes, extends and settings only change the
// in-memory data.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sabbakix/icefs/internal/config"
	"github.com/sabbakix/icefs/internal/engine"
	"github.com/sabbakix/icefs/internal/store"
	"github.com/sabbakix/icefs/internal/veeam"
)

const (
	GiB = int64(1) << 30
	TiB = int64(1) << 40
	day = 24 * time.Hour
)

var now = time.Now()

type demo struct {
	mu     sync.Mutex
	shares []engine.ShareStatus
	chains map[string][]engine.Chain
	events []store.Event
}

// chain builds a weekly chain: a full on start, then one incremental a day.
// frozenUntil is when it melts (zero for a chain Veeam is still writing).
func chain(share, job, host string, start time.Time, incs int, full, inc int64, meltsAt time.Time, closed bool) engine.Chain {
	dir := filepath.Join("/srv", share, job)
	c := engine.Chain{Dir: dir, Closed: closed}
	state := engine.StateFrozen
	switch {
	case meltsAt.IsZero():
		state = engine.StateWriting
	case !meltsAt.After(now):
		state = engine.StateMelted
	case meltsAt.Sub(now) < engine.MeltingWindow:
		state = engine.StateMelting
	}
	c.State = state
	if !meltsAt.IsZero() && state != engine.StateMelted {
		c.MeltsAt = meltsAt.Unix()
	}
	for i := 0; i <= incs; i++ {
		t := start.Add(time.Duration(i) * day)
		kind, size := veeam.Incremental, inc+int64(i%3)*GiB
		if i == 0 {
			kind, size = veeam.Full, full
		}
		name := fmt.Sprintf("%s%s_%04X.%s", host, t.Format("D2006-01-02T150405"), 0x3A1F+i*97, kind)
		fs := state
		if state == engine.StateWriting {
			fs = "pending"
		}
		f := engine.FileView{Path: filepath.Join(dir, name), Kind: kind, Size: size, MTime: t.Unix(), State: fs, MeltsAt: c.MeltsAt}
		c.Files = append(c.Files, f)
		c.Bytes += size
		c.Newest = t.Unix()
	}
	c.ID = c.Files[0].Path
	return c
}

func newDemo() *demo {
	d := &demo{chains: map[string][]engine.Chain{}}
	// Backups run at 22:00; the last finished last night.
	last := time.Date(now.Year(), now.Month(), now.Day(), 22, 0, 12, 0, time.Local).Add(-day)
	if last.After(now) {
		last = last.Add(-day)
	}
	weekStart := func(weeksAgo int) time.Time { return last.Add(-time.Duration(6+7*weeksAgo) * day) }
	melt := func(weeksAgo int) time.Time { return weekStart(weeksAgo).Add(6*day + 15*day + 40*time.Minute) }

	d.chains["veeam1"] = []engine.Chain{
		chain("veeam1", "SQL01", "SQL01", weekStart(4), 6, 412*GiB, 18*GiB, melt(4), true),
		chain("veeam1", "SQL01", "SQL01", weekStart(3), 6, 415*GiB, 19*GiB, melt(3), true),
		chain("veeam1", "SQL01", "SQL01", weekStart(2), 6, 419*GiB, 17*GiB, melt(2), true),
		chain("veeam1", "SQL01", "SQL01", weekStart(1), 6, 421*GiB, 18*GiB, melt(1), true),
		chain("veeam1", "SQL01", "SQL01", weekStart(0), 6, 426*GiB, 18*GiB, melt(0), false),
		chain("veeam1", "FS01", "FS01", weekStart(2), 6, 288*GiB, 9*GiB, melt(2), true),
		chain("veeam1", "FS01", "FS01", weekStart(1), 6, 291*GiB, 8*GiB, now.Add(19*time.Hour), true),
		chain("veeam1", "FS01", "FS01", weekStart(0), 6, 293*GiB, 9*GiB, melt(0), false),
		chain("veeam1", "DC01", "DC01", now.Add(-25*time.Minute), 0, 61*GiB, 0, time.Time{}, false),
	}
	d.chains["veeam2"] = []engine.Chain{
		chain("veeam2", "EXCH01", "EXCH01", weekStart(2), 6, 640*GiB, 31*GiB, melt(2).Add(day), true),
		chain("veeam2", "EXCH01", "EXCH01", weekStart(1), 6, 655*GiB, 29*GiB, melt(1).Add(day), true),
		chain("veeam2", "EXCH01", "EXCH01", weekStart(0), 6, 661*GiB, 30*GiB, melt(0).Add(day), false),
	}
	d.chains["offsite"] = []engine.Chain{
		chain("offsite", "SQL01-copy", "SQL01", weekStart(1), 6, 421*GiB, 18*GiB, weekStart(1).Add(30*day), true),
		chain("offsite", "SQL01-copy", "SQL01", weekStart(0), 6, 426*GiB, 18*GiB, weekStart(0).Add(30*day), false),
	}

	d.shares = []engine.ShareStatus{
		{Share: config.Share{Name: "veeam1", Path: "/srv/veeam1", RetentionDays: 15, SettleMinutes: 15, MinFreePercent: 15, AutoDelete: true},
			Disk: engine.Disk{Total: uint64(8 * TiB), Free: uint64(1843 * GiB)}, Snapshots: 15},
		{Share: config.Share{Name: "veeam2", Path: "/srv/veeam2", RetentionDays: 16, SettleMinutes: 20, MinFreePercent: 15},
			Disk: engine.Disk{Total: uint64(4 * TiB), Free: uint64(452 * GiB)}, Snapshots: 16},
		{Share: config.Share{Name: "offsite", Path: "/srv/offsite", RetentionDays: 30, SettleMinutes: 15, MinFreePercent: 10},
			Disk: engine.Disk{Total: uint64(12 * TiB), Free: uint64(9420 * GiB)}, Snapshots: 13},
	}
	d.recount()

	add := func(t time.Time, share, action, path, detail string) {
		d.events = append(d.events, store.Event{ID: int64(len(d.events) + 1), At: t.Unix(), Share: share, Action: action, Path: path, Detail: detail})
	}
	sql := d.chains["veeam1"][4]
	fs := d.chains["veeam1"][7]
	snap := last.Add(52 * time.Minute)
	add(last.Add(-6*day+2*time.Hour), "veeam1", "deleted", "/srv/veeam1/SQL01/SQL01"+weekStart(5).Format("D2006-01-02T150405")+"_3A1F.vbk", "auto-delete: free space at or below threshold")
	add(last.Add(-2*day+3*time.Hour), "veeam1", "melted", d.chains["veeam1"][0].Files[6].Path, "")
	add(last.Add(-2*day+3*time.Hour), "veeam1", "melted", d.chains["veeam1"][0].Files[5].Path, "")
	add(last.Add(-day+3*time.Hour), "veeam2", "low-space", "/srv/veeam2", "11.0% free, threshold 15%")
	add(last.Add(-day+9*time.Hour), "veeam1", "refrozen", d.chains["veeam1"][2].Files[3].Path, "immutable flag was cleared before retention ended")
	add(last.Add(-day+11*time.Hour), "veeam2", "settings", "/srv/veeam2", "retention 16d, settle 20m, min free 15%, auto-delete false")
	add(snap, "veeam1", "frozen", sql.Files[len(sql.Files)-1].Path, "")
	add(snap, "veeam1", "snapshot", "/srv/veeam1/.icefs/vbm/SQL01/SQL01.vbm."+snap.Format("20060102T150405")+".vbm", "copy of /srv/veeam1/SQL01/SQL01.vbm")
	add(last.Add(71*time.Minute), "veeam1", "frozen", fs.Files[len(fs.Files)-1].Path, "")
	add(last.Add(2*time.Hour), "veeam2", "frozen", d.chains["veeam2"][2].Files[5].Path, "")
	add(last.Add(9*time.Hour), "veeam1", "extended", d.chains["veeam1"][3].ID, "+7 days")
	// Newest first, as icefsd returns them.
	for i, j := 0, len(d.events)-1; i < j; i, j = i+1, j-1 {
		d.events[i], d.events[j] = d.events[j], d.events[i]
	}
	return d
}

// recount fills in each share's totals from its chains.
func (d *demo) recount() {
	for i := range d.shares {
		s := &d.shares[i]
		s.FrozenBytes, s.MeltedBytes, s.FrozenFiles, s.MeltedFiles, s.PendingFiles, s.NextMelt = 0, 0, 0, 0, 0, 0
		for _, c := range d.chains[s.Name] {
			n := len(c.Files)
			switch c.State {
			case engine.StateFrozen, engine.StateMelting:
				s.FrozenBytes += c.Bytes
				s.FrozenFiles += n
				if s.NextMelt == 0 || c.MeltsAt < s.NextMelt {
					s.NextMelt = c.MeltsAt
				}
			case engine.StateMelted:
				s.MeltedBytes += c.Bytes
				s.MeltedFiles += n
			default:
				s.PendingFiles += n
			}
		}
		s.FreePercent = s.Disk.FreePercent()
		s.LowSpace = s.FreePercent <= s.MinFreePercent
	}
}

func (d *demo) share(name string) *engine.ShareStatus {
	for i := range d.shares {
		if d.shares[i].Name == name {
			return &d.shares[i]
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func main() {
	socket := flag.String("socket", "/tmp/icefs-demo.sock", "unix socket to listen on")
	flag.Parse()
	d := newDemo()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		writeJSON(w, 200, map[string]any{"shares": d.shares})
	})
	mux.HandleFunc("GET /v1/shares/{share}/chains", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		writeJSON(w, 200, map[string]any{"chains": d.chains[r.PathValue("share")]})
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		writeJSON(w, 200, map[string]any{"events": d.events})
	})
	mux.HandleFunc("POST /v1/shares/{share}/chains/delete", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Chain string }
		json.NewDecoder(r.Body).Decode(&req)
		d.mu.Lock()
		defer d.mu.Unlock()
		name := r.PathValue("share")
		var keep []engine.Chain
		for _, c := range d.chains[name] {
			if c.ID == req.Chain {
				if c.State != engine.StateMelted {
					writeJSON(w, 409, map[string]string{"error": "chain is not melted"})
					return
				}
				if s := d.share(name); s != nil {
					s.Disk.Free += uint64(c.Bytes)
				}
				continue
			}
			keep = append(keep, c)
		}
		d.chains[name] = keep
		d.recount()
		writeJSON(w, 200, map[string]string{})
	})
	mux.HandleFunc("POST /v1/shares/{share}/chains/extend", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Chain string
			Days  int
		}
		json.NewDecoder(r.Body).Decode(&req)
		d.mu.Lock()
		defer d.mu.Unlock()
		cs := d.chains[r.PathValue("share")]
		for i := range cs {
			if cs[i].ID == req.Chain {
				cs[i].MeltsAt += int64(req.Days) * int64(day/time.Second)
				cs[i].State = engine.StateFrozen
			}
		}
		d.recount()
		writeJSON(w, 200, map[string]string{})
	})
	mux.HandleFunc("PUT /v1/shares/{share}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RetentionDays  int     `json:"retention_days"`
			SettleMinutes  int     `json:"settle_minutes"`
			MinFreePercent float64 `json:"min_free_percent"`
			AutoDelete     bool    `json:"auto_delete"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		d.mu.Lock()
		defer d.mu.Unlock()
		if s := d.share(r.PathValue("share")); s != nil {
			s.RetentionDays, s.SettleMinutes, s.MinFreePercent, s.AutoDelete = req.RetentionDays, req.SettleMinutes, req.MinFreePercent, req.AutoDelete
		}
		d.recount()
		writeJSON(w, 200, map[string]string{})
	})

	os.Remove(*socket)
	ln, err := net.Listen("unix", *socket)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("demo icefsd API on %s", *socket)
	log.Fatal(http.Serve(ln, mux))
}
