package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/dockgit/internal/compose"
	"github.com/bferg314/dockgit/internal/config"
)

func testApp(t *testing.T, w int, cfg *config.Config) *App {
	t.Helper()
	// Never open a real editor (Notepad, vi) from a test: with no tool
	// bound to o, files fall back to $VISUAL, which here doesn't exist.
	t.Setenv("VISUAL", noEditor)
	// Nor touch the real clipboard.
	old := writeClipboard
	writeClipboard = func(string) error { return nil }
	t.Cleanup(func() { writeClipboard = old })
	a := New(cfg, filepath.Join(t.TempDir(), "config.toml"), &fakeRunner{})
	a.Update(tea.WindowSizeMsg{Width: w, Height: 24})
	return a
}

func press(a *App, key rune) {
	a.Update(tea.KeyPressMsg{Code: key, Text: string(key)})
}

func screen(a *App) string { return ansi.Strip(a.render()) }

func bar(a *App) string {
	lines := strings.Split(a.render(), "\n")
	return lines[len(lines)-1]
}

func TestTabsSwitch(t *testing.T) {
	a := testApp(t, 120, config.Default())
	for i, mode := range []string{"DOCKER", "REPOS", "COMPOSE", "SETTINGS"} {
		press(a, rune('1'+i))
		if a.tab != i || !strings.HasPrefix(strings.TrimSpace(ansi.Strip(bar(a))), mode) {
			t.Fatalf("key %d: tab=%d bar=%q", i+1, a.tab, ansi.Strip(bar(a)))
		}
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if a.tab != tabDocker {
		t.Fatalf("tab should wrap to Docker, got %d", a.tab)
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if a.tab != tabSettings {
		t.Fatalf("shift+tab should wrap to Settings, got %d", a.tab)
	}
}

func TestHeaderCounts(t *testing.T) {
	cfg := config.Default()
	cfg.Repos = []config.Repo{{Path: "~/code/clock"}, {Path: "~/code/face-to-face"}}
	cfg.ComposeRoots = []string{"~/code/stacks"}
	a := testApp(t, 120, cfg)
	a.Update(stacksMsg{key: "~/code/stacks", stacks: []compose.Stack{{Rel: "a/compose.yaml"}, {Rel: "b/compose.yaml"}}})
	head := strings.Split(screen(a), "\n")[0]
	for _, want := range []string{"dockgit", "Docker", "Repos 2", "Compose 2", "Settings"} {
		if !strings.Contains(head, want) {
			t.Errorf("header missing %q: %q", want, head)
		}
	}
}

func TestHelpOverlay(t *testing.T) {
	a := testApp(t, 120, config.Default())
	press(a, '?')
	if !a.help.open || !strings.Contains(screen(a), "switch tabs") {
		t.Fatalf("help should open:\n%s", screen(a))
	}
	press(a, 'x')
	if a.help.open {
		t.Fatal("any key should close help")
	}
}

// The help shows only the keys for what's on screen.
func TestHelpFollowsTheScreen(t *testing.T) {
	a := dockerApp(t, 120)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	helpFor := func() string {
		press(a, '?')
		s := screen(a)
		press(a, 'x')
		return s
	}
	cases := []struct {
		setup     func()
		want, not []string
	}{
		{func() {}, []string{"Containers", "start or stop", "Status", "Keys for Docker"}, []string{"switch branch", "prune dangling"}},
		{func() { press(a, 'i') }, []string{"Images", "prune dangling", "Keys for Images"}, []string{"start or stop"}},
		{func() { press(a, 'i'); press(a, '2') }, []string{"Repos", "switch branch", "Keys for Repos"}, []string{"start or stop", "Status"}},
		{func() { press(a, '4') }, []string{"Settings", "toggle"}, []string{"switch branch", "start or stop"}},
		{func() { press(a, '1'); press(a, 'l') }, []string{"Logs", "follow again", "wrap long lines"}, []string{"switch tabs", "start or stop"}},
	}
	for i, c := range cases {
		c.setup()
		s := helpFor()
		for _, w := range c.want {
			if !strings.Contains(s, w) {
				t.Errorf("case %d: missing %q:\n%s", i, w, s)
			}
		}
		for _, n := range c.not {
			if strings.Contains(s, n) {
				t.Errorf("case %d: should not show %q:\n%s", i, n, s)
			}
		}
	}
}

// In a terminal too short for it, the help scrolls instead of being cut
// off, and j/k scroll rather than close it.
func TestHelpScrollsWhenShort(t *testing.T) {
	a := dockerApp(t, 80)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 16})
	press(a, '?')
	s := screen(a)
	if a.help.maxScroll == 0 || !strings.Contains(s, "j/k ↕ 0%") || !strings.Contains(s, "Everywhere") {
		t.Fatalf("expected a scrolling help:\n%s", s)
	}
	for range a.help.maxScroll {
		press(a, 'j')
	}
	if !a.help.open || !strings.Contains(screen(a), "j/k ↕ 100%") || !strings.Contains(screen(a), "reload from Docker") {
		t.Fatalf("j should scroll to the end:\n%s", screen(a))
	}
	for i, line := range strings.Split(a.render(), "\n") {
		if w := ansi.StringWidth(line); w > 80 {
			t.Fatalf("line %d is %d wide", i, w)
		}
	}
	press(a, 'x')
	if a.help.open {
		t.Fatal("other keys close it")
	}
}

