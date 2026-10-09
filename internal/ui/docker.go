package ui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"

	"github.com/bferg314/dockgit/internal/dock"
	"github.com/bferg314/dockgit/internal/link"
)

// dockerTab lists containers grouped by compose project.
type dockerTab struct {
	all map[string]*dock.Container // by ID
	// removed holds destroyed containers, so an inspect that started before
	// the destroy event can't bring one back. IDs are never reused.
	removed map[string]bool
	view    []dockerRow
	cursor  int // index into view; always a container row when there is one
	offset  int
	filter  textinput.Model

	showStopped bool
	showEnv     bool // reveal env values in the detail pane

	// busy marks containers with an action in flight: id -> "stopping".
	busy map[string]string

	// Stats column: sampled in a loop while on; statsGen stops old loops.
	statsOn  bool
	statsGen int
	stats    map[string]dock.Stats

	// The images and volumes views.
	mode    dockerMode
	imgs    []*dock.Image
	vols    []*dock.Volume
	images  listState
	volumes listState
	disk    diskState

	info    dock.Info
	loading bool
	loaded  bool  // a list has arrived at least once
	err     error // set while the daemon can't be reached

	// Event stream. gen ties messages to the current stream, so a stream
	// that was replaced can't deliver into the new one.
	events    <-chan dock.Event
	eventsErr <-chan error
	stop      context.CancelFunc
	gen       int
	backoff   time.Duration
}

// dockerRow is a group heading (c == nil) or a container.
type dockerRow struct {
	group          string // compose project, "" for unmanaged
	dir            string // the project's working directory
	running, total int
	first          *dock.Container // a container of the group, for its source
	c              *dock.Container
	matches        []int // byte offsets into the displayed name
}

func newDockerTab(showStopped, stats bool) dockerTab {
	return dockerTab{
		all: map[string]*dock.Container{}, removed: map[string]bool{}, busy: map[string]string{},
		filter: newFilter(), showStopped: showStopped, statsOn: stats, loading: true,
	}
}

func newFilter() textinput.Model {
	f := textinput.New()
	f.Prompt = ""
	f.Placeholder = "type to filter…"
	return f
}

// Messages.
type (
	dockerInfoMsg struct {
		info dock.Info
		err  error
	}
	containersMsg struct {
		cs  []*dock.Container
		err error
	}
	containersUpdatedMsg struct {
		cs   []*dock.Container
		gone []string
		err  error
	}
	dockerEventsMsg struct {
		gen    int
		events []dock.Event
		done   bool
		err    error
	}
	eventsRestartMsg struct{ gen int }
)

// displayName drops the compose project prefix, which the group heading
// already shows: "calliope-poker-app-1" becomes "app-1".
func displayName(c *dock.Container) string {
	if p := c.Project(); p != "" {
		if rest, ok := strings.CutPrefix(c.Name, p+"-"); ok && rest != "" {
			return rest
		}
	}
	return c.Name
}

func (t *dockerTab) selected() *dock.Container {
	if t.cursor >= 0 && t.cursor < len(t.view) {
		return t.view[t.cursor].c
	}
	return nil
}

// visible reports whether c is listed with the current stopped setting.
func (t *dockerTab) visible(c *dock.Container) bool {
	switch c.State {
	case dock.StateExited, dock.StateCreated, dock.StateDead:
		return t.showStopped
	}
	return true
}

// counts returns running, stopped and unhealthy containers across all.
func (t *dockerTab) counts() (running, stopped, unhealthy int) {
	for _, c := range t.all {
		if c.Running() {
			running++
		} else {
			stopped++
		}
		if c.Health == "unhealthy" {
			unhealthy++
		}
	}
	return
}

// containerRows is how many container rows are listed.
func (t *dockerTab) containerRows() (n, at int) {
	for i, r := range t.view {
		if r.c != nil {
			n++
			if i == t.cursor {
				at = n
			}
		}
	}
	return n, at
}

