// Package ui is dockgit's Bubble Tea interface.
package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/dockgit/internal/cache"
	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/dock"
	"github.com/bferg314/dockgit/internal/job"
	"github.com/bferg314/dockgit/internal/link"
	"github.com/bferg314/dockgit/internal/registry"
	"github.com/bferg314/dockgit/internal/updates"
)

const (
	tabDocker = iota
	tabRepos
	tabCompose
	tabSettings
)

var tabNames = []string{"Docker", "Repos", "Compose", "Settings"}

// chromeLines is the screen height used by the header, its rule and the
// status bar around the body.
const chromeLines = 3

// App is the root model.
type App struct {
	cfg     *config.Config
	cfgPath string
	runner  dock.Runner
	st      styles

	w, h     int
	tab      int
	docker   dockerTab
	repos    reposTab
	compose  composeTab
	settings settingsTab
	detail   detailState
	logs     logCache

	// Builds dockgit ran, saved to buildsPath ("" keeps them in memory).
	builds     cache.Builds
	buildsPath string
	exec       job.Exec
	jobGen     int
	jobs       map[int]*jobState // running jobs by generation
	registry   updates.Registry  // for update checks; tests swap it
	links      *link.Index       // see index(); nil when stale

	// Overlays and full-screen views, checked in this order for keys.
	confirmDlg *confirmDialog
	addRepo    *addRepoDialog
	branches   *branchPicker
	chooser    *fileChooser
	newStack   *newStackDialog
	valueDlg   *valueDialog
	logView    *logView
	popup      *actionPopup
	logGen     int

	spin     spinner.Model
	spinning bool
	help     helpState
	toast    toast
}

type toast struct {
	id   int
	text string
	kind int // 0 info, 1 ok, 2 error
}

type toastExpireMsg struct{ id int }

// New builds the app. runner is how it talks to Docker.
func New(cfg *config.Config, cfgPath string, runner dock.Runner) *App {
	a := &App{
		cfg:      cfg,
		cfgPath:  cfgPath,
		runner:   runner,
		st:       newStyles(true),
		docker:   newDockerTab(cfg.ShowStopped, cfg.Stats),
		detail:   detailState{show: true},
		logs:     logCache{entries: map[string]*logEntry{}},
		repos:    newReposTab(),
		compose:  newComposeTab(),
		builds:   cache.Builds{},
		exec:     job.OS,
		jobs:     map[int]*jobState{},
		registry: &registry.Client{},
	}
	a.spin = spinner.New(spinner.WithSpinner(spinner.MiniDot))
	a.spin.Style = a.st.logo
	a.syncRepos()
	a.syncRoots()
	return a
}

// UseBuilds loads and keeps the build records at path.
func (a *App) UseBuilds(path string) {
	a.buildsPath = path
	a.builds = cache.LoadBuilds(path)
}

func (a *App) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor, a.reloadDocker(), a.loadRepoStatuses(), a.loadStacks()}
	if a.docker.statsOn {
		cmds = append(cmds, a.loadStats())
	}
	cmds = append(cmds, a.thresholdCleanup())
	return tea.Batch(cmds...)
}

func (a *App) busy() bool {
	t := &a.docker
	if t.loading || t.images.loading || t.volumes.loading || t.disk.loading || len(t.busy) > 0 || a.detailLoading() || a.anyChecking() ||
		a.logView != nil && len(a.logView.lines) == 0 && !a.logView.ended ||
		a.branches != nil && (a.branches.loading || a.branches.fetching) {
		return true
	}
	for _, r := range a.compose.roots {
		if !r.loaded {
			return true
		}
	}
	return a.anyJobRunning()
}

func (a *App) startSpinner() tea.Cmd {
	if a.spinning {
		return nil
	}
	a.spinning = true
	return a.spin.Tick
}

