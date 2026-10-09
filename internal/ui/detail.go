package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bferg314/dockgit/internal/dock"
	"github.com/bferg314/dockgit/internal/link"
)

// Below this width the pane can't sit beside the list; "d" then shows it
// full-screen instead.
const sidePaneMinWidth = 100

// logTail is how many log lines the detail pane shows.
const logTail = 20

type detailLayout int

const (
	layoutNone detailLayout = iota
	layoutSide
	layoutFull
)

type detailState struct {
	show bool // side pane enabled (wide terminals)
	full bool // full-screen pane (narrow terminals)

	// Scrolling. scroll resets when a different item is selected
	// (scrollKey). maxScroll and bodyH are recorded at render time for the
	// key handlers.
	scroll    int
	scrollKey string
	maxScroll int
	bodyH     int
}

// logCache holds the recent log lines of the container the detail pane
// shows, loaded after a short delay.
type logCache struct {
	entries map[string]*logEntry
	want    string // container the pane is waiting on
	seq     int
}

type logEntry struct {
	lines []string
	err   error
}

func (l *logCache) invalidate(id string) {
	delete(l.entries, id)
	if l.want == id {
		l.want = ""
	}
}

func (l *logCache) invalidateAll() {
	l.entries = map[string]*logEntry{}
	l.want = ""
}

type (
	logsTickMsg struct {
		seq int
		id  string
	}
	logsMsg struct {
		id    string
		lines []string
		err   error
	}
)

func (a *App) detailLayout() detailLayout {
	switch {
	case a.logView != nil:
		return layoutNone
	case a.tab == tabCompose && a.compose.selectedStack() == nil:
		return layoutNone
	case a.tab != tabCompose && (a.tab != tabDocker || a.docker.mode != modeContainers || a.docker.selected() == nil):
		return layoutNone
	case a.detail.full:
		return layoutFull
	case a.w >= sidePaneMinWidth && a.detail.show:
		return layoutSide
	}
	return layoutNone
}

// toggleDetails cycles the pane: beside the list, then full screen, then
// hidden. Narrow terminals have no room beside the list, so there it's
// full screen or hidden.
func (a *App) toggleDetails() {
	d := &a.detail
	switch {
	case d.full:
		d.full, d.show = false, false
	case a.w >= sidePaneMinWidth && d.show:
		d.full = true
	case a.w >= sidePaneMinWidth:
		d.show = true
	default:
		d.full = true
	}
}

// syncDetails schedules a log load when the selection moves to a running
// or stopped container whose logs aren't cached. The short delay means
// scrolling quickly through the list doesn't run docker for every row.
func (a *App) syncDetails() tea.Cmd {
	if a.tab != tabDocker || a.detailLayout() == layoutNone {
		return nil
	}
	c := a.docker.selected()
	if c == nil || c.ID == a.logs.want {
		return nil
	}
	// Only the shown container's tail is kept: coming back to one reloads
	// it, so a busy container's logs don't go stale.
	delete(a.logs.entries, a.logs.want)
	a.logs.want = c.ID
	if a.logs.entries[c.ID] != nil {
		return nil
	}
	a.logs.seq++
	seq, id := a.logs.seq, c.ID
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return logsTickMsg{seq: seq, id: id} })
}

func (a *App) loadLogs(id string) tea.Cmd {
	r := a.runner
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		lines, err := dock.Logs(ctx, r, id, logTail)
		return logsMsg{id: id, lines: lines, err: err}
	}
}

// detailLoading reports whether the visible pane is waiting for logs.
func (a *App) detailLoading() bool {
	c := a.docker.selected()
	return a.detailLayout() != layoutNone && c != nil && a.logs.entries[c.ID] == nil
}

// scrollDetail moves the pane's scroll position by delta lines.
func (a *App) scrollDetail(delta int) {
	a.measureDetail()
	a.detail.scroll = max(0, min(a.detail.maxScroll, a.detail.scroll+delta))
}

