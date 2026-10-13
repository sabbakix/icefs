package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sabbakix/icefs/internal/config"
	"github.com/sabbakix/icefs/internal/store"
	"github.com/sabbakix/icefs/internal/veeam"
)

// Chain states shown in the TUI.
const (
	StateWriting = "writing" // only pending files: Veeam is still on it
	StateFrozen  = "frozen"
	StateMelting = "melting" // frozen, melts within MeltingWindow
	StateMelted  = "melted"
)

const MeltingWindow = 24 * time.Hour

type FileView struct {
	Path    string     `json:"path"`
	Kind    veeam.Kind `json:"kind"`
	Size    int64      `json:"size"`
	MTime   int64      `json:"mtime"`
	State   string     `json:"state"`
	MeltsAt int64      `json:"melts_at,omitempty"`
}

type Chain struct {
	ID      string     `json:"id"`
	Dir     string     `json:"dir"`
	State   string     `json:"state"`
	MeltsAt int64      `json:"melts_at,omitempty"`
	Bytes   int64      `json:"bytes"`
	Newest  int64      `json:"newest"`
	Closed  bool       `json:"closed"` // a newer full exists, Veeam will not add to it
	Files   []FileView `json:"files"`
}

type ShareStatus struct {
	config.Share
	Disk         Disk    `json:"disk"`
	FreePercent  float64 `json:"free_percent"`
	FrozenBytes  int64   `json:"frozen_bytes"`
	MeltedBytes  int64   `json:"melted_bytes"`
	PendingFiles int     `json:"pending_files"`
	FrozenFiles  int     `json:"frozen_files"`
	MeltedFiles  int     `json:"melted_files"`
	Snapshots    int     `json:"snapshots"`
	NextMelt     int64   `json:"next_melt,omitempty"`
	LowSpace     bool    `json:"low_space"`
}

// chains groups a share's files into chains, oldest first. Pending files that
// have no chain yet are grouped the same way frozen ones will be.
func (e *Engine) chains(sh config.Share) ([]Chain, error) {
	live, err := e.st.LiveFiles(sh.Name)
	if err != nil {
		return nil, err
	}
	// Pending files get a provisional chain from their folder.
	byDir := map[string][]veeam.File{}
	for _, f := range live {
		byDir[f.Dir] = append(byDir[f.Dir], veeam.File{Path: f.Path, Kind: f.Kind, MTime: time.Unix(f.MTime, 0)})
	}
	provisional := map[string]string{}
	for _, files := range byDir {
		for p, c := range veeam.AssignChains(files) {
			provisional[p] = c
		}
	}
	now := e.Now().Unix()
	idx := map[string]*Chain{}
	var order []string
	for _, f := range live {
		if f.State == store.Missing {
			continue
		}
		id := f.Chain
		if id == "" {
			id = provisional[f.Path]
		}
		c, ok := idx[id]
		if !ok {
			c = &Chain{ID: id, Dir: f.Dir}
			idx[id] = c
			order = append(order, id)
		}
		c.Files = append(c.Files, FileView{Path: f.Path, Kind: f.Kind, Size: f.Size, MTime: f.MTime, State: f.State, MeltsAt: f.MeltsAt})
		c.Bytes += f.Size
		if f.MTime > c.Newest {
			c.Newest = f.MTime
		}
		if f.State == store.Frozen && f.MeltsAt > c.MeltsAt {
			c.MeltsAt = f.MeltsAt
		}
	}
	// Newest full per folder, to tell whether a chain is closed.
	newestFull := map[string]int64{}
	for _, f := range live {
		if f.Kind == veeam.Full && f.MTime > newestFull[f.Dir] {
			newestFull[f.Dir] = f.MTime
		}
	}
	out := make([]Chain, 0, len(order))
	for _, id := range order {
		c := idx[id]
		c.State = chainState(c, now)
		var start int64
		for _, f := range c.Files {
			if f.Kind == veeam.Full {
				start = f.MTime
			}
		}
		c.Closed = !strings.HasPrefix(id, veeam.OrphanPrefix) && newestFull[c.Dir] > start
		out = append(out, *c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Newest < out[j].Newest })
	return out, nil
}

func chainState(c *Chain, now int64) string {
	var frozen, melted, pending int
	for _, f := range c.Files {
		switch f.State {
		case store.Frozen:
			frozen++
		case store.Melted:
			melted++
		case store.Pending:
			pending++
		}
	}
	switch {
	case frozen > 0 && c.MeltsAt-now <= int64(MeltingWindow/time.Second):
		return StateMelting
	case frozen > 0:
		return StateFrozen
	case pending > 0:
		return StateWriting
	default:
		return StateMelted
	}
}

