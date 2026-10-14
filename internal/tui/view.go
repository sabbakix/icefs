package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sabbakix/icefs/internal/engine"
)

func (m Model) View() string {
	var title, body string
	switch {
	case m.mode == modeSettings:
		title, body = "Settings › "+m.form.share, m.form.view()
	case m.view == viewShares:
		title, body = "Shares", m.sharesView()
	case m.view == viewChains:
		title, body = m.crumb(), m.chainsView()
	case m.view == viewFiles:
		title, body = m.crumb(), m.filesView()
	case m.view == viewEvents:
		title, body = "Activity", m.eventsView()
	}
	return m.header() + "\n" + panel(title, body, m.width, m.height-6) + "\n" + m.footer()
}

func (m Model) crumb() string {
	sh, _ := m.currentShare()
	crumb := "Shares › " + sh.Name
	if c, ok := m.currentChain(); ok && m.view == viewFiles {
		crumb += " › " + chainName(c)
	}
	return crumb
}

// header is the ice banner with the totals across every share.
func (m Model) header() string {
	var frozen, melted int64
	for _, s := range m.shares {
		frozen += s.FrozenBytes
		melted += s.MeltedBytes
	}
	right := []string{"❄ ", humanBytes(frozen) + " frozen", "   ≈ ", humanBytes(melted) + " melted ", " "}
	if m.err != nil && len(m.shares) == 0 {
		right = []string{"⚠ ", "icefsd unreachable ", " "}
	}
	return banner([]string{" ❄ IceFs", "  ·  immutable backup vault"}, right, m.width)
}

func (m Model) footer() string {
	var b strings.Builder
	switch m.mode {
	case modeDelete:
		c, _ := m.currentChain()
		b.WriteString(errStyle.Render(fmt.Sprintf("✖ Delete %s: %d files, %s. This cannot be undone.", chainName(c), len(c.Files), humanBytes(c.Bytes))) + "\n")
		b.WriteString(" Type delete and press enter: " + m.input.View() + dimStyle.Render("  esc cancel"))
		return b.String()
	case modeExtend:
		c, _ := m.currentChain()
		b.WriteString("\n" + fmt.Sprintf(" ❄ Keep %s frozen for how many more days? ", chainName(c)) + m.input.View() + dimStyle.Render("  esc cancel"))
		return b.String()
	}
	switch {
	case m.err != nil:
		b.WriteString(errStyle.Render(" ✖ "+m.err.Error()) + "\n")
	case m.note != "":
		b.WriteString(okStyle.Render(" ✔ "+m.note) + "\n")
	default:
		b.WriteString("\n")
	}
	keys := map[view]string{
		viewShares: "↑↓ move  enter chains  s settings  e activity  r refresh  q quit",
		viewChains: "↑↓ move  enter files  d delete  x extend  s settings  e activity  esc back  q quit",
		viewFiles:  "↑↓ scroll  d delete  x extend  esc back  q quit",
		viewEvents: "↑↓ scroll  esc back  q quit",
	}
	k := keys[m.view]
	if m.mode == modeSettings {
		k = "tab/↑↓ move  space toggle  enter save  esc cancel"
	}
	b.WriteString(" " + keyHints(k))
	return b.String()
}

func (m Model) sharesView() string {
	if len(m.shares) == 0 {
		if m.err != nil {
			return "Cannot reach icefsd. Is the service running, and are you root?\n"
		}
		return "No shares configured. Add [[share]] entries to /etc/icefs/config.toml.\n"
	}
	now := m.now()
	w := []int{12, 10, 18, 10, 10, 9, 12}
	var b strings.Builder
	b.WriteString(heading([]string{"Share", "Disk used", "Free", "Frozen", "Melted", "Next melt", "Auto-delete"}, w))
	for i, s := range m.shares {
		free := fmt.Sprintf("%s %.0f%%", humanBytes(int64(s.Disk.Free)), s.FreePercent)
		if s.LowSpace {
			free = errStyle.Render(free + " LOW")
		}
		auto := "off"
		if s.AutoDelete {
			auto = fmt.Sprintf("at %.0f%% free", s.MinFreePercent)
		}
		line := row([]string{
			s.Name, diskBar(s.Disk), free, frozenStyle.Render(humanBytes(s.FrozenBytes)), meltedStyle.Render(humanBytes(s.MeltedBytes)),
			until(s.NextMelt, now), auto,
		}, w)
		if i == m.shareIdx {
			line = selected(line)
		} else {
			line = unselected(line)
		}
		b.WriteString(line + "\n")
	}
	if sh, ok := m.currentShare(); ok {
		b.WriteString("\n  " + dimStyle.Render(fmt.Sprintf("%s · freeze %d days · settle %d min · %d frozen, %d melted, %d writing · %d .vbm copies",
			sh.Path, sh.RetentionDays, sh.SettleMinutes, sh.FrozenFiles, sh.MeltedFiles, sh.PendingFiles, sh.Snapshots)) + "\n")
		if sh.LowSpace && sh.MeltedFiles == 0 {
			b.WriteString("  " + errStyle.Render("⚠ Low on space and nothing has melted yet: shorten the freeze or add disk.") + "\n")
		}
	}
	return b.String()
}

