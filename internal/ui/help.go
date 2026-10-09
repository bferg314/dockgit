package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// helpState is the "?" overlay. It shows only the keys for what's on
// screen, and scrolls when the terminal is too short for it.
type helpState struct {
	open      bool
	scroll    int
	maxScroll int // recorded at render time for the key handler
}

// helpKey scrolls an overflowing help screen; any other key closes it.
func (a *App) helpKey(key string) {
	h := &a.help
	switch key {
	case "j", "down":
		if h.maxScroll > 0 {
			h.scroll = min(h.maxScroll, h.scroll+1)
			return
		}
	case "k", "up":
		if h.maxScroll > 0 {
			h.scroll = max(0, h.scroll-1)
			return
		}
	}
	*h = helpState{}
}

// helpContext names the screen the help describes, for its title.
func (a *App) helpContext() string {
	switch {
	case a.logView != nil && a.logView.job != nil:
		return "Job output"
	case a.logView != nil:
		return "Logs"
	case a.tab == tabDocker && a.docker.mode == modeImages:
		return "Images"
	case a.tab == tabDocker && a.docker.mode == modeVolumes:
		return "Volumes"
	case a.tab == tabDocker && a.docker.mode == modeDisk:
		return "Disk use"
	}
	return tabNames[a.tab]
}

// editorName names the editor things open in, for the help.
func (a *App) editorName() string {
	t, _ := a.editor()
	return t.Name
}

// helpSections returns the left and right columns for the current screen.
func (a *App) helpSections(keyW, descW int) (left, right []string) {
	st := a.st
	row := func(k, d string) string { return fit(st.key.Render(k), keyW) + fit(st.textS.Render(d), descW) }
	title := func(t string) string { return fit(st.boxTitle.Render(t), keyW+descW) }

	if a.logView != nil {
		left = []string{
			title(a.helpContext()),
			row("j k", "scroll a line"),
			row("pgup/pgdn", "scroll a page (also ctrl+u/d)"),
			row("g / G", "top · newest, and follow again"),
			row("/", "search (enter: jump, esc: clear)"),
			row("n / N", "next · previous match"),
			row("w / t", "wrap long lines · timestamps"),
		}
		if a.logView.job == nil {
			left = append(left, row("r", "follow again after it stopped"))
		}
		left = append(left, row("y", "copy the lines on screen"), row("esc / q", "close"), row("?", "this help"))
		return left, nil
	}

	left = []string{
		title("Everywhere"),
		row("tab / 1-4", "switch tabs"),
		row("↑↓ / j k", "move"),
		row("/", "fuzzy filter (esc clears)"),
		row("?", "this help"),
		row("q", "quit"),
	}

	tools := func() []string {
		lines := []string{title("Tools  " + st.dim.Render("from the enter menu"))}
		ts := a.enabledTools()
		for _, t := range ts {
			lines = append(lines, row(t.Key, t.Name))
		}
		if len(ts) == 0 {
			lines = append(lines, fit(st.dim.Render("none enabled: see Settings"), keyW+descW))
		}
		return lines
	}

	switch {
	case a.tab == tabDocker && a.docker.mode == modeImages:
		right = []string{
			title("Images"),
			row("x", "remove (only when unused)"),
			row("P", "prune dangling images"),
			row("r", "reload"),
			row("i / esc", "back to containers"),
			row("v", "volumes"),
		}
	case a.tab == tabDocker && a.docker.mode == modeDisk:
		right = []string{
			title("Disk use"),
			row("b / B", "prune build cache · all of it"),
			row("i", "prune dangling images"),
			row("c", "remove stopped containers"),
			row("r", "reload"),
			row("C / esc", "back to containers"),
		}
	case a.tab == tabDocker && a.docker.mode == modeVolumes:
		right = []string{
			title("Volumes"),
			row("x", "remove (only when unused)"),
			row("r", "reload (sizes take a few seconds)"),
			row("v / esc", "back to containers"),
			row("i", "images"),
		}
	case a.tab == tabDocker:
		left = append(left, "",
			title("Status"),
			fit(st.ok.Render("●")+" "+st.textS.Render("running   ")+st.warn.Render("●")+" "+st.textS.Render("health starting"), keyW+descW),
			fit(st.bad.Render("●")+" "+st.textS.Render("unhealthy ")+st.dim.Render("○")+" "+st.textS.Render("stopped ")+st.bad.Render("○")+" "+st.textS.Render("failed"), keyW+descW),
			fit(st.warn.Render("‖")+" "+st.textS.Render("paused    ")+st.warn.Render("↻")+" "+st.textS.Render("restarting"), keyW+descW),
		)
		right = append([]string{
			title("Containers"),
			row("enter", "actions…"),
			row("l", "logs, full screen"),
			row("s / S", "start or stop · restart"),
			row("x", "remove"),
			row("e", "shell in the container"),
			row("w", "open the first port in a browser"),
			row("g", "go to its repo or stack"),
			row("y", "copy its ID, name, URL or folder"),
			row(editorKey, "open its project in "+a.editorName()),
			row("a / t", "stopped containers · stats column"),
			row("d", "details: beside · full screen · hidden"),
			row("J / K", "scroll details (ctrl+d/u: half page)"),
			row("m", "show / hide env values"),
			row("i / v / C", "images · volumes · disk use, cleanup"),
			row("r", "reload from Docker"),
			"",
		}, tools()...)
	case a.tab == tabRepos:
		right = append([]string{
			title("Repos"),
			row("enter", "actions…"),
			row("n / x", "add a repo · remove it from the list"),
			row("b", "switch branch, then build"),
			row("u / U", "pull and build · build"),
			row("D", "compose down"),
			row("l", "logs of all its services"),
			row("O", "output of the last job"),
			row("c", "compose files"),
			row("g", "go to its containers"),
			row("y", "copy its path, web page or branch"),
			row(editorKey+" / w", "open in "+a.editorName()+" · its web page"),
			row("r", "refresh git status"),
			"",
		}, tools()...)
	case a.tab == tabCompose:
		right = []string{
			title("Stacks"),
			row("enter", "actions…"),
			row("U / R", "up · pull newer images, then up"),
			row("D", "down"),
			row("l / O", "logs · output of the last job"),
			row("E / e", ".env (created from .env.example) · compose file"),
			row("g", "go to its containers"),
			row("u", "check for image updates (b bumps them)"),
			row("y", "copy its file, folder or up command"),
			row(editorKey, "open its folder in "+a.editorName()),
			row("d", "details: beside · full screen · hidden"),
			row("J / K", "scroll details (j/k when full screen)"),
			"",
			title("Compose roots"),
			row("p", "get latest (git pull --ff-only)"),
			row("!", "doctor: check the stack layout"),
			row("u", "check every stack for updates"),
			row("n", "new stack"),
			row("A / X", "add a root · stop listing it"),
			row("r", "rescan"),
		}
	case a.tab == tabSettings:
		right = []string{
			title("Settings"),
			row("space", "toggle"),
			row("enter", "change a value"),
		}
	}
	return left, right
}

