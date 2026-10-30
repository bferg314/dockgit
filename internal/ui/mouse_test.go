package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/dockgit/internal/config"
)

// mouseAt sends a mouse event at (x, y) after drawing the screen, as the
// program does before any event can arrive.
func mouseAt(a *App, msg func(tea.Mouse) tea.Msg, x, y int, b tea.MouseButton) tea.Cmd {
	a.render()
	_, cmd := a.Update(msg(tea.Mouse{X: x, Y: y, Button: b}))
	return cmd
}

func click(a *App, x, y int) tea.Cmd {
	return mouseAt(a, func(m tea.Mouse) tea.Msg { return tea.MouseClickMsg(m) }, x, y, tea.MouseLeft)
}

func rightClick(a *App, x, y int) tea.Cmd {
	return mouseAt(a, func(m tea.Mouse) tea.Msg { return tea.MouseClickMsg(m) }, x, y, tea.MouseRight)
}

func wheel(a *App, x, y int, down bool) tea.Cmd {
	b := tea.MouseWheelUp
	if down {
		b = tea.MouseWheelDown
	}
	return mouseAt(a, func(m tea.Mouse) tea.Msg { return tea.MouseWheelMsg(m) }, x, y, b)
}

// find is where text first appears on the screen.
func find(t *testing.T, a *App, text string) (x, y int) {
	t.Helper()
	for y, l := range strings.Split(screen(a), "\n") {
		if i := strings.Index(l, text); i >= 0 {
			return ansi.StringWidth(l[:i]), y
		}
	}
	t.Fatalf("%q isn't on the screen:\n%s", text, screen(a))
	return 0, 0
}

// clickText clicks the first place text appears, and forgets the click so
// the next one isn't a double click.
func clickText(t *testing.T, a *App, text string) tea.Cmd {
	t.Helper()
	x, y := find(t, a, text)
	cmd := click(a, x, y)
	a.mouse.lastAt = time.Time{}
	return cmd
}

func doubleClickText(t *testing.T, a *App, text string) tea.Cmd {
	t.Helper()
	x, y := find(t, a, text)
	click(a, x, y)
	cmd := click(a, x, y)
	a.mouse.lastAt = time.Time{}
	return cmd
}

func cursorContainer(a *App) string {
	if c := a.docker.selected(); c != nil {
		return displayName(c)
	}
	return ""
}

func TestMouseTabs(t *testing.T) {
	a := dockerApp(t, 140)
	for i, name := range []string{"Repos", "Compose", "Settings", "Docker"} {
		clickText(t, a, name)
		if want := []int{tabRepos, tabCompose, tabSettings, tabDocker}[i]; a.tab != want {
			t.Fatalf("clicking %s: tab %d", name, a.tab)
		}
	}
}

func TestMouseContainerRows(t *testing.T) {
	a := dockerApp(t, 140)

	clickText(t, a, "redis-1")
	if cursorContainer(a) != "redis-1" || a.popup != nil {
		t.Fatalf("a click moves the cursor and nothing else: %s popup=%v", cursorContainer(a), a.popup != nil)
	}
	doubleClickText(t, a, "postgres-1")
	if cursorContainer(a) != "postgres-1" || a.popup == nil {
		t.Fatalf("a double click opens the actions: %s popup=%v", cursorContainer(a), a.popup != nil)
	}

	// The popup is modal: a click on it that isn't on an action does
	// nothing, even with a row behind it, and a click outside closes it.
	x, y := find(t, a, "Restart")
	click(a, x-6, y-4)
	a.mouse.lastAt = time.Time{}
	if a.popup == nil || cursorContainer(a) != "postgres-1" {
		t.Fatalf("a click inside the popup: popup=%v cursor=%s", a.popup != nil, cursorContainer(a))
	}
	click(a, 0, a.h-2)
	a.mouse.lastAt = time.Time{}
	if a.popup != nil {
		t.Fatal("a click outside the popup should close it")
	}

	x, y = find(t, a, "old-nginx")
	rightClick(a, x, y)
	if cursorContainer(a) != "old-nginx" || a.popup == nil {
		t.Fatalf("a right click opens the actions: %s popup=%v", cursorContainer(a), a.popup != nil)
	}
	clickText(t, a, "Logs")
	if a.popup != nil || a.logView == nil {
		t.Fatalf("a click on an action runs it: popup=%v logs=%v", a.popup != nil, a.logView != nil)
	}
}

