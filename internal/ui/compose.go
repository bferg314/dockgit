package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"

	"github.com/bferg314/dockgit/internal/compose"
	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/doctor"
	"github.com/bferg314/dockgit/internal/gitinfo"
	"github.com/bferg314/dockgit/internal/job"
	"github.com/bferg314/dockgit/internal/updates"
)

// stackDepth is how many folders deep compose files are looked for.
const stackDepth = 5

// composeTab lists the stacks under each compose root.
type composeTab struct {
	roots  []*rootState
	view   []composeRow
	cursor int
	offset int
	filter textinput.Model
}

// rootState is one compose root: a git repo of stacks or a plain folder.
type rootState struct {
	key, path, name string
	status          *gitinfo.Status // nil for a folder that isn't a git repo
	stacks          []*stackRow
	findings        []doctor.Finding
	loaded          bool
	err             error
	job             *jobState // get latest
}

type stackRow struct {
	compose.Stack
	job *jobState // up, down, pull

	// The last update check (u).
	updates  []updates.Result
	checked  time.Time
	checking bool
}

// composeRow is a root heading (s == nil) or a stack.
type composeRow struct {
	root    *rootState
	s       *stackRow
	matches []int
}

type stacksMsg struct {
	key    string
	stacks []compose.Stack
	status *gitinfo.Status
	err    error
}

func newComposeTab() composeTab { return composeTab{filter: newFilter()} }

// syncRoots rebuilds the roots from the config, keeping what's loaded.
func (a *App) syncRoots() {
	t := &a.compose
	old := map[string]*rootState{}
	for _, r := range t.roots {
		old[r.key] = r
	}
	t.roots = t.roots[:0]
	for _, key := range a.cfg.ComposeRoots {
		r := old[key]
		if r == nil {
			p := config.ExpandPath(key)
			r = &rootState{key: key, path: p, name: filepath.Base(p)}
		}
		t.roots = append(t.roots, r)
	}
	a.refreshCompose()
	a.invalidateLinks()
}

func (a *App) loadStacks() tea.Cmd {
	var cmds []tea.Cmd
	for _, r := range a.compose.roots {
		cmds = append(cmds, loadRoot(r.key, r.path))
	}
	return tea.Batch(cmds...)
}

func loadRoot(key, path string) tea.Cmd {
	return func() tea.Msg {
		stacks, err := compose.FindStacks(path, stackDepth)
		msg := stacksMsg{key: key, stacks: stacks, err: err}
		if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			s := gitinfo.Get(ctx, path)
			msg.status = &s
		}
		return msg
	}
}

func (a *App) handleStacks(msg stacksMsg) {
	var r *rootState
	for _, x := range a.compose.roots {
		if x.key == msg.key {
			r = x
		}
	}
	if r == nil {
		return
	}
	old := map[string]*stackRow{}
	for _, s := range r.stacks {
		old[s.File] = s
	}
	r.loaded, r.err, r.status = true, msg.err, msg.status
	r.stacks = r.stacks[:0]
	for _, s := range msg.stacks {
		row := &stackRow{Stack: s}
		if o := old[s.File]; o != nil {
			row.job, row.updates, row.checked, row.checking = o.job, o.updates, o.checked, o.checking
		}
		r.stacks = append(r.stacks, row)
	}
	a.runDoctor(r)
	a.refreshCompose()
	a.invalidateLinks()
}

// runDoctor checks a root's stacks and the containers started from it.
func (a *App) runDoctor(r *rootState) {
	stacks := make([]compose.Stack, len(r.stacks))
	for i, s := range r.stacks {
		stacks[i] = s.Stack
	}
	var cs []doctor.Container
	for _, c := range a.docker.all {
		cs = append(cs, doctor.Container{Name: c.Name, ConfigFiles: c.ConfigFiles()})
	}
	r.findings = doctor.Check(r.path, stacks, cs)
}

