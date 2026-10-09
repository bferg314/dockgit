package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/dock"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../dock/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestActionPopup(t *testing.T) {
	a := dockerApp(t, 140)
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.popup == nil {
		t.Fatal("enter should open the action popup")
	}
	s := screen(a)
	for _, want := range []string{"calliope-poker-app-1", "Logs", "Shell", "Open in browser", "http://localhost:8080", "Stop", "Restart", "Remove"} {
		if !strings.Contains(s, want) {
			t.Errorf("popup missing %q:\n%s", want, s)
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(ansi.Strip(bar(a))), "ACTIONS") {
		t.Errorf("bar: %q", ansi.Strip(bar(a)))
	}

	// A key from the popup runs that action and closes it.
	press(a, 's')
	if a.popup != nil || a.docker.busy[appID] != "stopping" {
		t.Fatalf("s should start stopping app-1: popup=%v busy=%v", a.popup, a.docker.busy)
	}
	if !strings.Contains(screen(a), "stopping") {
		t.Error("the row should show the action in flight")
	}
	a.Update(actionDoneMsg{id: appID, done: "Stopped app-1"})
	if a.docker.busy[appID] != "" || a.toast.text != "Stopped app-1" {
		t.Fatalf("done: busy=%v toast=%q", a.docker.busy, a.toast.text)
	}

	// A stopped container is offered Start, and no shell or browser.
	for range 3 {
		press(a, 'j')
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = screen(a)
	if !strings.Contains(s, "Start") || strings.Contains(s, "Shell") || strings.Contains(s, "Open in browser") {
		t.Fatalf("old-nginx popup:\n%s", s)
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if a.popup != nil {
		t.Fatal("esc should close the popup")
	}
}

// captureBrowser records opened URLs instead of launching a browser.
func captureBrowser(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	old := openURL
	openURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openURL = old })
	return &opened
}

func TestWOpensPortAndOOpensEditor(t *testing.T) {
	opened := captureBrowser(t)
	t.Setenv("PATH", t.TempDir())
	a := dockerApp(t, 140)
	press(a, 'w')
	if len(*opened) != 1 || (*opened)[0] != "http://localhost:8080" {
		t.Fatalf("w opened %v", *opened)
	}

	// No editor turned on: o says how to get one.
	press(a, 'o')
	if !strings.Contains(a.toast.text, "No tool is bound to o") {
		t.Fatalf("toast: %q", a.toast.text)
	}
	// VS Code on but not installed here: o reaches the launcher, which
	// reports it (the fixture's project folder may not exist, so check
	// either outcome of the folder lookup).
	for i := range a.cfg.Tools {
		if a.cfg.Tools[i].Key == "o" {
			a.cfg.Tools[i].Enabled = true
		}
	}
	press(a, 'o')
	if !strings.Contains(a.toast.text, "not found on PATH") && !strings.Contains(a.toast.text, "no project folder") {
		t.Fatalf("toast: %q", a.toast.text)
	}
	// old-nginx has no compose project.
	for range 3 {
		press(a, 'j')
	}
	press(a, 'o')
	if !strings.Contains(a.toast.text, "old-nginx has no project folder") {
		t.Fatalf("toast: %q", a.toast.text)
	}
}

func TestRemoveAsksFirst(t *testing.T) {
	a := dockerApp(t, 140)
	press(a, 'x')
	if a.confirmDlg == nil || !strings.Contains(screen(a), "Remove container app-1?") ||
		!strings.Contains(screen(a), "it will be stopped first") {
		t.Fatalf("x should ask first:\n%s", screen(a))
	}
	press(a, 'n')
	if a.confirmDlg != nil || a.docker.busy[appID] != "" {
		t.Fatal("n should cancel")
	}
	press(a, 'x')
	press(a, 'y')
	if a.docker.busy[appID] != "removing" {
		t.Fatalf("y should remove: %v", a.docker.busy)
	}

	// With confirmations off in Settings, it goes straight ahead.
	cfg := config.Default()
	cfg.ConfirmDestructive = false
	b := testApp(t, 140, cfg)
	b.Update(containersMsg{cs: fixtureContainers(t)})
	press(b, 'x')
	if b.confirmDlg != nil || b.docker.busy[appID] != "removing" {
		t.Fatalf("no confirmation expected: dlg=%v busy=%v", b.confirmDlg, b.docker.busy)
	}
}

func TestShellErrors(t *testing.T) {
	a := dockerApp(t, 140)
	c := a.docker.selected()
	a.Update(shellMsg{c: c, err: dock.ErrNoShell})
	if !strings.Contains(a.toast.text, "has no shell") {
		t.Errorf("toast: %q", a.toast.text)
	}
	for range 3 {
		press(a, 'j')
	}
	press(a, 'e')
	if !strings.Contains(a.toast.text, "isn't running") {
		t.Errorf("a stopped container can't have a shell: %q", a.toast.text)
	}
}