func (e *Engine) shareByName(name string) (config.Share, error) {
	sh, ok := e.cfg.Share(name)
	if !ok {
		return config.Share{}, fmt.Errorf("unknown share %q", name)
	}
	return *sh, nil
}

// Status summarises every share for the TUI.
func (e *Engine) Status() ([]ShareStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]ShareStatus, 0, len(e.cfg.Shares))
	for _, sh := range e.cfg.Shares {
		live, err := e.st.LiveFiles(sh.Name)
		if err != nil {
			return nil, err
		}
		snaps, err := e.st.Snapshots(sh.Name)
		if err != nil {
			return nil, err
		}
		s := ShareStatus{Share: sh, Disk: e.disks[sh.Name], Snapshots: len(snaps)}
		s.FreePercent = s.Disk.FreePercent()
		s.LowSpace = s.Disk.Total > 0 && s.FreePercent <= sh.MinFreePercent
		for _, f := range live {
			switch f.State {
			case store.Pending:
				s.PendingFiles++
			case store.Frozen:
				s.FrozenFiles++
				s.FrozenBytes += f.Size
				if s.NextMelt == 0 || f.MeltsAt < s.NextMelt {
					s.NextMelt = f.MeltsAt
				}
			case store.Melted:
				s.MeltedFiles++
				s.MeltedBytes += f.Size
			}
		}
		out = append(out, s)
	}
	return out, nil
}

func (e *Engine) Chains(share string) ([]Chain, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sh, err := e.shareByName(share)
	if err != nil {
		return nil, err
	}
	return e.chains(sh)
}

// DeleteChain deletes a fully melted chain on the user's request.
func (e *Engine) DeleteChain(share, chain string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	sh, err := e.shareByName(share)
	if err != nil {
		return err
	}
	if err := e.deleteChain(sh, chain, "deleted from the TUI"); err != nil {
		return err
	}
	return e.checkDisk(sh)
}

// ExtendChain pushes a chain's melt time later by the given number of days.
func (e *Engine) ExtendChain(share, chain string, days int) error {
	if days < 1 {
		return fmt.Errorf("days must be at least 1")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	sh, err := e.shareByName(share)
	if err != nil {
		return err
	}
	members, err := e.st.ChainFiles(chain)
	if err != nil {
		return err
	}
	extended := false
	for _, m := range members {
		if m.State != store.Frozen {
			continue
		}
		m.MeltsAt += int64(days) * 86400
		if err := e.st.PutFile(m); err != nil {
			return err
		}
		extended = true
	}
	if !extended {
		return fmt.Errorf("chain %q has no frozen files", chain)
	}
	e.event(sh.Name, "extended", chain, fmt.Sprintf("+%d days", days))
	return nil
}

// UpdateShare changes a share's settings and saves the config file.
// A longer retention extends frozen chains; a shorter one only affects
// files frozen from now on.
func (e *Engine) UpdateShare(name string, retentionDays, settleMinutes int, minFreePercent float64, autoDelete bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	sh, ok := e.cfg.Share(name)
	if !ok {
		return fmt.Errorf("unknown share %q", name)
	}
	next := *sh
	next.RetentionDays, next.SettleMinutes = retentionDays, settleMinutes
	next.MinFreePercent, next.AutoDelete = minFreePercent, autoDelete
	if err := config.ValidateShareSettings(next); err != nil {
		return err
	}
	prev := *sh
	*sh = next
	if err := e.cfg.Save(e.cfgPath); err != nil {
		*sh = prev
		return fmt.Errorf("save config: %w", err)
	}
	e.event(name, "settings", sh.Path, fmt.Sprintf("retention %dd, settle %dm, min free %.0f%%, auto-delete %t",
		retentionDays, settleMinutes, minFreePercent, autoDelete))
	if retentionDays > prev.RetentionDays {
		live, err := e.st.LiveFiles(name)
		if err != nil {
			return err
		}
		done := map[string]bool{}
		for _, f := range live {
			if f.State == store.Frozen && !done[f.Chain] {
				done[f.Chain] = true
				if err := e.extendChain(next, f.Chain); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (e *Engine) Events(limit int) ([]store.Event, error) {
	return e.st.Events(limit)
}