// The bar is always exactly the terminal width, keeps "? help", and never
// loses a message.
func TestStatusBarFitsEveryWidth(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // Settings looks up every tool on PATH per render
	for _, pl := range []bool{false, true} {
		for w := 30; w <= 200; w++ {
			cfg := config.Default()
			cfg.Powerline = pl
			a := testApp(t, w, cfg)
			a.setTab(tabSettings)
			a.notify(2, "Build clock failed: port 8080 is already allocated")
			b := bar(a)
			if got := ansi.StringWidth(b); got != w {
				t.Fatalf("w=%d powerline=%v: bar is %d wide: %q", w, pl, got, ansi.Strip(b))
			}
			s := ansi.Strip(b)
			if !strings.Contains(s, "? help") || !strings.Contains(s, "✗ Bu") {
				t.Fatalf("w=%d powerline=%v: lost help or message: %q", w, pl, s)
			}
		}
	}
}

func TestSettingsToggleSaves(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cfg := config.Default()
	a := testApp(t, 140, cfg)
	press(a, '4')
	if !strings.Contains(screen(a), "Show stopped containers") {
		t.Fatalf("settings not shown:\n%s", screen(a))
	}

	// Rows: Compose roots, Zellij tabs, Confirm destructive, Show stopped.
	for range 3 {
		press(a, 'j')
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if cfg.ShowStopped {
		t.Fatal("space should turn Show stopped containers off")
	}
	if data, err := os.ReadFile(a.cfgPath); err != nil || !strings.Contains(string(data), "show_stopped = false") {
		t.Fatalf("config not saved: %v\n%s", err, data)
	}

	// Build cache cleanup cycles through its modes.
	for range 3 {
		press(a, 'j')
	}
	for _, want := range []string{config.CleanupThreshold, config.CleanupOff, config.CleanupAfterBuild} {
		a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cfg.Cleanup.Mode != want {
			t.Fatalf("cleanup mode = %q, want %q", cfg.Cleanup.Mode, want)
		}
	}
}

// Opening Settings picks up a tool installed while dockgit was running.
func TestSettingsTabEnablesNewlyInstalledTool(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("PATHEXT", ".EXE")
	cfg := &config.Config{Version: 1, Tools: []config.Tool{
		{Name: "lazydocker", Cmd: "lazydocker", Key: "d", Mode: config.ModeTerminal, AutoDisabled: true},
	}}
	a := testApp(t, 120, cfg)

	name := "lazydocker"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	press(a, '4')
	if !cfg.Tools[0].Enabled || !strings.Contains(a.toast.text, "lazydocker") {
		t.Fatalf("lazydocker should be enabled with a toast; enabled=%v toast=%q", cfg.Tools[0].Enabled, a.toast.text)
	}
}

// noEditor is a command that isn't installed anywhere.
const noEditor = "dockgit-test-no-editor"