func (a *App) notify(kind int, format string, args ...any) tea.Cmd {
	a.toast.id++
	a.toast.text = fmt.Sprintf(format, args...)
	a.toast.kind = kind
	id := a.toast.id
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return toastExpireMsg{id} })
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := a.dispatch(msg)
	// Any message can move the selection (keys, filtering, containers
	// coming and going), so keep the detail pane in step afterwards.
	if logs := a.syncDetails(); logs != nil {
		cmd = tea.Batch(cmd, logs, a.startSpinner())
	}
	return a, cmd
}

func (a *App) dispatch(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		return nil

	case tea.BackgroundColorMsg:
		a.st = newStyles(msg.IsDark())
		a.spin.Style = a.st.logo
		return nil

	case spinner.TickMsg:
		if !a.busy() {
			a.spinning = false
			return nil
		}
		var cmd tea.Cmd
		a.spin, cmd = a.spin.Update(msg)
		return cmd

	case dockerInfoMsg:
		// Keep the last known context when the daemon goes away.
		if msg.info.Context != "" || msg.err == nil {
			a.docker.info = msg.info
		}
		return nil

	case containersMsg:
		t := &a.docker
		t.loading = false
		t.err = msg.err
		// On failure the list is emptied, so the error shows instead of
		// containers that may no longer be there.
		t.all = map[string]*dock.Container{}
		for _, c := range msg.cs {
			t.all[c.ID] = c
		}
		t.loaded = t.loaded || msg.err == nil
		t.refresh()
		return nil

	case containersUpdatedMsg:
		t := &a.docker
		if msg.err != nil {
			return a.notify(2, "Docker: %v", msg.err)
		}
		for _, c := range msg.cs {
			if !t.removed[c.ID] {
				t.all[c.ID] = c
			}
		}
		for _, id := range msg.gone {
			delete(t.all, id)
		}
		t.refresh()
		return nil

	case dockerEventsMsg:
		return a.handleDockerEvents(msg)

	case eventsRestartMsg:
		if msg.gen != a.docker.gen {
			return nil
		}
		return a.reloadDocker()

	case logsTickMsg:
		if msg.seq != a.logs.seq || a.logs.entries[msg.id] != nil {
			return nil
		}
		return a.loadLogs(msg.id)

	case logsMsg:
		a.logs.entries[msg.id] = &logEntry{lines: msg.lines, err: msg.err}
		return nil

	case logLinesMsg:
		return a.handleLogLines(msg)

	case actionDoneMsg:
		delete(a.docker.busy, msg.id)
		if msg.err != nil {
			return a.notify(2, "%v", msg.err)
		}
		return a.notify(1, "%s", msg.done)

	case shellMsg:
		return a.handleShell(msg)

	case toolExitMsg:
		if msg.err != nil {
			return a.notify(2, "%s exited: %v", msg.name, msg.err)
		}
		return nil

	case statsMsg:
		return a.handleStats(msg)

	case statsTickMsg:
		if msg.gen != a.docker.statsGen || !a.docker.statsOn {
			return nil
		}
		return a.loadStats()

	case repoStatusMsg:
		if r := a.repos.find(msg.key); r != nil {
			s := msg.status
			r.status = &s
			a.invalidateLinks() // remotes link images to repos
		}
		return nil

	case jobLinesMsg:
		return a.handleJobLines(msg)

	case diskMsg:
		d := &a.docker.disk
		d.loading, d.err = false, msg.err
		if msg.err == nil {
			d.loaded, d.usage = true, msg.usage
		}
		return nil

	case cleanupMsg:
		return a.handleCleanup(msg)

	case updatesMsg:
		return a.handleUpdates(msg)

	case stacksMsg:
		a.handleStacks(msg)
		return nil

	case buildRecordedMsg:
		return a.handleBuildRecorded(msg)

	case branchesMsg:
		a.handleBranches(msg)
		return nil

	case fetchedMsg:
		return a.handleFetched(msg)

	case imagesMsg:
		l := &a.docker.images
		l.loading, l.err = false, msg.err
		if msg.err == nil {
			l.loaded = true
			a.docker.imgs = msg.imgs
		}
		a.docker.refresh()
		return nil

	case volumesMsg:
		l := &a.docker.volumes
		l.loading, l.err = false, msg.err
		if msg.err == nil {
			l.loaded = true
			a.docker.vols = msg.vols
		}
		a.docker.refresh()
		return nil

	case opDoneMsg:
		reload := a.loadImages()
		switch msg.reload {
		case modeVolumes:
			reload = a.loadVolumes()
		case modeDisk:
			reload = a.loadDisk()
		}
		if msg.err != nil {
			return tea.Batch(reload, a.notify(2, "%v", msg.err))
		}
		return tea.Batch(reload, a.notify(1, "%s", msg.done))

	case toastExpireMsg:
		if msg.id == a.toast.id {
			a.toast.text = ""
		}
		return nil

	case tea.KeyPressMsg:
		return a.handleKey(msg)
	}
	// Let a focused input receive anything else (pastes, cursor blink etc).
	return a.updateFilter(msg)
}

