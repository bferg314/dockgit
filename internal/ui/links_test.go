package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/dockgit/internal/cache"
	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/dock"
	"github.com/bferg314/dockgit/internal/gitinfo"
)

// linkedApp has a repo (clock) and a compose root whose clock stack
// deploys it, plus a downloads stack publishing 8080.
func linkedApp(t *testing.T) (*App, string, string, *stepRecorder) {
	t.Helper()
	repo := gitRepo(t, "docker-compose.yml")
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"stacks/global.env.example":     "TZ=UTC\n",
		"stacks/clock/compose.yaml":     "services:\n  clock:\n    image: ghcr.io/me/clock:1.4.0\n    ports: [\"8090:80\"]\nx-dockgit:\n  repo: " + filepath.ToSlash(repo) + "\n",
		"stacks/downloads/compose.yaml": "services:\n  gluetun:\n    image: qmcgaw/gluetun:v3.41.3\n    ports: [\"8080:8080\"]\n",
	})
	cfg := config.Default()
	cfg.Repos = []config.Repo{{Path: repo, ComposeFiles: []string{"docker-compose.yml"}}}
	cfg.ComposeRoots = []string{root}
	a := testApp(t, 160, cfg)
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	rec := &stepRecorder{}
	a.exec = rec.exec
	a.UseBuilds(filepath.Join(t.TempDir(), "builds.json"))
	a.Update(repoStatusMsg{key: repo, status: gitinfo.Get(context.Background(), repo)})
	a.Update(loadRoot(root, root)())
	return a, repo, root, rec
}

func stackFile(root, name string) string {
	return filepath.Join(root, "stacks", name, "compose.yaml")
}

func labelled(name, image, file, state string, ports ...dock.Port) *dock.Container {
	return &dock.Container{ID: name, Name: name, Image: image, State: state, Ports: ports, Labels: map[string]string{
		dock.LabelProject: filepath.Base(filepath.Dir(file)), dock.LabelConfigFiles: file, dock.LabelWorkingDir: filepath.Dir(file),
		dock.LabelService: strings.Split(name, "-")[1],
	}}
}

func TestDockerHeadingShowsSourceAndGJumps(t *testing.T) {
	a, repo, root, _ := linkedApp(t)
	a.Update(containersMsg{cs: []*dock.Container{
		labelled("clock-clock-1", "ghcr.io/me/clock:1.4.0", stackFile(root, "clock"), dock.StateRunning),
		labelled("repo-clock-1", "clock-clock", filepath.Join(repo, "docker-compose.yml"), dock.StateRunning),
	}})
	s := screen(a)
	if !strings.Contains(s, "stack stacks/clock") || !strings.Contains(s, "repo "+filepath.Base(repo)) {
		t.Fatalf("headings should name their source:\n%s", s)
	}
	// Select the stack's container and press g.
	for i, r := range a.docker.view {
		if r.c != nil && r.c.Name == "clock-clock-1" {
			a.docker.cursor = i
		}
	}
	press(a, 'g')
	if a.tab != tabCompose || a.compose.selectedStack() == nil || a.compose.selectedStack().Name() != "stacks/clock" {
		t.Fatalf("g should open the stack: tab=%d", a.tab)
	}
	// And the repo's.
	press(a, '1')
	for i, r := range a.docker.view {
		if r.c != nil && r.c.Name == "repo-clock-1" {
			a.docker.cursor = i
		}
	}
	press(a, 'g')
	if a.tab != tabRepos || a.repos.selected().path != repo {
		t.Fatalf("g should open the repo: tab=%d", a.tab)
	}
}