func TestLogViewer(t *testing.T) {
	a := dockerApp(t, 100)
	press(a, 'l')
	v := a.logView
	if v == nil || v.id != appID {
		t.Fatal("l should open the logs viewer")
	}
	if !strings.Contains(screen(a), "waiting for output") {
		t.Errorf("empty viewer:\n%s", screen(a))
	}
	var lines []string
	for i := range 100 {
		lines = append(lines, fmt.Sprintf("2026-10-08T14:12:16.813046562Z line %d", i))
	}
	lines = append(lines, "2026-10-08T14:12:17.000000000Z ERROR something broke")
	a.Update(logLinesMsg{gen: v.gen, lines: lines})
	s := screen(a)
	if !strings.Contains(s, "ERROR something broke") || strings.Contains(s, "2026-10-08") {
		t.Fatalf("following should show the newest line without timestamps:\n%s", s)
	}
	b := ansi.Strip(bar(a))
	if !strings.Contains(b, "LOGS") || !strings.Contains(b, "following") || !strings.Contains(b, "101 lines") {
		t.Errorf("bar: %q", b)
	}

	press(a, 't')
	if !strings.Contains(screen(a), "2026-10-08 14:12:17  ERROR") {
		t.Errorf("t should show timestamps:\n%s", screen(a))
	}

	// Scrolling up stops following; search jumps back to a match.
	press(a, 'g')
	if v.follow || !strings.Contains(screen(a), "line 0") {
		t.Fatal("g should go to the top and stop following")
	}
	press(a, '/')
	for _, r := range "line 42" {
		press(a, r)
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s := screen(a); !strings.Contains(strings.Split(s, "\n")[2], "line 42") {
		t.Fatalf("search should put line 42 at the top:\n%s", s)
	}
	press(a, 'G')
	if !v.follow {
		t.Fatal("G should follow again")
	}

	// Stale lines from a replaced stream are ignored.
	a.Update(logLinesMsg{gen: v.gen - 1, lines: []string{"stale"}})
	if strings.Contains(screen(a), "stale") {
		t.Fatal("old stream delivered lines")
	}

	a.Update(logLinesMsg{gen: v.gen, done: true})
	if !strings.Contains(screen(a), "the container stopped") {
		t.Errorf("end of stream:\n%s", screen(a))
	}
	press(a, 'q')
	if a.logView != nil {
		t.Fatal("q should close the viewer")
	}
}

func TestImagesView(t *testing.T) {
	a := dockerApp(t, 140)
	a.runner = &fakeRunner{out: map[string]string{"image ls --no-trunc --format json": readFixture(t, "images.jsonl")}}
	press(a, 'i')
	if a.docker.mode != modeImages || !a.docker.images.loading {
		t.Fatal("i should switch to images and load them")
	}
	imgs, err := dock.Images(context.Background(), a.runner)
	if err != nil {
		t.Fatal(err)
	}
	a.Update(imagesMsg{imgs: imgs})
	s := screen(a)
	for _, want := range []string{"IMAGE", "calliope-poker-app:latest", "322MB", "app-1", "<none>", "dangling", "unused"} {
		if !strings.Contains(s, want) {
			t.Errorf("images view missing %q:\n%s", want, s)
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(ansi.Strip(bar(a))), "IMAGES") {
		t.Errorf("bar: %q", ansi.Strip(bar(a)))
	}

	// The newest image is in use: x refuses and says by what.
	if img := a.selectedImage(); img.Repository != "calliope-poker-app" {
		t.Fatalf("selected %s", img.Ref())
	}
	press(a, 'x')
	if a.confirmDlg != nil || !strings.Contains(a.toast.text, "used by app-1") {
		t.Fatalf("x on a used image: dlg=%v toast=%q", a.confirmDlg, a.toast.text)
	}

	press(a, 'P')
	if a.confirmDlg == nil || !strings.Contains(screen(a), "Prune 1 dangling image?") {
		t.Fatalf("P should ask to prune:\n%s", screen(a))
	}
	press(a, 'n')

	// esc goes back to containers.
	a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if a.docker.mode != modeContainers {
		t.Fatal("esc should go back to containers")
	}
}

func TestVolumesView(t *testing.T) {
	a := dockerApp(t, 140)
	a.runner = &fakeRunner{out: map[string]string{"system df -v --format json": readFixture(t, "df.json")}}
	press(a, 'v')
	vols, err := dock.Volumes(context.Background(), a.runner)
	if err != nil {
		t.Fatal(err)
	}
	a.Update(volumesMsg{vols: vols})
	s := screen(a)
	for _, want := range []string{"VOLUME", "calliope-poker_pgdata", "194MB", "postgres-1", "unused"} {
		if !strings.Contains(s, want) {
			t.Errorf("volumes view missing %q:\n%s", want, s)
		}
	}
	// The first volume (an anonymous one) is unused: x asks.
	press(a, 'x')
	if a.confirmDlg == nil || !strings.Contains(screen(a), "deleted for good") {
		t.Fatalf("x on an unused volume should ask:\n%s", screen(a))
	}
	press(a, 'n')
	press(a, 'v')
	if a.docker.mode != modeContainers {
		t.Fatal("v again should go back")
	}
}

func TestStatsColumn(t *testing.T) {
	a := dockerApp(t, 160)
	press(a, 't')
	if !a.docker.statsOn {
		t.Fatal("t should turn stats on")
	}
	stats, err := dock.GetStats(context.Background(), &fakeRunner{out: map[string]string{
		"stats --no-stream --no-trunc --format json": readFixture(t, "stats.jsonl"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if cmd := a.handleStats(statsMsg{gen: a.docker.statsGen, stats: stats}); cmd == nil {
		t.Error("another sample should be scheduled")
	}
	s := screen(a)
	if !strings.Contains(s, "CPU") || !strings.Contains(s, "0.06%") || !strings.Contains(s, "MiB") {
		t.Fatalf("stats column:\n%s", s)
	}
	press(a, 't')
	if strings.Contains(screen(a), "0.06%") {
		t.Fatal("t again should hide stats")
	}
	if cmd := a.handleStats(statsMsg{gen: a.docker.statsGen - 1, stats: stats}); cmd != nil {
		t.Fatal("a sample from the old loop must not schedule another")
	}
}