func (a *App) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" {
		return tea.Quit
	}
	if a.help.open {
		a.helpKey(key)
		return nil
	}
	switch {
	case a.confirmDlg != nil:
		return a.confirmKey(key)
	case a.dialogOpen():
		return a.dialogKey(msg)
	case a.logView != nil:
		return a.logViewKey(msg)
	case a.popup != nil:
		return a.popupKey(key)
	}
	if f := a.activeFilter(); f != nil && f.Focused() {
		switch key {
		case "esc":
			f.SetValue("")
			f.Blur()
		case "enter", "up", "down":
			f.Blur()
		default:
			cmd := a.updateFilter(msg)
			a.refreshViews()
			return cmd
		}
		a.refreshViews()
		return nil
	}

	switch key {
	case "q":
		return tea.Quit
	case "?":
		a.help = helpState{open: true}
		return nil
	case "tab", "right":
		return a.setTab((a.tab + 1) % len(tabNames))
	case "shift+tab", "left":
		return a.setTab((a.tab + len(tabNames) - 1) % len(tabNames))
	case "1", "2", "3", "4":
		return a.setTab(int(key[0] - '1'))
	case "/":
		if f := a.activeFilter(); f != nil {
			return f.Focus()
		}
	case "esc":
		if f := a.activeFilter(); f != nil && f.Value() != "" {
			f.SetValue("")
			a.refreshViews()
			return nil
		}
		if a.detailLayout() == layoutFull {
			a.detail.full = false
			return nil
		}
	}

	switch a.tab {
	case tabDocker:
		return a.dockerKey(key)
	case tabRepos:
		return a.reposKey(key)
	case tabCompose:
		return a.composeKey(key)
	case tabSettings:
		return a.settingsKey(key)
	}
	return nil
}

// activeFilter is the filter input of the current tab, if it has one.
func (a *App) activeFilter() *textinput.Model {
	switch a.tab {
	case tabDocker:
		return &a.docker.filter
	case tabRepos:
		return &a.repos.filter
	case tabCompose:
		return &a.compose.filter
	}
	return nil
}

func (a *App) refreshViews() {
	a.docker.refresh()
	a.repos.refresh()
	a.refreshCompose()
}

func (a *App) updateFilter(msg tea.Msg) tea.Cmd {
	f := a.activeFilter()
	if f == nil || !f.Focused() {
		return nil
	}
	var cmd tea.Cmd
	*f, cmd = f.Update(msg)
	return cmd
}

// enabledTools returns the tools switched on in settings.
func (a *App) enabledTools() []config.Tool {
	var ts []config.Tool
	for _, t := range a.cfg.Tools {
		if t.Enabled {
			ts = append(ts, t)
		}
	}
	return ts
}

