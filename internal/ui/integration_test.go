package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/dock"
)

// TestLiveDockerUpdates runs the app against the real daemon and checks
// that a container started and stopped outside dockgit shows up within a
// second. Opt in with DOCKGIT_IT=1; DOCKGIT_IT_IMAGE picks an image with
// `sleep` (default busybox, pulled if missing).
func TestLiveDockerUpdates(t *testing.T) {
	if os.Getenv("DOCKGIT_IT") == "" {
		t.Skip("set DOCKGIT_IT=1 to run against the real Docker daemon")
	}
	image := os.Getenv("DOCKGIT_IT_IMAGE")
	if image == "" {
		image = "busybox"
	}
	name := fmt.Sprintf("dockgit-it-%d", time.Now().UnixNano())
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })

	a := New(offCleanup(), filepath.Join(t.TempDir(), "config.toml"), dock.CLI{})
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	p := newPump(a)
	defer p.stop()
	p.run(a.Init())

	p.until(t, 15*time.Second, "initial load", func() bool { return a.docker.loaded })

	docker := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
	}
	find := func() *dock.Container {
		for _, c := range a.docker.all {
			if c.Name == name {
				return c
			}
		}
		return nil
	}

	docker("run", "-d", "--name", name, image, "sleep", "300")
	p.until(t, time.Second, "container shown as running", func() bool {
		c := find()
		return c != nil && c.Running()
	})

	docker("stop", "-t", "0", name)
	p.until(t, time.Second, "container shown as exited", func() bool {
		c := find()
		return c != nil && c.State == dock.StateExited
	})

	docker("rm", name)
	p.until(t, time.Second, "container removed", func() bool { return find() == nil })
}