// refresh rebuilds the visible rows, keeping the cursor on the same
// container.
func (t *dockerTab) refresh() {
	var keep string
	if c := t.selected(); c != nil {
		keep = c.ID
	}

	type group struct {
		name, dir      string
		running, total int
		first          *dock.Container
		rows           []dockerRow
	}
	groups := map[string]*group{}
	q := t.filter.Value()
	for _, c := range t.all {
		g := groups[c.Project()]
		if g == nil {
			g = &group{name: c.Project(), dir: c.WorkingDir(), first: c}
			groups[c.Project()] = g
		}
		g.total++
		if c.Running() {
			g.running++
		}
		if !t.visible(c) {
			continue
		}
		row := dockerRow{c: c}
		if q != "" {
			name := displayName(c)
			ms := fuzzy.Find(q, []string{name + " " + c.Image})
			if len(ms) == 0 {
				continue
			}
			for _, i := range ms[0].MatchedIndexes {
				if i < len(name) {
					row.matches = append(row.matches, i)
				}
			}
		}
		g.rows = append(g.rows, row)
	}

	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	// Compose projects by name, then everything unmanaged.
	sort.Slice(names, func(i, j int) bool {
		if (names[i] == "") != (names[j] == "") {
			return names[j] == ""
		}
		return names[i] < names[j]
	})

	t.view = t.view[:0]
	for _, n := range names {
		g := groups[n]
		if len(g.rows) == 0 {
			continue
		}
		sort.Slice(g.rows, func(i, j int) bool {
			a, b := g.rows[i].c, g.rows[j].c
			if a.Service() != b.Service() {
				return a.Service() < b.Service()
			}
			return a.Name < b.Name
		})
		t.view = append(t.view, dockerRow{group: g.name, dir: g.dir, running: g.running, total: g.total, first: g.first})
		t.view = append(t.view, g.rows...)
	}

	t.cursor = t.firstContainer(0, 1)
	for i, r := range t.view {
		if r.c != nil && r.c.ID == keep {
			t.cursor = i
			break
		}
	}
	t.images.cursor = min(t.images.cursor, max(0, len(t.imageView())-1))
	t.volumes.cursor = min(t.volumes.cursor, max(0, len(t.volumeView())-1))
}

// firstContainer finds the nearest container row from i in direction dir
// (then the other way), or -1 when there is none.
func (t *dockerTab) firstContainer(i, dir int) int {
	for _, d := range []int{dir, -dir} {
		for j := i; j >= 0 && j < len(t.view); j += d {
			if t.view[j].c != nil {
				return j
			}
		}
	}
	return -1
}

// move steps the cursor delta containers, skipping headings.
func (t *dockerTab) move(delta int) {
	if t.cursor < 0 {
		return
	}
	step := 1
	if delta < 0 {
		step, delta = -1, -delta
	}
	i := t.cursor
	for ; delta > 0; delta-- {
		j := i + step
		for j >= 0 && j < len(t.view) && t.view[j].c == nil {
			j += step
		}
		if j < 0 || j >= len(t.view) {
			break
		}
		i = j
	}
	t.cursor = i
}

// ---- loading ----

func (a *App) loadDockerInfo() tea.Cmd {
	r := a.runner
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		info, err := dock.GetInfo(ctx, r)
		return dockerInfoMsg{info: info, err: err}
	}
}

func (a *App) loadContainers() tea.Cmd {
	a.docker.loading = true
	r := a.runner
	return tea.Batch(a.startSpinner(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cs, err := dock.List(ctx, r)
		return containersMsg{cs: cs, err: err}
	})
}

func (a *App) inspectContainers(ids []string) tea.Cmd {
	r := a.runner
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cs, gone, err := dock.Inspect(ctx, r, ids...)
		return containersUpdatedMsg{cs: cs, gone: gone, err: err}
	}
}

// reloadDocker reconnects from scratch: daemon info, the full list and a
// new event stream.
func (a *App) reloadDocker() tea.Cmd {
	a.logs.invalidateAll()
	return tea.Batch(a.loadDockerInfo(), a.loadContainers(), a.startEvents())
}

// startEvents replaces the event stream.
func (a *App) startEvents() tea.Cmd {
	t := &a.docker
	if t.stop != nil {
		t.stop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.stop = cancel
	t.gen++
	t.events, t.eventsErr = dock.Events(ctx, a.runner)
	return waitDockerEvents(t.gen, t.events, t.eventsErr)
}

// waitDockerEvents blocks for the next event, then drains whatever else is
// ready so a burst (compose up starting ten containers) is one update.
func waitDockerEvents(gen int, ch <-chan dock.Event, errc <-chan error) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return dockerEventsMsg{gen: gen, done: true, err: <-errc}
		}
		evs := []dock.Event{e}
		for len(evs) < 256 {
			select {
			case e, ok := <-ch:
				if !ok {
					return dockerEventsMsg{gen: gen, events: evs}
				}
				evs = append(evs, e)
			default:
				return dockerEventsMsg{gen: gen, events: evs}
			}
		}
		return dockerEventsMsg{gen: gen, events: evs}
	}
}

