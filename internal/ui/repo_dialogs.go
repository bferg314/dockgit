package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"

	"github.com/bferg314/dockgit/internal/compose"
	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/gitinfo"
	"github.com/bferg314/dockgit/internal/job"
)

// dialogOpen reports whether one of the input dialogs has the keyboard.
func (a *App) dialogOpen() bool {
	return a.addRepo != nil || a.branches != nil || a.chooser != nil || a.newStack != nil || a.valueDlg != nil
}

func (a *App) dialogKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case a.addRepo != nil:
		return a.addRepoKey(msg)
	case a.branches != nil:
		return a.branchKey(msg)
	case a.chooser != nil:
		return a.chooserKey(msg.String())
	case a.newStack != nil:
		return a.newStackKey(msg)
	case a.valueDlg != nil:
		return a.valueKey(msg)
	}
	return nil
}

func (a *App) renderDialog() string {
	switch {
	case a.addRepo != nil:
		return a.renderAddRepo()
	case a.branches != nil:
		return a.renderBranches()
	case a.chooser != nil:
		return a.renderChooser()
	case a.newStack != nil:
		return a.renderNewStack()
	case a.valueDlg != nil:
		return a.renderValue()
	}
	return ""
}

func newSearch() textinput.Model {
	s := textinput.New()
	s.Prompt = ""
	s.Placeholder = "search…"
	return s
}

// ---- add a repo ----

// addRepoDialog asks for a folder: a repo for the Repos tab, or (forRoot)
// a compose root for the Compose tab.
type addRepoDialog struct {
	input   textinput.Model
	options []string // completions shown under the input
	err     string
	forRoot bool
}

func (a *App) openAddRepo() {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "~/code/my-app"
	if home, err := os.UserHomeDir(); err == nil {
		if wd, err := os.Getwd(); err == nil && wd != home {
			in.SetValue(tildePath(wd) + string(filepath.Separator))
			in.CursorEnd()
		}
	}
	in.Focus()
	a.addRepo = &addRepoDialog{input: in}
}

func (a *App) addRepoKey(msg tea.KeyPressMsg) tea.Cmd {
	d := a.addRepo
	switch msg.String() {
	case "esc":
		a.addRepo = nil
		return nil
	case "tab":
		v, opts := completePath(d.input.Value())
		d.input.SetValue(v)
		d.input.CursorEnd()
		d.options, d.err = opts, ""
		return nil
	case "enter":
		if d.forRoot {
			return a.submitAddRoot()
		}
		return a.submitAddRepo()
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	d.options, d.err = nil, ""
	return cmd
}

// submitAddRepo adds the folder typed: its repository's root, with its
// compose file chosen, or asks which when it's not clear.
func (a *App) submitAddRepo() tea.Cmd {
	d := a.addRepo
	typed := strings.TrimSpace(d.input.Value())
	dir := config.ExpandPath(typed)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		d.err = "No such folder."
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	top, err := gitinfo.TopLevel(ctx, dir)
	if err != nil {
		d.err = "Not a git repository."
		return nil
	}
	top = filepath.Clean(filepath.FromSlash(top))
	key := tildePath(top)
	for _, rc := range a.cfg.Repos {
		if compose.NormPath(config.ExpandPath(rc.Path)) == compose.NormPath(top) {
			d.err = filepath.Base(top) + " is already in the list."
			return nil
		}
	}
	found, _ := compose.Discover(top)
	a.cfg.Repos = append(a.cfg.Repos, config.Repo{Path: key, ComposeFiles: compose.Pick(found)})
	a.addRepo = nil
	a.syncRepos()
	r := a.repos.find(key)
	for i, row := range a.repos.view {
		if row == r {
			a.repos.cursor = i
		}
	}
	cmds := []tea.Cmd{a.saveConfig(), loadRepoStatus(r.key, r.path)}
	switch rc := a.repoConfig(key); {
	case len(found) == 0:
		cmds = append(cmds, a.notify(2, "Added %s, but it has no compose file yet", r.name))
	case len(rc.ComposeFiles) == 0 || len(found) > 1:
		// Several files (or only variants): ask which to use.
		cmds = append(cmds, a.openFileChooser(r))
	default:
		cmds = append(cmds, a.notify(1, "Added %s · %s", r.name, rc.ComposeFiles[0]))
	}
	return tea.Batch(cmds...)
}

func (a *App) renderAddRepo() string {
	st := a.st
	d := a.addRepo
	w := min(70, max(30, a.w-10))
	d.input.SetWidth(w - 4)
	title, prompt := "Add a repo", "Folder of a git repo that has a compose file:"
	if d.forRoot {
		title, prompt = "Add a compose root", "Folder (or git repo) of compose files:"
	}
	lines := []string{st.boxTitle.Render(title), "",
		st.dim.Render(prompt),
		fit(st.key.Render("› ")+d.input.View(), w)}
	if len(d.options) > 0 {
		opts := d.options
		if len(opts) > 8 {
			opts = append(opts[:8:8], fmt.Sprintf("… %d more", len(d.options)-8))
		}
		lines = append(lines, fit(st.dim.Render("  "+strings.Join(opts, "  ")), w))
	}
	if d.err != "" {
		lines = append(lines, "", st.bad.Render(d.err))
	}
	lines = append(lines, "", st.dim.Render("tab completes · enter adds · esc cancels"))
	return a.boxed(st.box, lines)
}

// ---- branch picker ----

type branchPicker struct {
	key      string
	current  string
	all      []gitinfo.Branch
	view     []gitinfo.Branch
	cursor   int
	filter   textinput.Model
	loading  bool
	fetching bool
	err      error
}

type (
	branchesMsg struct {
		key      string
		branches []gitinfo.Branch
		err      error
	}
	fetchedMsg struct {
		key string
		err error
	}
)

func (a *App) openBranchPicker(r *repoRow) tea.Cmd {
	if r.job.running() {
		return a.notify(2, "%s is busy: %s", r.name, r.job.title)
	}
	f := textinput.New()
	f.Prompt = ""
	f.Placeholder = "type to filter…"
	f.Focus()
	p := &branchPicker{key: r.key, filter: f, loading: true}
	if r.status != nil {
		p.current = r.status.Branch
	}
	a.branches = p
	return tea.Batch(a.startSpinner(), loadBranches(r.key, r.path))
}

func loadBranches(key, path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		bs, err := gitinfo.Branches(ctx, path)
		return branchesMsg{key: key, branches: bs, err: err}
	}
}

