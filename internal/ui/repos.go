package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"

	"github.com/bferg314/dockgit/internal/cache"
	"github.com/bferg314/dockgit/internal/compose"
	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/gitinfo"
	"github.com/bferg314/dockgit/internal/job"
	"github.com/bferg314/dockgit/internal/match"
)

// reposTab lists the git repos added to dockgit, with their branch, local
// changes, containers and last build.
type reposTab struct {
	rows   []*repoRow // sorted by name
	view   []*repoRow // after the filter
	cursor int
	offset int
	filter textinput.Model
}

type repoRow struct {
	key    string // the path as written in the config (with ~)
	path   string // expanded
	name   string
	status *gitinfo.Status
	job    *jobState // the latest job, running or finished
}

type (
	repoStatusMsg struct {
		key    string
		status gitinfo.Status
	}
	buildRecordedMsg struct {
		key   string
		build cache.Build
	}
)

func newReposTab() reposTab { return reposTab{filter: newFilter()} }

// syncRepos rebuilds the rows from the config, keeping what's known about
// repos that are still there.
func (a *App) syncRepos() {
	t := &a.repos
	old := map[string]*repoRow{}
	for _, r := range t.rows {
		old[r.key] = r
	}
	t.rows = t.rows[:0]
	for _, rc := range a.cfg.Repos {
		r := old[rc.Path]
		if r == nil {
			p := config.ExpandPath(rc.Path)
			r = &repoRow{key: rc.Path, path: p, name: filepath.Base(p)}
		}
		t.rows = append(t.rows, r)
	}
	sort.SliceStable(t.rows, func(i, j int) bool { return strings.ToLower(t.rows[i].name) < strings.ToLower(t.rows[j].name) })
	t.refresh()
	a.invalidateLinks()
}

func (t *reposTab) refresh() {
	var keep string
	if r := t.selected(); r != nil {
		keep = r.key
	}
	t.view = t.rows
	if q := t.filter.Value(); q != "" {
		names := make([]string, len(t.rows))
		for i, r := range t.rows {
			names[i] = r.name
		}
		t.view = nil
		for _, m := range fuzzy.Find(q, names) {
			t.view = append(t.view, t.rows[m.Index])
		}
	}
	t.cursor = min(t.cursor, max(0, len(t.view)-1))
	for i, r := range t.view {
		if r.key == keep {
			t.cursor = i
		}
	}
}

func (t *reposTab) selected() *repoRow {
	if t.cursor >= 0 && t.cursor < len(t.view) {
		return t.view[t.cursor]
	}
	return nil
}

func (t *reposTab) find(key string) *repoRow {
	for _, r := range t.rows {
		if r.key == key {
			return r
		}
	}
	return nil
}

// repoConfig is the config entry for a row.
func (a *App) repoConfig(key string) *config.Repo {
	for i := range a.cfg.Repos {
		if a.cfg.Repos[i].Path == key {
			return &a.cfg.Repos[i]
		}
	}
	return nil
}

// project is how a repo's compose project runs.
func (a *App) project(r *repoRow) compose.Project {
	p := compose.Project{Dir: r.path}
	if rc := a.repoConfig(r.key); rc != nil {
		p.Files, p.Name = rc.ComposeFiles, rc.Project
		if rc.EnvFile != "" {
			p.EnvFiles = []string{rc.EnvFile}
		}
	}
	return p
}

