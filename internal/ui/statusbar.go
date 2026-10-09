package ui

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/dockgit/internal/dock"
)

// Powerline glyphs (need a Nerd Font / Powerline font).
const (
	plRight     = "" // solid arrow pointing right
	plRightThin = ""
	plLeft      = "" // solid arrow pointing left
	plLeftThin  = ""
	plBranch    = ""
)

// part is a run of text inside a segment. Every part is rendered with the
// segment's background, since a nested style's reset would otherwise clear
// it mid-segment.
type part struct {
	text string
	fg   color.Color
	bold bool
}

type segment struct {
	parts []part
	bg    color.Color
}

func seg(bg color.Color, parts ...part) segment { return segment{parts: parts, bg: bg} }

func (s segment) empty() bool {
	for _, p := range s.parts {
		if p.text != "" {
			return false
		}
	}
	return true
}

func (s segment) width() int {
	w := 2 // padding
	for _, p := range s.parts {
		w += ansi.StringWidth(p.text)
	}
	return w
}

func (s segment) render() string {
	pad := lipgloss.NewStyle().Background(s.bg).Render(" ")
	var b strings.Builder
	b.WriteString(pad)
	for _, p := range s.parts {
		b.WriteString(lipgloss.NewStyle().Background(s.bg).Foreground(p.fg).Bold(p.bold).Render(p.text))
	}
	b.WriteString(pad)
	return b.String()
}

func sameColor(a, b color.Color) bool {
	r1, g1, b1, a1 := a.RGBA()
	r2, g2, b2, a2 := b.RGBA()
	return r1 == r2 && g1 == g2 && b1 == b2 && a1 == a2
}

// renderStatusBar draws the airline-style bar at the bottom of the screen:
// mode, the selection's state and its path on the left; activity, a summary,
// position and a help hint on the right.
func (a *App) renderStatusBar() string {
	st := a.st
	mode, modeBg := a.barMode()
	left := []segment{seg(modeBg, part{text: mode, fg: st.accentFg, bold: true})}
	left = append(left, a.barContext()...)
	path := a.barPath()

	activity, summary, position := a.barActivity(), a.barSummary(), a.barPosition()
	help := seg(modeBg, part{text: "? help", fg: st.accentFg, bold: true})
	right := func() []segment {
		var r []segment
		for _, s := range []segment{activity, summary, position, help} {
			if !s.empty() {
				r = append(r, s)
			}
		}
		return r
	}

	// Fit, giving things up in order of importance: shorten the path from
	// the left down to a minimum, drop the summary, drop the position,
	// shorten the message, and finally the path.
	const minPath = 12
	room := func() int { return a.w - a.barWidth(left, right()) - 3 } // path padding and trailing space
	if room() < minPath && !summary.empty() {
		summary = segment{}
	}
	if room() < minPath && !position.empty() {
		position = segment{}
	}
	if over := minPath - room(); over > 0 && !activity.empty() {
		activity = activity.shorten(over)
	}
	if room() < 0 && len(left) > 1 {
		left = left[:1] // very narrow: keep only the mode on the left
	}
	switch r := room(); {
	case r >= ansi.StringWidth(path):
	case r >= 2:
		path = "…" + ansi.TruncateLeft(path, ansi.StringWidth(path)-r+1, "")
	default:
		path = ""
	}
	left = append(left, seg(st.barLow, part{text: path, fg: st.text}))
	return a.joinBar(left, right())
}

// shorten trims n cells off the end of the segment's text (last part
// first), keeping at least a few characters and marking the cut with "…".
func (s segment) shorten(n int) segment {
	parts := append([]part{}, s.parts...)
	for i := len(parts) - 1; i >= 0 && n > 0; i-- {
		w := ansi.StringWidth(parts[i].text)
		newW := max(5, w-n) // ansi.Truncate counts the "…" in newW
		if newW >= w {
			continue
		}
		parts[i].text = ansi.Truncate(parts[i].text, newW, "…")
		n -= w - newW
	}
	s.parts = parts
	return s
}

// barWidth is the width of all segments and separators, excluding the
// path segment's text.
func (a *App) barWidth(left, right []segment) int {
	w := len(left) + len(right) // one separator after each left and before each right segment
	for _, s := range append(append([]segment{}, left...), right...) {
		w += s.width()
	}
	return w
}

