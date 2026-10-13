// Package tui is the IceFs terminal interface.
package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/sabbakix/icefs/internal/client"
	"github.com/sabbakix/icefs/internal/engine"
	"github.com/sabbakix/icefs/internal/store"
)

// API is what the TUI needs from icefsd; *client.Client implements it.
type API interface {
	Status() ([]engine.ShareStatus, error)
	Chains(share string) ([]engine.Chain, error)
	DeleteChain(share, chain string) error
	ExtendChain(share, chain string, days int) error
	UpdateShare(share string, s client.ShareSettings) error
	Events(limit int) ([]store.Event, error)
}

type view int

const (
	viewShares view = iota
	viewChains
	viewFiles
	viewEvents
)

type mode int

const (
	modeNormal mode = iota
	modeDelete
	modeExtend
	modeSettings
)

const refreshEvery = 5 * time.Second

type Model struct {
	api  API
	now  func() time.Time
	view view
	back view // where esc goes from the events view
	mode mode

	shares   []engine.ShareStatus
	shareIdx int
	chains   []engine.Chain
	chainIdx int
	fileOff  int
	events   []store.Event
	eventOff int

	input textinput.Model
	form  settingsForm

	tick func() tea.Cmd // the refresh timer, replaceable in tests

	note   string
	err    error
	width  int
	height int
}

func New(api API) Model {
	ti := textinput.New()
	ti.CharLimit = 16
	return Model{api: api, now: time.Now, tick: tick, input: ti, width: 100, height: 30}
}

// Messages.
type (
	statusMsg []engine.ShareStatus
	chainsMsg struct {
		share  string
		chains []engine.Chain
	}
	eventsMsg []store.Event
	errMsg    struct{ err error }
	doneMsg   string
	tickMsg   time.Time
)

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadStatus(), m.tick())
}

func (m Model) loadStatus() tea.Cmd {
	return func() tea.Msg {
		s, err := m.api.Status()
		if err != nil {
			return errMsg{err}
		}
		return statusMsg(s)
	}
}

func (m Model) loadChains(share string) tea.Cmd {
	return func() tea.Msg {
		c, err := m.api.Chains(share)
		if err != nil {
			return errMsg{err}
		}
		return chainsMsg{share: share, chains: c}
	}
}

func (m Model) loadEvents() tea.Cmd {
	return func() tea.Msg {
		e, err := m.api.Events(500)
		if err != nil {
			return errMsg{err}
		}
		return eventsMsg(e)
	}
}

// refresh reloads what the current view shows.
func (m Model) refresh() tea.Cmd {
	cmds := []tea.Cmd{m.loadStatus()}
	if sh, ok := m.currentShare(); ok && (m.view == viewChains || m.view == viewFiles) {
		cmds = append(cmds, m.loadChains(sh.Name))
	}
	if m.view == viewEvents {
		cmds = append(cmds, m.loadEvents())
	}
	return tea.Batch(cmds...)
}

func (m Model) currentShare() (engine.ShareStatus, bool) {
	if m.shareIdx < 0 || m.shareIdx >= len(m.shares) {
		return engine.ShareStatus{}, false
	}
	return m.shares[m.shareIdx], true
}

func (m Model) currentChain() (engine.Chain, bool) {
	if m.chainIdx < 0 || m.chainIdx >= len(m.chains) {
		return engine.Chain{}, false
	}
	return m.chains[m.chainIdx], true
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		if m.mode != modeNormal {
			return m, m.tick()
		}
		return m, tea.Batch(m.refresh(), m.tick())
	case statusMsg:
		m.err = nil
		m.shares = msg
		if m.shareIdx >= len(m.shares) {
			m.shareIdx = max(0, len(m.shares)-1)
		}
		return m, nil
	case chainsMsg:
		sh, ok := m.currentShare()
		if !ok || sh.Name != msg.share {
			return m, nil
		}
		// Keep the cursor on the same chain across refreshes.
		prev, hadPrev := m.currentChain()
		m.chains = msg.chains
		m.chainIdx = min(m.chainIdx, max(0, len(m.chains)-1))
		if hadPrev {
			for i, c := range m.chains {
				if c.ID == prev.ID {
					m.chainIdx = i
				}
			}
		}
		return m, nil
	case eventsMsg:
		m.events = msg
		return m, nil
	case errMsg:
		m.err = msg.err
		return m, nil
	case doneMsg:
		m.note, m.err = string(msg), nil
		return m, m.refresh()
	case tea.KeyMsg:
		switch m.mode {
		case modeDelete, modeExtend:
			return m.updatePrompt(msg)
		case modeSettings:
			return m.updateSettings(msg)
		}
		return m.updateNormal(msg)
	}
	return m, nil
}

