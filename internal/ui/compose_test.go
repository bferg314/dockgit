package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/dock"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

var composeTree = map[string]string{
	".env.example":                   "PUID=1000\n",
	"stacks/apps/clock/compose.yaml": "services:\n  clock:\n    image: ghcr.io/me/clock:${TAG:-latest}\n    ports:\n      - \"8080:80\"\nx-dockgit:\n  description: Wall clock\n",
	"stacks/media/plex/compose.yaml": "services:\n  plex:\n    image: linuxserver/plex:1.40\n    environment:\n      - PUID=${PUID}\n      - CLAIM=${PLEX_CLAIM}\n",
	"stacks/media/plex/.env.example": "PLEX_CLAIM=\n",
	"arr/gluetun.yml":                "services:\n  gluetun:\n    image: qmcgaw/gluetun:v3\n    ports:\n      - 8080:8080\n",
	"arr/qbit.yml":                   "services:\n  qbittorrent:\n    image: linuxserver/qbittorrent:4.6\n    network_mode: \"service:gluetun\"\n",
}

// composeApp lists one root holding composeTree, on the Compose tab.
func composeApp(t *testing.T) (*App, string, *stepRecorder) {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, composeTree)
	cfg := config.Default()
	cfg.ComposeRoots = []string{root}
	a := testApp(t, 150, cfg)
	a.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
	rec := &stepRecorder{}
	a.exec = rec.exec
	a.Update(loadRoot(root, root)())
	press(a, '3')
	return a, root, rec
}

// selectStack moves the cursor to the stack with this name.
func selectStack(t *testing.T, a *App, name string) *stackRow {
	t.Helper()
	for i, row := range a.compose.view {
		if row.s != nil && row.s.Name() == name {
			a.compose.cursor = i
			return row.s
		}
	}
	t.Fatalf("no stack %q", name)
	return nil
}

func TestComposeList(t *testing.T) {
	a, root, _ := composeApp(t)
	clock := selectStack(t, a, "stacks/apps/clock")
	a.Update(containersMsg{cs: []*dock.Container{{ID: "c1", Name: "clock-clock-1", State: dock.StateRunning,
		Created: time.Now().Add(time.Hour), // made after the file was written
		Labels:  map[string]string{dock.LabelConfigFiles: clock.File}}}})
	s := screen(a)
	for _, want := range []string{
		filepath.Base(root), "4 stacks", "✗", "! doctor", // a root heading with its problem count
		"stacks/apps/clock", "● up 1/1", "Wall clock",
		"stacks/media/plex", "○ down", "2 unset",
		"arr/gluetun.yml", "arr/qbit.yml",
		"Compose 4",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	// The detail pane describes the selected stack.
	for _, want := range []string{"SERVICES", "ghcr.io/me/clock:${TAG:-latest}", "8080", "ENV FILES", "FILE", "project"} {
		if !strings.Contains(s, want) {
			t.Errorf("detail missing %q:\n%s", want, s)
		}
	}
	if b := ansi.Strip(bar(a)); !strings.Contains(b, "COMPOSE") || !strings.Contains(b, "compose.yaml") {
		t.Errorf("bar: %q", b)
	}

	// A container made before the file changed: "changed since up".
	a.Update(containersMsg{cs: []*dock.Container{{ID: "c1", Name: "clock-clock-1", State: dock.StateRunning,
		Created: time.Now().Add(-time.Hour), Labels: map[string]string{dock.LabelConfigFiles: clock.File}}}})
	if !strings.Contains(screen(a), "changed since up") {
		t.Errorf("expected changed since up:\n%s", screen(a))
	}
}

func TestComposeUpLayersEnvFiles(t *testing.T) {
	a, root, rec := composeApp(t)
	writeFiles(t, root, map[string]string{".env": "PUID=1000\n", "stacks/media/plex/.env": "PLEX_CLAIM=x\n"})
	selectStack(t, a, "stacks/media/plex")
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'U', Text: "U"})
	p := newPump(a)
	defer p.stop()
	p.run(cmd)
	s := a.compose.selectedStack()
	p.until(t, 5*time.Second, "up finished", func() bool { return s.job != nil && s.job.done })
	got := rec.all()
	want := "docker compose -f compose.yaml --env-file " + filepath.Join(root, ".env") + " --env-file " +
		filepath.Join(root, "stacks", "media", "plex", ".env") + " --progress plain up -d"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("steps:\n%q\nwant\n%q", got, want)
	}
}

