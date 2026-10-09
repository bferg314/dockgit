package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/dock"
	"github.com/bferg314/dockgit/internal/launcher"
)

type (
	// actionDoneMsg reports a container action. The new state itself
	// arrives through the event stream.
	actionDoneMsg struct {
		id, done string // done: "Stopped app-1"
		err      error
	}
	shellMsg struct {
		c     *dock.Container
		shell string
		err   error
	}
	statsMsg struct {
		gen   int
		stats map[string]dock.Stats
		err   error
	}
	toolExitMsg struct {
		name string
		err  error
	}
)

// containerAction runs f on c in the background, marking c busy with verb
// ("stopping") until it returns.
func (a *App) containerAction(c *dock.Container, verb, done string, f func(context.Context, dock.Runner, string) error) tea.Cmd {
	t := &a.docker
	if t.busy[c.ID] != "" {
		return nil
	}
	t.busy[c.ID] = verb
	r, id, name := a.runner, c.ID, displayName(c)
	return tea.Batch(a.startSpinner(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return actionDoneMsg{id: id, done: done + " " + name, err: f(ctx, r, id)}
	})
}

func (a *App) toggleRunning(c *dock.Container) tea.Cmd {
	if c.Running() || c.State == dock.StateRestarting || c.State == dock.StatePaused {
		return a.containerAction(c, "stopping", "Stopped", dock.Stop)
	}
	return a.containerAction(c, "starting", "Started", dock.Start)
}

func (a *App) restartContainer(c *dock.Container) tea.Cmd {
	return a.containerAction(c, "restarting", "Restarted", dock.Restart)
}

func (a *App) removeContainer(c *dock.Container) tea.Cmd {
	name := displayName(c)
	body := []string{c.Name + "  (" + c.Image + ")"}
	running := c.State == dock.StateRunning || c.State == dock.StateRestarting || c.State == dock.StatePaused
	if running {
		body = append(body, "It's running: it will be stopped first.")
	}
	return a.confirm("Remove container "+name+"?", body, func() tea.Cmd {
		return a.containerAction(c, "removing", "Removed", func(ctx context.Context, r dock.Runner, id string) error {
			return dock.Remove(ctx, r, id, running)
		})
	})
}

// openShell finds a shell in c, then hands the terminal to it.
func (a *App) openShell(c *dock.Container) tea.Cmd {
	if !c.Running() {
		return a.notify(2, "%s isn't running", displayName(c))
	}
	r := a.runner
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		sh, err := dock.Shell(ctx, r, c.ID)
		return shellMsg{c: c, shell: sh, err: err}
	}
}

func (a *App) handleShell(msg shellMsg) tea.Cmd {
	name := displayName(msg.c)
	switch {
	case errors.Is(msg.err, dock.ErrNoShell):
		return a.notify(2, "%s has no shell (sh) to open", name)
	case msg.err != nil:
		return a.notify(2, "Shell in %s: %v", name, msg.err)
	}
	tool := config.Tool{Name: name + " shell", Cmd: "docker", Args: dock.ExecArgs(msg.c.ID, msg.shell), Mode: config.ModeTerminal}
	return a.launch(tool, shellDir(msg.c))
}

