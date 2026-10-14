package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"
)

// The ice palette. lipgloss maps these down on terminals without true color.
const (
	cFrost   = "#e0f7ff" // near-white text on ice
	cIce     = "#67e8f9" // frozen, keys, headings
	cSky     = "#38bdf8"
	cGlacier = "#1f6f8b" // panel borders and rules
	cDeep    = "#0c4a6e" // selected row
	cSlate   = "#7b8ba1" // secondary text
	cAmber   = "#fbbf24" // melting
	cThaw    = "#4ade80" // melted
	cAlert   = "#f87171"
)

// The banner fades from deep water to glacier ice.
var bannerStops = []string{"#06213d", "#0b4f7c", "#0e7fa8", "#22b5d6"}

// icicles is the edge under the banner; it repeats across the width.
const icicles = "▀▀▀▜▀▀▀▀▀█▀▀▜▀▀▀▀▀▀▀▀▛▀▀▀▀█▀▀▀▀▀▀▛▀▀▀▀▀▀▀▜▀▀▀▀█▀▀▀"

// gradient returns the banner color at x of w columns.
func gradient(x, w int) colorful.Color {
	if w < 2 {
		w = 2
	}
	t := float64(x) / float64(w-1) * float64(len(bannerStops)-1)
	i := min(int(t), len(bannerStops)-2)
	a, _ := colorful.Hex(bannerStops[i])
	b, _ := colorful.Hex(bannerStops[i+1])
	return a.BlendLab(b, t-float64(i)).Clamped()
}

// banner draws the full-width title bar: the name on the left, right on the
// right, over the ice gradient, with icicles hanging from its lower edge.
func banner(left, right []string, w int) string {
	// left and right are [text, style] pairs so each part keeps its own look
	// while the background changes under it.
	var cells []lipgloss.Style
	var text []rune
	add := func(s string, st lipgloss.Style) {
		for _, r := range s {
			text = append(text, r)
			cells = append(cells, st)
		}
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(cFrost))
	sub := lipgloss.NewStyle().Foreground(lipgloss.Color("#a5d8ee"))
	for i, s := range left {
		add(s, []lipgloss.Style{title, sub}[i%2])
	}
	rw := 0
	for _, s := range right {
		rw += len([]rune(s))
	}
	gap := w - len(text) - rw
	if gap < 1 {
		right, gap = nil, max(0, w-len(text))
	}
	add(strings.Repeat(" ", gap), sub)
	for i, s := range right {
		add(s, []lipgloss.Style{title, sub}[i%2])
	}
	var top, edge strings.Builder
	edgeRunes := []rune(icicles)
	for x := 0; x < w && x < len(text); x++ {
		c := lipgloss.Color(gradient(x, w).Hex())
		top.WriteString(cells[x].Background(c).Render(string(text[x])))
		edge.WriteString(lipgloss.NewStyle().Foreground(c).Render(string(edgeRunes[x%len(edgeRunes)])))
	}
	return top.String() + "\n" + edge.String()
}

// panel frames body in a rounded border w columns wide with title set into
// the top edge, and pads it to at least h inner rows so the footer stays at
// the bottom of the screen.
func panel(title, body string, w, h int) string {
	bc := lipgloss.NewStyle().Foreground(lipgloss.Color(cGlacier))
	inner := max(10, w-4)
	t := " " + title + " "
	fill := max(0, w-4-lipgloss.Width(t))
	var b strings.Builder
	b.WriteString(bc.Render("╭─") + panelTitleStyle.Render(t) + bc.Render(strings.Repeat("─", fill)+"─╮") + "\n")
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	for _, l := range lines {
		l = ansi.Truncate(l, inner, "~")
		b.WriteString(bc.Render("│") + " " + l + strings.Repeat(" ", inner-lipgloss.Width(l)) + " " + bc.Render("│") + "\n")
	}
	b.WriteString(bc.Render("╰" + strings.Repeat("─", w-2) + "╯"))
	return b.String()
}

// keyHints renders "key what  key what" pairs with the keys picked out.
func keyHints(s string) string {
	var parts []string
	for _, p := range strings.Split(s, "  ") {
		k, what, _ := strings.Cut(p, " ")
		parts = append(parts, keyStyle.Render(k)+" "+dimStyle.Render(what))
	}
	return strings.Join(parts, dimStyle.Render(" · "))
}

// rule is the thin line under a table heading.
func rule(w int) string {
	return ruleStyle.Render(strings.Repeat("─", w))
}
