package ui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/dockgit/internal/compose"
	"github.com/bferg314/dockgit/internal/dock"
	"github.com/bferg314/dockgit/internal/job"
	"github.com/bferg314/dockgit/internal/link"
)

// index links containers to repos and stacks. It's rebuilt when repos or
// stacks change (invalidateLinks), not on every render.
func (a *App) index() *link.Index {
	if a.links != nil {
		return a.links
	}
	ix := &link.Index{}
	for _, r := range a.repos.rows {
		lr := link.Repo{Key: r.key, Name: r.name, Project: a.project(r)}
		if r.status != nil {
			lr.Remotes = r.status.Remotes
		}
		ix.Repos = append(ix.Repos, lr)
	}
	for _, root := range a.compose.roots {
		for _, s := range root.stacks {
			ix.Stacks = append(ix.Stacks, s.Stack)
		}
	}
	a.links = ix
	return ix
}

func (a *App) invalidateLinks() { a.links = nil }

// sourceOf is where a container came from.
func (a *App) sourceOf(c *dock.Container) link.Source { return a.index().SourceOf(c) }

// goToSource shows the repo or stack a container came from.
func (a *App) goToSource(c *dock.Container) tea.Cmd {
	src := a.sourceOf(c)
	switch src.Kind {
	case link.FromRepo:
		a.repos.filter.SetValue("")
		a.repos.refresh()
		for i, r := range a.repos.view {
			if r.key == src.Key {
				a.repos.cursor = i
			}
		}
		return a.setTab(tabRepos)
	case link.FromStack:
		a.compose.filter.SetValue("")
		a.refreshCompose()
		for i, row := range a.compose.view {
			if row.s != nil && row.s.File == src.Key {
				a.compose.cursor = i
			}
		}
		return a.setTab(tabCompose)
	case link.Gone:
		return a.notify(0, "%s was started from %s, which no longer exists", displayName(c), src.Name)
	case link.Compose:
		return a.notify(0, "%s is from compose project %s, which isn't one of your repos or stacks", displayName(c), src.Name)
	}
	return a.notify(0, "%s wasn't started by compose", displayName(c))
}

// staleBuild says why a repo's running containers may not match its code:
// the branch or commit changed since dockgit last built it.
func (a *App) staleBuild(r *repoRow) string {
	b, ok := a.builds[r.key]
	s := r.status
	if !ok || !b.OK || s == nil || s.Err != nil {
		return ""
	}
	if running, _ := a.repoContainers(r); running == 0 {
		return ""
	}
	switch {
	case b.Branch != "" && s.Branch != "" && b.Branch != s.Branch:
		return "built from " + b.Branch
	case b.Commit != "" && s.Head != "" && !strings.HasPrefix(b.Commit, s.Head) && !strings.HasPrefix(s.Head, b.Commit):
		return "new commits since build"
	}
	return ""
}

// stacksDeploying lists the stacks that run a repo's code, with whether
// they're up: "stack clock ●".
func (a *App) stacksDeploying(r *repoRow) []string {
	var out []string
	for _, s := range a.index().StacksForRepo(r.key) {
		state := "○"
		for _, c := range a.docker.all {
			if s.Owns(c.ConfigFiles()) && c.Running() {
				state = "●"
			}
		}
		out = append(out, "stack "+s.Name()+" "+state)
	}
	sort.Strings(out)
	return out
}

func (a *App) allContainers() []*dock.Container {
	cs := make([]*dock.Container, 0, len(a.docker.all))
	for _, c := range a.docker.all {
		cs = append(cs, c)
	}
	return cs
}

// preflight checks what's in the way of starting something and runs it
// straight away when nothing is. Otherwise it lists the problems and
// offers to clear the containers in the way first; run gets the steps
// that do that (nil to start as is).
func (a *App) preflight(name string, t link.Target, editEnv func() tea.Cmd, run func(pre []job.Step) tea.Cmd) tea.Cmd {
	problems := link.Preflight(t, a.allContainers())
	if len(problems) == 0 {
		return run(nil)
	}
	var body []string
	var stop, remove []*dock.Container
	seen := map[string]bool{}
	for _, p := range problems {
		body = append(body, "✗ "+p.Msg)
		for _, c := range p.Holders {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			if p.Remove {
				remove = append(remove, c)
			} else {
				stop = append(stop, c)
			}
		}
	}

	pop := &actionPopup{title: "Before starting " + name, body: body}
	if len(stop)+len(remove) > 0 {
		var steps []job.Step
		var parts []string
		if len(stop) > 0 {
			args := []string{"stop"}
			for _, c := range stop {
				args = append(args, c.ID)
			}
			steps = append(steps, job.Step{Cmd: "docker", Args: args})
			parts = append(parts, "stop "+names(stop))
		}
		if len(remove) > 0 {
			args := []string{"rm", "-f"}
			for _, c := range remove {
				args = append(args, c.ID)
			}
			steps = append(steps, job.Step{Cmd: "docker", Args: args})
			parts = append(parts, "remove "+names(remove))
		}
		label := strings.ToUpper(parts[0][:1]) + parts[0][1:]
		if len(parts) > 1 {
			label += " and " + parts[1]
		}
		pop.actions = append(pop.actions, popupAction{key: "s", label: label + ", then start",
			note: "data in volumes and folders is kept", run: func() tea.Cmd { return run(steps) }})
	}
	if editEnv != nil {
		for _, p := range problems {
			if p.Kind == link.Unset {
				pop.actions = append(pop.actions, popupAction{key: "E", label: "Edit .env first", run: editEnv})
				break
			}
		}
	}
	pop.actions = append(pop.actions,
		popupAction{key: "c", label: "Start anyway", run: func() tea.Cmd { return run(nil) }},
		popupAction{key: "n", label: "Cancel", run: func() tea.Cmd { return nil }})
	a.popup = pop
	return nil
}

func names(cs []*dock.Container) string {
	n := make([]string, len(cs))
	for i, c := range cs {
		n[i] = c.Name
	}
	return strings.Join(n, ", ")
}

// stackTarget describes a stack for preflight.
func (a *App) stackTarget(s *stackRow) link.Target {
	return link.Target{Name: s.Name(), Services: s.Services, Missing: a.stackMissing(s),
		Owns: func(c *dock.Container) bool { return s.Owns(c.ConfigFiles()) }}
}

// repoTarget describes a repo's compose project for preflight, from its
// files as they are now.
func (a *App) repoTarget(r *repoRow) link.Target {
	p := a.project(r)
	t := link.Target{Name: r.name, Owns: func(c *dock.Container) bool { return p.Owns(c.ConfigFiles(), c.WorkingDir()) }}
	services, vars, err := compose.ParseFiles(p.FilePaths())
	if err != nil {
		return t
	}
	t.Services = services
	envs := p.EnvFiles
	if len(envs) == 0 {
		envs = []string{filepath.Join(p.Dir, ".env")}
	}
	set := map[string]bool{}
	for _, f := range envs {
		env, _ := compose.ReadEnv(f)
		for k, v := range env {
			if v != "" {
				set[k] = true
			}
		}
	}
	for _, v := range vars {
		// The shell's environment counts too, for repos run by hand.
		if !v.HasDefault && !set[v.Name] && os.Getenv(v.Name) == "" {
			t.Missing = append(t.Missing, v.Name)
		}
	}
	return t
}