// handleDockerEvents applies a batch of events: removed containers are
// dropped straight away, the rest are re-inspected.
func (a *App) handleDockerEvents(msg dockerEventsMsg) tea.Cmd {
	t := &a.docker
	if msg.gen != t.gen {
		return nil
	}
	if msg.done {
		// The stream ended (daemon restarted or stopped). Retry with
		// backoff; a successful restart reloads everything it missed.
		t.backoff = min(30*time.Second, max(2*time.Second, t.backoff*2))
		gen := t.gen
		return tea.Tick(t.backoff, func(time.Time) tea.Msg { return eventsRestartMsg{gen: gen} })
	}
	t.backoff = 0

	pending := map[string]bool{}
	changed := false
	for _, e := range msg.events {
		a.logs.invalidate(e.ID)
		if e.Action == "destroy" {
			delete(t.all, e.ID)
			delete(pending, e.ID)
			t.removed[e.ID] = true
			changed = true
			continue
		}
		pending[e.ID] = true
	}
	ids := slices.Sorted(maps.Keys(pending))
	if changed {
		t.refresh()
	}
	cmds := []tea.Cmd{waitDockerEvents(msg.gen, t.events, t.eventsErr)}
	if len(ids) > 0 {
		cmds = append(cmds, a.inspectContainers(ids))
	}
	return tea.Batch(cmds...)
}

// ---- keys ----

func (a *App) dockerKey(key string) tea.Cmd {
	t := &a.docker
	switch {
	case key == "i":
		return a.setDockerMode(modeImages)
	case key == "v" && t.mode != modeDisk:
		return a.setDockerMode(modeVolumes)
	case key == "C":
		return a.setDockerMode(modeDisk)
	case key == "esc" && t.mode != modeContainers:
		return a.setDockerMode(t.mode)
	case t.mode == modeImages:
		return a.imagesKey(key)
	case t.mode == modeVolumes:
		return a.volumesKey(key)
	case t.mode == modeDisk:
		return a.diskKey(key)
	}
	if a.detailScrollKey(key) {
		return nil
	}
	page := max(1, a.h-8)
	switch key {
	case "up", "k":
		t.move(-1)
	case "down", "j":
		t.move(1)
	case "pgup":
		t.move(-page)
	case "pgdown":
		t.move(page)
	case "home":
		t.cursor = t.firstContainer(0, 1)
	case "end":
		t.cursor = t.firstContainer(len(t.view)-1, -1)
	case "a":
		t.showStopped = !t.showStopped
		t.refresh()
		_, stopped, _ := t.counts()
		if t.showStopped {
			return a.notify(0, "Showing %d stopped %s", stopped, plural(stopped, "container", "containers"))
		}
		return a.notify(0, "Hiding %d stopped %s · a shows them", stopped, plural(stopped, "container", "containers"))
	case "m":
		t.showEnv = !t.showEnv
	case "d":
		a.toggleDetails()
	case "r":
		return a.reloadDocker()
	case "t":
		return a.toggleStats()
	}

	c := t.selected()
	if c == nil {
		return nil
	}
	switch key {
	case "enter":
		a.popup = a.containerPopup(c)
	case "l":
		return a.openLogs(c)
	case "s":
		return a.toggleRunning(c)
	case "S":
		return a.restartContainer(c)
	case "x":
		return a.removeContainer(c)
	case "e":
		return a.openShell(c)
	case "w":
		return a.openPort(c)
	case "g":
		return a.goToSource(c)
	case editorKey:
		return a.openInEditor(displayName(c), projectDir(c))
	}
	return nil
}

// ---- view ----

func (a *App) renderDocker(w, h int) string {
	switch a.docker.mode {
	case modeImages:
		return a.renderImages(w, h)
	case modeVolumes:
		return a.renderVolumes(w, h)
	case modeDisk:
		return a.renderDisk()
	}
	return a.renderContainers(w, h)
}