// renderHelp is the key reference for the current screen: the status bar
// keeps the screen clean, so the keys live here behind "?".
func (a *App) renderHelp() string {
	st := a.st
	const keyW, descW = 11, 36
	left, right := a.helpSections(keyW, descW)

	var lines []string
	if right != nil && a.w >= 2*(keyW+descW)+12 {
		for len(right) < len(left) {
			right = append(right, "")
		}
		for len(left) < len(right) {
			left = append(left, "")
		}
		lines = strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(left, "\n"), "    ", strings.Join(right, "\n")), "\n")
	} else {
		lines = left
		if right != nil {
			lines = append(append(lines, ""), right...)
		}
	}

	footer := []string{"",
		st.dim.Render("Keys for " + a.helpContext() + "; each tab has its own. Any key closes."),
		st.dim.Render("Tools and settings: ") + st.textS.Render(tildePath(a.cfgPath)),
	}

	// Scroll the keys (not the footer) when the box wouldn't fit.
	const chrome = 2 // the box's top and bottom border
	room := max(3, a.h-chrome-len(footer))
	h := &a.help
	h.maxScroll = max(0, len(lines)-room)
	h.scroll = min(h.scroll, h.maxScroll)
	if h.maxScroll > 0 {
		lines = lines[h.scroll : h.scroll+room]
		ind := st.dim.Render(fmt.Sprintf("j/k ↕ %d%%", h.scroll*100/h.maxScroll))
		footer[1] = ind + "  " + footer[1]
	}
	all := append(lines, footer...)
	for i, l := range all {
		all[i] = ansi.Truncate(l, max(20, a.w-6), "…") // box border and padding
	}
	return a.boxed(st.box.Padding(0, 2), all)
}
