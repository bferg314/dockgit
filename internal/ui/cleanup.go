package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/dock"
)

type (
	diskMsg struct {
		usage []dock.Usage
		err   error
	}
	// cleanupMsg reports an automatic cleanup: what was freed, or why it
	// didn't run.
	cleanupMsg struct {
		why           string // "after the build", "the build cache passed 20GB"
		cache, images string
		err           error
	}
)

// afterBuild cleans up as Settings says, once a build has finished.
func (a *App) afterBuild() tea.Cmd {
	switch a.cfg.Cleanup.Mode {
	case config.CleanupAfterBuild:
		return a.runCleanup("after the build")
	case config.CleanupThreshold:
		return a.thresholdCleanup()
	}
	return nil
}

// cleanupEvery is how often threshold mode checks the build cache while
// dockgit runs, besides at startup and after builds.
const cleanupEvery = time.Hour

type cleanupTickMsg struct{}

// cleanupTicker schedules the next periodic check.
func cleanupTicker() tea.Cmd {
	return tea.Tick(cleanupEvery, func(time.Time) tea.Msg { return cleanupTickMsg{} })
}

// thresholdCleanup prunes when the build cache is bigger than the
// threshold in Settings.
func (a *App) thresholdCleanup() tea.Cmd {
	c := a.cfg.Cleanup
	limit, err := dock.ParseSize(c.Threshold)
	if err != nil || c.Mode != config.CleanupThreshold {
		return nil
	}
	r := a.runner
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		us, err := dock.DiskUsage(ctx, r)
		if err != nil {
			return cleanupMsg{err: err}
		}
		for _, u := range us {
			if u.Type == "Build Cache" {
				if size, err := dock.ParseSize(u.Size); err == nil && size > limit {
					return pruneNow(r, c, "the build cache passed "+c.Threshold)
				}
			}
		}
		return nil
	}
}

func (a *App) runCleanup(why string) tea.Cmd {
	r, c := a.runner, a.cfg.Cleanup
	return func() tea.Msg { return pruneNow(r, c, why) }
}

// pruneNow prunes the build cache beyond what Settings keeps, and dangling
// images if asked to.
func pruneNow(r dock.Runner, c config.Cleanup, why string) tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	msg := cleanupMsg{why: why}
	msg.cache, msg.err = dock.PruneBuildCache(ctx, r, c.KeepStorage, c.OlderThan)
	if msg.err == nil && c.PruneDanglingImages {
		msg.images, msg.err = dock.PruneDanglingImages(ctx, r)
	}
	return msg
}

func (a *App) handleCleanup(msg cleanupMsg) tea.Cmd {
	if msg.err != nil {
		return a.notify(2, "Cleanup: %v", msg.err)
	}
	var parts []string
	if msg.cache != "" && msg.cache != "0B" {
		parts = append(parts, msg.cache+" of build cache")
	}
	if msg.images != "" && msg.images != "0B" {
		parts = append(parts, msg.images+" of images")
	}
	if len(parts) == 0 {
		return nil // nothing to free: stay quiet
	}
	cmds := []tea.Cmd{a.notify(1, "Cleaned up %s: freed %s", msg.why, strings.Join(parts, " and "))}
	if a.docker.mode == modeDisk {
		cmds = append(cmds, a.loadDisk())
	}
	return tea.Batch(cmds...)
}

// ---- disk usage view ----

func (a *App) loadDisk() tea.Cmd {
	a.docker.disk.loading = true
	r := a.runner
	return tea.Batch(a.startSpinner(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		us, err := dock.DiskUsage(ctx, r)
		return diskMsg{usage: us, err: err}
	})
}

