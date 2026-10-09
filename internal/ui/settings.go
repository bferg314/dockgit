package ui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/launcher"
)

type settingsTab struct {
	cursor int
}

type settingItem struct {
	section string
	label   string
	detail  string
	on      *bool
	warn    string
	tool    *config.Tool // set for tool rows

	// Action rows have no checkbox (on == nil): enter/space runs set.
	set func() tea.Cmd
}

func (a *App) settingItems() []settingItem {
	zellij := settingItem{section: "General", label: "Zellij tabs",
		detail: "inside zellij, terminal tools open in a new tab", on: &a.cfg.ZellijTabs}
	if !launcher.InZellij() {
		zellij.detail += " · not in zellij now"
	}
	items := []settingItem{
		a.composeRootsItem(),
		zellij,
		{section: "General", label: "Confirm destructive actions", detail: "down, remove and prune ask first", on: &a.cfg.ConfirmDestructive},
		{section: "Docker", label: "Show stopped containers", on: &a.cfg.ShowStopped},
		{section: "Docker", label: "Stats column", detail: "CPU and memory; polls docker stats", on: &a.cfg.Stats},
		{section: "Docker", label: "Mask env values", detail: "in container and stack details", on: &a.cfg.MaskEnv},
	}
	items = append(items, a.cleanupItem())
	items = append(items, a.cleanupValues()...)
	for i := range a.cfg.Tools {
		t := &a.cfg.Tools[i]
		it := settingItem{
			section: "Tools",
			label:   t.Name,
			detail:  "[" + t.Key + "]  " + strings.TrimSpace(t.Cmd+" "+strings.Join(t.Args, " ")) + "  · " + t.Mode,
			on:      &t.Enabled,
			tool:    t,
		}
		if !config.Available(t.Cmd) {
			it.warn = "not installed"
		}
		items = append(items, it)
	}
	items = append(items,
		settingItem{section: "Appearance", label: "Powerline status bar", detail: "arrow separators; needs a Nerd Font", on: &a.cfg.Powerline},
	)
	return items
}

// composeRootsItem lists the compose roots. Adding and removing them
// happens on the Compose tab.
func (a *App) composeRootsItem() settingItem {
	it := settingItem{section: "General", label: "Compose roots"}
	if len(a.cfg.ComposeRoots) == 0 {
		it.detail = "none yet: add one on the Compose tab, or in the config file"
		return it
	}
	it.detail = strings.Join(a.cfg.ComposeRoots, ", ")
	return it
}

// cleanupItem cycles the build cache cleanup mode.
func (a *App) cleanupItem() settingItem {
	c := &a.cfg.Cleanup
	it := settingItem{section: "Cleanup", label: "Automatic cleanup"}
	switch c.Mode {
	case config.CleanupOff:
		it.detail = "off"
	case config.CleanupAfterBuild:
		it.detail = "after each build · keeps " + c.KeepStorage + ", prunes cache older than " + c.OlderThan
	case config.CleanupThreshold:
		it.detail = "when build cache is over " + c.Threshold + " · keeps " + c.KeepStorage
	}
	it.detail += " · enter: change"
	it.set = func() tea.Cmd {
		i := slices.Index(config.CleanupModes, c.Mode)
		c.Mode = config.CleanupModes[(i+1)%len(config.CleanupModes)]
		return a.saveConfig()
	}
	return it
}

// setTab switches tabs. Opening Repos refreshes their git state, and
// opening Settings re-checks for tools installed while dockgit was running.
func (a *App) setTab(tab int) tea.Cmd {
	a.tab = tab
	switch tab {
	case tabRepos:
		return a.loadRepoStatuses()
	case tabCompose:
		return a.loadStacks()
	}
	if tab != tabSettings {
		return nil
	}
	names := a.cfg.EnableInstalled()
	if len(names) == 0 {
		return nil
	}
	return tea.Batch(a.saveConfig(), a.notify(1, "Found and enabled %s", strings.Join(names, ", ")))
}

func (a *App) settingsKey(key string) tea.Cmd {
	items := a.settingItems()
	t := &a.settings
	switch key {
	case "up", "k":
		t.cursor = max(0, t.cursor-1)
	case "down", "j":
		t.cursor = min(len(items)-1, t.cursor+1)
	case "space", "enter":
		it := items[t.cursor]
		if it.on == nil {
			if it.set != nil {
				return it.set()
			}
			return nil
		}
		*it.on = !*it.on
		if it.tool != nil {
			it.tool.AutoDisabled = false // a hand-made choice sticks
		}
		return a.saveConfig()
	}
	return nil
}

func (a *App) renderSettings(h int) string {
	st := a.st
	items := a.settingItems()
	var lines []string
	cursorLine := 0
	section := ""
	for i, it := range items {
		if it.section != section {
			section = it.section
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, "  "+st.boxTitle.Render(section))
		}
		marker := "  "
		label := st.textS.Render(it.label)
		if i == a.settings.cursor {
			cursorLine = len(lines)
			marker = st.marker.Render("▌") + " "
			label = st.selName.Render(it.label)
		}
		var box string
		switch {
		case it.on == nil && it.set == nil:
			box = "   "
		case it.on == nil:
			box = st.key.Render(" ▸ ")
		case *it.on:
			box = st.ok.Render("[✓]")
		default:
			box = st.faintText.Render("[ ]")
		}
		line := marker + "  " + box + " " + fit(label, 30) + st.dim.Render(it.detail)
		if it.warn != "" {
			line += "  " + st.warn.Render(it.warn)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "",
		"    "+st.dim.Render("Add tools, change keys, cleanup sizes and log length in the config file below."))

	// Scroll so the cursor (and its section heading) stay visible.
	if len(lines) > h {
		start := max(0, min(cursorLine-h/2, len(lines)-h))
		lines = lines[start : start+h]
	}
	return strings.Join(lines, "\n")
}
