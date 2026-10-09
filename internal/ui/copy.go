package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"github.com/bferg314/dockgit/internal/dock"
	"github.com/bferg314/dockgit/internal/match"
)

// writeClipboard writes the local clipboard; tests replace it.
var writeClipboard = clipboard.WriteAll

// copyItem is one thing y can copy.
type copyItem struct {
	key, label, value string
}

// copyText puts s on the clipboard: through the terminal (OSC 52, which
// reaches your own machine even over SSH) and, when not over SSH, the
// local clipboard as well, for terminals that ignore OSC 52.
func (a *App) copyText(what, s string) tea.Cmd {
	if os.Getenv("SSH_CONNECTION") == "" && os.Getenv("SSH_TTY") == "" {
		_ = writeClipboard(s)
	}
	return tea.Batch(tea.SetClipboard(s), a.notify(1, "Copied %s", what))
}

// copyMenu copies the only item straight away, or lets you pick one.
func (a *App) copyMenu(title string, items []copyItem) tea.Cmd {
	switch len(items) {
	case 0:
		return nil
	case 1:
		return a.copyText(items[0].label, items[0].value)
	}
	p := &actionPopup{title: "Copy from " + title}
	for _, it := range items {
		p.actions = append(p.actions, popupAction{key: it.key, label: it.label, note: shorten(it.value, 48),
			run: func() tea.Cmd { return a.copyText(it.label, it.value) }})
	}
	a.popup = p
	return nil
}

func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (a *App) copyContainer(c *dock.Container) tea.Cmd {
	items := []copyItem{{"i", "ID", c.ID}, {"n", "name", c.Name}}
	if url := portURL(c); url != "" {
		items = append(items, copyItem{"u", "URL", url})
	}
	if d := projectDir(c); d != "" {
		items = append(items, copyItem{"p", "project folder", d})
	}
	return a.copyMenu(c.Name, items)
}

func (a *App) copyRepo(r *repoRow) tea.Cmd {
	items := []copyItem{{"p", "path", r.path}}
	if r.status != nil {
		for _, remote := range r.status.Remotes {
			if url := match.WebURL(remote); url != "" {
				items = append(items, copyItem{"w", "web page", url})
				break
			}
		}
		if r.status.Branch != "" {
			items = append(items, copyItem{"b", "branch", r.status.Branch})
		}
	}
	return a.copyMenu(r.name, items)
}

func (a *App) copyStack(s *stackRow) tea.Cmd {
	p := a.stackProject(s)
	cmd := "cd " + quoteArg(p.Dir) + " && docker " + joinArgs(p.Args("up", "-d"))
	return a.copyMenu(s.Name(), []copyItem{
		{"f", "file", s.File},
		{"d", "folder", s.Dir},
		{"c", "up command", cmd},
	})
}

// copyLogs copies the log lines on screen.
func (a *App) copyLogs() tea.Cmd {
	v := a.logView
	rows := v.rows(a.w)
	h := v.height
	if h <= 0 { // not drawn yet
		h = max(1, a.h-chromeLines)
	}
	start := v.top
	if v.follow {
		start = max(0, len(rows)-h)
	}
	var lines []string
	last := -1
	for _, r := range rows[min(start, len(rows)):min(len(rows), start+h)] {
		if r.line != last { // a wrapped line is copied once, whole
			lines = append(lines, v.text(v.lines[r.line]))
			last = r.line
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return a.copyText(fmt.Sprintf("%d %s", len(lines), plural(len(lines), "line", "lines")), strings.Join(lines, "\n"))
}

// quoteArg quotes an argument for a shell when it needs it.
func quoteArg(s string) string {
	if s == "" || strings.ContainsAny(s, " \t'\"$&|;<>()*?") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return filepath.ToSlash(s)
}

func joinArgs(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = quoteArg(a)
	}
	return strings.Join(out, " ")
}
