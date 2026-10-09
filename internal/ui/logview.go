package ui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/dockgit/internal/dock"
)

// logKeep caps the lines kept in memory; older ones are dropped. How many
// the viewer starts with is the log_tail setting.
const logKeep = 10000

// logView shows a log full screen: a container's logs, a compose
// project's logs (args), or a job's output (job).
type logView struct {
	id, name string
	args     []string // docker command to follow instead of a container's logs
	dir      string   // where args run
	job      *jobState
	static   bool // a fixed report (the doctor): nothing to follow

	lines []string // as docker sends them, with a timestamp prefix
	ended bool     // the stream finished (container stopped)
	err   error

	follow     bool // stick to the newest line
	top        int  // first row shown when not following
	wrap       bool
	timestamps bool

	search textinput.Model
	query  string // last submitted search, lower case

	stop   context.CancelFunc
	ch     <-chan string
	errc   <-chan error
	gen    int
	height int // body rows at the last render, for paging
}

type logLinesMsg struct {
	gen   int
	lines []string
	done  bool
	err   error
}

// openLogs starts following c's logs.
func (a *App) openLogs(c *dock.Container) tea.Cmd {
	a.closeLogs()
	a.logView = &logView{id: c.ID, name: c.Name, follow: true, wrap: true, search: newSearch()}
	return a.startLogStream()
}

