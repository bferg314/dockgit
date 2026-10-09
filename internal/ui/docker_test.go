package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/dock"
)

// fakeRunner answers every inspect with the recorded fixture, other
// commands from out (keyed by the joined args), and logs with a fixed tail.
type fakeRunner struct {
	inspect string
	out     map[string]string
}

func (f *fakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "inspect" {
		return []byte(f.inspect), nil
	}
	return []byte(f.out[strings.Join(args, " ")]), nil
}

func (f *fakeRunner) Combined(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "logs" {
		return []byte("server listening on :3000\n"), nil
	}
	return nil, nil
}

func (f *fakeRunner) Stream(context.Context, string, ...string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (f *fakeRunner) StreamCombined(context.Context, string, ...string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

const (
	appID   = "fc468a524c81e8d4e8b53f201d60273a63a0d30ef783b92ca5adb80870913e2a"
	nginxID = "9a1b2c3d4e5f0000000000000000000000000000000000000000000000000000"
)

// fixtureContainers parses the dock package's recorded inspect output:
// calliope-poker's app, postgres and redis (running), and old-nginx
// (unmanaged, exited 137).
func fixtureContainers(t *testing.T) []*dock.Container {
	t.Helper()
	b, err := os.ReadFile("../dock/testdata/inspect.json")
	if err != nil {
		t.Fatal(err)
	}
	cs, _, err := dock.Inspect(context.Background(), &fakeRunner{inspect: string(b)}, appID)
	if err != nil || len(cs) != 4 {
		t.Fatalf("fixture: %d containers, %v", len(cs), err)
	}
	return cs
}

func dockerApp(t *testing.T, w int) *App {
	t.Helper()
	a := testApp(t, w, config.Default())
	a.Update(containersMsg{cs: fixtureContainers(t)})
	return a
}

func TestDockerListGroupsByProject(t *testing.T) {
	a := dockerApp(t, 140)
	s := screen(a)
	for _, want := range []string{
		"calliope-poker  3/3 running", "other containers  0/1 running",
		"app-1", "postgres-1", "redis-1", "old-nginx",
		"calliope-poker-app", "8080→3000", "127.0.0.1:5432→5432",
		"up ", "healthy", "exited 137",
		"Docker 3", // running count on the tab
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	// Compose groups come before unmanaged containers, and the cursor
	// starts on the first container, not a heading.
	if strings.Index(s, "calliope-poker  3/3") > strings.Index(s, "other containers") {
		t.Error("compose projects should be listed first")
	}
	if c := a.docker.selected(); c == nil || c.Name != "calliope-poker-app-1" {
		t.Fatalf("cursor should start on app-1, got %+v", c)
	}
	b := ansi.Strip(bar(a))
	for _, want := range []string{"DOCKER", "running", "●3 running", "○1 stopped", "1/4"} {
		if !strings.Contains(b, want) {
			t.Errorf("bar missing %q: %q", want, b)
		}
	}
}

func TestDockerCursorSkipsHeadings(t *testing.T) {
	a := dockerApp(t, 140)
	for range 3 {
		press(a, 'j')
	}
	if c := a.docker.selected(); c == nil || c.Name != "old-nginx" {
		t.Fatalf("j past the last compose row should land on old-nginx, got %+v", c)
	}
	press(a, 'j')
	if c := a.docker.selected(); c.Name != "old-nginx" {
		t.Fatal("the cursor should stop at the last container")
	}
	press(a, 'k')
	if c := a.docker.selected(); c.Name != "calliope-poker-redis-1" {
		t.Fatalf("k should skip the heading back to redis, got %s", c.Name)
	}
}

func TestDockerHideStopped(t *testing.T) {
	a := dockerApp(t, 140)
	press(a, 'a')
	s := screen(a)
	if strings.Contains(s, "old-nginx") || strings.Contains(s, "other containers") {
		t.Fatalf("a should hide stopped containers and their empty group:\n%s", s)
	}
	if !strings.Contains(a.toast.text, "Hiding 1 stopped container") {
		t.Errorf("toast: %q", a.toast.text)
	}
	press(a, 'a')
	if !strings.Contains(screen(a), "old-nginx") {
		t.Fatal("a again should show them")
	}
}

func TestDockerFilter(t *testing.T) {
	a := dockerApp(t, 140)
	press(a, '/')
	for _, r := range "nginx" {
		press(a, r)
	}
	s := screen(a)
	if !strings.Contains(s, "old-nginx") || strings.Contains(s, "postgres-1") {
		t.Fatalf("filter should keep only nginx:\n%s", s)
	}
	if c := a.docker.selected(); c == nil || c.Name != "old-nginx" {
		t.Fatalf("cursor should move to the match, got %+v", c)
	}
	if !strings.HasPrefix(strings.TrimSpace(ansi.Strip(bar(a))), "FILTER") {
		t.Errorf("bar should show FILTER: %q", ansi.Strip(bar(a)))
	}

	// Matching the image works too.
	a.docker.filter.SetValue("postgres:17")
	a.docker.refresh()
	if c := a.docker.selected(); c == nil || c.Name != "calliope-poker-postgres-1" {
		t.Fatalf("image match: %+v", c)
	}

	a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if a.docker.filter.Value() != "" || !strings.Contains(screen(a), "app-1") {
		t.Fatal("esc should clear the filter")
	}
}

func TestDockerUnreachable(t *testing.T) {
	a := testApp(t, 120, config.Default())
	a.Update(dockerInfoMsg{info: dock.Info{Context: "desktop-linux"}, err: errors.New("daemon down")})
	a.Update(containersMsg{err: errors.New("error during connect: the docker daemon is not running")})
	s := screen(a)
	for _, want := range []string{"Can't reach Docker", "the docker daemon is not running", "press r to retry", "desktop-linux · unreachable"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
}

func TestDockerEvents(t *testing.T) {
	a := dockerApp(t, 140)
	gen := a.docker.gen

	// destroy removes at once; the batch for another container asks for
	// an inspect (a command to run).
	cmd := a.handleDockerEvents(dockerEventsMsg{gen: gen, events: []dock.Event{
		{ID: nginxID, Action: "destroy"},
		{ID: appID, Action: "die"},
	}})
	if cmd == nil {
		t.Fatal("expected follow-up commands")
	}
	if strings.Contains(screen(a), "old-nginx") {
		t.Fatal("destroyed container still listed")
	}

	// An inspect that started before the destroy can't bring it back.
	cs := fixtureContainers(t)
	a.Update(containersUpdatedMsg{cs: cs})
	if strings.Contains(screen(a), "old-nginx") {
		t.Fatal("a stale inspect resurrected a destroyed container")
	}

	// Messages from a replaced stream are ignored.
	if cmd := a.handleDockerEvents(dockerEventsMsg{gen: gen - 1, events: []dock.Event{{ID: appID, Action: "die"}}}); cmd != nil {
		t.Fatal("old generation should be ignored")
	}

	// The stream ending schedules a restart with backoff.
	a.handleDockerEvents(dockerEventsMsg{gen: gen, done: true})
	if a.docker.backoff <= 0 {
		t.Fatal("expected a backoff after the stream ended")
	}
}

func TestDockerDetailPane(t *testing.T) {
	a := dockerApp(t, 160)
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	a.Update(logsMsg{id: appID, lines: []string{"server listening on :3000"}})
	s := screen(a)
	for _, want := range []string{
		"COMPOSE", "project", "calliope-poker", `docker-compose.yml`,
		"CONTAINER", "fc468a524c81", "PORTS", "0.0.0.0:8080 → 3000/tcp",
		"NETWORKS", "calliope-poker_default",
		"ENVIRONMENT", "m shows values", "••••",
		"LOGS", "server listening on :3000",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("detail missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "=redacted") {
		t.Error("env values should be masked by default")
	}
	press(a, 'm')
	if !strings.Contains(screen(a), "=redacted") {
		t.Error("m should reveal env values")
	}

	// d cycles: beside the list, full screen, hidden.
	press(a, 'd')
	s = screen(a)
	if a.detailLayout() != layoutFull || !strings.Contains(s, "ENVIRONMENT") || strings.Contains(s, "NAME ") {
		t.Fatalf("d should make the details full screen:\n%s", s)
	}
	// Full screen: j scrolls the details instead of moving the cursor.
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 20})
	press(a, 'j')
	if a.detail.scroll != 1 || a.docker.selected().ID != appID {
		t.Fatalf("j should scroll: scroll=%d", a.detail.scroll)
	}
	// esc goes back to the pane beside the list.
	a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if a.detailLayout() != layoutSide {
		t.Fatalf("esc should go back to the side pane, got %d", a.detailLayout())
	}
	press(a, 'd')
	press(a, 'd')
	if a.detailLayout() != layoutNone || strings.Contains(screen(a), "ENVIRONMENT") {
		t.Fatal("d from full screen should hide the details")
	}
	press(a, 'd')
	if a.detailLayout() != layoutSide {
		t.Fatal("d again should show them beside the list")
	}
}

// Moving to a container whose logs aren't cached schedules a load.
func TestDockerDetailLoadsLogs(t *testing.T) {
	a := dockerApp(t, 160)
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	if a.logs.want != appID {
		t.Fatalf("pane should be waiting on app-1, got %q", a.logs.want)
	}
	if !strings.Contains(screen(a), "loading…") {
		t.Error("logs should show as loading")
	}
	a.Update(logsMsg{id: appID, lines: []string{"old"}})
	press(a, 'j')
	if a.logs.want == appID || a.logs.want == "" {
		t.Fatalf("moving should switch the wanted logs, got %q", a.logs.want)
	}
	if a.logs.entries[appID] != nil {
		t.Fatal("leaving a container should drop its cached tail")
	}
}

// Every width keeps the list and pane inside the terminal.
func TestDockerFitsEveryWidth(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for w := 40; w <= 200; w += 7 {
		a := dockerApp(t, w)
		for i, line := range strings.Split(a.render(), "\n") {
			if got := ansi.StringWidth(line); got > w {
				t.Fatalf("w=%d line %d is %d wide: %q", w, i, got, ansi.Strip(line))
			}
		}
	}
}
