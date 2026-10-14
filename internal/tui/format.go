package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/sabbakix/icefs/internal/engine"
	"github.com/sabbakix/icefs/internal/veeam"
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("24")).Padding(0, 1)
	headStyle    = lipgloss.NewStyle().Bold(true).Underline(true)
	selStyle     = lipgloss.NewStyle().Reverse(true)
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	okStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	frozenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	meltingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	meltedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	writingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	boxStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

// selected highlights a whole row. Colors inside the row are dropped first:
// their resets would otherwise end the highlight partway through the line.
func selected(line string) string {
	return selStyle.Render(ansi.Strip(line))
}

// stateLabel turns an engine state into the word shown on screen.
func stateLabel(s string) string {
	switch s {
	case engine.StateFrozen:
		return frozenStyle.Render("Frozen")
	case engine.StateMelting:
		return meltingStyle.Render("Melting")
	case engine.StateMelted:
		return meltedStyle.Render("Melted")
	case engine.StateWriting, "pending":
		return writingStyle.Render("Writing")
	}
	return s
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// until renders the time left before a melt, like "3d 4h".
func until(unix int64, now time.Time) string {
	if unix == 0 {
		return "-"
	}
	d := time.Unix(unix, 0).Sub(now)
	if d <= 0 {
		return "now"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes())+1)
}

func when(unix int64) string {
	if unix == 0 {
		return "-"
	}
	return time.Unix(unix, 0).Local().Format("2006-01-02 15:04")
}

// diskBar draws used space as a 10-cell bar.
func diskBar(d engine.Disk) string {
	if d.Total == 0 {
		return "[          ]"
	}
	used := int(float64(d.Total-d.Free) / float64(d.Total) * 10)
	if used > 10 {
		used = 10
	}
	return "[" + strings.Repeat("#", used) + strings.Repeat(".", 10-used) + "]"
}

func chainName(c engine.Chain) string {
	if strings.HasPrefix(c.ID, veeam.OrphanPrefix) {
		return filepath.Base(c.Dir) + "/(no full)"
	}
	return filepath.Base(c.Dir) + "/" + filepath.Base(c.ID)
}

// pad cuts or pads s to width w, counting visible characters only.
func pad(s string, w int) string {
	vis := lipgloss.Width(s)
	if vis > w {
		r := []rune(s)
		if len(r) > w && w > 1 {
			return string(r[:w-1]) + "~"
		}
		return s
	}
	return s + strings.Repeat(" ", w-vis)
}

func row(cols []string, widths []int) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = pad(c, widths[i])
	}
	return strings.Join(parts, "  ")
}