// hasCompose reports whether the repo has a compose file to run.
func (a *App) hasCompose(r *repoRow) bool {
	for _, f := range a.project(r).FilePaths() {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

// repoContainers counts the containers started from a repo's project.
func (a *App) repoContainers(r *repoRow) (running, total int) {
	p := a.project(r)
	for _, c := range a.docker.all {
		if p.Owns(c.ConfigFiles(), c.WorkingDir()) {
			total++
			if c.Running() {
				running++
			}
		}
	}
	return running, total
}

// ---- loading ----

func (a *App) loadRepoStatuses() tea.Cmd {
	var cmds []tea.Cmd
	for _, r := range a.repos.rows {
		cmds = append(cmds, loadRepoStatus(r.key, r.path))
	}
	return tea.Batch(cmds...)
}

func loadRepoStatus(key, path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return repoStatusMsg{key: key, status: gitinfo.Get(ctx, path)}
	}
}

// ---- jobs ----

// startJob runs steps for a repo. Afterwards its git status is reloaded
// and, for a build, the result is recorded.
func (a *App) startJob(r *repoRow, title string, build bool, steps []job.Step) tea.Cmd {
	return a.runJob(&r.job, r.name, title, steps, func(j *jobState) tea.Cmd {
		cmds := []tea.Cmd{loadRepoStatus(r.key, r.path)}
		if build {
			cmds = append(cmds, a.recordBuild(r, j.err == nil))
			if j.err == nil {
				cmds = append(cmds, a.afterBuild())
			}
		}
		return tea.Batch(cmds...)
	})
}

// recordBuild saves which branch and commit were built.
func (a *App) recordBuild(r *repoRow, ok bool) tea.Cmd {
	key, path := r.key, r.path
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b := cache.Build{Finished: time.Now(), OK: ok}
		s := gitinfo.Get(ctx, path)
		b.Branch = s.Branch
		b.Commit, _ = gitinfo.Commit(ctx, path)
		return buildRecordedMsg{key: key, build: b}
	}
}

func (a *App) handleBuildRecorded(msg buildRecordedMsg) tea.Cmd {
	a.builds[msg.key] = msg.build
	if a.buildsPath == "" {
		return nil
	}
	if err := a.builds.Save(a.buildsPath); err != nil {
		return a.notify(2, "Saving build record: %v", err)
	}
	return nil
}

// gitStep is a git command with the non-interactive environment.
func gitStep(dir string, args ...string) job.Step {
	return job.Step{Dir: dir, Cmd: "git", Args: args, Env: gitinfo.NonInteractiveEnv(context.Background(), dir)}
}

func (a *App) composeStep(r *repoRow, sub ...string) job.Step {
	return job.Step{Dir: r.path, Cmd: "docker", Args: a.project(r).Args(sub...)}
}

// build runs `compose up -d --build` after the given git steps, first
// stashing local changes when stash is set.
func (a *App) build(r *repoRow, title string, stash bool, steps ...job.Step) tea.Cmd {
	if !a.hasCompose(r) {
		return a.notify(2, "%s has no compose file: c chooses one", r.name)
	}
	return a.preflight(r.name, a.repoTarget(r), nil, func(pre []job.Step) tea.Cmd {
		all := pre
		if stash {
			all = append(all, gitStep(r.path, "stash", "push", "--include-untracked", "-m", "dockgit: before "+strings.ToLower(title)))
		}
		all = append(all, steps...)
		all = append(all, a.composeStep(r, "up", "-d", "--build"))
		return a.startJob(r, title, true, all)
	})
}

// guardDirty runs next straight away on a clean tree, and otherwise asks
// whether to stash the changes first.
func (a *App) guardDirty(r *repoRow, what string, next func(stash bool) tea.Cmd) tea.Cmd {
	if r.status == nil || !r.status.Dirty() {
		return next(false)
	}
	a.popup = &actionPopup{
		title: fmt.Sprintf("%s has %d uncommitted %s", r.name, r.status.Changes(), plural(r.status.Changes(), "change", "changes")),
		actions: []popupAction{
			{key: "s", label: "Stash them, then " + what, note: "git stash pop brings them back", run: func() tea.Cmd { return next(true) }},
			{key: "c", label: "Cancel", run: func() tea.Cmd { return nil }},
		},
	}
	return nil
}

