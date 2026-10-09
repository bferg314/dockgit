package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/dockgit/internal/dock"
	"github.com/bferg314/dockgit/internal/registry"
	"github.com/bferg314/dockgit/internal/updates"
)

type updatesMsg struct {
	file    string
	results []updates.Result
	show    bool // a check of one stack: show the results
}

// checkUpdates asks the registries about a stack's images. show says
// whether to show the results when they arrive (one stack) or just mark
// the stack (a whole root).
func (a *App) checkUpdates(s *stackRow, show bool) tea.Cmd {
	s.checking = true
	reg, r, file, services := a.registry, a.runner, s.File, s.Services
	return tea.Batch(a.startSpinner(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		local := func(ctx context.Context, image string) ([]string, error) { return dock.RepoDigests(ctx, r, image) }
		return updatesMsg{file: file, results: updates.Check(ctx, reg, local, services), show: show}
	})
}

// checkRootUpdates checks every stack under a root.
func (a *App) checkRootUpdates(r *rootState) tea.Cmd {
	var cmds []tea.Cmd
	for _, s := range r.stacks {
		if s.Err == nil {
			cmds = append(cmds, a.checkUpdates(s, false))
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(append(cmds, a.notify(0, "Checking %d %s for updates…", len(cmds), plural(len(cmds), "stack", "stacks")))...)
}

func (a *App) handleUpdates(msg updatesMsg) tea.Cmd {
	s := a.findStack(msg.file)
	if s == nil {
		return nil
	}
	s.checking, s.updates, s.checked = false, msg.results, time.Now()
	if msg.show && a.popup == nil && a.logView == nil && a.confirmDlg == nil && !a.dialogOpen() {
		a.popup = a.updatesPopup(s)
		return nil
	}
	if !a.anyChecking() {
		n := 0
		for _, r := range a.compose.roots {
			for _, st := range r.stacks {
				if pending(st.updates) > 0 {
					n++
				}
			}
		}
		if n == 0 {
			return a.notify(1, "Every checked image is up to date")
		}
		return a.notify(0, "%d %s with updates: u on one shows them", n, plural(n, "stack", "stacks"))
	}
	return nil
}

func (a *App) anyChecking() bool {
	for _, r := range a.compose.roots {
		for _, s := range r.stacks {
			if s.checking {
				return true
			}
		}
	}
	return false
}

func (a *App) findStack(file string) *stackRow {
	for _, r := range a.compose.roots {
		for _, s := range r.stacks {
			if s.File == file {
				return s
			}
		}
	}
	return nil
}

// pending counts the updates there are to act on.
func pending(rs []updates.Result) int {
	n := 0
	for _, r := range rs {
		if r.State == updates.NewVersion || r.State == updates.NewImage {
			n++
		}
	}
	return n
}

// updatesPopup shows a stack's results, with bumping and pulling.
func (a *App) updatesPopup(s *stackRow) *actionPopup {
	p := &actionPopup{title: "Updates for " + s.Name()}
	bumps := map[string]string{}
	pulls := 0
	for _, r := range s.updates {
		ref := registry.Parse(r.Image)
		line := fmt.Sprintf("%-14s ", r.Service)
		switch r.State {
		case updates.UpToDate:
			line += r.Image + "  up to date"
		case updates.NewVersion:
			line += r.Image + " → " + registry.Parse(r.Suggest).Tag
			bumps[r.Image] = r.Suggest
		case updates.NewImage:
			line += r.Image + "  newer image for :" + ref.Tag
			pulls++
		case updates.NotPulled:
			if registry.Pinned(ref.Tag) {
				line += r.Image + "  up to date · not on this machine yet"
			} else {
				line += r.Image + "  not on this machine yet"
			}
		case updates.Unknown:
			line += r.Image + "  couldn't check: " + r.Err.Error()
		}
		if r.Major != "" {
			line += "  (" + r.Major + " is out: a major version, bump it by hand)"
		}
		p.body = append(p.body, line)
	}
	if len(s.updates) == 0 {
		p.body = append(p.body, "No images to check: built here, or written with variables.")
	}
	if len(bumps) > 0 {
		p.actions = append(p.actions, popupAction{key: "b",
			label: fmt.Sprintf("Bump %d %s in %s", len(bumps), plural(len(bumps), "version", "versions"), filepath.Base(s.File)),
			note:  "then R, or commit and let each host's sync apply it",
			run:   func() tea.Cmd { return a.bump(s, bumps) }})
	}
	if pulls > 0 || len(bumps) > 0 {
		p.actions = append(p.actions, popupAction{key: "R", label: "Pull and up", note: "the images in the file now",
			run: func() tea.Cmd { return a.stackPullUp(s) }})
	}
	p.actions = append(p.actions, popupAction{key: "c", label: "Close", run: func() tea.Cmd { return nil }})
	return p
}

// bump rewrites the stack's image lines to the newer versions.
func (a *App) bump(s *stackRow, changes map[string]string) tea.Cmd {
	if err := updates.Bump(s.File, changes); err != nil {
		return a.notify(2, "Bumping: %v", err)
	}
	var names []string
	for _, n := range changes {
		names = append(names, shortRef(n))
	}
	cmds := []tea.Cmd{a.notify(1, "Bumped %s in %s: R applies it here; commit and push for the hosts", strings.Join(names, ", "), s.Name())}
	for _, r := range a.compose.roots {
		if r.path == s.Root {
			cmds = append(cmds, loadRoot(r.key, r.path))
		}
	}
	s.updates = nil
	return tea.Batch(cmds...)
}

func shortRef(image string) string {
	if i := strings.LastIndex(image, "/"); i >= 0 {
		return image[i+1:]
	}
	return image
}