func (a *App) refreshCompose() {
	t := &a.compose
	var keep string
	if s := t.selectedStack(); s != nil {
		keep = s.File
	}
	t.view = t.view[:0]
	q := t.filter.Value()
	for _, r := range t.roots {
		t.view = append(t.view, composeRow{root: r})
		for _, s := range r.stacks {
			row := composeRow{root: r, s: s}
			if q != "" {
				ms := fuzzy.Find(q, []string{s.Name()})
				if len(ms) == 0 {
					continue
				}
				row.matches = ms[0].MatchedIndexes
			}
			t.view = append(t.view, row)
		}
	}
	t.cursor = min(t.cursor, max(0, len(t.view)-1))
	for i, row := range t.view {
		if row.s != nil && row.s.File == keep {
			t.cursor = i
		}
	}
}

func (t *composeTab) selected() *composeRow {
	if t.cursor >= 0 && t.cursor < len(t.view) {
		return &t.view[t.cursor]
	}
	return nil
}

func (t *composeTab) selectedStack() *stackRow {
	if r := t.selected(); r != nil {
		return r.s
	}
	return nil
}

func (t *composeTab) selectedRoot() *rootState {
	if r := t.selected(); r != nil {
		return r.root
	}
	return nil
}

// stackContainers counts the containers started from a stack's file.
func (a *App) stackContainers(s *stackRow) (running, total int, changed bool) {
	for _, c := range a.docker.all {
		if !s.Owns(c.ConfigFiles()) {
			continue
		}
		total++
		if c.Running() {
			running++
			// The file was edited (or pulled) after the container was made.
			if !s.ModTime.IsZero() && c.Created.Before(s.ModTime) {
				changed = true
			}
		}
	}
	return running, total, changed
}

// findingsFor are the doctor's findings about one stack.
func (r *rootState) findingsFor(s *stackRow) []doctor.Finding {
	var out []doctor.Finding
	dir := strings.TrimSuffix(s.Rel, filepath.Base(s.File))
	for _, f := range r.findings {
		if f.Stack == s.Name() || (f.Stack != "" && f.Stack == dir) {
			out = append(out, f)
		}
	}
	return out
}

// missingVars are the variables a stack needs (no default) that no env
// file sets to a value.
func missingVars(s compose.Stack) []string {
	set := map[string]bool{}
	for _, f := range s.EnvFiles() {
		env, _ := compose.ReadEnv(f)
		for k, v := range env {
			if v != "" {
				set[k] = true
			}
		}
	}
	var out []string
	for _, v := range s.Vars {
		if !v.HasDefault && !set[v.Name] {
			out = append(out, v.Name)
		}
	}
	return out
}

// ---- actions ----

func (a *App) stackStep(s *stackRow, sub ...string) job.Step {
	p := s.Project()
	return job.Step{Dir: p.Dir, Cmd: "docker", Args: p.Args(sub...)}
}

func (a *App) stackJob(s *stackRow, title string, steps ...job.Step) tea.Cmd {
	return a.runJob(&s.job, s.Name(), title, steps, nil)
}

func (a *App) stackUp(s *stackRow) tea.Cmd {
	if s.Err != nil {
		return a.notify(2, "%s can't be read: ! shows why", s.Name())
	}
	return a.preflight(s.Name(), a.stackTarget(s), func() tea.Cmd { return a.stackEnv(s) }, func(pre []job.Step) tea.Cmd {
		return a.stackJob(s, "Up", append(pre, a.stackStep(s, "up", "-d"))...)
	})
}

func (a *App) stackPullUp(s *stackRow) tea.Cmd {
	return a.preflight(s.Name(), a.stackTarget(s), func() tea.Cmd { return a.stackEnv(s) }, func(pre []job.Step) tea.Cmd {
		return a.stackJob(s, "Pull and up", append(pre, a.stackStep(s, "pull"), a.stackStep(s, "up", "-d"))...)
	})
}

func (a *App) stackDown(s *stackRow) tea.Cmd {
	running, total, _ := a.stackContainers(s)
	if total == 0 {
		return a.notify(0, "%s has no containers to take down", s.Name())
	}
	body := []string{fmt.Sprintf("Stops and removes its %d %s (%d running). Volumes are kept.", total, plural(total, "container", "containers"), running)}
	if s.Siblings > 0 {
		body = append(body, "Containers from the other files in its folder are left alone.")
	}
	return a.confirm("Take "+s.Name()+" down?", body, func() tea.Cmd {
		return a.stackJob(s, "Down", a.stackStep(s, "down"))
	})
}

