package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// actionPopup lists what can be done to the selected item. Each action
// can be picked with the cursor or by its key.
type actionPopup struct {
	title   string
	body    []string // shown above the actions, e.g. what's in the way
	actions []popupAction
	cursor  int
}

type popupAction struct {
	key   string // may be "" when every sensible key is taken
	label string
	note  string // dimmed, after the label
	run   func() tea.Cmd
}

func (a *App) popupKey(key string) tea.Cmd {
	p := a.popup
	switch key {
	case "esc", "q":
		a.popup = nil
	case "up", "k":
		p.cursor = (p.cursor + len(p.actions) - 1) % len(p.actions)
	case "down", "j":
		p.cursor = (p.cursor + 1) % len(p.actions)
	case "enter":
		a.popup = nil
		return p.actions[p.cursor].run()
	default:
		for _, act := range p.actions {
			if act.key == key {
				a.popup = nil
				return act.run()
			}
		}
	}
	return nil
}

func (a *App) renderPopup() string {
	st := a.st
	p := a.popup
	lines := []string{st.bold.Render(p.title), ""}
	if len(p.body) > 0 {
		w := max(30, min(90, a.w-12))
		for _, b := range p.body {
			for _, l := range strings.Split(ansiWrap(b, w), "\n") {
				lines = append(lines, st.warn.Render(l))
			}
		}
		lines = append(lines, "")
	}
	for i, act := range p.actions {
		marker := "  "
		label := st.textS.Render(act.label)
		if i == p.cursor {
			marker = st.marker.Render("▌ ")
			label = st.selName.Render(act.label)
		}
		key := " "
		if act.key != "" {
			key = act.key
		}
		line := marker + st.key.Render(key) + "  " + label
		if act.note != "" {
			line += "  " + st.dim.Render(act.note)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", st.dim.Render("enter or a key runs it · esc closes"))
	return a.boxed(st.box, lines)
}

// confirmDialog asks before something that can't be undone.
type confirmDialog struct {
	title string
	body  []string
	yes   func() tea.Cmd
}

// confirm runs yes straight away when confirmations are turned off in
// Settings, and otherwise asks first.
func (a *App) confirm(title string, body []string, yes func() tea.Cmd) tea.Cmd {
	if !a.cfg.ConfirmDestructive {
		return yes()
	}
	a.confirmDlg = &confirmDialog{title: title, body: body, yes: yes}
	return nil
}

func (a *App) confirmKey(key string) tea.Cmd {
	d := a.confirmDlg
	switch key {
	case "y", "Y", "enter":
		a.confirmDlg = nil
		return d.yes()
	case "n", "N", "esc", "q":
		a.confirmDlg = nil
	}
	return nil
}

func (a *App) renderConfirm() string {
	st := a.st
	d := a.confirmDlg
	lines := []string{st.bad.Render(d.title)}
	if len(d.body) > 0 {
		lines = append(lines, "")
		for _, b := range d.body {
			lines = append(lines, st.textS.Render(b))
		}
	}
	lines = append(lines, "", st.key.Render("y")+st.dim.Render(" yes  ·  ")+st.key.Render("n")+st.dim.Render(" no"))
	return a.boxed(st.box.BorderForeground(st.red), lines)
}

// ansiWrap wraps s at word boundaries to w cells.
func ansiWrap(s string, w int) string { return ansi.Wordwrap(s, w, "") }

// boxed draws lines in a popup box no wider than the terminal: the box's
// border and padding take 6 columns, and longer lines are shortened so
// the border stays whole.
func (a *App) boxed(box lipgloss.Style, lines []string) string {
	w := max(10, a.w-6)
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			lines[i] = ansi.Truncate(l, w, "…")
		}
	}
	return box.Render(strings.Join(lines, "\n"))
}
