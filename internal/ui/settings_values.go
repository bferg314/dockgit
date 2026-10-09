package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/dockgit/internal/dock"
)

// valueDialog edits one Settings value.
type valueDialog struct {
	label, help string
	input       textinput.Model
	set         func(string) error
	err         string
}

// valueItem is a Settings row whose value is typed in.
func (a *App) valueItem(section, label, help string, get func() string, set func(string) error) settingItem {
	return settingItem{section: section, label: label, detail: get() + " · enter: change", set: func() tea.Cmd {
		in := textinput.New()
		in.Prompt = ""
		in.SetValue(get())
		in.CursorEnd()
		in.Focus()
		a.valueDlg = &valueDialog{label: label, help: help, input: in, set: set}
		return nil
	}}
}

func (a *App) valueKey(msg tea.KeyPressMsg) tea.Cmd {
	d := a.valueDlg
	switch msg.String() {
	case "esc":
		a.valueDlg = nil
		return nil
	case "enter":
		if err := d.set(strings.TrimSpace(d.input.Value())); err != nil {
			d.err = err.Error()
			return nil
		}
		a.valueDlg = nil
		return a.saveConfig()
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	d.err = ""
	return cmd
}

func (a *App) renderValue() string {
	st := a.st
	d := a.valueDlg
	w := min(56, max(30, a.w-10))
	d.input.SetWidth(w - 4)
	lines := []string{st.boxTitle.Render(d.label), "", st.dim.Render(d.help), "", fit(st.key.Render("› ")+d.input.View(), w)}
	if d.err != "" {
		lines = append(lines, "", st.bad.Render(d.err))
	}
	lines = append(lines, "", st.dim.Render("enter saves · esc cancels"))
	return a.boxed(st.box, lines)
}

// cleanupValues are the editable cleanup and log settings.
func (a *App) cleanupValues() []settingItem {
	c := &a.cfg.Cleanup
	size := func(dst *string) func(string) error {
		return func(v string) error {
			if _, err := dock.ParseSize(v); err != nil {
				return fmt.Errorf("a size like 10GB or 512MB")
			}
			*dst = v
			return nil
		}
	}
	return []settingItem{
		a.valueItem("Cleanup", "Keep build cache up to", "Pruning keeps this much build cache (docker builder prune --reserved-space).",
			func() string { return c.KeepStorage }, size(&c.KeepStorage)),
		a.valueItem("Cleanup", "Prune cache unused for", "Only cache not used for this long is pruned: 168h is a week, 720h a month.",
			func() string { return c.OlderThan }, func(v string) error {
				if _, err := time.ParseDuration(v); err != nil {
					return fmt.Errorf("a duration like 168h or 30m")
				}
				c.OlderThan = v
				return nil
			}),
		a.valueItem("Cleanup", "Prune when cache passes", "In threshold mode, prune once the build cache is bigger than this.",
			func() string { return c.Threshold }, size(&c.Threshold)),
		{section: "Cleanup", label: "Prune dangling images too", detail: "unnamed images left by rebuilds", on: &c.PruneDanglingImages},
		a.valueItem("Docker", "Log lines to load", "How many past lines the logs viewer starts with.",
			func() string { return strconv.Itoa(a.cfg.LogTail) }, func(v string) error {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 || n > 100000 {
					return fmt.Errorf("a number from 1 to 100000")
				}
				a.cfg.LogTail = n
				return nil
			}),
	}
}