// M5's acceptance check: a held port says who holds it and offers to stop
// it first.
func TestUpOffersToStopWhatHoldsThePort(t *testing.T) {
	a, repo, root, rec := linkedApp(t)
	holder := labelled("repo-clock-1", "clock-clock", filepath.Join(repo, "docker-compose.yml"), dock.StateRunning,
		dock.Port{Container: "80/tcp", HostIP: "0.0.0.0", HostPort: "8080"})
	a.Update(containersMsg{cs: []*dock.Container{holder}})
	press(a, '3')
	selectStack(t, a, "stacks/downloads")
	press(a, 'U')
	if a.popup == nil {
		t.Fatalf("U should stop to show the problem:\n%s", screen(a))
	}
	s := screen(a)
	for _, want := range []string{"Before starting stacks/downloads", "port 8080 (gluetun) is held by repo-clock-1", "Stop repo-clock-1, then start", "Start anyway"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	_, cmd := a.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	p := newPump(a)
	defer p.stop()
	p.run(cmd)
	st := a.compose.selectedStack()
	p.until(t, 5*time.Second, "job", func() bool { return st.job != nil && st.job.done })
	got := rec.all()
	if len(got) != 2 || got[0] != "docker stop repo-clock-1" || !strings.HasSuffix(got[1], "up -d") {
		t.Fatalf("steps: %q", got)
	}
	_ = root
}

func TestNothingInTheWayStartsStraightAway(t *testing.T) {
	a, _, _, rec := linkedApp(t)
	press(a, '3')
	selectStack(t, a, "stacks/downloads")
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'U', Text: "U"})
	if a.popup != nil {
		t.Fatalf("no popup expected:\n%s", screen(a))
	}
	p := newPump(a)
	defer p.stop()
	p.run(cmd)
	st := a.compose.selectedStack()
	p.until(t, 5*time.Second, "job", func() bool { return st.job != nil && st.job.done })
	if got := rec.all(); len(got) != 1 {
		t.Fatalf("steps: %q", got)
	}
}

func TestOldDeploymentIsOfferedForRemoval(t *testing.T) {
	a, _, root, _ := linkedApp(t)
	old := labelled("arr-gluetun-1", "qmcgaw/gluetun", filepath.Join(t.TempDir(), "arr", "gluetun.yml"), dock.StateRunning)
	a.Update(containersMsg{cs: []*dock.Container{old}})
	s := screen(a)
	if !strings.Contains(s, "gone ") {
		t.Errorf("a container from a missing file should say so:\n%s", s)
	}
	press(a, '3')
	selectStack(t, a, "stacks/downloads")
	press(a, 'U')
	s = screen(a)
	if !strings.Contains(s, "arr-gluetun-1 already runs gluetun:v3.41.3") || !strings.Contains(s, "Remove arr-gluetun-1, then start") {
		t.Fatalf("old deployment:\n%s", s)
	}
	_ = root
}

func TestStaleBuildAndLinkedStack(t *testing.T) {
	a, repo, root, _ := linkedApp(t)
	a.Update(containersMsg{cs: []*dock.Container{
		labelled("repo-clock-1", "clock-clock", filepath.Join(repo, "docker-compose.yml"), dock.StateRunning),
		labelled("clock-clock-1", "ghcr.io/me/clock:1.4.0", stackFile(root, "clock"), dock.StateRunning),
	}})
	a.builds[repo] = cache.Build{Branch: "feature", Commit: "abc1234", Finished: time.Now(), OK: true}
	press(a, '2')
	s := screen(a)
	if !strings.Contains(s, "stale: built from feature") || !strings.Contains(s, "also stack stacks/clock ●") {
		t.Fatalf("repos row:\n%s", s)
	}
	// Same branch, other commit.
	a.builds[repo] = cache.Build{Branch: "main", Commit: "abc1234", Finished: time.Now(), OK: true}
	if !strings.Contains(screen(a), "stale: new commits since build") {
		t.Fatalf("commit moved:\n%s", screen(a))
	}
	// Up to date: no badge.
	a.builds[repo] = cache.Build{Branch: "main", Commit: a.repos.selected().status.Head, Finished: time.Now(), OK: true}
	if strings.Contains(screen(a), "stale") {
		t.Fatalf("not stale:\n%s", screen(a))
	}
	// The stack shows its repo.
	press(a, '3')
	selectStack(t, a, "stacks/clock")
	if s := screen(a); !strings.Contains(s, "repo "+filepath.Base(repo)) || !strings.Contains(s, "on the Repos tab") {
		t.Fatalf("stack row / detail:\n%s", s)
	}
}