func TestMouseDoubleClickAfterLayoutChange(t *testing.T) {
	a := dockerApp(t, 140)
	press(a, '/') // an empty filter line, which goes when a row is clicked

	x, y := find(t, a, "postgres-1")
	click(a, x, y)
	if a.docker.filter.Focused() {
		t.Fatal("a click on a row should leave the filter")
	}
	click(a, x, y) // now on the row below
	if a.popup != nil {
		t.Fatalf("the second click landed on another row and should only move: cursor %s", cursorContainer(a))
	}
	if cursorContainer(a) != "redis-1" {
		t.Fatalf("cursor on %s, want the row now under the pointer", cursorContainer(a))
	}
}

func TestMouseWheel(t *testing.T) {
	a := dockerApp(t, 140)
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 14})

	wheel(a, 5, 5, true)
	if cursorContainer(a) != "postgres-1" {
		t.Fatalf("the wheel over the list moves it: %s", cursorContainer(a))
	}
	wheel(a, 5, 5, false)
	if cursorContainer(a) != "app-1" {
		t.Fatalf("the wheel over the list moves it back: %s", cursorContainer(a))
	}

	wheel(a, a.w-5, 8, true)
	if a.detail.scroll != wheelLines || cursorContainer(a) != "app-1" {
		t.Fatalf("the wheel over the pane scrolls it: scroll %d cursor %s", a.detail.scroll, cursorContainer(a))
	}
	wheel(a, a.w-5, 8, false)
	if a.detail.scroll != 0 {
		t.Fatalf("scroll %d after scrolling back", a.detail.scroll)
	}
}

func TestMouseSettings(t *testing.T) {
	a := testApp(t, 160, config.Default())
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	press(a, '4')

	clickText(t, a, "Powerline status bar")
	if a.cfg.Powerline {
		t.Fatal("a click on a setting's name only moves to it")
	}
	if it := a.settingItems()[a.settings.cursor]; it.label != "Powerline status bar" {
		t.Fatalf("cursor on %q", it.label)
	}
	x, y := find(t, a, "Powerline status bar")
	click(a, x-4, y) // its checkbox
	if !a.cfg.Powerline {
		t.Fatal("a click on the checkbox should change it")
	}
}

func TestMouseStatusBarHelp(t *testing.T) {
	a := dockerApp(t, 140)
	clickText(t, a, "? help")
	if !a.help.open {
		t.Fatal("? help in the status bar should open help")
	}
	click(a, 0, 0)
	if a.help.open {
		t.Fatal("a click outside help should close it")
	}
}

func TestMouseConfirm(t *testing.T) {
	a := dockerApp(t, 140)
	clickText(t, a, "old-nginx")
	press(a, 'x')
	if a.confirmDlg == nil {
		t.Fatal("x should ask first")
	}
	x, y := find(t, a, "n no")
	click(a, x, y)
	if a.confirmDlg != nil {
		t.Fatal("a click on n should cancel")
	}
}

func TestMouseTypingDialogStaysOpen(t *testing.T) {
	a := dockerApp(t, 140)
	press(a, '2')
	press(a, 'n')
	if a.addRepo == nil {
		t.Fatal("n should open the add-repo dialog")
	}
	click(a, 0, a.h-2)
	if a.addRepo == nil {
		t.Fatal("a click outside a dialog being typed in mustn't close it")
	}
}

func TestMouseOff(t *testing.T) {
	a := dockerApp(t, 140)
	a.cfg.Mouse = false
	clickText(t, a, "Repos")
	if a.tab != tabDocker {
		t.Fatal("with the mouse off, clicks do nothing")
	}
	if a.View().MouseMode != tea.MouseModeNone {
		t.Fatal("with the mouse off, dockgit shouldn't ask for mouse events")
	}
}