// measureDetail lays the pane out without drawing it, so the scroll limits
// match the current selection even if several keys arrive between frames.
func (a *App) measureDetail() {
	h := a.h - chromeLines
	switch a.detailLayout() {
	case layoutSide:
		a.renderDetail(a.paneWidth(), h, true)
	case layoutFull:
		a.renderDetail(a.w, h, false)
	}
}

// detailScrollKey handles pane scrolling keys. In the full-screen layout
// the plain movement keys scroll too, since there is no list to move.
func (a *App) detailScrollKey(key string) bool {
	layout := a.detailLayout()
	if layout == layoutNone {
		return false
	}
	half := max(1, a.detail.bodyH/2)
	switch key {
	case "J":
		a.scrollDetail(1)
	case "K":
		a.scrollDetail(-1)
	case "ctrl+d":
		a.scrollDetail(half)
	case "ctrl+u":
		a.scrollDetail(-half)
	default:
		if layout != layoutFull {
			return false
		}
		switch key {
		case "j", "down":
			a.scrollDetail(1)
		case "k", "up":
			a.scrollDetail(-1)
		case "pgdown", "space":
			a.scrollDetail(a.detail.bodyH)
		case "pgup":
			a.scrollDetail(-a.detail.bodyH)
		case "home":
			a.scrollDetail(-a.detail.maxScroll)
		case "end":
			a.scrollDetail(a.detail.maxScroll)
		default:
			return false
		}
	}
	return true
}

func (a *App) paneWidth() int { return max(40, min(70, a.w*2/5)) }

// renderWithPane draws a tab's list with the detail pane beside it (or
// instead of it, on narrow terminals).
func (a *App) renderWithPane(list func(w, h int) string, h int) string {
	switch a.detailLayout() {
	case layoutSide:
		pw := a.paneWidth()
		lw := a.w - pw
		return lipgloss.JoinHorizontal(lipgloss.Top,
			padLines(list(lw, h), h, lw),
			padLines(a.renderDetail(pw, h, true), h, pw))
	case layoutFull:
		return a.renderDetail(a.w, h, false)
	}
	return list(a.w, h)
}

func (a *App) renderDetail(w, h int, border bool) string {
	st := a.st
	c := a.docker.selected()
	cw := w - 3 // content width after the border/gutter

	// The header (name, state, image) stays put; the body scrolls.
	var header, body []string
	lines := &header
	add := func(s string) { *lines = append(*lines, fit(s, cw)) }
	section := func(title string) {
		*lines = append(*lines, "")
		add(st.colHead.Render(title))
	}

	title := ""
	if s := a.compose.selectedStack(); a.tab == tabCompose && s != nil {
		title = s.Name()
		add(st.selName.Render(title))
		add(a.stackState(s) + "  " + st.dim.Render(filepath.Base(s.File)))
		lines = &body
		a.appendStackDetails(a.compose.selectedRoot(), s, add, section)
		if s.File != a.detail.scrollKey {
			a.detail.scrollKey, a.detail.scroll = s.File, 0
		}
	} else if c == nil {
		add(st.dim.Render("No container selected"))
	} else {
		title = c.Name
		add(st.selName.Render(title))
		add(st.stateGlyph(c) + " " + st.containerStatus(c, time.Now()))
		add(st.dim.Render(c.Image))

		lines = &body
		a.appendContainerDetails(c, cw, add, section)

		if c.ID != a.detail.scrollKey {
			a.detail.scrollKey, a.detail.scroll = c.ID, 0
		}
	}

	bodyH := max(0, h-len(header))
	a.detail.bodyH = bodyH
	a.detail.maxScroll = max(0, len(body)-bodyH)
	a.detail.scroll = min(a.detail.scroll, a.detail.maxScroll)
	if a.detail.maxScroll > 0 && len(header) > 0 {
		// Scroll position on the title line, e.g. "J/K ↕ 40%".
		pct := a.detail.scroll * 100 / a.detail.maxScroll
		ind := st.dim.Render(fmt.Sprintf("J/K ↕ %d%%", pct))
		header[0] = fit(st.selName.Render(title), max(1, cw-lipgloss.Width(ind)-1)) + " " + ind
	}
	all := append(header, body[a.detail.scroll:]...)
	if len(all) > h {
		all = all[:h]
	}
	return a.framePane(all, h, cw, border)
}