func (a *App) saveConfig() tea.Cmd {
	if err := a.cfg.Save(a.cfgPath); err != nil {
		return a.notify(2, "Saving config: %v", err)
	}
	return nil
}

// ---- View ----

func (a *App) View() tea.View {
	v := tea.NewView(a.render())
	v.AltScreen = true
	v.WindowTitle = "dockgit"
	return v
}

func (a *App) render() string {
	if a.w == 0 || a.h == 0 {
		return ""
	}
	header := a.renderHeader()
	rule := a.st.rule.Render(strings.Repeat("─", a.w))

	bodyH := a.h - chromeLines
	var body string
	switch {
	case a.logView != nil:
		body = a.renderLogView(bodyH)
	case a.tab == tabDocker:
		body = a.renderWithPane(a.renderDocker, bodyH)
	case a.tab == tabRepos:
		body = a.renderRepos(a.w, bodyH)
	case a.tab == tabCompose:
		body = a.renderWithPane(a.renderCompose, bodyH)
	case a.tab == tabSettings:
		body = a.renderSettings(bodyH)
	}
	body = padLines(body, bodyH, a.w)

	base := strings.Join([]string{header, rule, body, a.renderStatusBar()}, "\n")
	var overlay string
	switch {
	case a.help.open:
		overlay = a.renderHelp()
	case a.confirmDlg != nil:
		overlay = a.renderConfirm()
	case a.dialogOpen():
		overlay = a.renderDialog()
	case a.popup != nil:
		overlay = a.renderPopup()
	default:
		return base
	}
	overlay = clipBlock(overlay, a.w, a.h)
	ow, oh := lipgloss.Width(overlay), lipgloss.Height(overlay)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(overlay).X(max(0, (a.w-ow)/2)).Y(max(0, (a.h-oh)/2)).Z(1),
	).Render()
}

// clipBlock cuts a popup down to the terminal: lines are shortened, and a
// popup taller than the screen keeps its top (title and first actions)
// and its bottom border.
func clipBlock(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h && h >= 2 {
		lines = append(lines[:h-1:h-1], lines[len(lines)-1])
	}
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			lines[i] = ansi.Truncate(l, w, "")
		}
	}
	return strings.Join(lines, "\n")
}

// renderPlaceholder fills a tab that isn't built yet.
func (a *App) renderPlaceholder(what string) string {
	return "\n  " + a.st.dim.Render(what) + "\n  " + a.st.faintText.Render("Not built yet.")
}

func padLines(s string, h, w int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = fit(l, w)
	}
	return strings.Join(lines, "\n")
}

func (a *App) renderHeader() string {
	logo := a.st.logo.Render(" ◆ dockgit ")
	running, _, _ := a.docker.counts()
	stacks := 0
	for _, r := range a.compose.roots {
		stacks += len(r.stacks)
	}
	counts := []string{countLabel(running), countLabel(len(a.cfg.Repos)), countLabel(stacks), ""}
	var tabs []string
	for i, name := range tabNames {
		label := name
		if counts[i] != "" {
			label += " " + counts[i]
		}
		if i == a.tab {
			tabs = append(tabs, a.st.tabOn.Render(label))
		} else {
			tabs = append(tabs, a.st.tabOff.Render(label))
		}
	}
	left := logo + " " + strings.Join(tabs, " ")

	right := a.dockerLabel() + " "
	gap := a.w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fit(left, a.w)
	}
	return left + strings.Repeat(" ", gap) + right
}

// dockerLabel names the daemon in the header: "desktop-linux · 29.8.2".
func (a *App) dockerLabel() string {
	st, t := a.st, a.docker
	name := t.info.Context
	if name == "" {
		name = "docker"
	}
	switch {
	case t.err != nil:
		return st.bad.Render(name + " · unreachable")
	case t.info.Version != "":
		return st.dim.Render(name + " · " + t.info.Version)
	}
	return st.dim.Render(name)
}

// countLabel shows n, or nothing when there is nothing to count.
func countLabel(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}