func (a *App) diskKey(key string) tea.Cmd {
	c := a.cfg.Cleanup
	switch key {
	case "r":
		return a.loadDisk()
	case "b":
		body := []string{"Removes cache not used in " + c.OlderThan + ", keeping up to " + c.KeepStorage + " (change these in Settings)."}
		return a.confirm("Prune the build cache?", body, func() tea.Cmd {
			return a.diskOp(func(ctx context.Context, r dock.Runner) (string, error) {
				freed, err := dock.PruneBuildCache(ctx, r, c.KeepStorage, c.OlderThan)
				return "Pruned the build cache, freed " + freed, err
			})
		})
	case "B":
		return a.confirm("Prune all unused build cache?", []string{"The next build of each repo starts from scratch."}, func() tea.Cmd {
			return a.diskOp(func(ctx context.Context, r dock.Runner) (string, error) {
				freed, err := dock.PruneBuildCache(ctx, r, "", "")
				return "Pruned all build cache, freed " + freed, err
			})
		})
	case "i":
		return a.confirm("Prune dangling images?", []string{"Unnamed images left over from rebuilds, not used by any container."}, func() tea.Cmd {
			return a.diskOp(func(ctx context.Context, r dock.Runner) (string, error) {
				freed, err := dock.PruneDanglingImages(ctx, r)
				return "Pruned dangling images, freed " + freed, err
			})
		})
	case "c":
		_, stopped, _ := a.docker.counts()
		if stopped == 0 {
			return a.notify(0, "No stopped containers")
		}
		return a.confirm(fmt.Sprintf("Remove %d stopped %s?", stopped, plural(stopped, "container", "containers")),
			[]string{"Their volumes are kept."}, func() tea.Cmd {
				return a.diskOp(func(ctx context.Context, r dock.Runner) (string, error) {
					freed, err := dock.PruneContainers(ctx, r)
					return "Removed stopped containers, freed " + freed, err
				})
			})
	case "v":
		return a.setDockerMode(modeVolumes)
	}
	return nil
}

// diskOp runs a prune and reloads the usage.
func (a *App) diskOp(f func(context.Context, dock.Runner) (string, error)) tea.Cmd {
	r := a.runner
	return tea.Batch(a.startSpinner(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		done, err := f(ctx, r)
		return opDoneMsg{done: done, err: err, reload: modeDisk}
	})
}

func (a *App) renderDisk() string {
	t := &a.docker
	st := a.st
	var lines []string
	if msg := listMessage(st, a, &t.disk.listState, len(t.disk.usage), "", "Docker reported nothing."); msg != "" {
		return strings.Join(append(lines, "", "   "+msg), "\n")
	}
	const typeW, numW, sizeW, recW = 14, 8, 10, 18
	lines = append(lines, "  "+fit(st.colHead.Render("WHAT"), typeW)+"  "+fit(st.colHead.Render("COUNT"), numW)+"  "+
		fit(st.colHead.Render("IN USE"), numW)+"  "+fit(st.colHead.Render("SIZE"), sizeW)+"  "+st.colHead.Render("RECLAIMABLE"))
	names := map[string]string{"Images": "Images", "Containers": "Containers", "Local Volumes": "Volumes", "Build Cache": "Build cache"}
	for _, u := range t.disk.usage {
		name := names[u.Type]
		if name == "" {
			name = u.Type
		}
		rec := st.textS.Render(u.Reclaimable)
		if size, err := dock.ParseSize(u.Reclaimable); err == nil && size >= 1_000_000_000 {
			rec = st.warn.Render(u.Reclaimable)
		}
		lines = append(lines, "  "+fit(st.textS.Render(name), typeW)+"  "+fit(st.dim.Render(fmt.Sprint(u.Total)), numW)+"  "+
			fit(st.dim.Render(fmt.Sprint(u.Active)), numW)+"  "+fit(st.textS.Render(u.Size), sizeW)+"  "+fit(rec, recW))
	}
	c := a.cfg.Cleanup
	auto := map[string]string{
		config.CleanupOff:        "Automatic cleanup is off.",
		config.CleanupAfterBuild: "After each build, cache older than " + c.OlderThan + " beyond " + c.KeepStorage + " is pruned.",
		config.CleanupThreshold:  "When the build cache passes " + c.Threshold + ", cache older than " + c.OlderThan + " beyond " + c.KeepStorage + " is pruned.",
	}[c.Mode]
	key := func(k, what string) string {
		return "  " + st.key.Render(fmt.Sprintf("%-3s", k)) + st.textS.Render(what)
	}
	lines = append(lines, "",
		key("b", "prune the build cache (as above)"),
		key("B", "prune all unused build cache"),
		key("i", "prune dangling images"),
		key("c", "remove stopped containers"),
		key("v", "volumes, to remove unused ones"),
		"", "  "+st.dim.Render(auto+" Settings changes this."))
	return strings.Join(lines, "\n")
}