func (m Model) updateNormal(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.note = ""
	switch k.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "r":
		return m, m.refresh()
	case "e":
		if m.view != viewEvents {
			m.back, m.view, m.eventOff = m.view, viewEvents, 0
			return m, m.loadEvents()
		}
	case "esc", "backspace", "left", "h":
		switch m.view {
		case viewChains:
			m.view = viewShares
		case viewFiles:
			m.view = viewChains
		case viewEvents:
			m.view = m.back
		}
		return m, nil
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		m.move(-10)
	case "pgdown":
		m.move(10)
	case "enter", "right", "l":
		switch m.view {
		case viewShares:
			if sh, ok := m.currentShare(); ok {
				m.view, m.chains, m.chainIdx = viewChains, nil, 0
				return m, m.loadChains(sh.Name)
			}
		case viewChains:
			if _, ok := m.currentChain(); ok {
				m.view, m.fileOff = viewFiles, 0
			}
		}
	case "s":
		if sh, ok := m.currentShare(); ok && m.view != viewEvents {
			m.form = newSettingsForm(sh.Share)
			m.mode = modeSettings
		}
	case "d":
		if c, ok := m.currentChain(); ok && (m.view == viewChains || m.view == viewFiles) {
			if c.State != engine.StateMelted {
				m.err = fmt.Errorf("only melted chains can be deleted; this one is %s", strings.ToLower(c.State))
				return m, nil
			}
			m.mode = modeDelete
			m.input.SetValue("")
			m.input.Placeholder = "delete"
			m.input.Focus()
		}
	case "x":
		if c, ok := m.currentChain(); ok && (m.view == viewChains || m.view == viewFiles) {
			if c.State != engine.StateFrozen && c.State != engine.StateMelting {
				m.err = fmt.Errorf("only frozen chains can be extended")
				return m, nil
			}
			m.mode = modeExtend
			m.input.SetValue("")
			m.input.Placeholder = "7"
			m.input.Focus()
		}
	}
	return m, nil
}

func (m *Model) move(d int) {
	switch m.view {
	case viewShares:
		m.shareIdx = clamp(m.shareIdx+d, 0, len(m.shares)-1)
	case viewChains:
		m.chainIdx = clamp(m.chainIdx+d, 0, len(m.chains)-1)
	case viewFiles:
		if c, ok := m.currentChain(); ok {
			m.fileOff = clamp(m.fileOff+d, 0, len(c.Files)-1)
		}
	case viewEvents:
		m.eventOff = clamp(m.eventOff+d, 0, len(m.events)-1)
	}
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(max(v, lo), hi)
}

// updatePrompt handles the delete confirmation and the extend-days prompt.
func (m Model) updatePrompt(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "ctrl+c":
		m.mode = modeNormal
		m.input.Blur()
		return m, nil
	case "enter":
		sh, _ := m.currentShare()
		c, _ := m.currentChain()
		val := strings.TrimSpace(m.input.Value())
		mode := m.mode
		m.mode = modeNormal
		m.input.Blur()
		api := m.api
		if mode == modeDelete {
			if val != "delete" {
				m.err = fmt.Errorf("not deleted: type the word delete to confirm")
				return m, nil
			}
			name := chainName(c)
			return m, func() tea.Msg {
				if err := api.DeleteChain(sh.Name, c.ID); err != nil {
					return errMsg{err}
				}
				return doneMsg(fmt.Sprintf("Deleted %s (%s freed)", name, humanBytes(c.Bytes)))
			}
		}
		days, err := strconv.Atoi(val)
		if err != nil || days < 1 || days > 3650 {
			m.err = fmt.Errorf("enter a number of days between 1 and 3650")
			return m, nil
		}
		name := chainName(c)
		return m, func() tea.Msg {
			if err := api.ExtendChain(sh.Name, c.ID, days); err != nil {
				return errMsg{err}
			}
			return doneMsg(fmt.Sprintf("Extended %s by %d days", name, days))
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m Model) updateSettings(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "ctrl+c":
		m.mode = modeNormal
		return m, nil
	case "enter":
		s, err := m.form.values()
		if err != nil {
			m.form.err = err
			return m, nil
		}
		m.mode = modeNormal
		name := m.form.share
		api := m.api
		return m, func() tea.Msg {
			if err := api.UpdateShare(name, s); err != nil {
				return errMsg{err}
			}
			return doneMsg("Saved settings for " + name)
		}
	}
	var cmd tea.Cmd
	m.form, cmd = m.form.update(k)
	return m, cmd
}