func (a *App) joinBar(left, right []segment) string {
	st := a.st
	var b strings.Builder
	sep := func(glyph string, fg, bg color.Color) {
		if a.cfg.Powerline {
			b.WriteString(lipgloss.NewStyle().Foreground(fg).Background(bg).Render(glyph))
		} else {
			b.WriteString(lipgloss.NewStyle().Background(bg).Render(" "))
		}
	}

	for i, s := range left {
		b.WriteString(s.render())
		next := st.barLow
		if i+1 < len(left) {
			next = left[i+1].bg
		}
		switch {
		case i == len(left)-1:
			// The path segment runs into the fill; no separator needed.
			b.WriteString(lipgloss.NewStyle().Background(s.bg).Render(" "))
		case sameColor(s.bg, next):
			sep(plRightThin, st.muted, s.bg)
		default:
			sep(plRight, s.bg, next)
		}
	}

	var rb strings.Builder
	prev := st.barLow
	for _, s := range right {
		if a.cfg.Powerline {
			glyph, fg := plLeft, s.bg
			if sameColor(prev, s.bg) {
				glyph, fg = plLeftThin, st.muted
			}
			rb.WriteString(lipgloss.NewStyle().Foreground(fg).Background(prev).Render(glyph))
		} else {
			rb.WriteString(lipgloss.NewStyle().Background(prev).Render(" "))
		}
		rb.WriteString(s.render())
		prev = s.bg
	}

	fill := a.w - lipgloss.Width(b.String()) - lipgloss.Width(rb.String())
	if fill > 0 {
		b.WriteString(lipgloss.NewStyle().Background(st.barLow).Render(strings.Repeat(" ", fill)))
	}
	b.WriteString(rb.String())
	return ansi.Truncate(b.String(), a.w, "")
}

// barMode names what the keyboard is currently driving, like vim's modes.
func (a *App) barMode() (string, color.Color) {
	st := a.st
	switch {
	case a.help.open:
		return "HELP", st.accent
	case a.confirmDlg != nil:
		return "CONFIRM", st.red
	case a.popup != nil:
		return "ACTIONS", st.green
	case a.logView != nil && a.logView.search.Focused():
		return "SEARCH", st.yellow
	case a.logView != nil && a.logView.static:
		return "DOCTOR", st.blue
	case a.logView != nil && a.logView.job != nil:
		return "OUTPUT", st.blue
	case a.logView != nil:
		return "LOGS", st.blue
	case a.addRepo != nil && a.addRepo.forRoot:
		return "ADD ROOT", st.green
	case a.addRepo != nil:
		return "ADD REPO", st.green
	case a.branches != nil:
		return "BRANCH", st.green
	case a.chooser != nil:
		return "FILES", st.green
	case a.newStack != nil:
		return "NEW STACK", st.green
	case a.valueDlg != nil:
		return "EDIT", st.green
	}
	if f := a.activeFilter(); f != nil && f.Focused() {
		return "FILTER", st.yellow
	}
	if a.tab == tabDocker {
		switch a.docker.mode {
		case modeImages:
			return "IMAGES", st.blue
		case modeVolumes:
			return "VOLUMES", st.blue
		case modeDisk:
			return "DISK", st.blue
		}
	}
	switch a.tab {
	case tabRepos:
		return "REPOS", st.blue
	case tabCompose:
		return "COMPOSE", st.green
	case tabSettings:
		return "SETTINGS", st.magenta
	}
	return "DOCKER", st.accent
}

// barContext is the segment after the mode: the selected container's
// state and health.
func (a *App) barContext() []segment {
	st := a.st
	if v := a.logView; v != nil {
		return []segment{seg(st.barMid, part{text: v.status(), fg: st.text, bold: true})}
	}
	if a.tab == tabRepos {
		return a.repoBarContext()
	}
	if a.tab != tabDocker || a.docker.mode != modeContainers {
		return nil
	}
	c := a.docker.selected()
	if c == nil {
		return nil
	}
	parts := []part{{text: c.State, fg: st.text, bold: true}}
	switch c.Health {
	case "healthy":
		parts = append(parts, part{text: " healthy", fg: st.green})
	case "unhealthy":
		parts = append(parts, part{text: " unhealthy", fg: st.red})
	case "starting":
		parts = append(parts, part{text: " starting", fg: st.yellow})
	}
	if c.State == dock.StateExited && c.ExitCode != 0 {
		parts = append(parts, part{text: fmt.Sprintf(" %d", c.ExitCode), fg: st.red})
	}
	return []segment{seg(st.barMid, parts...)}
}

func (a *App) barPath() string {
	if a.logView != nil {
		return a.logView.name
	}
	switch a.tab {
	case tabDocker:
		switch a.docker.mode {
		case modeImages:
			if img := a.selectedImage(); img != nil {
				return img.ID
			}
			return ""
		case modeVolumes:
			if v := a.selectedVolume(); v != nil {
				return v.Name
			}
			return ""
		}
		c := a.docker.selected()
		switch {
		case c == nil:
			return ""
		case c.WorkingDir() != "":
			return tildePath(c.WorkingDir())
		}
		return c.Image
	case tabRepos:
		if r := a.repos.selected(); r != nil {
			return tildePath(r.path)
		}
	case tabCompose:
		if s := a.compose.selectedStack(); s != nil {
			return tildePath(s.File)
		}
		if r := a.compose.selectedRoot(); r != nil {
			return tildePath(r.path)
		}
	case tabSettings:
		return tildePath(a.cfgPath)
	}
	return ""
}

