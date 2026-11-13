package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/dock"
	"github.com/bferg314/dockgit/internal/registry"
	"github.com/bferg314/dockgit/internal/updates"
)

func TestDiskView(t *testing.T) {
	a := dockerApp(t, 140)
	a.runner = &fakeRunner{out: map[string]string{"system df --format json": readFixture(t, "df-summary.jsonl")}}
	press(a, 'C')
	if a.docker.mode != modeDisk {
		t.Fatal("C should open disk use")
	}
	a.Update(diskMsg{usage: mustUsage(t, a)})
	s := screen(a)
	for _, want := range []string{"WHAT", "Images", "Build cache", "6.997GB", "RECLAIMABLE", "prune the build cache", "After each build"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	if b := ansi.Strip(bar(a)); !strings.Contains(b, "DISK") {
		t.Errorf("bar: %q", b)
	}
	press(a, 'b')
	if a.confirmDlg == nil || !strings.Contains(screen(a), "keeping up to 10GB") {
		t.Fatalf("b should ask:\n%s", screen(a))
	}
	press(a, 'n')
	press(a, 'C')
	if a.docker.mode != modeContainers {
		t.Fatal("C again goes back")
	}
}

func mustUsage(t *testing.T, a *App) []dock.Usage {
	t.Helper()
	us, err := dock.DiskUsage(context.Background(), a.runner)
	if err != nil {
		t.Fatal(err)
	}
	return us
}

func TestCleanupToasts(t *testing.T) {
	a := dockerApp(t, 120)
	a.Update(cleanupMsg{why: "after the build", cache: "1.2GB", images: "0B"})
	if a.toast.text != "Cleaned up after the build: freed 1.2GB of build cache" {
		t.Fatalf("toast: %q", a.toast.text)
	}
	a.toast.text = ""
	a.Update(cleanupMsg{why: "after the build", cache: "0B", images: "0B"})
	if a.toast.text != "" {
		t.Fatalf("nothing freed should stay quiet: %q", a.toast.text)
	}
	// Off means no cleanup command at all.
	a.cfg.Cleanup.Mode = config.CleanupOff
	if a.afterBuild() != nil {
		t.Fatal("cleanup is off")
	}
	a.cfg.Cleanup.Mode = config.CleanupAfterBuild
	if a.afterBuild() == nil {
		t.Fatal("after_build should clean up")
	}
}

func TestSettingsValues(t *testing.T) {
	a := testApp(t, 140, config.Default())
	press(a, '4')
	s := screen(a)
	for _, want := range []string{"Cleanup", "Automatic cleanup", "Keep build cache up to", "10GB", "Prune cache unused for", "168h", "Log lines to load", "500"} {
		if !strings.Contains(s, want) {
			t.Errorf("settings missing %q:\n%s", want, s)
		}
	}
	// Move to "Keep build cache up to" and change it.
	items := a.settingItems()
	for i, it := range items {
		if it.label == "Keep build cache up to" {
			a.settings.cursor = i
		}
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.valueDlg == nil {
		t.Fatal("enter should open the editor")
	}
	a.valueDlg.input.SetValue("lots")
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.valueDlg == nil || !strings.Contains(a.valueDlg.err, "a size like") {
		t.Fatalf("bad value should be refused: %+v", a.valueDlg)
	}
	a.valueDlg.input.SetValue("25GB")
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.valueDlg != nil || a.cfg.Cleanup.KeepStorage != "25GB" {
		t.Fatalf("not saved: %+v %q", a.valueDlg, a.cfg.Cleanup.KeepStorage)
	}
	if data, _ := os.ReadFile(a.cfgPath); !strings.Contains(string(data), `keep_storage = "25GB"`) {
		t.Fatalf("config file:\n%s", data)
	}
}

// fakeReg answers tags and digests from tables.
type fakeReg struct{ tags map[string][]string }

func (f fakeReg) Tags(_ context.Context, ref registry.Ref) ([]string, error) {
	return f.tags[ref.Repo], nil
}
func (f fakeReg) Digest(context.Context, registry.Ref) (string, error) { return "sha256:x", nil }

func TestUpdateCheckAndBump(t *testing.T) {
	a, root, _ := composeApp(t)
	a.registry = fakeReg{tags: map[string][]string{"linuxserver/plex": {"1.40", "1.41", "2.0"}}}
	a.runner = &fakeRunner{} // nothing pulled locally
	s := selectStack(t, a, "stacks/media/plex")
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	p := newPump(a)
	defer p.stop()
	p.run(cmd)
	p.until(t, 5*time.Second, "check", func() bool { return a.popup != nil })
	text := screen(a)
	for _, want := range []string{"Updates for stacks/media/plex", "linuxserver/plex:1.40 → 1.41", "2.0 is out", "Bump 1 version in compose.yaml"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !strings.Contains(screen(a), "↑1 update") {
		t.Errorf("the row should show the update:\n%s", screen(a))
	}
	_, cmd = a.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	p.run(cmd)
	p.until(t, 5*time.Second, "check again", func() bool { return a.popup != nil })
	_, cmd = a.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	p.run(cmd)
	data, _ := os.ReadFile(filepath.Join(root, "stacks", "media", "plex", "compose.yaml"))
	if !strings.Contains(string(data), "image: linuxserver/plex:1.41") {
		t.Fatalf("not bumped:\n%s", data)
	}
	if !strings.Contains(a.toast.text, "Bumped plex:1.41") {
		t.Errorf("toast: %q", a.toast.text)
	}
	_ = s
}

// After a successful pull and up, a newer image for the same tag is on the
// machine, so its update note clears. A newer version stays until bumped.
func TestPullAndUpClearsPulledUpdates(t *testing.T) {
	for _, fail := range []bool{false, true} {
		a, root, rec := composeApp(t)
		writeFiles(t, root, map[string]string{".env": "PUID=1000\n", "stacks/media/plex/.env": "PLEX_CLAIM=x\n"})
		if fail {
			rec.fail = "pull"
		}
		s := selectStack(t, a, "stacks/media/plex")
		s.updates = []updates.Result{
			{Service: "plex", Image: "linuxserver/plex:1.40", State: updates.NewImage},
			{Service: "web", Image: "nginx:1.26", State: updates.NewVersion, Suggest: "nginx:1.27"},
		}
		if !strings.Contains(screen(a), "↑2 updates") {
			t.Fatalf("before: want ↑2 updates:\n%s", screen(a))
		}
		_, cmd := a.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
		p := newPump(a)
		p.run(cmd)
		p.until(t, 5*time.Second, "pull and up finished", func() bool {
			st := a.findStack(s.File)
			return st.job != nil && st.job.done
		})
		p.stop()
		a.logView, a.popup = nil, nil
		want := "↑1 update"
		if fail {
			want = "↑2 updates"
		}
		if got := screen(a); !strings.Contains(got, want) {
			t.Errorf("fail=%v: want %q:\n%s", fail, want, got)
		}
	}
}