// fetchBranches runs git fetch --prune so new remote branches show up.
func (a *App) fetchBranches(r *repoRow) tea.Cmd {
	a.branches.fetching = true
	exec, key, path := a.exec, r.key, r.path
	return tea.Batch(a.startSpinner(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		ch, errc := job.Run(ctx, exec, []job.Step{gitStep(path, "fetch", "--all", "--prune")})
		var last string
		for l := range ch {
			if strings.TrimSpace(l) != "" {
				last = l
			}
		}
		err := <-errc
		if err != nil && last != "" && !strings.HasPrefix(last, "$ ") {
			err = fmt.Errorf("%s", last)
		}
		return fetchedMsg{key: key, err: err}
	})
}

func (a *App) handleBranches(msg branchesMsg) {
	p := a.branches
	if p == nil || p.key != msg.key {
		return
	}
	p.loading, p.err = false, msg.err
	p.all = msg.branches
	p.refresh()
}

func (a *App) handleFetched(msg fetchedMsg) tea.Cmd {
	p := a.branches
	if p == nil || p.key != msg.key {
		return nil
	}
	p.fetching = false
	r := a.repos.find(msg.key)
	if msg.err != nil || r == nil {
		return a.notify(2, "Fetch failed: %v", msg.err)
	}
	return loadBranches(r.key, r.path)
}

func (p *branchPicker) refresh() {
	p.view = p.all
	if q := p.filter.Value(); q != "" {
		names := make([]string, len(p.all))
		for i, b := range p.all {
			names[i] = b.Name
		}
		p.view = nil
		for _, m := range fuzzy.Find(q, names) {
			p.view = append(p.view, p.all[m.Index])
		}
	}
	p.cursor = min(p.cursor, max(0, len(p.view)-1))
}

func (a *App) branchKey(msg tea.KeyPressMsg) tea.Cmd {
	p := a.branches
	r := a.repos.find(p.key)
	switch msg.String() {
	case "esc":
		a.branches = nil
		return nil
	case "up", "ctrl+p":
		p.cursor = max(0, p.cursor-1)
		return nil
	case "down", "ctrl+n":
		p.cursor = max(0, min(len(p.view)-1, p.cursor+1))
		return nil
	case "ctrl+f":
		if r != nil && !p.fetching {
			return a.fetchBranches(r)
		}
		return nil
	case "enter":
		if r == nil || p.cursor >= len(p.view) {
			return nil
		}
		b := p.view[p.cursor]
		a.branches = nil
		return a.switchAndBuild(r, b)
	}
	var cmd tea.Cmd
	p.filter, cmd = p.filter.Update(msg)
	p.refresh()
	return cmd
}