// TestLiveDockerActions drives the container actions, the logs viewer and
// the images and volumes views against the real daemon. Opt in as above.
func TestLiveDockerActions(t *testing.T) {
	if os.Getenv("DOCKGIT_IT") == "" {
		t.Skip("set DOCKGIT_IT=1 to run against the real Docker daemon")
	}
	image := os.Getenv("DOCKGIT_IT_IMAGE")
	if image == "" {
		image = "busybox"
	}
	name := fmt.Sprintf("dockgit-it-%d", time.Now().UnixNano())
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	script := "i=0; while true; do echo tick $i; i=$((i+1)); sleep 0.2; done"
	if out, err := exec.Command("docker", "run", "-d", "--name", name, image, "sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}

	a := New(offCleanup(), filepath.Join(t.TempDir(), "config.toml"), dock.CLI{})
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	p := newPump(a)
	defer p.stop()
	p.run(a.Init())
	find := func() *dock.Container {
		for _, c := range a.docker.all {
			if c.Name == name {
				return c
			}
		}
		return nil
	}
	p.until(t, 15*time.Second, "container listed", func() bool { c := find(); return c != nil && c.Running() })
	selectIt := func() {
		t.Helper()
		for i, r := range a.docker.view {
			if r.c != nil && r.c.Name == name {
				a.docker.cursor = i
				return
			}
		}
		t.Fatal("container not in the list")
	}
	key := func(r rune) {
		_, cmd := a.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		p.run(cmd)
	}

	// Logs follow live output.
	selectIt()
	key('l')
	p.until(t, 5*time.Second, "log lines", func() bool { return a.logView != nil && len(a.logView.lines) >= 3 })
	n := len(a.logView.lines)
	p.until(t, 3*time.Second, "more log lines", func() bool { return len(a.logView.lines) > n })
	key('q')

	// Stop, start, restart.
	selectIt()
	key('s')
	p.until(t, 20*time.Second, "stopped", func() bool { c := find(); return c != nil && c.State == dock.StateExited && len(a.docker.busy) == 0 })
	key('s')
	p.until(t, 10*time.Second, "started", func() bool { c := find(); return c != nil && c.Running() && len(a.docker.busy) == 0 })
	started := find().StartedAt
	key('S')
	p.until(t, 20*time.Second, "restarted", func() bool {
		c := find()
		return c != nil && c.Running() && c.StartedAt.After(started) && len(a.docker.busy) == 0
	})

	// A shell is found (the terminal hand-off itself needs a person).
	if sh, err := dock.Shell(context.Background(), dock.CLI{}, find().ID); err != nil || sh == "" {
		t.Fatalf("shell: %q %v", sh, err)
	}

	// Images and volumes load from the real daemon.
	key('i')
	p.until(t, 15*time.Second, "images", func() bool { return a.docker.images.loaded })
	if a.docker.images.err != nil || len(a.docker.imgs) == 0 {
		t.Fatalf("images: %v %d", a.docker.images.err, len(a.docker.imgs))
	}
	key('v')
	p.until(t, 60*time.Second, "volumes", func() bool { return a.docker.volumes.loaded || a.docker.volumes.err != nil })
	if a.docker.volumes.err != nil {
		t.Fatalf("volumes: %v", a.docker.volumes.err)
	}
	key('v')

	// Remove asks, then removes the running container.
	selectIt()
	key('x')
	if a.confirmDlg == nil {
		t.Fatal("remove should ask first")
	}
	key('y')
	p.until(t, 20*time.Second, "removed", func() bool { return find() == nil && len(a.docker.busy) == 0 })
	if strings.Contains(a.toast.text, "rror") {
		t.Fatalf("toast: %q", a.toast.text)
	}
}

// TestLiveSwitchBranchAndBuild is M3's acceptance check: b, pick a
// branch, and the containers are rebuilt from it with no other input.
// It builds from DOCKGIT_IT_IMAGE (default busybox), so nothing else is
// pulled.
func TestLiveSwitchBranchAndBuild(t *testing.T) {
	if os.Getenv("DOCKGIT_IT") == "" {
		t.Skip("set DOCKGIT_IT=1 to run against the real Docker daemon")
	}
	image := os.Getenv("DOCKGIT_IT_IMAGE")
	if image == "" {
		image = "busybox"
	}
	dir := filepath.Join(t.TempDir(), fmt.Sprintf("dockgit-it-%d", time.Now().UnixNano()))
	os.Mkdir(dir, 0o755)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dockerfile := func(branch string) string {
		return "FROM " + image + "\nCMD [\"sh\", \"-c\", \"echo built from " + branch + "; sleep 300\"]\n"
	}
	git("init", "-q", "-b", "main")
	write("compose.yaml", "services:\n  app:\n    build: .\n")
	write("Dockerfile", dockerfile("main"))
	git("add", ".")
	git("commit", "-q", "-m", "main")
	git("switch", "-q", "-c", "feature")
	write("Dockerfile", dockerfile("feature"))
	git("commit", "-q", "-am", "feature")
	git("switch", "-q", "main")
	t.Cleanup(func() {
		exec.Command("docker", "compose", "--project-directory", dir, "-f", filepath.Join(dir, "compose.yaml"), "down", "--rmi", "local").Run()
	})

	cfg := config.Default()
	cfg.Cleanup.Mode = config.CleanupOff // never prune the real host's cache in tests
	cfg.Repos = []config.Repo{{Path: dir, ComposeFiles: []string{"compose.yaml"}}}
	a := New(cfg, filepath.Join(t.TempDir(), "config.toml"), dock.CLI{})
	a.UseBuilds(filepath.Join(t.TempDir(), "builds.json"))
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	p := newPump(a)
	defer p.stop()
	p.run(a.Init())
	key := func(k tea.KeyPressMsg) {
		_, cmd := a.Update(k)
		p.run(cmd)
	}
	key(tea.KeyPressMsg{Code: '2', Text: "2"})
	r := a.repos.selected()
	p.until(t, 15*time.Second, "repo status", func() bool { return r.status != nil && a.docker.loaded })

	// b, type to find feature, enter.
	key(tea.KeyPressMsg{Code: 'b', Text: "b"})
	p.until(t, 10*time.Second, "branches", func() bool { return a.branches != nil && !a.branches.loading })
	for _, c := range "feature" {
		key(tea.KeyPressMsg{Code: c, Text: string(c)})
	}
	key(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.until(t, 3*time.Minute, "build finished", func() bool {
		_, recorded := a.builds[r.key]
		return r.job != nil && r.job.done && recorded
	})
	if r.job.err != nil {
		t.Fatalf("build failed: %v\n%s", r.job.err, strings.Join(r.job.lines, "\n"))
	}
	if b := a.builds[r.key]; b.Branch != "feature" || !b.OK {
		t.Fatalf("build record: %+v", b)
	}
	p.until(t, 10*time.Second, "container running", func() bool { running, _ := a.repoContainers(r); return running == 1 })

	var c *dock.Container
	for _, x := range a.docker.all {
		if a.project(r).Owns(x.ConfigFiles(), x.WorkingDir()) {
			c = x
		}
	}
	out, _ := exec.Command("docker", "logs", c.ID).CombinedOutput()
	if !strings.Contains(string(out), "built from feature") {
		t.Fatalf("container wasn't rebuilt from feature: %q", out)
	}
	if s := screen(a); !strings.Contains(s, "● up 1/1") || !strings.Contains(s, "feature") {
		t.Errorf("repos row:\n%s", s)
	}
}

// TestLiveComposeStackUp is M4's acceptance check in miniature: a fresh
// clone of a stack-layout repo, E creates the stack's .env from its
// .env.example, U brings it up with the root's and the stack's .env
// layered, and D takes it down.
func TestLiveComposeStackUp(t *testing.T) {
	if os.Getenv("DOCKGIT_IT") == "" {
		t.Skip("set DOCKGIT_IT=1 to run against the real Docker daemon")
	}
	image := os.Getenv("DOCKGIT_IT_IMAGE")
	if image == "" {
		image = "busybox"
	}
	name := fmt.Sprintf("dockgit-it-%d", time.Now().UnixNano())
	origin := filepath.Join(t.TempDir(), "composes")
	stack := "stacks/apps/" + name
	files := map[string]string{
		".gitignore":   ".env\n",
		".env.example": "WHO=world\n",
		stack + "/compose.yaml": "services:\n  app:\n    image: " + image + "\n" +
			"    command: [\"sh\", \"-c\", \"echo $$GREETING, $$WHO; sleep 300\"]\n" +
			"    environment:\n      GREETING: ${GREETING}\n      WHO: ${WHO:-nobody}\n",
		stack + "/.env.example": "GREETING=hello\n",
	}
	for p, body := range files {
		full := filepath.Join(origin, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(body), 0o644)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(origin, "init", "-q", "-b", "main")
	git(origin, "add", ".")
	git(origin, "commit", "-q", "-m", "stacks")
	clone := filepath.Join(t.TempDir(), "composes")
	if out, err := exec.Command("git", "clone", "-q", origin, clone).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	stackDir := filepath.Join(clone, filepath.FromSlash(stack))
	t.Cleanup(func() {
		exec.Command("docker", "compose", "--project-directory", stackDir, "-f", filepath.Join(stackDir, "compose.yaml"), "down").Run()
	})
	// The shared .env, as a person would fill it in on a new machine.
	os.WriteFile(filepath.Join(clone, ".env"), []byte("WHO=dockgit\n"), 0o600)

	cfg := config.Default()
	cfg.Cleanup.Mode = config.CleanupOff // never prune the real host's cache in tests
	cfg.ComposeRoots = []string{clone}
	a := New(cfg, filepath.Join(t.TempDir(), "config.toml"), dock.CLI{})
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	p := newPump(a)
	defer p.stop()
	p.run(a.Init())
	key := func(r rune) {
		_, cmd := a.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		p.run(cmd)
	}
	key('3')
	p.until(t, 15*time.Second, "stacks", func() bool { return a.compose.roots[0].loaded && a.docker.loaded })
	var s *stackRow
	for i, row := range a.compose.view {
		if row.s != nil && row.s.Name() == stack {
			a.compose.cursor, s = i, row.s
		}
	}
	if s == nil {
		t.Fatalf("stack not found:\n%s", screen(a))
	}

	key('E')
	if data, err := os.ReadFile(filepath.Join(stackDir, ".env")); err != nil || string(data) != "GREETING=hello\n" {
		t.Fatalf(".env from the example: %q %v", data, err)
	}
	key('U')
	p.until(t, 2*time.Minute, "up", func() bool { return s.job != nil && s.job.done })
	if s.job.err != nil {
		t.Fatalf("up failed: %v\n%s", s.job.err, strings.Join(s.job.lines, "\n"))
	}
	p.until(t, 10*time.Second, "running", func() bool { r, _, _ := a.stackContainers(s); return r == 1 })
	if !strings.Contains(screen(a), "● up 1/1") {
		t.Errorf("row:\n%s", screen(a))
	}
	var id string
	for _, c := range a.docker.all {
		if s.Owns(c.ConfigFiles()) {
			id = c.ID
		}
	}
	var out []byte
	p.until(t, 5*time.Second, "output", func() bool {
		out, _ = exec.Command("docker", "logs", id).CombinedOutput()
		return len(out) > 0
	})
	if !strings.Contains(string(out), "hello, dockgit") {
		t.Fatalf("the stack's and the root's .env should both apply: %q", out)
	}

	key('D')
	key('y')
	p.until(t, time.Minute, "down", func() bool { r, total, _ := a.stackContainers(s); return s.job.done && r == 0 && total == 0 })
}

// TestLivePortHolderIsStoppedFirst is M5's acceptance check: starting a
// stack whose host port another container holds shows which one, and s
// stops it and starts the stack.
func TestLivePortHolderIsStoppedFirst(t *testing.T) {
	if os.Getenv("DOCKGIT_IT") == "" {
		t.Skip("set DOCKGIT_IT=1 to run against the real Docker daemon")
	}
	image := os.Getenv("DOCKGIT_IT_IMAGE")
	if image == "" {
		image = "busybox"
	}
	name := fmt.Sprintf("dockgit-it-%d", time.Now().UnixNano())
	const port = "18765"
	holder := name + "-holder"
	root := t.TempDir()
	stackDir := filepath.Join(root, "stacks", name)
	os.MkdirAll(stackDir, 0o755)
	os.WriteFile(filepath.Join(stackDir, "compose.yaml"), []byte("services:\n  app:\n    image: "+image+"\n"+
		"    command: [\"sleep\", \"300\"]\n    ports:\n      - \""+port+":80\"\n"), 0o644)
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", holder).Run()
		exec.Command("docker", "compose", "--project-directory", stackDir, "-f", filepath.Join(stackDir, "compose.yaml"), "down").Run()
	})
	if out, err := exec.Command("docker", "run", "-d", "--name", holder, "-p", port+":80", image, "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("holder: %v\n%s", err, out)
	}

	cfg := config.Default()
	cfg.Cleanup.Mode = config.CleanupOff // never prune the real host's cache in tests
	cfg.ComposeRoots = []string{root}
	a := New(cfg, filepath.Join(t.TempDir(), "config.toml"), dock.CLI{})
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	p := newPump(a)
	defer p.stop()
	p.run(a.Init())
	key := func(r rune) {
		_, cmd := a.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		p.run(cmd)
	}
	key('3')
	p.until(t, 15*time.Second, "loaded", func() bool {
		for _, c := range a.docker.all {
			if c.Name == holder && c.Running() {
				return a.compose.roots[0].loaded
			}
		}
		return false
	})
	var s *stackRow
	for i, row := range a.compose.view {
		if row.s != nil {
			a.compose.cursor, s = i, row.s
		}
	}

	key('U')
	if a.popup == nil || !strings.Contains(screen(a), "port "+port+" (app) is held by "+holder) {
		t.Fatalf("expected the port holder:\n%s", screen(a))
	}
	key('s')
	p.until(t, time.Minute, "up", func() bool { return s.job != nil && s.job.done })
	if s.job.err != nil {
		t.Fatalf("up failed: %v\n%s", s.job.err, strings.Join(s.job.lines, "\n"))
	}
	p.until(t, 10*time.Second, "stack running, holder stopped", func() bool {
		r, _, _ := a.stackContainers(s)
		for _, c := range a.docker.all {
			if c.Name == holder && c.Running() {
				return false
			}
		}
		return r == 1
	})
}

// pump is a minimal Bubble Tea runtime for tests: it runs commands in
// goroutines and feeds their messages back through Update on the test's
// goroutine. Spinner ticks are dropped so idle loops don't spin.
type pump struct {
	a    *App
	msgs chan tea.Msg
	ctx  context.Context
	stop context.CancelFunc
}

func newPump(a *App) *pump {
	ctx, cancel := context.WithCancel(context.Background())
	return &pump{a: a, msgs: make(chan tea.Msg, 256), ctx: ctx, stop: func() {
		cancel()
		if a.docker.stop != nil {
			a.docker.stop()
		}
	}}
}

func (p *pump) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		select {
		case p.msgs <- msg:
		case <-p.ctx.Done():
		}
	}()
}

// until applies messages until cond holds, failing after d.
func (p *pump) until(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(d)
	for !cond() {
		select {
		case msg := <-p.msgs:
			switch msg := msg.(type) {
			case tea.BatchMsg:
				for _, c := range msg {
					p.run(c)
				}
			case spinner.TickMsg, nil:
			default:
				_, cmd := p.a.Update(msg)
				p.run(cmd)
			}
		case <-deadline:
			t.Fatalf("timed out after %v waiting for: %s", d, what)
		}
	}
}

// offCleanup is the default config with automatic cleanup off, so live
// tests never prune the real host's build cache.
func offCleanup() *config.Config {
	cfg := config.Default()
	cfg.Cleanup.Mode = config.CleanupOff
	return cfg
}