// switchAndBuild checks out branch b (creating a tracking branch for a
// remote-only one) and rebuilds.
func (a *App) switchAndBuild(r *repoRow, b gitinfo.Branch) tea.Cmd {
	title := "Build " + b.Name
	return a.guardDirty(r, "switch to "+b.Name, func(stash bool) tea.Cmd {
		var steps []job.Step
		switch {
		case b.RemoteOnly():
			steps = append(steps, gitStep(r.path, "switch", "--track", b.Remote+"/"+b.Name))
		case r.status == nil || r.status.Branch != b.Name:
			steps = append(steps, gitStep(r.path, "switch", b.Name))
		}
		return a.build(r, title, stash, steps...)
	})
}

// pullAndBuild fast-forwards the current branch and rebuilds.
func (a *App) pullAndBuild(r *repoRow) tea.Cmd {
	if r.status != nil && r.status.Upstream == "" {
		return a.notify(2, "%s has no upstream to pull from: U builds without pulling", r.name)
	}
	return a.guardDirty(r, "pull", func(stash bool) tea.Cmd {
		return a.build(r, "Pull and build", stash, gitStep(r.path, "pull", "--ff-only"))
	})
}

func (a *App) composeDown(r *repoRow) tea.Cmd {
	running, total := a.repoContainers(r)
	if total == 0 {
		return a.notify(0, "%s has no containers to take down", r.name)
	}
	body := []string{fmt.Sprintf("Stops and removes its %d %s (%d running). Volumes are kept.", total, plural(total, "container", "containers"), running)}
	return a.confirm("Take "+r.name+" down?", body, func() tea.Cmd {
		return a.startJob(r, "Down", false, []job.Step{a.composeStep(r, "down")})
	})
}

// repoLogs follows the logs of every service in the repo's project.
func (a *App) repoLogs(r *repoRow) tea.Cmd {
	if _, total := a.repoContainers(r); total == 0 {
		return a.notify(0, "%s has no containers yet: b or U builds them", r.name)
	}
	p := a.project(r)
	a.closeLogs()
	a.logView = &logView{name: r.name, dir: r.path,
		args:   p.Args("logs", "--follow", "--timestamps", "--tail", fmt.Sprint(a.cfg.LogTail)),
		follow: true, wrap: true, search: newSearch()}
	return a.startLogStream()
}

// goToContainers shows the repo's first container on the Docker tab.
func (a *App) goToContainers(r *repoRow) tea.Cmd {
	p := a.project(r)
	t := &a.docker
	if t.mode != modeContainers {
		a.setDockerMode(t.mode)
	}
	t.filter.SetValue("")
	t.refresh()
	for i, row := range t.view {
		if row.c != nil && p.Owns(row.c.ConfigFiles(), row.c.WorkingDir()) {
			t.cursor = i
			return a.setTab(tabDocker)
		}
	}
	return a.notify(0, "%s has no containers yet", r.name)
}

func (a *App) removeRepo(r *repoRow) tea.Cmd {
	return a.confirm("Remove "+r.name+" from dockgit?", []string{"The folder and its containers are left alone."}, func() tea.Cmd {
		repos := a.cfg.Repos[:0]
		for _, rc := range a.cfg.Repos {
			if rc.Path != r.key {
				repos = append(repos, rc)
			}
		}
		a.cfg.Repos = repos
		a.syncRepos()
		return tea.Batch(a.saveConfig(), a.notify(1, "Removed %s", r.name))
	})
}

