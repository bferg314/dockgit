package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/dockgit/internal/cache"
	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/dock"
	"github.com/bferg314/dockgit/internal/gitinfo"
	"github.com/bferg314/dockgit/internal/job"
)

// gitRepo makes a repo on main with a second branch, feature, and the
// given files committed.
func gitRepo(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("services: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "first")
	run("branch", "feature")
	return dir
}

// stepRecorder is a job.Exec that records steps and fails the ones whose
// command line contains fail.
type stepRecorder struct {
	mu    sync.Mutex
	steps []string
	fail  string
}

func (r *stepRecorder) exec(_ context.Context, s job.Step) (io.ReadCloser, error) {
	line := strings.TrimPrefix(s.String(), "$ ")
	r.mu.Lock()
	r.steps = append(r.steps, line)
	r.mu.Unlock()
	pr, pw := io.Pipe()
	go func() {
		io.WriteString(pw, "output of "+s.Cmd+"\n")
		if r.fail != "" && strings.Contains(line, r.fail) {
			pw.CloseWithError(errors.New("exit status 1"))
			return
		}
		pw.Close()
	}()
	return pr, nil
}

func (r *stepRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.steps...)
}

// reposApp has one repo, dir, on the Repos tab with its status loaded.
func reposApp(t *testing.T, dir string, files ...string) (*App, *stepRecorder) {
	t.Helper()
	cfg := config.Default()
	cfg.Repos = []config.Repo{{Path: dir, ComposeFiles: files}}
	a := testApp(t, 140, cfg)
	rec := &stepRecorder{}
	a.exec = rec.exec
	a.UseBuilds(filepath.Join(t.TempDir(), "builds.json"))
	a.syncRepos()
	a.Update(repoStatusMsg{key: dir, status: gitinfo.Get(context.Background(), dir)})
	press(a, '2')
	return a, rec
}

// finishJob runs the selected repo's job to the end through Update, as
// the runtime would, including recording the build.
func finishJob(t *testing.T, a *App, start tea.Cmd) {
	t.Helper()
	r := a.repos.selected()
	p := newPump(a)
	defer p.stop()
	p.run(start)
	p.until(t, 10*time.Second, "job finished", func() bool {
		if !r.job.done {
			return false
		}
		_, recorded := a.builds[r.key]
		return !strings.Contains(strings.ToLower(r.job.title), "build") || recorded
	})
}

func TestAddRepoChoosesComposeFiles(t *testing.T) {
	dir := gitRepo(t, "docker-compose.yml", "docker-compose.build.yml")
	a := testApp(t, 140, config.Default())
	a.UseBuilds("")
	press(a, '2')
	if !strings.Contains(screen(a), "No repos yet") {
		t.Fatalf("empty tab:\n%s", screen(a))
	}
	press(a, 'n')
	if a.addRepo == nil {
		t.Fatal("n should open the add dialog")
	}
	a.addRepo.input.SetValue(filepath.Join(dir, "sub", ".."))
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.addRepo != nil || len(a.cfg.Repos) != 1 {
		t.Fatalf("enter should add: dlg=%v repos=%v", a.addRepo, a.cfg.Repos)
	}
	// Two compose files: the chooser opens with compose's default picked.
	c := a.chooser
	if c == nil || len(c.files) != 2 || !c.selected["docker-compose.yml"] {
		t.Fatalf("chooser: %+v", c)
	}
	press(a, 'j')
	a.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := a.cfg.Repos[0].ComposeFiles; strings.Join(got, ",") != "docker-compose.yml,docker-compose.build.yml" {
		t.Fatalf("files: %q", got)
	}
	data, _ := os.ReadFile(a.cfgPath)
	if !strings.Contains(string(data), "docker-compose.build.yml") {
		t.Fatalf("not saved:\n%s", data)
	}

	// Adding it again is refused.
	press(a, 'n')
	a.addRepo.input.SetValue(dir)
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.addRepo == nil || !strings.Contains(a.addRepo.err, "already") {
		t.Fatalf("duplicate: %+v", a.addRepo)
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	// A folder that isn't a repo is refused.
	press(a, 'n')
	a.addRepo.input.SetValue(t.TempDir())
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.addRepo == nil || !strings.Contains(a.addRepo.err, "Not a git repository") {
		t.Fatalf("not a repo: %+v", a.addRepo)
	}
}

func TestReposRow(t *testing.T) {
	dir := gitRepo(t, "compose.yaml")
	a, _ := reposApp(t, dir, "compose.yaml")
	// Two containers from this project, one running.
	file := filepath.Join(dir, "compose.yaml")
	mk := func(id, state string) *dock.Container {
		return &dock.Container{ID: id, Name: filepath.Base(dir) + "-" + id, State: state,
			Labels: map[string]string{dock.LabelProject: "p", dock.LabelConfigFiles: file, dock.LabelWorkingDir: dir}}
	}
	a.Update(containersMsg{cs: []*dock.Container{mk("web", dock.StateRunning), mk("db", dock.StateExited)}})
	s := screen(a)
	for _, want := range []string{filepath.Base(dir), "main", "✓", "◐ 1/2", "—"} {
		if !strings.Contains(s, want) {
			t.Errorf("row missing %q:\n%s", want, s)
		}
	}
	if b := ansi.Strip(bar(a)); !strings.Contains(b, "REPOS") || !strings.Contains(b, "main") {
		t.Errorf("bar: %q", b)
	}
}

func TestSwitchBranchAndBuild(t *testing.T) {
	dir := gitRepo(t, "compose.yaml")
	a, rec := reposApp(t, dir, "compose.yaml")

	press(a, 'b')
	if a.branches == nil {
		t.Fatal("b should open the branch picker")
	}
	a.Update(loadBranches(dir, dir)())
	if s := screen(a); !strings.Contains(s, "feature") || !strings.Contains(s, "current · rebuilds") {
		t.Fatalf("picker:\n%s", s)
	}
	// Filter to feature and pick it.
	for _, r := range "feat" {
		press(a, r)
	}
	_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.branches != nil || !a.repos.selected().job.running() {
		t.Fatal("enter should start the build")
	}
	finishJob(t, a, cmd)
	want := []string{"git switch feature", "docker compose -f compose.yaml --progress plain up -d --build"}
	if got := rec.all(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("steps:\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(a.toast.text, "Build feature") {
		t.Errorf("toast: %q", a.toast.text)
	}
	// The fake didn't really switch, so the record says main; what matters
	// is that it was saved and shows.
	if b, ok := cache.LoadBuilds(a.buildsPath)[dir]; !ok || !b.OK || b.Commit == "" {
		t.Fatalf("build record: %+v %v", b, ok)
	}
	if !strings.Contains(screen(a), "just now · main") {
		t.Errorf("BUILT column:\n%s", screen(a))
	}
}

func TestDirtyTreeAsksToStash(t *testing.T) {
	dir := gitRepo(t, "compose.yaml")
	os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("x"), 0o644)
	a, rec := reposApp(t, dir, "compose.yaml")

	press(a, 'b')
	a.Update(loadBranches(dir, dir)())
	for _, r := range "feat" {
		press(a, r)
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.popup == nil || !strings.Contains(screen(a), "1 uncommitted change") {
		t.Fatalf("should ask about the changes:\n%s", screen(a))
	}
	_, cmd := a.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	finishJob(t, a, cmd)
	got := rec.all()
	if len(got) != 3 || !strings.HasPrefix(got[0], "git stash push --include-untracked") || got[1] != "git switch feature" {
		t.Fatalf("steps: %q", got)
	}
}

func TestFailedBuildShowsOutput(t *testing.T) {
	dir := gitRepo(t, "compose.yaml")
	a, rec := reposApp(t, dir, "compose.yaml")
	rec.fail = "up -d"
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'U', Text: "U"})
	finishJob(t, a, cmd)
	if a.logView == nil || a.logView.job == nil {
		t.Fatal("a failed build should open its output")
	}
	s := screen(a)
	if !strings.Contains(s, "output of docker") || !strings.Contains(s, "failed") {
		t.Fatalf("output view:\n%s", s)
	}
	press(a, 'q')
	if !strings.Contains(screen(a), "build failed") {
		t.Fatalf("row should show the failure:\n%s", screen(a))
	}
	if b := a.builds[dir]; b.OK {
		t.Fatal("a failed build must be recorded as failed")
	}
	// O opens it again.
	press(a, 'O')
	if a.logView == nil {
		t.Fatal("O should show the last output")
	}
}