func TestComposeDoctorView(t *testing.T) {
	a, _, _ := composeApp(t)
	press(a, '!')
	if a.logView == nil || !a.logView.static {
		t.Fatal("! should open the doctor report")
	}
	s := screen(a)
	for _, want := range []string{"Problems", "network_mode service:gluetun from another file", "host port 8080",
		"Warnings", "share this folder", "→ Give each stack its own folder"} {
		if !strings.Contains(s, want) {
			t.Errorf("report missing %q:\n%s", want, s)
		}
	}
	if b := ansi.Strip(bar(a)); !strings.Contains(b, "DOCTOR") {
		t.Errorf("bar: %q", b)
	}
	press(a, 'r') // nothing to follow
	press(a, 'q')
	if a.logView != nil {
		t.Fatal("q closes the report")
	}
}

func TestComposeEnvCreatesFromExample(t *testing.T) {
	a, root, _ := composeApp(t)
	selectStack(t, a, "stacks/media/plex")
	press(a, 'E')
	data, err := os.ReadFile(filepath.Join(root, "stacks", "media", "plex", ".env"))
	if err != nil || string(data) != "PLEX_CLAIM=\n" {
		t.Fatalf(".env: %q %v", data, err)
	}
	if !strings.Contains(a.toast.text, "Created .env for stacks/media/plex") || !strings.Contains(a.toast.text, "PUID, PLEX_CLAIM") {
		t.Errorf("toast: %q", a.toast.text)
	}
	// Running it again leaves the file alone.
	os.WriteFile(filepath.Join(root, "stacks", "media", "plex", ".env"), []byte("PLEX_CLAIM=abc\n"), 0o600)
	press(a, 'E')
	if data, _ := os.ReadFile(filepath.Join(root, "stacks", "media", "plex", ".env")); string(data) != "PLEX_CLAIM=abc\n" {
		t.Fatalf("existing .env was changed: %q", data)
	}
	if !strings.Contains(a.toast.text, "still needs PUID") {
		t.Errorf("toast: %q", a.toast.text)
	}
}

func TestComposeNewStack(t *testing.T) {
	a, root, _ := composeApp(t)
	press(a, 'n')
	if a.newStack == nil {
		t.Fatal("n should ask for a name")
	}
	for _, r := range "Bad Name" {
		press(a, r)
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.newStack == nil || !strings.Contains(a.newStack.err, "lower case") {
		t.Fatalf("bad name: %+v", a.newStack)
	}
	a.newStack.input.SetValue("tools/dozzle")
	_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.newStack != nil {
		t.Fatal("enter should create it")
	}
	dir := filepath.Join(root, "stacks", "tools", "dozzle")
	for _, f := range []string{"compose.yaml", ".env.example"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	// The rescan it asked for finds the new stack.
	p := newPump(a)
	defer p.stop()
	p.run(cmd)
	p.until(t, 5*time.Second, "rescan", func() bool { return strings.Contains(screen(a), "stacks/tools/dozzle") })
}

func TestComposeDownAndRoots(t *testing.T) {
	a, root, _ := composeApp(t)
	clock := selectStack(t, a, "stacks/apps/clock")
	press(a, 'D')
	if a.confirmDlg != nil || !strings.Contains(a.toast.text, "no containers") {
		t.Fatalf("down with nothing running: dlg=%v toast=%q", a.confirmDlg, a.toast.text)
	}
	a.Update(containersMsg{cs: []*dock.Container{{ID: "c1", Name: "c", State: dock.StateRunning, Labels: map[string]string{dock.LabelConfigFiles: clock.File}}}})
	press(a, 'D')
	if a.confirmDlg == nil {
		t.Fatal("D should ask")
	}
	press(a, 'n')

	// p on a root that isn't a git repo says so.
	press(a, 'p')
	if !strings.Contains(a.toast.text, "isn't a git repo") {
		t.Errorf("toast: %q", a.toast.text)
	}

	// A adds a second root; X on it stops listing it.
	other := t.TempDir()
	writeFiles(t, other, map[string]string{"compose.yaml": "services:\n  a:\n    image: x:1\n"})
	press(a, 'A')
	a.addRepo.input.SetValue(other)
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(a.cfg.ComposeRoots) != 2 {
		t.Fatalf("roots: %v", a.cfg.ComposeRoots)
	}
	press(a, 'A')
	a.addRepo.input.SetValue(root)
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.addRepo == nil || !strings.Contains(a.addRepo.err, "already listed") {
		t.Fatalf("duplicate root: %+v", a.addRepo)
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	a.compose.cursor = len(a.compose.view) - 1 // the new root's heading
	press(a, 'X')
	press(a, 'y')
	if len(a.cfg.ComposeRoots) != 1 {
		t.Fatalf("X should remove the root: %v", a.cfg.ComposeRoots)
	}
}

func TestComposeEmpty(t *testing.T) {
	a := testApp(t, 120, config.Default())
	press(a, '3')
	if !strings.Contains(screen(a), "No compose roots yet. Press A") {
		t.Fatalf("empty tab:\n%s", screen(a))
	}
}