// framePane pads the pane to h lines and adds the left border or gutter.
func (a *App) framePane(lines []string, h, cw int, border bool) string {
	st := a.st
	prefix := " "
	if border {
		prefix = st.rule.Render("│") + " "
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = prefix + fit(l, cw)
	}
	return strings.Join(lines, "\n")
}

func (a *App) appendContainerDetails(c *dock.Container, cw int, add func(string), section func(string)) {
	st := a.st
	kv := func(k, v string) { add(st.dim.Render(fmt.Sprintf("%-10s", k)) + v) }

	if c.Project() != "" {
		section("COMPOSE")
		if src := a.sourceOf(c); src.Kind != link.None {
			kv("from", st.branch.Render(src.Label()))
		}
		kv("project", st.textS.Render(c.Project()))
		kv("service", st.textS.Render(c.Service()))
		for _, f := range c.ConfigFiles() {
			kv("file", st.textS.Render(tildePath(f)))
		}
	}

	if len(c.Ports) > 0 {
		section("PORTS")
		published := map[string]bool{}
		for _, p := range c.PublishedPorts() {
			published[p.Container] = true
			host := p.HostIP
			if host == "" {
				host = "0.0.0.0"
			}
			add(st.textS.Render(host+":"+p.HostPort) + st.dim.Render(" → ") + st.textS.Render(p.Container))
		}
		for _, p := range c.Ports {
			if !published[p.Container] && !p.Published() {
				published[p.Container] = true
				add(st.dim.Render(p.Container + "  not published"))
			}
		}
	}

	if len(c.Mounts) > 0 {
		section("MOUNTS")
		for _, m := range c.Mounts {
			src := m.Name
			if m.Type != "volume" || src == "" {
				src = m.Source
			}
			ro := ""
			if !m.RW {
				ro = st.dim.Render("  read-only")
			}
			add(st.dim.Render(fmt.Sprintf("%-7s", m.Type)) + st.textS.Render(src) + st.dim.Render(" → ") + st.textS.Render(m.Destination) + ro)
		}
	}

	section(fmt.Sprintf("LOGS  %s", st.dim.Render(fmt.Sprintf("last %d lines", logTail))))
	switch l := a.logs.entries[c.ID]; {
	case l == nil:
		add(a.spin.View() + st.dim.Render(" loading…"))
	case l.err != nil:
		add(st.bad.Render(l.err.Error()))
	case len(l.lines) == 0:
		add(st.dim.Render("no output"))
	default:
		for _, line := range l.lines {
			add(st.dim.Render(line))
		}
	}

	section("CONTAINER")
	kv("id", st.warn.Render(c.ShortID()))
	kv("image id", st.dim.Render(dock.ShortImageID(c.ImageID)))
	kv("created", st.textS.Render(ago(c.Created)))
	if !c.StartedAt.IsZero() {
		kv("started", st.textS.Render(ago(c.StartedAt)))
	}
	if !c.Running() && !c.FinishedAt.IsZero() {
		kv("finished", st.textS.Render(ago(c.FinishedAt)))
	}
	if c.RestartCount > 0 {
		kv("restarts", st.warn.Render(fmt.Sprint(c.RestartCount)))
	}
	if c.OOM {
		kv("", st.bad.Render("killed: out of memory"))
	}
	if c.Error != "" {
		kv("error", st.bad.Render(c.Error))
	}
	kv("command", st.textS.Render(c.Command))

	if len(c.Networks) > 0 {
		section("NETWORKS")
		add(st.textS.Render(strings.Join(c.Networks, ", ")))
	}

	if len(c.Env) > 0 {
		hint := "m shows values"
		if a.docker.showEnv {
			hint = "m hides values"
		}
		section("ENVIRONMENT  " + st.dim.Render(hint))
		env := append([]string(nil), c.Env...)
		sort.Strings(env)
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			if !a.docker.showEnv {
				v = "••••"
			}
			add(st.textS.Render(k) + st.dim.Render("=") + st.dim.Render(v))
		}
	}

}
