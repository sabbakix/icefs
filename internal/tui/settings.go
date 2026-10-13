package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/sabbakix/icefs/internal/client"
	"github.com/sabbakix/icefs/internal/config"
)

// settingsForm edits one share's retention, settle time, free-space
// threshold and auto-delete switch.
type settingsForm struct {
	share      string
	inputs     [3]textinput.Model // retention days, settle minutes, min free %
	autoDelete bool
	focus      int // 0..2 inputs, 3 auto-delete
	err        error
}

var settingLabels = [3]string{"Freeze for (days)", "Settle time (minutes)", "Low space at (% free)"}

func newSettingsForm(s config.Share) settingsForm {
	f := settingsForm{share: s.Name, autoDelete: s.AutoDelete}
	vals := [3]string{
		strconv.Itoa(s.RetentionDays),
		strconv.Itoa(s.SettleMinutes),
		strconv.FormatFloat(s.MinFreePercent, 'f', -1, 64),
	}
	for i := range f.inputs {
		ti := textinput.New()
		ti.CharLimit = 6
		ti.Prompt = ""
		ti.Width = 8
		ti.SetValue(vals[i])
		f.inputs[i] = ti
	}
	f.inputs[0].Focus()
	return f
}

func (f settingsForm) update(k tea.KeyMsg) (settingsForm, tea.Cmd) {
	switch k.String() {
	case "tab", "down":
		f.setFocus((f.focus + 1) % 4)
		return f, nil
	case "shift+tab", "up":
		f.setFocus((f.focus + 3) % 4)
		return f, nil
	case " ":
		if f.focus == 3 {
			f.autoDelete = !f.autoDelete
			return f, nil
		}
	}
	if f.focus == 3 {
		return f, nil
	}
	var cmd tea.Cmd
	f.inputs[f.focus], cmd = f.inputs[f.focus].Update(k)
	f.err = nil
	return f, cmd
}

func (f *settingsForm) setFocus(i int) {
	f.focus = i
	for j := range f.inputs {
		if j == i {
			f.inputs[j].Focus()
		} else {
			f.inputs[j].Blur()
		}
	}
}

func (f settingsForm) values() (client.ShareSettings, error) {
	ret, err1 := strconv.Atoi(strings.TrimSpace(f.inputs[0].Value()))
	settle, err2 := strconv.Atoi(strings.TrimSpace(f.inputs[1].Value()))
	free, err3 := strconv.ParseFloat(strings.TrimSpace(f.inputs[2].Value()), 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return client.ShareSettings{}, fmt.Errorf("all three values must be numbers")
	}
	s := client.ShareSettings{RetentionDays: ret, SettleMinutes: settle, MinFreePercent: free, AutoDelete: f.autoDelete}
	if err := config.ValidateShareSettings(config.Share{RetentionDays: ret, SettleMinutes: settle, MinFreePercent: free}); err != nil {
		return client.ShareSettings{}, err
	}
	return s, nil
}

func (f settingsForm) view() string {
	var b strings.Builder
	b.WriteString(headStyle.Render("Settings for "+f.share) + "\n\n")
	for i, in := range f.inputs {
		marker := "  "
		if f.focus == i {
			marker = "> "
		}
		b.WriteString(marker + pad(settingLabels[i], 24) + in.View() + "\n")
	}
	marker := "  "
	if f.focus == 3 {
		marker = "> "
	}
	box := "[ ]"
	if f.autoDelete {
		box = "[x]"
	}
	threshold := strings.TrimSpace(f.inputs[2].Value())
	b.WriteString(marker + pad("Auto-delete melted", 24) + box + dimStyle.Render(" when free space is "+threshold+"% or less") + "\n\n")
	b.WriteString(dimStyle.Render("A longer freeze extends frozen chains now. A shorter one only\napplies to files frozen from now on.") + "\n\n")
	if f.err != nil {
		b.WriteString(errStyle.Render(f.err.Error()) + "\n")
	}
	b.WriteString(dimStyle.Render("tab/up/down move  space toggle  enter save  esc cancel"))
	return boxStyle.Render(b.String())
}