// shellDir is where a shell is started from: the compose project's folder
// when it exists here, otherwise home.
func shellDir(c *dock.Container) string {
	if d := c.WorkingDir(); d != "" {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "."
}

// portURL is the address of c's first published port.
func portURL(c *dock.Container) string {
	ps := c.PublishedPorts()
	if len(ps) == 0 {
		return ""
	}
	host := "localhost"
	if ip := ps[0].HostIP; ip != "" {
		host = ip
	}
	return "http://" + host + ":" + ps[0].HostPort
}

// openURL opens a link in the default browser; tests replace it.
var openURL = launcher.OpenURL

func (a *App) openPort(c *dock.Container) tea.Cmd {
	url := portURL(c)
	if url == "" {
		return a.notify(2, "%s has no published ports", displayName(c))
	}
	if err := openURL(url); err != nil {
		return a.notify(2, "Opening %s: %v", url, err)
	}
	return a.notify(1, "Opened %s", url)
}

// projectDir is the container's compose project folder, if it's on this
// machine.
func projectDir(c *dock.Container) string {
	if d := c.WorkingDir(); d != "" {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return ""
}

// editorKey opens things in the tool bound to it: VS Code by default.
const editorKey = "o"

// editor is the tool things open in: the enabled tool bound to editorKey,
// or else the terminal's editor (see terminalEditor). folders reports
// whether it can open a folder, not just a file.
func (a *App) editor() (t config.Tool, folders bool) {
	for _, t := range a.enabledTools() {
		if t.Key == editorKey {
			return t, true
		}
	}
	return terminalEditor()
}

// terminalEditor is $VISUAL or $EDITOR (which may carry arguments, such
// as "code --wait"), or else vi (Notepad on Windows): what's there on a
// server reached over SSH. Only an editor someone chose is trusted with
// folders; plain vi and Notepad can't open one.
func terminalEditor() (config.Tool, bool) {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if f := strings.Fields(os.Getenv(env)); len(f) > 0 {
			return config.Tool{Name: "$" + env + " (" + filepath.Base(f[0]) + ")", Cmd: f[0],
				Args: append(f[1:], "{path}"), Mode: config.ModeTerminal}, true
		}
	}
	if runtime.GOOS == "windows" {
		return config.Tool{Name: "Notepad", Cmd: "notepad", Args: []string{"{path}"}, Mode: config.ModeDetach}, false
	}
	return config.Tool{Name: "vi", Cmd: "vi", Args: []string{"{path}"}, Mode: config.ModeTerminal}, false
}

// openInEditor opens a file or folder in the editor.
func (a *App) openInEditor(name, path string) tea.Cmd {
	if path == "" {
		return a.notify(2, "%s has no project folder on this machine", name)
	}
	t, folders := a.editor()
	if fi, err := os.Stat(path); err == nil && fi.IsDir() && !folders {
		return a.notify(2, "No tool is bound to %s: turn one on in Settings (or set $EDITOR) to open folders", editorKey)
	}
	return a.launch(t, path)
}

// containerPopup lists what can be done to c.
func (a *App) containerPopup(c *dock.Container) *actionPopup {
	p := &actionPopup{title: c.Name}
	add := func(key, label, note string, run func() tea.Cmd) {
		p.actions = append(p.actions, popupAction{key: key, label: label, note: note, run: run})
	}
	add("l", "Logs", "follow full screen", func() tea.Cmd { return a.openLogs(c) })
	if c.Running() {
		add("e", "Shell", "docker exec", func() tea.Cmd { return a.openShell(c) })
	}
	if url := portURL(c); url != "" {
		add("w", "Open in browser", url, func() tea.Cmd { return a.openPort(c) })
	}
	if c.Running() || c.State == dock.StateRestarting || c.State == dock.StatePaused {
		add("s", "Stop", "", func() tea.Cmd { return a.toggleRunning(c) })
		add("S", "Restart", "", func() tea.Cmd { return a.restartContainer(c) })
	} else {
		add("s", "Start", "", func() tea.Cmd { return a.toggleRunning(c) })
	}
	add("x", "Remove", "", func() tea.Cmd { return a.removeContainer(c) })

	// Tools open the compose project's folder, when it's on this machine.
	if d := projectDir(c); d != "" {
		taken := map[string]bool{}
		for _, act := range p.actions {
			taken[act.key] = true
		}
		for _, tool := range a.enabledTools() {
			key := tool.Key
			if taken[key] {
				key = ""
			}
			taken[key] = true
			add(key, "Open project in "+tool.Name, filepath.Base(d), func() tea.Cmd { return a.launch(tool, d) })
		}
	}
	return p
}

// ---- stats ----

// loadStats takes one sample; handleStats schedules the next while the
// column is on. docker stats itself takes about two seconds, so samples
// arrive every three or so.
func (a *App) loadStats() tea.Cmd {
	r, gen := a.runner, a.docker.statsGen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		stats, err := dock.GetStats(ctx, r)
		return statsMsg{gen: gen, stats: stats, err: err}
	}
}

func (a *App) toggleStats() tea.Cmd {
	t := &a.docker
	t.statsOn = !t.statsOn
	t.statsGen++
	t.stats = nil
	if t.statsOn {
		return a.loadStats()
	}
	return nil
}

func (a *App) handleStats(msg statsMsg) tea.Cmd {
	t := &a.docker
	if msg.gen != t.statsGen || !t.statsOn {
		return nil
	}
	if msg.err == nil {
		t.stats = msg.stats
	}
	gen := t.statsGen
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return statsTickMsg{gen} })
}

type statsTickMsg struct{ gen int }

// ---- tools ----

// launch runs tool t in path.
func (a *App) launch(t config.Tool, path string) tea.Cmd {
	if !config.Available(t.Cmd) {
		return a.notify(2, "%s: %q not found on PATH", t.Name, t.Cmd)
	}
	if a.inZellijTab(t) {
		cmd := launcher.ZellijTab(t, path)
		wait := func() tea.Msg {
			if out, err := cmd.CombinedOutput(); err != nil {
				return toolExitMsg{name: t.Name, err: launcher.ZellijError(out, err)}
			}
			return toolExitMsg{name: t.Name}
		}
		return tea.Batch(wait, a.notify(1, "Opened %s in a new tab", t.Name))
	}
	cmd := launcher.Command(t, path)
	if t.Mode == config.ModeTerminal {
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			return toolExitMsg{name: t.Name, err: err}
		})
	}
	if err := launcher.Detach(cmd); err != nil {
		return a.notify(2, "%s: %v", t.Name, err)
	}
	return a.notify(1, "Opened %s in %s", filepath.Base(path), t.Name)
}

// inZellijTab reports whether tool t opens in a new zellij tab rather than
// suspending dockgit.
func (a *App) inZellijTab(t config.Tool) bool {
	return t.Mode == config.ModeTerminal && !t.InPlace && a.cfg.ZellijTabs && launcher.InZellij()
}