func (m Model) chainsView() string {
	if len(m.chains) == 0 {
		return "No backup chains found in this share yet.\n"
	}
	now := m.now()
	w := []int{32, 9, 12, 6, 10, 17}
	var b strings.Builder
	b.WriteString(heading([]string{"Chain", "State", "Melts in", "Files", "Size", "Last backup"}, w))
	start, end := window(m.chainIdx, len(m.chains), m.listHeight())
	for i := start; i < end; i++ {
		c := m.chains[i]
		melts := until(c.MeltsAt, now)
		if c.State == engine.StateMelted || c.State == engine.StateWriting {
			melts = "-"
		}
		name := chainName(c)
		if !c.Closed {
			// Shorten the name first so a long one cannot push the marker out.
			name = strings.TrimRight(pad(name, w[0]-2), " ") + " *"
		}
		line := row([]string{name, stateLabel(c.State), melts, fmt.Sprint(len(c.Files)), humanBytes(c.Bytes), when(c.Newest)}, w)
		if i == m.chainIdx {
			line = selected(line)
		} else {
			line = unselected(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n  " + dimStyle.Render("* current chain: Veeam may still add incrementals, so it is never auto-deleted") + "\n")
	return b.String()
}

func (m Model) filesView() string {
	c, ok := m.currentChain()
	if !ok {
		return ""
	}
	now := m.now()
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  %s  %s  ·  %d files  ·  %s", stateLabel(c.State), c.Dir, len(c.Files), humanBytes(c.Bytes)))
	if c.MeltsAt > 0 {
		b.WriteString(fmt.Sprintf("  ·  melts %s (in %s)", when(c.MeltsAt), until(c.MeltsAt, now)))
	}
	b.WriteString("\n\n")
	w := []int{40, 5, 10, 9, 17}
	b.WriteString(heading([]string{"File", "Type", "Size", "State", "Written"}, w))
	h := m.listHeight()
	end := min(len(c.Files), m.fileOff+h)
	for _, f := range c.Files[m.fileOff:end] {
		b.WriteString("  " + row([]string{filepath.Base(f.Path), string(f.Kind), humanBytes(f.Size), stateLabel(f.State), when(f.MTime)}, w) + "\n")
	}
	return b.String()
}

func (m Model) eventsView() string {
	if len(m.events) == 0 {
		return "No activity yet.\n"
	}
	w := []int{17, 10, 16, 40}
	var b strings.Builder
	b.WriteString(heading([]string{"When", "Share", "What", "File"}, w))
	h := m.listHeight()
	end := min(len(m.events), m.eventOff+h)
	for _, e := range m.events[m.eventOff:end] {
		what := e.Action
		switch e.Action {
		case "frozen", "refrozen":
			what = frozenStyle.Render(e.Action)
		case "melted":
			what = meltedStyle.Render(e.Action)
		case "deleted", "freeze-failed", "melt-failed", "delete-failed", "missing", "low-space", "refreeze-failed":
			what = errStyle.Render(e.Action)
		}
		detail := filepath.Base(e.Path)
		if e.Detail != "" {
			detail += dimStyle.Render("  " + e.Detail)
		}
		b.WriteString("  " + dimStyle.Render(pad(when(e.At), 17)) + "  " + pad(e.Share, 10) + "  " + pad(what, 16) + "  " + detail + "\n")
	}
	return b.String()
}

// heading is a table's column titles over a thin rule, indented past the
// cursor gutter.
func heading(cols []string, w []int) string {
	width := 2 * (len(w) - 1)
	for _, n := range w {
		width += n
	}
	return "  " + headStyle.Render(row(cols, w)) + "\n  " + rule(width) + "\n"
}

// listHeight is how many rows fit in the panel between the table heading and
// the notes under it.
func (m Model) listHeight() int {
	return max(5, m.height-10)
}

// window returns the slice of rows to draw so the cursor stays visible.
func window(cursor, n, h int) (int, int) {
	if n <= h {
		return 0, n
	}
	start := min(max(0, cursor-h/2), n-h)
	return start, start + h
}