func sortedGens(jobs map[int]*jobState) []int {
	gens := make([]int, 0, len(jobs))
	for g := range jobs {
		gens = append(gens, g)
	}
	sort.Ints(gens)
	return gens
}

// repoBarContext is the selected repo's branch and state, as in folgit.
func (a *App) repoBarContext() []segment {
	st := a.st
	r := a.repos.selected()
	if r == nil || r.status == nil || r.status.Err != nil {
		return nil
	}
	s := r.status
	icon := ""
	if a.cfg.Powerline {
		icon = plBranch + " "
	}
	parts := []part{{text: icon + branchLabel(s), fg: st.text, bold: true}}
	add := func(n int, format string, fg color.Color) {
		if n > 0 {
			parts = append(parts, part{text: " " + fmt.Sprintf(format, n), fg: fg})
		}
	}
	add(s.Conflicts, "!%d", st.red)
	add(s.Staged+s.Unstaged+s.Untracked, "●%d", st.yellow)
	add(s.Ahead, "↑%d", st.blue)
	add(s.Behind, "↓%d", st.magenta)
	add(s.Stashes, "≡%d", st.muted)
	return []segment{seg(st.barMid, parts...)}
}

// barActivity shows the latest message, or what dockgit is busy with.
func (a *App) barActivity() segment {
	st := a.st
	if a.toast.text != "" {
		bg := [...]color.Color{st.accent, st.green, st.red}[a.toast.kind]
		icon := [...]string{"•", "✓", "✗"}[a.toast.kind]
		return seg(bg, part{text: icon + " " + a.toast.text, fg: st.accentFg, bold: true})
	}
	var what string
	switch t := &a.docker; {
	case t.loading:
		what = "loading containers"
	case t.images.loading && t.mode == modeImages:
		what = "loading images"
	case t.volumes.loading && t.mode == modeVolumes:
		what = "measuring volumes"
	case len(t.busy) > 0:
		what = fmt.Sprintf("%d %s", len(t.busy), plural(len(t.busy), "action", "actions"))
	default:
		var running []string
		for _, gen := range sortedGens(a.jobs) {
			j := a.jobs[gen]
			running = append(running, strings.ToLower(j.title)+" "+j.name)
		}
		what = strings.Join(running, ", ")
	}
	if what == "" {
		return segment{}
	}
	return seg(st.barMid, part{text: a.spin.View(), fg: st.accent}, part{text: " " + what, fg: st.text})
}

// barSummary totals the containers: running, stopped and unhealthy.
func (a *App) barSummary() segment {
	st := a.st
	if a.logView != nil {
		return seg(st.barLow, part{text: a.logView.lineCount(), fg: st.muted})
	}
	if a.docker.err != nil || !a.docker.loaded {
		return segment{}
	}
	running, stopped, unhealthy := a.docker.counts()
	var parts []part
	add := func(n int, format string, fg color.Color) {
		if n > 0 {
			if len(parts) > 0 {
				parts = append(parts, part{text: " ", fg: st.text})
			}
			parts = append(parts, part{text: fmt.Sprintf(format, n), fg: fg})
		}
	}
	add(unhealthy, "!%d unhealthy", st.red)
	add(running, "●%d running", st.green)
	add(stopped, "○%d stopped", st.muted)
	if len(parts) == 0 {
		return segment{}
	}
	return seg(st.barLow, parts...)
}

func (a *App) barPosition() segment {
	if a.tab == tabRepos && a.logView == nil {
		if n := len(a.repos.view); n > 0 {
			return seg(a.st.barMid, part{text: fmt.Sprintf("%d/%d", a.repos.cursor+1, n), fg: a.st.text, bold: true})
		}
		return segment{}
	}
	if a.tab != tabDocker || a.logView != nil {
		return segment{}
	}
	n, at := a.docker.containerRows()
	switch a.docker.mode {
	case modeImages:
		n, at = len(a.docker.imageView()), a.docker.images.cursor+1
	case modeVolumes:
		n, at = len(a.docker.volumeView()), a.docker.volumes.cursor+1
	}
	if n == 0 {
		return segment{}
	}
	return seg(a.st.barMid, part{text: fmt.Sprintf("%d/%d", at, n), fg: a.st.text, bold: true})
}

// tildePath shortens the home directory to ~.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		if rel == "." {
			return "~"
		}
		return "~" + string(filepath.Separator) + rel
	}
	return p
}