func (a *App) stackLogs(s *stackRow) tea.Cmd {
	if _, total, _ := a.stackContainers(s); total == 0 {
		return a.notify(0, "%s has no containers: U starts it", s.Name())
	}
	p := s.Project()
	a.closeLogs()
	a.logView = &logView{name: s.Name(), dir: p.Dir,
		args:   p.Args("logs", "--follow", "--timestamps", "--tail", fmt.Sprint(a.cfg.LogTail)),
		follow: true, wrap: true, search: newSearch()}
	return a.startLogStream()
}

func (a *App) stackGo(s *stackRow) tea.Cmd {
	t := &a.docker
	if t.mode != modeContainers {
		a.setDockerMode(t.mode)
	}
	t.filter.SetValue("")
	t.refresh()
	for i, row := range t.view {
		if row.c != nil && s.Owns(row.c.ConfigFiles()) {
			t.cursor = i
			return a.setTab(tabDocker)
		}
	}
	return a.notify(0, "%s has no containers", s.Name())
}

// stackEnv makes sure the stack has a .env (copied from .env.example, or
// listing the variables it needs) and opens it.
func (a *App) stackEnv(s *stackRow) tea.Cmd {
	env := filepath.Join(s.Dir, ".env")
	created, err := ensureEnv(env, filepath.Join(s.Dir, ".env.example"), missingVars(s.Stack))
	if err != nil {
		return a.notify(2, "Creating .env: %v", err)
	}
	open := a.openInEditor(s.Name(), env)
	still := missingVars(s.Stack)
	switch {
	case created && len(still) > 0:
		return tea.Batch(open, a.notify(1, "Created .env for %s · still to set: %s", s.Name(), strings.Join(still, ", ")))
	case created:
		return tea.Batch(open, a.notify(1, "Created .env for %s", s.Name()))
	case len(still) > 0:
		return tea.Batch(open, a.notify(0, "%s still needs %s", s.Name(), strings.Join(still, ", ")))
	}
	return open
}

// rootEnv does the same for the root's shared values: stacks/global.env
// when the root keeps them there for dockge, otherwise the root's .env.
func (a *App) rootEnv(r *rootState) tea.Cmd {
	env := filepath.Join(r.path, ".env")
	if _, err := os.Stat(filepath.Join(r.path, "stacks", "global.env.example")); err == nil {
		env = filepath.Join(r.path, "stacks", "global.env")
	}
	created, err := ensureEnv(env, env+".example", nil)
	if err != nil {
		return a.notify(2, "Creating .env: %v", err)
	}
	if created {
		return tea.Batch(a.openInEditor(r.name, env), a.notify(1, "Created %s for %s", filepath.Base(env), r.name))
	}
	return a.openInEditor(r.name, env)
}

// ensureEnv creates env if it's missing: a copy of example when there is
// one, otherwise a list of the needed variables to fill in.
func ensureEnv(env, example string, needed []string) (bool, error) {
	if _, err := os.Stat(env); err == nil {
		return false, nil
	}
	data, err := os.ReadFile(example)
	// A Windows checkout (core.autocrlf) gives the example CRLF endings;
	// .env gets LF, so no value ends in a stray carriage return.
	data = []byte(strings.ReplaceAll(string(data), "\r\n", "\n"))
	if err != nil {
		var b strings.Builder
		b.WriteString("# Values for this stack. Not committed: keep .env in .gitignore.\n")
		for _, v := range needed {
			b.WriteString(v + "=\n")
		}
		data = []byte(b.String())
	}
	return true, os.WriteFile(env, data, 0o600)
}

func (a *App) openStackFile(s *stackRow) tea.Cmd { return a.openInEditor(s.Name(), s.File) }

// getLatest fast-forwards a git root; changed files then show as "changed
// since up" on their running stacks.
func (a *App) getLatest(r *rootState) tea.Cmd {
	switch {
	case r.status == nil:
		return a.notify(2, "%s isn't a git repo", r.name)
	case r.status.Upstream == "":
		return a.notify(2, "%s has no upstream to pull from", r.name)
	}
	return a.runJob(&r.job, r.name, "Get latest", []job.Step{gitStep(r.path, "pull", "--ff-only")}, func(*jobState) tea.Cmd {
		return loadRoot(r.key, r.path)
	})
}