// repoPopup lists what can be done with a repo.
func (a *App) repoPopup(r *repoRow) *actionPopup {
	p := &actionPopup{title: r.name}
	add := func(key, label, note string, run func() tea.Cmd) {
		p.actions = append(p.actions, popupAction{key: key, label: label, note: note, run: run})
	}
	add("b", "Switch branch and build…", "", func() tea.Cmd { return a.openBranchPicker(r) })
	add("u", "Pull and build", "fast-forward only", func() tea.Cmd { return a.pullAndBuild(r) })
	add("U", "Build", "compose up -d --build", func() tea.Cmd { return a.build(r, "Build", false) })
	add("D", "Down", "compose down", func() tea.Cmd { return a.composeDown(r) })
	add("l", "Logs", "all services", func() tea.Cmd { return a.repoLogs(r) })
	if r.job != nil {
		note := "running"
		if r.job.done {
			note = "finished " + ago(r.job.started)
		}
		add("O", "Output of "+strings.ToLower(r.job.title), note, func() tea.Cmd { a.openJobView(r.job); return nil })
	}
	add("g", "Go to containers", "", func() tea.Cmd { return a.goToContainers(r) })
	add("w", "Open its web page", "", func() tea.Cmd { return a.repoWeb(r) })
	add("c", "Compose files…", strings.Join(a.project(r).Files, ", "), func() tea.Cmd { return a.openFileChooser(r) })
	taken := map[string]bool{}
	for _, act := range p.actions {
		taken[act.key] = true
	}
	for _, tool := range a.enabledTools() {
		key := tool.Key
		if taken[key] {
			key = ""
		}
		taken[key] = true
		add(key, "Open in "+tool.Name, "", func() tea.Cmd { return a.launch(tool, r.path) })
	}
	add("x", "Remove from dockgit", "", func() tea.Cmd { return a.removeRepo(r) })
	return p
}

// ---- keys ----

func (a *App) reposKey(key string) tea.Cmd {
	t := &a.repos
	switch key {
	case "up", "k":
		t.cursor = max(0, t.cursor-1)
		return nil
	case "down", "j":
		t.cursor = max(0, min(len(t.view)-1, t.cursor+1))
		return nil
	case "home":
		t.cursor = 0
		return nil
	case "end":
		t.cursor = max(0, len(t.view)-1)
		return nil
	case "n":
		a.openAddRepo()
		return nil
	case "r":
		return a.loadRepoStatuses()
	}
	r := t.selected()
	if r == nil {
		return nil
	}
	switch key {
	case "enter":
		a.popup = a.repoPopup(r)
	case "b":
		return a.openBranchPicker(r)
	case "u":
		return a.pullAndBuild(r)
	case "U":
		return a.build(r, "Build", false)
	case "D":
		return a.composeDown(r)
	case "l":
		return a.repoLogs(r)
	case "O":
		if r.job != nil {
			a.openJobView(r.job)
		}
	case "c":
		return a.openFileChooser(r)
	case "g":
		return a.goToContainers(r)
	case "w":
		return a.repoWeb(r)
	case "y":
		return a.copyRepo(r)
	case editorKey:
		return a.openInEditor(r.name, r.path)
	case "x":
		return a.removeRepo(r)
	}
	return nil
}

// repoWeb opens the repo's page from its first remote that looks like a
// hosted repo (GitHub, GitLab and others).
func (a *App) repoWeb(r *repoRow) tea.Cmd {
	var url string
	if r.status != nil {
		for _, remote := range r.status.Remotes {
			if url = match.WebURL(remote); url != "" {
				break
			}
		}
	}
	if url == "" {
		return a.notify(2, "%s has no remote with a web page", r.name)
	}
	if err := openURL(url); err != nil {
		return a.notify(2, "Opening %s: %v", url, err)
	}
	return a.notify(1, "Opened %s in your browser", strings.TrimPrefix(url, "https://"))
}

// ---- view ----