func (a *App) renderContainers(w, h int) string {
	t := &a.docker
	st := a.st
	var lines []string

	if t.filter.Focused() || t.filter.Value() != "" {
		lines = append(lines, " "+st.key.Render("/")+" "+t.filter.View())
		h--
	}

	if t.selected() == nil {
		running, stopped, _ := t.counts()
		var msg []string
		switch {
		case t.err != nil:
			msg = []string{st.bad.Render("Can't reach Docker."), st.textS.Render(t.err.Error()), "",
				st.dim.Render("Start Docker (or check your context), then press r to retry.")}
		case !t.loaded:
			msg = []string{a.spin.View() + st.dim.Render(" Connecting to Docker…")}
		case t.filter.Value() != "":
			msg = []string{st.dim.Render("Nothing matches that filter.")}
		case running == 0 && stopped > 0:
			msg = []string{st.dim.Render(fmt.Sprintf("Nothing running. %d stopped %s hidden: press a to show them.",
				stopped, plural(stopped, "container is", "containers are")))}
		default:
			msg = []string{st.dim.Render("No containers. Start one and it shows up here.")}
		}
		lines = append(lines, "")
		for _, m := range msg {
			lines = append(lines, "   "+m)
		}
		return strings.Join(lines, "\n")
	}

	// Columns. Status and ports take what they need (within limits); the
	// name has priority over the image when space is short.
	const markW, stateW, minImageW = 2, 2, 12
	statusW, portsW, nameW, imageW, statsW := len("STATUS"), len("PORTS"), len("NAME"), len("IMAGE"), 0
	if t.statsOn {
		statsW = len("CPU   MEM") + 2
	}
	now := time.Now()
	for _, r := range t.view {
		if r.c == nil {
			continue
		}
		nameW = max(nameW, utf8.RuneCountInString(displayName(r.c)))
		imageW = max(imageW, utf8.RuneCountInString(r.c.Image))
		portsW = max(portsW, utf8.RuneCountInString(portsLabel(r.c)))
		statusW = max(statusW, lipgloss.Width(a.statusCell(r.c, now)))
		if t.statsOn {
			statsW = max(statsW, lipgloss.Width(a.statsCell(r.c))+2)
		}
	}
	statusW, portsW = min(statusW, 26), min(portsW, 30)
	// Narrow terminals drop the ports (the detail pane has them) before
	// cutting the name or status short.
	portsGap := 2
	if w < narrowList {
		portsW, portsGap = 0, 0
	}
	avail := w - markW - stateW - statusW - portsW - portsGap - statsW - 5 // gaps
	if nameW+imageW > avail {
		imageW = max(min(imageW, minImageW), avail-nameW)
		nameW = max(8, avail-imageW)
	}

	lines = append(lines, strings.Repeat(" ", markW+stateW)+
		fit(st.colHead.Render("NAME"), nameW)+"  "+
		fit(st.colHead.Render("IMAGE"), imageW)+"  "+
		portsCol(st.colHead.Render("PORTS"), portsW)+
		fit(st.colHead.Render("STATUS"), statusW)+statsHead(st, statsW))
	h--

	// Keep the cursor, and the heading above a group's first row, in view.
	top := t.cursor
	if top > 0 && t.view[top-1].c == nil {
		top--
	}
	if top < t.offset {
		t.offset = top
	}
	if t.cursor >= t.offset+h {
		t.offset = t.cursor - h + 1
	}
	t.offset = max(0, min(t.offset, len(t.view)-h))

	for i := t.offset; i < min(len(t.view), t.offset+h); i++ {
		r := t.view[i]
		if r.c == nil {
			lines = append(lines, a.renderGroupHeading(r, w))
			continue
		}
		sel := i == t.cursor
		marker := "  "
		if sel {
			marker = st.marker.Render("▌") + " "
		}
		c := r.c
		name := st.highlightName(displayName(c), nameW, r.matches, sel)
		image := st.dim.Render(c.Image)
		lines = append(lines, marker+st.stateGlyph(c)+" "+name+"  "+fit(image, imageW)+"  "+
			portsCol(st.textS.Render(portsLabel(c)), portsW)+fit(a.statusCell(c, now), statusW)+statsCol(a.statsCell(c), statsW))
	}
	return strings.Join(lines, "\n")
}