func (a *App) startLogStream() tea.Cmd {
	v := a.logView
	if v.stop != nil {
		v.stop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.logGen++
	v.stop, v.gen = cancel, a.logGen
	v.lines, v.ended, v.err = nil, false, nil
	if v.args != nil {
		v.ch, v.errc = dock.FollowArgs(ctx, a.runner, v.dir, v.args...)
	} else {
		v.ch, v.errc = dock.Follow(ctx, a.runner, v.id, a.cfg.LogTail)
	}
	return waitLogLines(v.gen, v.ch, v.errc)
}

func (a *App) closeLogs() {
	if a.logView != nil && a.logView.stop != nil {
		a.logView.stop()
	}
	a.logView = nil
}

// waitLogLines blocks for the next line, then takes whatever else is ready
// so a burst renders once.
func waitLogLines(gen int, ch <-chan string, errc <-chan error) tea.Cmd {
	return func() tea.Msg {
		l, ok := <-ch
		if !ok {
			return logLinesMsg{gen: gen, done: true, err: <-errc}
		}
		lines := []string{l}
		for len(lines) < 2000 {
			select {
			case l, ok := <-ch:
				if !ok {
					return logLinesMsg{gen: gen, lines: lines}
				}
				lines = append(lines, l)
			default:
				return logLinesMsg{gen: gen, lines: lines}
			}
		}
		return logLinesMsg{gen: gen, lines: lines}
	}
}

func (a *App) handleLogLines(msg logLinesMsg) tea.Cmd {
	v := a.logView
	if v == nil || msg.gen != v.gen {
		return nil
	}
	if msg.done {
		v.ended, v.err = true, msg.err
		return nil
	}
	v.lines = append(v.lines, msg.lines...)
	if over := len(v.lines) - logKeep; over > 0 {
		v.lines = append([]string(nil), v.lines[over:]...)
		v.top = max(0, v.top-over)
	}
	return waitLogLines(v.gen, v.ch, v.errc)
}

// text is a line as shown: without its timestamp unless they're on.
func (v *logView) text(line string) string {
	ts, rest := dock.TrimTimestamp(line)
	if v.timestamps && ts != "" {
		if len(ts) > 19 {
			ts = ts[:19] // seconds are enough on screen
		}
		return strings.Replace(ts, "T", " ", 1) + "  " + rest
	}
	return rest
}

// row is one screen line: part of a log line.
type logRow struct {
	line int
	text string
}

// rows lays the lines out for width w.
func (v *logView) rows(w int) []logRow {
	rows := make([]logRow, 0, len(v.lines))
	for i, l := range v.lines {
		t := v.text(l)
		if !v.wrap || ansi.StringWidth(t) <= w {
			rows = append(rows, logRow{i, t})
			continue
		}
		for _, part := range strings.Split(ansi.Hardwrap(t, w, true), "\n") {
			rows = append(rows, logRow{i, part})
		}
	}
	return rows
}

func (a *App) logViewKey(msg tea.KeyPressMsg) tea.Cmd {
	v := a.logView
	key := msg.String()
	if v.search.Focused() {
		switch key {
		case "esc":
			v.search.SetValue("")
			v.search.Blur()
			v.query = ""
		case "enter":
			v.search.Blur()
			v.query = strings.ToLower(v.search.Value())
			return a.logSearch(1)
		default:
			var cmd tea.Cmd
			v.search, cmd = v.search.Update(msg)
			return cmd
		}
		return nil
	}

	page := max(1, v.height-1)
	switch key {
	case "esc", "q":
		if v.query != "" && key == "esc" {
			v.query = ""
			v.search.SetValue("")
			return nil
		}
		a.closeLogs()
	case "up", "k":
		a.logScroll(-1)
	case "down", "j":
		a.logScroll(1)
	case "pgup", "ctrl+u":
		a.logScroll(-page)
	case "pgdown", "ctrl+d", "space":
		a.logScroll(page)
	case "g", "home":
		v.follow, v.top = false, 0
	case "G", "end":
		v.follow = true
	case "?":
		a.help = helpState{open: true}
	case "w":
		v.wrap = !v.wrap
	case "t":
		v.timestamps = !v.timestamps
	case "/":
		return v.search.Focus()
	case "n":
		return a.logSearch(1)
	case "N":
		return a.logSearch(-1)
	case "r":
		if v.ended && v.job == nil && !v.static {
			return a.startLogStream()
		}
	}
	return nil
}

// logScroll moves the view by delta rows; reaching the bottom resumes
// following.
func (a *App) logScroll(delta int) {
	v := a.logView
	rows := v.rows(a.w)
	maxTop := max(0, len(rows)-v.height)
	if v.follow {
		v.top = maxTop
	}
	v.top = max(0, min(maxTop, v.top+delta))
	v.follow = v.top == maxTop && delta > 0
}

// logSearch jumps to the next (dir 1) or previous (-1) line containing the
// query, starting from the one at the top of the screen.
func (a *App) logSearch(dir int) tea.Cmd {
	v := a.logView
	if v.query == "" || len(v.lines) == 0 {
		return nil
	}
	rows := v.rows(a.w)
	if v.follow {
		v.top = max(0, len(rows)-v.height)
	}
	cur := 0
	if v.top < len(rows) {
		cur = rows[v.top].line
	}
	for n := 1; n <= len(v.lines); n++ {
		i := ((cur+dir*n)%len(v.lines) + len(v.lines)) % len(v.lines)
		if strings.Contains(strings.ToLower(v.text(v.lines[i])), v.query) {
			for r, row := range rows {
				if row.line == i {
					v.top, v.follow = max(0, min(r, len(rows)-v.height)), false
					return nil
				}
			}
		}
	}
	return a.notify(0, "No matches for %q", v.query)
}

func (a *App) renderLogView(h int) string {
	st := a.st
	v := a.logView
	var footer string
	switch {
	case v.search.Focused():
		footer = " " + st.key.Render("/") + " " + v.search.View()
	case v.query != "":
		footer = " " + st.dim.Render("search: ") + st.match.Render(v.query) + st.dim.Render("  n/N next/previous · esc clears")
	}
	if footer != "" {
		h--
	}
	if v.ended {
		h-- // for the end marker
	}
	v.height = h

	rows := v.rows(a.w)
	maxTop := max(0, len(rows)-h)
	if v.follow {
		v.top = maxTop
	}
	v.top = min(v.top, maxTop)

	var lines []string
	if len(rows) == 0 {
		msg := a.spin.View() + st.dim.Render(" waiting for output…")
		if v.ended {
			msg = st.dim.Render("No output.")
		}
		lines = append(lines, "", "  "+msg)
	}
	for _, r := range rows[v.top:min(len(rows), v.top+h)] {
		lines = append(lines, highlight(st, r.text, v.query, a.w))
	}
	body := padLines(strings.Join(lines, "\n"), h, a.w)
	if v.ended {
		end := "— the container stopped · r follows again —"
		switch {
		case v.static:
			end = "— esc closes —"
		case v.job != nil && v.err != nil:
			end = "— failed · esc closes —"
		case v.job != nil:
			end = "— finished · esc closes —"
		case v.args != nil:
			end = "— the logs ended · r follows again —"
		case v.err != nil:
			end = "— " + v.err.Error() + " · r tries again —"
		}
		body += "\n" + fit(st.faintText.Render(end), a.w)
	}
	if footer != "" {
		body += "\n" + fit(footer, a.w)
	}
	return body
}

// highlight marks case-insensitive matches of q in s and fits it to w.
func highlight(st styles, s, q string, w int) string {
	s = ansi.Truncate(s, w, "…")
	if q == "" {
		return st.textS.Render(s)
	}
	lower := strings.ToLower(s)
	var b strings.Builder
	for {
		i := strings.Index(lower, q)
		if i < 0 {
			b.WriteString(st.textS.Render(s))
			break
		}
		b.WriteString(st.textS.Render(s[:i]))
		b.WriteString(st.match.Render(s[i : i+len(q)]))
		s, lower = s[i+len(q):], lower[i+len(q):]
	}
	return b.String()
}

// logStatus describes the viewer for the status bar.
func (v *logView) status() string {
	var parts []string
	switch {
	case v.static:
		parts = append(parts, "report")
	case v.job != nil && v.job.running():
		parts = append(parts, "running")
	case v.job != nil && v.err != nil:
		parts = append(parts, "failed")
	case v.job != nil:
		parts = append(parts, "finished")
	case v.ended:
		parts = append(parts, "stopped")
	case v.follow:
		parts = append(parts, "following")
	default:
		parts = append(parts, "paused · G follows")
	}
	if v.wrap {
		parts = append(parts, "wrap")
	}
	if v.timestamps {
		parts = append(parts, "timestamps")
	}
	return strings.Join(parts, " · ")
}

func (v *logView) lineCount() string {
	return fmt.Sprintf("%d %s", len(v.lines), plural(len(v.lines), "line", "lines"))
}