// openDoctor shows a root's findings full screen.
func (a *App) openDoctor(r *rootState) tea.Cmd {
	if !r.loaded {
		return nil
	}
	a.runDoctor(r)
	a.closeLogs()
	a.logView = &logView{name: "Doctor · " + r.name, lines: doctorReport(r), ended: true, static: true, wrap: true, search: newSearch()}
	return nil
}

// doctorReport lays the findings out by severity, with the fix under each.
func doctorReport(r *rootState) []string {
	counts := [3]int{}
	for _, f := range r.findings {
		counts[f.Severity]++
	}
	lines := []string{
		fmt.Sprintf("%s · %d %s", tildePath(r.path), len(r.stacks), plural(len(r.stacks), "stack", "stacks")),
		fmt.Sprintf("%d %s · %d %s · %d %s",
			counts[doctor.Problem], plural(counts[doctor.Problem], "problem", "problems"),
			counts[doctor.Warning], plural(counts[doctor.Warning], "warning", "warnings"),
			counts[doctor.Note], plural(counts[doctor.Note], "suggestion", "suggestions")),
	}
	if len(r.findings) == 0 {
		return append(lines, "", "✓ Everything follows the stack layout.")
	}
	titles := [...]string{"Problems", "Warnings", "Suggestions"}
	sev := doctor.Severity(-1)
	for _, f := range r.findings {
		if f.Severity != sev {
			sev = f.Severity
			lines = append(lines, "", titles[sev])
		}
		where := f.Stack
		if where == "" {
			where = "(root)"
		}
		lines = append(lines, fmt.Sprintf("  %s %s  %s", f.Severity.Icon(), where, f.Msg), "      → "+f.Fix)
	}
	return append(lines, "", "The stack layout is described in dockgit's PLAN.md (§5a). Nothing here stops a stack from running.")
}

func (a *App) removeRoot(r *rootState) tea.Cmd {
	return a.confirm("Stop listing "+r.name+"?", []string{"The folder and its stacks are left alone."}, func() tea.Cmd {
		roots := a.cfg.ComposeRoots[:0]
		for _, k := range a.cfg.ComposeRoots {
			if k != r.key {
				roots = append(roots, k)
			}
		}
		a.cfg.ComposeRoots = roots
		a.syncRoots()
		return tea.Batch(a.saveConfig(), a.notify(1, "Removed %s", r.name))
	})
}

func (a *App) stackPopup(s *stackRow) *actionPopup {
	p := &actionPopup{title: s.Name()}
	add := func(key, label, note string, run func() tea.Cmd) {
		p.actions = append(p.actions, popupAction{key: key, label: label, note: note, run: run})
	}
	add("U", "Up", "compose up -d", func() tea.Cmd { return a.stackUp(s) })
	add("R", "Pull and up", "newer images, then up", func() tea.Cmd { return a.stackPullUp(s) })
	add("D", "Down", "compose down", func() tea.Cmd { return a.stackDown(s) })
	add("l", "Logs", "all services", func() tea.Cmd { return a.stackLogs(s) })
	if s.job != nil {
		add("O", "Output of "+strings.ToLower(s.job.title), "", func() tea.Cmd { a.openJobView(s.job); return nil })
	}
	add("u", "Check for image updates", "", func() tea.Cmd { return a.checkUpdates(s, true) })
	add("E", "Edit .env", "creates it from .env.example", func() tea.Cmd { return a.stackEnv(s) })
	add("e", "Edit the compose file", filepath.Base(s.File), func() tea.Cmd { return a.openStackFile(s) })
	add("g", "Go to containers", "", func() tea.Cmd { return a.stackGo(s) })
	add(editorKey, "Open its folder", a.editorName(), func() tea.Cmd { return a.openInEditor(s.Name(), s.Dir) })
	return p
}