func (a *App) renderGroupHeading(r dockerRow, w int) string {
	st := a.st
	if r.group == "" {
		return fit(" "+st.boxTitle.Render("other containers")+st.dim.Render(fmt.Sprintf("  %d/%d running", r.running, r.total)), w)
	}
	s := " " + st.boxTitle.Render(r.group) + st.dim.Render(fmt.Sprintf("  %d/%d running", r.running, r.total))
	switch src := a.sourceOf(r.first); src.Kind {
	case link.FromRepo, link.FromStack:
		s += "  " + st.branch.Render(src.Label()) + st.faintText.Render(" · g")
	case link.Gone:
		s += "  " + st.warn.Render(src.Label())
	default:
		if r.dir != "" {
			s += "  " + st.faintText.Render(tildePath(r.dir))
		}
	}
	return fit(s, w)
}

// narrowList is the width below which the container list drops its PORTS
// column.
const narrowList = 72

// portsCol is the PORTS cell and its gap, or nothing when it's dropped.
func portsCol(cell string, w int) string {
	if w == 0 {
		return ""
	}
	return fit(cell, w) + "  "
}

// statusCell is the STATUS column: an action in flight, or the state.
func (a *App) statusCell(c *dock.Container, now time.Time) string {
	if verb := a.docker.busy[c.ID]; verb != "" {
		return a.spin.View() + " " + a.st.warn.Render(verb)
	}
	return a.st.containerStatus(c, now)
}

// statsCell is "0.06%  85MiB" for a running container once sampled.
func (a *App) statsCell(c *dock.Container) string {
	s, ok := a.docker.stats[c.ID]
	switch {
	case !c.Running():
		return ""
	case !ok:
		return a.st.faintText.Render("…")
	}
	mem, _, _ := strings.Cut(s.Mem, " / ")
	return a.st.textS.Render(fmt.Sprintf("%-6s", s.CPU)) + " " + a.st.dim.Render(mem)
}

func statsHead(st styles, w int) string {
	if w == 0 {
		return ""
	}
	return "  " + fit(st.colHead.Render("CPU    MEM"), w-2)
}

func statsCol(cell string, w int) string {
	if w == 0 {
		return ""
	}
	return "  " + fit(cell, w-2)
}

// stateGlyph is the coloured dot at the start of a row.
func (s styles) stateGlyph(c *dock.Container) string {
	switch c.State {
	case dock.StateRunning:
		switch c.Health {
		case "unhealthy":
			return s.bad.Render("●")
		case "starting":
			return s.warn.Render("●")
		}
		return s.ok.Render("●")
	case dock.StatePaused:
		return s.warn.Render("‖")
	case dock.StateRestarting:
		return s.warn.Render("↻")
	case dock.StateDead:
		return s.bad.Render("✗")
	case dock.StateExited:
		if c.ExitCode != 0 {
			return s.bad.Render("○")
		}
	}
	return s.dim.Render("○")
}

// containerStatus is the STATUS cell: "up 4h · healthy", "exited 137 · 2d ago".
func (s styles) containerStatus(c *dock.Container, now time.Time) string {
	switch c.State {
	case dock.StateRunning:
		out := s.textS.Render("up " + duration(now.Sub(c.StartedAt)))
		switch c.Health {
		case "healthy":
			out += s.dim.Render(" · ") + s.ok.Render("healthy")
		case "unhealthy":
			out += s.dim.Render(" · ") + s.bad.Render("unhealthy")
		case "starting":
			out += s.dim.Render(" · ") + s.warn.Render("starting")
		}
		return out
	case dock.StateExited:
		code := s.dim.Render(fmt.Sprintf("exited %d", c.ExitCode))
		if c.ExitCode != 0 {
			code = s.bad.Render(fmt.Sprintf("exited %d", c.ExitCode))
		}
		if c.FinishedAt.IsZero() {
			return code
		}
		return code + s.dim.Render(" · "+ago(c.FinishedAt))
	case dock.StateRestarting:
		return s.warn.Render(fmt.Sprintf("restarting (%d)", c.RestartCount))
	case dock.StatePaused:
		return s.warn.Render("paused")
	}
	return s.dim.Render(c.State)
}

// portsLabel lists published ports: "8080→3000, 127.0.0.1:5432→5432".
func portsLabel(c *dock.Container) string {
	var parts []string
	for _, p := range c.PublishedPorts() {
		parts = append(parts, portLabel(p))
	}
	return strings.Join(parts, ", ")
}

func portLabel(p dock.Port) string {
	container := strings.TrimSuffix(p.Container, "/tcp")
	if p.HostIP != "" {
		return p.HostIP + ":" + p.HostPort + "→" + container
	}
	return p.HostPort + "→" + container
}

// duration formats a running time compactly: "45s", "12m", "4h", "3d".
func duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