func (a *App) renderBranches() string {
	st := a.st
	p := a.branches
	r := a.repos.find(p.key)
	name := ""
	if r != nil {
		name = r.name
	}
	w := min(64, max(30, a.w-10))
	h := max(5, min(16, a.h-12))
	p.filter.SetWidth(w - 4)
	lines := []string{st.boxTitle.Render("Switch "+name+" to…") + st.dim.Render("  then build"), "",
		fit(st.key.Render("/ ")+p.filter.View(), w)}

	switch {
	case p.loading:
		lines = append(lines, "", a.spin.View()+st.dim.Render(" reading branches…"))
	case p.err != nil:
		lines = append(lines, "", st.bad.Render(p.err.Error()))
	case len(p.view) == 0:
		lines = append(lines, "", st.dim.Render("No branches match."))
	default:
		lines = append(lines, "")
		start := max(0, min(p.cursor-h/2, len(p.view)-h))
		for i := start; i < min(len(p.view), start+h); i++ {
			b := p.view[i]
			marker, label := "  ", st.textS.Render(b.Name)
			if i == p.cursor {
				marker, label = st.marker.Render("▌ "), st.selName.Render(b.Name)
			}
			note := st.dim.Render(ago(b.When))
			switch {
			case b.Name == p.current:
				note = st.ok.Render("current · rebuilds") + st.dim.Render(" · "+ago(b.When))
			case b.RemoteOnly():
				note = st.ahead.Render(b.Remote+" only") + st.dim.Render(" · "+ago(b.When))
			}
			lines = append(lines, fit(marker+label, w-lipgloss.Width(note)-1)+" "+note)
		}
	}
	hint := "enter switches and builds · ctrl+f fetches · esc cancels"
	if p.fetching {
		hint = a.spin.View() + " fetching…"
	}
	lines = append(lines, "", st.dim.Render(hint))
	return a.boxed(st.box, lines)
}

// ---- compose file chooser ----

type fileChooser struct {
	key      string
	files    []string
	selected map[string]bool
	cursor   int
}

func (a *App) openFileChooser(r *repoRow) tea.Cmd {
	found, err := compose.Discover(r.path)
	if err != nil || len(found) == 0 {
		return a.notify(2, "No compose files in %s", r.name)
	}
	sel := map[string]bool{}
	if rc := a.repoConfig(r.key); rc != nil {
		for _, f := range rc.ComposeFiles {
			sel[f] = true
		}
	}
	a.chooser = &fileChooser{key: r.key, files: found, selected: sel}
	return nil
}

func (a *App) chooserKey(key string) tea.Cmd {
	c := a.chooser
	switch key {
	case "esc":
		a.chooser = nil
	case "up", "k":
		c.cursor = max(0, c.cursor-1)
	case "down", "j":
		c.cursor = min(len(c.files)-1, c.cursor+1)
	case "space":
		f := c.files[c.cursor]
		c.selected[f] = !c.selected[f]
	case "enter":
		var files []string
		for _, f := range c.files { // keep the listed order: later files override earlier ones
			if c.selected[f] {
				files = append(files, f)
			}
		}
		if len(files) == 0 {
			files = []string{c.files[c.cursor]}
		}
		rc := a.repoConfig(c.key)
		a.chooser = nil
		if rc == nil {
			return nil
		}
		if slices.Equal(rc.ComposeFiles, files) {
			return nil
		}
		rc.ComposeFiles = files
		a.invalidateLinks()
		return tea.Batch(a.saveConfig(), a.notify(1, "%s builds with %s", filepath.Base(config.ExpandPath(rc.Path)), strings.Join(files, " + ")))
	}
	return nil
}

func (a *App) renderChooser() string {
	st := a.st
	c := a.chooser
	r := a.repos.find(c.key)
	name := ""
	if r != nil {
		name = r.name
	}
	lines := []string{st.boxTitle.Render("Compose files for " + name), "",
		st.dim.Render("Later files override earlier ones, as with -f."), ""}
	for i, f := range c.files {
		marker, label := "  ", st.textS.Render(f)
		if i == c.cursor {
			marker, label = st.marker.Render("▌ "), st.selName.Render(f)
		}
		box := st.faintText.Render("[ ]")
		if c.selected[f] {
			box = st.ok.Render("[✓]")
		}
		lines = append(lines, marker+box+" "+label)
	}
	lines = append(lines, "", st.dim.Render("space selects · enter saves (the highlighted one if none) · esc cancels"))
	return a.boxed(st.box, lines)
}