func (a *App) rootPopup(r *rootState) *actionPopup {
	p := &actionPopup{title: r.name}
	add := func(key, label, note string, run func() tea.Cmd) {
		p.actions = append(p.actions, popupAction{key: key, label: label, note: note, run: run})
	}
	if r.status != nil {
		add("p", "Get latest", "git pull --ff-only", func() tea.Cmd { return a.getLatest(r) })
	}
	add("!", "Doctor", "check the layout", func() tea.Cmd { return a.openDoctor(r) })
	add("u", "Check every stack for image updates", "", func() tea.Cmd { return a.checkRootUpdates(r) })
	add("n", "New stack…", "", func() tea.Cmd { a.openNewStack(r); return nil })
	add("E", "Edit the shared .env", "", func() tea.Cmd { return a.rootEnv(r) })
	add(editorKey, "Open in "+a.editorName(), "", func() tea.Cmd { return a.openInEditor(r.name, r.path) })
	if r.job != nil {
		add("O", "Output of "+strings.ToLower(r.job.title), "", func() tea.Cmd { a.openJobView(r.job); return nil })
	}
	add("X", "Stop listing it", "", func() tea.Cmd { return a.removeRoot(r) })
	return p
}

// ---- keys ----

func (a *App) composeKey(key string) tea.Cmd {
	t := &a.compose
	if a.detailScrollKey(key) {
		return nil
	}
	switch key {
	case "d":
		a.toggleDetails()
		return nil
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
	case "A":
		a.openAddRoot()
		return nil
	case "r":
		return a.loadStacks()
	}
	row := t.selected()
	if row == nil {
		return nil
	}
	r := row.root
	switch key {
	case "p":
		return a.getLatest(r)
	case "!":
		return a.openDoctor(r)
	case "n":
		a.openNewStack(r)
		return nil
	case "X":
		return a.removeRoot(r)
	}
	if row.s == nil {
		if key == "u" {
			return a.checkRootUpdates(r)
		}
		switch key {
		case "enter":
			a.popup = a.rootPopup(r)
		case "E":
			return a.rootEnv(r)
		case editorKey:
			return a.openInEditor(r.name, r.path)
		case "O":
			if r.job != nil {
				a.openJobView(r.job)
			}
		}
		return nil
	}
	s := row.s
	switch key {
	case "enter":
		a.popup = a.stackPopup(s)
	case "U":
		return a.stackUp(s)
	case "R":
		return a.stackPullUp(s)
	case "D":
		return a.stackDown(s)
	case "l":
		return a.stackLogs(s)
	case "E":
		return a.stackEnv(s)
	case "e":
		return a.openStackFile(s)
	case "g":
		return a.stackGo(s)
	case "u":
		if s.checking {
			return nil
		}
		return a.checkUpdates(s, true)
	case editorKey:
		return a.openInEditor(s.Name(), s.Dir)
	case "O":
		if s.job != nil {
			a.openJobView(s.job)
		}
	}
	return nil
}

// ---- view ----

func (a *App) renderCompose(w, h int) string {
	t := &a.compose
	st := a.st
	var lines []string
	if t.filter.Focused() || t.filter.Value() != "" {
		lines = append(lines, " "+st.key.Render("/")+" "+t.filter.View())
		h--
	}
	if len(t.roots) == 0 {
		lines = append(lines, "",
			"   "+st.dim.Render("No compose roots yet. Press A to add a folder (or git repo) of compose files."),
			"   "+st.dim.Render("Each stack is a folder with one compose.yaml; ! checks a root against that layout."))
		return strings.Join(lines, "\n")
	}

	const stateW, notesW = 14, 26
	nameW, svcW := len("STACK"), len("SERVICES")
	for _, row := range t.view {
		if row.s != nil {
			nameW = max(nameW, utf8.RuneCountInString(row.s.Name())+2)
			svcW = max(svcW, len(serviceNames(row.s)))
		}
	}
	nameW = min(nameW, max(16, w/3))
	svcW = min(svcW, max(10, w-2-nameW-stateW-notesW-6))

	lines = append(lines, "  "+fit(st.colHead.Render("STACK"), nameW)+"  "+fit(st.colHead.Render("STATE"), stateW)+"  "+
		fit(st.colHead.Render("SERVICES"), svcW)+"  "+st.colHead.Render("NOTES"))
	h--
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+h {
		t.offset = t.cursor - h + 1
	}
	t.offset = max(0, min(t.offset, len(t.view)-h))

	for i := t.offset; i < min(len(t.view), t.offset+h); i++ {
		row := t.view[i]
		sel := i == t.cursor
		marker := "  "
		if sel {
			marker = st.marker.Render("▌") + " "
		}
		if row.s == nil {
			lines = append(lines, marker+a.rootHeading(row.root, sel, w-2))
			continue
		}
		s := row.s
		name := "  " + st.highlightName(s.Name(), nameW-2, row.matches, sel)
		lines = append(lines, marker+name+"  "+fit(a.stackState(s), stateW)+"  "+
			fit(st.dim.Render(serviceNames(s)), svcW)+"  "+a.stackNotes(row.root, s))
	}
	return strings.Join(lines, "\n")
}