func TestRepoWebPage(t *testing.T) {
	opened := captureBrowser(t)
	dir := gitRepo(t, "compose.yaml")
	a, _ := reposApp(t, dir, "compose.yaml")
	press(a, 'w')
	if len(*opened) != 0 || !strings.Contains(a.toast.text, "no remote with a web page") {
		t.Fatalf("no remote: opened %v toast %q", *opened, a.toast.text)
	}
	exec.Command("git", "-C", dir, "remote", "add", "origin", "git@github.com:bferg314/clock.git").Run()
	a.Update(repoStatusMsg{key: dir, status: gitinfo.Get(context.Background(), dir)})
	press(a, 'w')
	if len(*opened) != 1 || (*opened)[0] != "https://github.com/bferg314/clock" {
		t.Fatalf("opened %v", *opened)
	}
}

func TestPullNeedsUpstream(t *testing.T) {
	dir := gitRepo(t, "compose.yaml")
	a, rec := reposApp(t, dir, "compose.yaml")
	press(a, 'u')
	if !strings.Contains(a.toast.text, "no upstream") || len(rec.all()) != 0 {
		t.Fatalf("toast=%q steps=%q", a.toast.text, rec.all())
	}
}

func TestDownAsksFirst(t *testing.T) {
	dir := gitRepo(t, "compose.yaml")
	a, rec := reposApp(t, dir, "compose.yaml")
	file := filepath.Join(dir, "compose.yaml")
	a.Update(containersMsg{cs: []*dock.Container{{ID: "web", Name: "web", State: dock.StateRunning,
		Labels: map[string]string{dock.LabelConfigFiles: file}}}})
	press(a, 'D')
	if a.confirmDlg == nil || !strings.Contains(screen(a), "Volumes are kept") {
		t.Fatalf("D should ask:\n%s", screen(a))
	}
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	finishJob(t, a, cmd)
	if got := rec.all(); len(got) != 1 || !strings.HasSuffix(got[0], "down") {
		t.Fatalf("steps: %q", got)
	}
}

func TestNoComposeFile(t *testing.T) {
	dir := gitRepo(t, "README.md")
	a, rec := reposApp(t, dir)
	if !strings.Contains(screen(a), "no compose") {
		t.Fatalf("row:\n%s", screen(a))
	}
	press(a, 'U')
	if !strings.Contains(a.toast.text, "no compose file") || len(rec.all()) != 0 {
		t.Fatalf("toast=%q steps=%q", a.toast.text, rec.all())
	}
}