func (a *App) renderRepos(w, h int) string {
	t := &a.repos
	st := a.st
	var lines []string
	if t.filter.Focused() || t.filter.Value() != "" {
		lines = append(lines, " "+st.key.Render("/")+" "+t.filter.View())
		h--
	}
	if len(t.view) == 0 {
		msg := []string{st.dim.Render("No repos yet. Press n to add one: a git repo with a compose file.")}
		if t.filter.Value() != "" {
			msg = []string{st.dim.Render("Nothing matches that filter.")}
		}
		lines = append(lines, "")
		for _, m := range msg {
			lines = append(lines, "   "+m)
		}
		return strings.Join(lines, "\n")
	}

	const changesW, contW, builtW = 8, 14, 22
	nameW, branchW := len("REPO"), len("BRANCH")
	for _, r := range t.view {
		nameW = max(nameW, utf8.RuneCountInString(r.name))
		if r.status != nil {
			branchW = max(branchW, utf8.RuneCountInString(branchLabel(r.status))+4)
		}
	}
	nameW = min(nameW, 28)
	branchW = min(branchW, max(12, w-2-nameW-changesW-contW-builtW-8))

	lines = append(lines, "  "+fit(st.colHead.Render("REPO"), nameW)+"  "+fit(st.colHead.Render("BRANCH"), branchW)+"  "+
		fit(st.colHead.Render("CHANGES"), changesW)+"  "+fit(st.colHead.Render("CONTAINERS"), contW)+"  "+st.colHead.Render("BUILT"))
	h--
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+h {
		t.offset = t.cursor - h + 1
	}
	t.offset = max(0, min(t.offset, len(t.view)-h))

	for i := t.offset; i < min(len(t.view), t.offset+h); i++ {
		r := t.view[i]
		sel := i == t.cursor
		marker, name := "  ", st.textS.Render(r.name)
		if sel {
			marker, name = st.marker.Render("▌")+" ", st.selName.Render(r.name)
		}

		var branch, changes string
		switch s := r.status; {
		case s == nil:
			branch = st.faintText.Render("…")
		case s.Err != nil:
			branch, changes = st.bad.Render("not a git repo?"), ""
		default:
			branch = st.branch.Render(branchLabel(s))
			if s.Ahead > 0 {
				branch += st.ahead.Render(fmt.Sprintf(" ↑%d", s.Ahead))
			}
			if s.Behind > 0 {
				branch += st.behind.Render(fmt.Sprintf(" ↓%d", s.Behind))
			}
			changes = st.ok.Render("✓")
			if n := s.Changes(); n > 0 {
				changes = st.warn.Render(fmt.Sprintf("●%d", n))
			}
		}

		lines = append(lines, marker+fit(name, nameW)+"  "+fit(branch, branchW)+"  "+fit(changes, changesW)+"  "+
			fit(a.containersCell(r), contW)+"  "+a.builtCell(r))
	}
	return strings.Join(lines, "\n")
}

func (a *App) containersCell(r *repoRow) string {
	st := a.st
	if !a.hasCompose(r) {
		return st.dim.Render("no compose")
	}
	running, total := a.repoContainers(r)
	switch {
	case total == 0:
		return st.dim.Render("○ none")
	case running == total:
		return st.ok.Render(fmt.Sprintf("● up %d/%d", running, total))
	case running == 0:
		return st.dim.Render(fmt.Sprintf("○ down 0/%d", total))
	}
	return st.warn.Render(fmt.Sprintf("◐ %d/%d", running, total))
}

// builtCell is the BUILT column: a job in progress, a failure, or when and
// from which branch the last build ran.
func (a *App) builtCell(r *repoRow) string {
	st := a.st
	if r.job.running() {
		return a.spin.View() + " " + st.warn.Render(strings.ToLower(r.job.title)+"…")
	}
	if r.job != nil && r.job.err != nil {
		return st.bad.Render("✗ "+strings.ToLower(r.job.title)+" failed") + st.dim.Render(" · O shows why")
	}
	out := st.faintText.Render("—")
	if b, ok := a.builds[r.key]; ok {
		out = st.agoStyle(b.Finished).Render(ago(b.Finished))
		if b.Branch != "" {
			out += st.dim.Render(" · " + b.Branch)
		}
		if !b.OK {
			out = st.bad.Render("✗ ") + out
		}
	}
	if why := a.staleBuild(r); why != "" {
		out += st.warn.Render(" · stale: " + why)
	}
	if also := a.stacksDeploying(r); len(also) > 0 {
		out += st.dim.Render(" · also " + strings.Join(also, ", "))
	}
	return out
}

func branchLabel(s *gitinfo.Status) string {
	if s.Branch != "" {
		return s.Branch
	}
	if s.Head != "" {
		return "@" + s.Head
	}
	return "(empty)"
}