func (a *App) rootHeading(r *rootState, sel bool, w int) string {
	st := a.st
	name := st.boxTitle.Render(r.name)
	if sel {
		name = st.selName.Render(r.name)
	}
	s := name + "  " + st.faintText.Render(tildePath(r.path))
	switch {
	case r.err != nil:
		s += "  " + st.bad.Render(r.err.Error())
	case !r.loaded:
		s += "  " + a.spin.View()
	default:
		s += st.dim.Render(fmt.Sprintf("  %d %s", len(r.stacks), plural(len(r.stacks), "stack", "stacks")))
	}
	if g := r.status; g != nil && g.Err == nil {
		s += "  " + st.branch.Render(branchLabel(g))
		if n := g.Changes(); n > 0 {
			s += st.warn.Render(fmt.Sprintf(" ●%d", n))
		}
		if g.Ahead > 0 {
			s += st.ahead.Render(fmt.Sprintf(" ↑%d", g.Ahead))
		}
		if g.Behind > 0 {
			s += st.behind.Render(fmt.Sprintf(" ↓%d", g.Behind))
		}
	}
	if r.job.running() {
		s += "  " + a.spin.View() + " " + st.warn.Render(strings.ToLower(r.job.title)+"…")
	}
	if n := countSeverity(r.findings, doctor.Problem); n > 0 {
		s += "  " + st.bad.Render(fmt.Sprintf("✗%d", n)) + st.dim.Render(" · ! doctor")
	}
	return fit(s, w)
}

func countSeverity(fs []doctor.Finding, sev doctor.Severity) int {
	n := 0
	for _, f := range fs {
		if f.Severity == sev {
			n++
		}
	}
	return n
}

func serviceNames(s *stackRow) string {
	names := make([]string, len(s.Services))
	for i, svc := range s.Services {
		names[i] = svc.Name
	}
	return strings.Join(names, ", ")
}

// stackState is "● up 2/2", "◐ 1/2" or "○ down", against the number of
// services in the file.
func (a *App) stackState(s *stackRow) string {
	st := a.st
	if s.job.running() {
		return a.spin.View() + " " + st.warn.Render(strings.ToLower(s.job.title)+"…")
	}
	if s.Err != nil {
		return st.bad.Render("✗ unreadable")
	}
	running, _, _ := a.stackContainers(s)
	n := max(1, len(s.Services))
	switch {
	case running == 0:
		return st.dim.Render("○ down")
	case running >= n:
		return st.ok.Render(fmt.Sprintf("● up %d/%d", running, n))
	}
	return st.warn.Render(fmt.Sprintf("◐ %d/%d", running, n))
}

