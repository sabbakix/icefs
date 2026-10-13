package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sabbakix/icefs/internal/client"
	"github.com/sabbakix/icefs/internal/config"
	"github.com/sabbakix/icefs/internal/engine"
	"github.com/sabbakix/icefs/internal/store"
)

type fakeAPI struct {
	shares   []engine.ShareStatus
	chains   []engine.Chain
	deleted  []string
	extended map[string]int
	settings *client.ShareSettings
}

func (f *fakeAPI) Status() ([]engine.ShareStatus, error) { return f.shares, nil }
func (f *fakeAPI) Chains(string) ([]engine.Chain, error) { return f.chains, nil }
func (f *fakeAPI) Events(int) ([]store.Event, error)     { return nil, nil }
func (f *fakeAPI) DeleteChain(_, chain string) error {
	f.deleted = append(f.deleted, chain)
	return nil
}
func (f *fakeAPI) ExtendChain(_, chain string, d int) error { f.extended[chain] = d; return nil }
func (f *fakeAPI) UpdateShare(_ string, s client.ShareSettings) error {
	f.settings = &s
	return nil
}

var now = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func newFake() *fakeAPI {
	return &fakeAPI{
		extended: map[string]int{},
		shares: []engine.ShareStatus{{
			Share:       config.Share{Name: "veeam1", Path: "/srv/veeam1", RetentionDays: 15, SettleMinutes: 15, MinFreePercent: 15},
			Disk:        engine.Disk{Total: 1000 << 30, Free: 100 << 30},
			FreePercent: 10, LowSpace: true, FrozenBytes: 500 << 30, FrozenFiles: 6,
		}},
		chains: []engine.Chain{
			{ID: "/srv/veeam1/Job/A.vbk", Dir: "/srv/veeam1/Job", State: engine.StateMelted, Closed: true, Bytes: 300 << 30,
				Files: []engine.FileView{{Path: "/srv/veeam1/Job/A.vbk", Kind: "vbk", State: "melted"}}},
			{ID: "/srv/veeam1/Job/B.vbk", Dir: "/srv/veeam1/Job", State: engine.StateFrozen, MeltsAt: now.Add(50 * time.Hour).Unix(),
				Files: []engine.FileView{{Path: "/srv/veeam1/Job/B.vbk", Kind: "vbk", State: "frozen"}}},
		},
	}
}

// run feeds messages to the model, executing any commands they return
// (except the refresh timer) until nothing is left.
func run(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	queue := append([]tea.Msg(nil), msgs...)
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		next, cmd := m.Update(msg)
		m = next.(Model)
		queue = append(queue, drain(cmd)...)
	}
	return m
}

func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch msg := msg.(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, drain(c)...)
		}
		return out
	case tickMsg, nil:
		return nil
	}
	return []tea.Msg{msg}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeText(s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		out = append(out, key(string(r)))
	}
	return out
}

func start(t *testing.T, api *fakeAPI) Model {
	m := New(api)
	m.now = func() time.Time { return now }
	m.tick = func() tea.Cmd { return nil }
	return run(t, m, drain(m.Init())...)
}

func TestSharesAndChains(t *testing.T) {
	m := start(t, newFake())
	v := m.View()
	for _, want := range []string{"veeam1", "LOW", "500.0 GiB"} {
		if !strings.Contains(v, want) {
			t.Errorf("shares view missing %q:\n%s", want, v)
		}
	}
	m = run(t, m, key("enter"))
	v = m.View()
	for _, want := range []string{"Job/A.vbk", "Melted", "Frozen", "2d 2h"} {
		if !strings.Contains(v, want) {
			t.Errorf("chains view missing %q:\n%s", want, v)
		}
	}
}

func TestDeleteNeedsMeltedChainAndTypedConfirmation(t *testing.T) {
	api := newFake()
	m := run(t, start(t, api), key("enter"))

	// Frozen chain: refused.
	m = run(t, m, key("down"), key("d"))
	if m.mode != modeNormal || !strings.Contains(m.View(), "only melted chains") {
		t.Fatal("delete was offered on a frozen chain")
	}

	// Melted chain: wrong word does nothing.
	m = run(t, m, key("k"), key("d"))
	m = run(t, m, append(typeText("yes"), key("enter"))...)
	if len(api.deleted) != 0 {
		t.Fatal("deleted without typing delete")
	}
	m = run(t, m, key("d"))
	m = run(t, m, append(typeText("delete"), key("enter"))...)
	if len(api.deleted) != 1 || api.deleted[0] != "/srv/veeam1/Job/A.vbk" {
		t.Fatalf("deleted %v", api.deleted)
	}
	if !strings.Contains(m.View(), "Deleted Job/A.vbk") {
		t.Fatalf("no confirmation shown:\n%s", m.View())
	}
}

func TestExtendFrozenChain(t *testing.T) {
	api := newFake()
	m := run(t, start(t, api), key("enter"))
	m = run(t, m, key("down"), key("x"))
	m = run(t, m, append(typeText("7"), key("enter"))...)
	if api.extended["/srv/veeam1/Job/B.vbk"] != 7 {
		t.Fatalf("extend calls: %v", api.extended)
	}
}

func TestSettingsForm(t *testing.T) {
	api := newFake()
	m := run(t, start(t, api), key("s"))
	if !strings.Contains(m.View(), "Freeze for (days)") {
		t.Fatalf("settings form not shown:\n%s", m.View())
	}
	// Retention 15 -> 20, then toggle auto-delete.
	m = run(t, m, key("backspace"), key("backspace"))
	m = run(t, m, typeText("20")...)
	m = run(t, m, key("tab"), key("tab"), key("tab"), key(" "), key("enter"))
	if api.settings == nil || api.settings.RetentionDays != 20 || !api.settings.AutoDelete || api.settings.MinFreePercent != 15 {
		t.Fatalf("saved %+v", api.settings)
	}

	// Invalid value stays in the form with an error.
	api.settings = nil
	m = run(t, m, key("s"), key("backspace"), key("backspace"))
	m = run(t, m, typeText("0")...)
	m = run(t, m, key("enter"))
	if api.settings != nil || m.mode != modeSettings || !strings.Contains(m.View(), "retention_days") {
		t.Fatalf("invalid retention accepted:\n%s", m.View())
	}
}
