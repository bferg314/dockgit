package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/dockgit/internal/config"
)

// captureClipboard records what's written to the local clipboard.
func captureClipboard(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := writeClipboard
	writeClipboard = func(s string) error { got = append(got, s); return nil }
	t.Cleanup(func() { writeClipboard = old })
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	return &got
}

func TestCopyContainer(t *testing.T) {
	a := dockerApp(t, 140)
	got := captureClipboard(t)
	press(a, 'y')
	if a.popup == nil || !strings.Contains(screen(a), "Copy from calliope-poker-app-1") || !strings.Contains(screen(a), "http://localhost:8080") {
		t.Fatalf("y should offer what to copy:\n%s", screen(a))
	}
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if len(*got) != 1 || (*got)[0] != "http://localhost:8080" || a.toast.text != "Copied URL" {
		t.Fatalf("copied %q, toast %q", *got, a.toast.text)
	}
	// The terminal gets it too (OSC 52), as a command for the runtime.
	if cmd == nil {
		t.Fatal("expected the clipboard command")
	}

	// Over SSH only the terminal's clipboard is used.
	t.Setenv("SSH_CONNECTION", "10.0.0.1 22 10.0.0.2 22")
	press(a, 'y')
	press(a, 'n')
	if len(*got) != 1 {
		t.Fatalf("over SSH the local clipboard must be left alone: %q", *got)
	}
}

func TestCopyStackAndLogs(t *testing.T) {
	a, root, _ := composeApp(t)
	got := captureClipboard(t)
	selectStack(t, a, "stacks/media/plex")
	press(a, 'y')
	press(a, 'c')
	want := "docker compose -f compose.yaml --progress plain up -d"
	if len(*got) != 1 || !strings.Contains((*got)[0], want) || !strings.Contains((*got)[0], "plex") {
		t.Fatalf("up command: %q", *got)
	}
	_ = root

	// In the logs viewer, y copies the lines on screen, whole.
	b := dockerApp(t, 60)
	got = captureClipboard(t) // after dockerApp, which stubs the clipboard
	press(b, 'l')
	b.Update(logLinesMsg{gen: b.logView.gen, lines: []string{
		"2026-10-08T14:12:16.813046562Z first",
		"2026-10-08T14:12:17.000000000Z " + strings.Repeat("long ", 30),
	}})
	press(b, 'y')
	if last := (*got)[len(*got)-1]; !strings.HasPrefix(last, "first\nlong long") || strings.Count(last, "\n") != 1 {
		t.Fatalf("log lines: %q", last)
	}
}

func TestComposeOverridesApply(t *testing.T) {
	a, root, rec := composeApp(t)
	plex := filepath.Join(root, "stacks", "media", "plex")
	writeFiles(t, plex, map[string]string{"host.env": "PUID=1\nPLEX_CLAIM=x\n"})
	a.cfg.ComposeOverrides = map[string]config.ComposeOverride{
		filepath.Join(plex, "compose.yaml"): {Project: "media", EnvFiles: []string{"host.env"}},
	}
	s := selectStack(t, a, "stacks/media/plex")
	if m := a.stackMissing(s); len(m) != 0 {
		t.Fatalf("host.env sets everything: %q", m)
	}
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'U', Text: "U"})
	p := newPump(a)
	defer p.stop()
	p.run(cmd)
	p.until(t, 5*time.Second, "up", func() bool { return s.job != nil && s.job.done })
	got := rec.all()
	want := "docker compose -f compose.yaml -p media --env-file " + filepath.Join(plex, "host.env") + " --progress plain up -d"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("steps:\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(screen(a), "media  (compose_overrides)") {
		t.Errorf("detail should name the override:\n%s", screen(a))
	}
}

func TestThresholdCheckRunsHourly(t *testing.T) {
	a := dockerApp(t, 120)
	_, cmd := a.Update(cleanupTickMsg{})
	if cmd == nil {
		t.Fatal("a tick should schedule the next one")
	}
	// Outside threshold mode the check itself does nothing.
	a.cfg.Cleanup.Mode = config.CleanupAfterBuild
	if a.thresholdCleanup() != nil {
		t.Fatal("only threshold mode checks the cache")
	}
	a.cfg.Cleanup.Mode = config.CleanupThreshold
	if a.thresholdCleanup() == nil {
		t.Fatal("threshold mode should check")
	}
	if cleanupEvery != time.Hour {
		t.Fatalf("checks every %v", cleanupEvery)
	}
}