// stackNotes flags what needs attention: a failed job, an edited file, a
// missing .env, doctor findings.
func (a *App) stackNotes(r *rootState, s *stackRow) string {
	st := a.st
	var notes []string
	if s.job != nil && s.job.err != nil {
		notes = append(notes, st.bad.Render("✗ "+strings.ToLower(s.job.title)+" failed"))
	}
	if s.checking {
		notes = append(notes, a.spin.View()+st.dim.Render(" checking"))
	} else if n := pending(s.updates); n > 0 {
		notes = append(notes, st.ahead.Render(fmt.Sprintf("↑%d %s", n, plural(n, "update", "updates"))))
	}
	if _, _, changed := a.stackContainers(s); changed {
		notes = append(notes, st.warn.Render("changed since up"))
	}
	if missing := missingVars(s.Stack); len(missing) > 0 {
		notes = append(notes, st.warn.Render(fmt.Sprintf("%d unset", len(missing))))
	}
	fs := r.findingsFor(s)
	if n := countSeverity(fs, doctor.Problem); n > 0 {
		notes = append(notes, st.bad.Render(fmt.Sprintf("✗%d", n)))
	}
	if n := countSeverity(fs, doctor.Warning); n > 0 {
		notes = append(notes, st.warn.Render(fmt.Sprintf("▲%d", n)))
	}
	if repo := a.index().RepoForStack(s.Stack); repo != nil {
		notes = append(notes, st.dim.Render("repo "+repo.Name))
	}
	if s.Meta.Description != "" && len(notes) == 0 {
		notes = append(notes, st.dim.Render(s.Meta.Description))
	}
	return strings.Join(notes, " ")
}

// appendStackDetails fills the detail pane for a stack.
func (a *App) appendStackDetails(r *rootState, s *stackRow, add func(string), section func(string)) {
	st := a.st
	kv := func(k, v string) { add(st.dim.Render(fmt.Sprintf("%-10s", k)) + v) }

	repo := a.index().RepoForStack(s.Stack)
	if s.Meta.Description != "" || s.Meta.Repo != "" || repo != nil {
		section("ABOUT")
		if s.Meta.Description != "" {
			add(st.textS.Render(s.Meta.Description))
		}
		switch {
		case repo != nil:
			kv("repo", st.textS.Render(repo.Name)+st.dim.Render("  on the Repos tab"))
		case s.Meta.Repo != "":
			kv("repo", st.textS.Render(s.Meta.Repo)+st.dim.Render("  not on the Repos tab (n there adds it)"))
		}
	}

	section("SERVICES")
	for _, svc := range s.Services {
		img := svc.Image
		if svc.Build {
			img = "built here"
		}
		line := st.textS.Render(svc.Name) + st.dim.Render("  "+img)
		if len(svc.Ports) > 0 {
			line += st.dim.Render("  ") + st.textS.Render(strings.Join(svc.Ports, ", "))
		}
		add(line)
		if svc.NetworkMode != "" {
			add(st.dim.Render("  network_mode " + svc.NetworkMode))
		}
	}

	section("ENV FILES")
	own := filepath.Join(s.Dir, ".env")
	for _, f := range s.EnvFiles() {
		state := st.ok.Render("✓")
		if _, err := os.Stat(f); err != nil {
			// Shared files the repo doesn't use (no example either) are
			// left out; the stack's own is always shown.
			if _, err := os.Stat(f + ".example"); err != nil && f != own {
				continue
			}
			state = st.dim.Render("–")
		}
		rel, _ := filepath.Rel(r.path, f)
		add(state + " " + st.textS.Render(filepath.ToSlash(rel)))
	}
	if missing := missingVars(s.Stack); len(missing) > 0 {
		add(st.warn.Render("unset: " + strings.Join(missing, ", ")))
		add(st.dim.Render("E creates or opens .env"))
	}

	if fs := r.findingsFor(s); len(fs) > 0 {
		section("DOCTOR")
		for _, f := range fs {
			icon := st.dim.Render(f.Severity.Icon())
			switch f.Severity {
			case doctor.Problem:
				icon = st.bad.Render(f.Severity.Icon())
			case doctor.Warning:
				icon = st.warn.Render(f.Severity.Icon())
			}
			add(icon + " " + st.textS.Render(f.Msg))
			add(st.dim.Render("  → " + f.Fix))
		}
	}

	section("FILE")
	kv("path", st.textS.Render(tildePath(s.File)))
	kv("project", st.textS.Render(projectFor(s.Stack)))
	if !s.ModTime.IsZero() {
		kv("edited", st.textS.Render(ago(s.ModTime)))
	}
}

// projectFor is compose's project name for a stack: its folder's name.
func projectFor(s compose.Stack) string {
	return strings.ToLower(filepath.Base(s.Dir))
}
