package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Mouse support. While render draws the screen, each part records the
// areas it makes clickable as zones, in screen cells. A mouse event goes
// to the topmost zone under it that handles that kind of event. Zone
// actions call the same code as the keys, so the mouse adds no behaviour
// of its own.

type zone struct {
	x, y, w, h int
	click      func() tea.Cmd
	dbl        func() tea.Cmd // a second click on the same cell; nil runs click again
	right      func() tea.Cmd
	wheel      func(delta int) tea.Cmd // delta is -1 (up) or 1 (down)
}

func (z zone) has(x, y int) bool {
	return x >= z.x && x < z.x+z.w && y >= z.y && y < z.y+z.h
}

// doubleClick is how soon a second click on the same cell counts as a
// double click.
const doubleClick = 400 * time.Millisecond

// wheelLines is how far one turn of the wheel scrolls text (the detail
// pane, logs). Lists move one row.
const wheelLines = 3

type mouseState struct {
	zones []zone  // from the last render, topmost last
	rec   *[]zone // where render is recording zones; nil outside render
	// The last click, for spotting a double click.
	lastAt time.Time
	lastX  int
	lastY  int
}

// addZone records z, in the coordinates of whatever is being drawn. It
// does nothing outside render (measureDetail renders the pane to measure it).
func (a *App) addZone(z zone) {
	if a.mouse.rec != nil {
		*a.mouse.rec = append(*a.mouse.rec, z)
	}
}

// capture runs draw, collecting the zones it records relative to its
// output's top left, for the caller to place with addZones.
func (a *App) capture(draw func() string) (string, []zone) {
	var zs []zone
	prev := a.mouse.rec
	a.mouse.rec = &zs
	s := draw()
	a.mouse.rec = prev
	return s, zs
}

// addZones records zones drawn at (dx, dy).
func (a *App) addZones(zs []zone, dx, dy int) {
	for _, z := range zs {
		z.x += dx
		z.y += dy
		a.addZone(z)
	}
}

// clipZones keeps the parts of zones that are inside w×h.
func clipZones(zs []zone, w, h int) []zone {
	var out []zone
	for _, z := range zs {
		x0, y0 := max(0, z.x), max(0, z.y)
		x1, y1 := min(w, z.x+z.w), min(h, z.y+z.h)
		if x1 > x0 && y1 > y0 {
			z.x, z.y, z.w, z.h = x0, y0, x1-x0, y1-y0
			out = append(out, z)
		}
	}
	return out
}

// zoneAt is the topmost zone under (x, y) that ok accepts.
func (a *App) zoneAt(x, y int, ok func(zone) bool) *zone {
	for i := len(a.mouse.zones) - 1; i >= 0; i-- {
		if z := &a.mouse.zones[i]; z.has(x, y) && ok(*z) {
			return z
		}
	}
	return nil
}

func (a *App) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if !a.cfg.Mouse {
		return nil
	}
	m := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		switch m.Button {
		case tea.MouseLeft:
			double := time.Since(a.mouse.lastAt) < doubleClick && m.X == a.mouse.lastX && m.Y == a.mouse.lastY
			if double {
				a.mouse.lastAt = time.Time{} // a third click starts again
			} else {
				a.mouse.lastAt, a.mouse.lastX, a.mouse.lastY = time.Now(), m.X, m.Y
			}
			z := a.zoneAt(m.X, m.Y, func(z zone) bool { return z.click != nil || double && z.dbl != nil })
			switch {
			case z == nil:
			case double && z.dbl != nil:
				return z.dbl()
			case z.click != nil:
				return z.click()
			}
		case tea.MouseRight:
			if z := a.zoneAt(m.X, m.Y, func(z zone) bool { return z.right != nil }); z != nil {
				return z.right()
			}
		}
	case tea.MouseWheelMsg:
		delta := 0
		switch m.Button {
		case tea.MouseWheelUp:
			delta = -1
		case tea.MouseWheelDown:
			delta = 1
		}
		if delta == 0 {
			return nil
		}
		if z := a.zoneAt(m.X, m.Y, func(z zone) bool { return z.wheel != nil }); z != nil {
			return z.wheel(delta)
		}
	}
	return nil
}

// listRow makes a list row drawn at (x, y), w cells wide, clickable: a
// click moves the cursor there (pick), and a double click and a right
// click pick it and then run dbl and right. Either may be nil.
//
// pick reports whether the cursor was already on the row. A double click
// runs dbl only then: if the first click changed the layout (a filter
// line went away, say), the second lands on another row and just moves.
//
// Like enter or an arrow, a click leaves a filter being typed in.
func (a *App) listRow(x, y, w int, pick func() bool, dbl, right func() tea.Cmd) {
	pick = a.leavingFilter(pick)
	z := zone{x: x, y: y, w: w, h: 1, click: func() tea.Cmd { pick(); return nil }}
	if dbl != nil {
		z.dbl = func() tea.Cmd {
			if !pick() {
				return nil
			}
			return dbl()
		}
	}
	if right != nil {
		z.right = func() tea.Cmd { pick(); return right() }
	}
	a.addZone(z)
}

// leavingFilter wraps pick to blur the filter first, when it has the
// keyboard.
func (a *App) leavingFilter(pick func() bool) func() bool {
	return func() bool {
		if f := a.activeFilter(); f != nil && f.Focused() && a.logView == nil && !a.dialogOpen() && a.popup == nil {
			f.Blur()
			a.refreshViews()
		}
		return pick()
	}
}

// pickIndex is a pick for listRow that moves *cursor to i.
func pickIndex(cursor *int, i int) func() bool {
	return func() bool {
		was := *cursor == i
		*cursor = i
		return was
	}
}

// keyCmd is a key press on a key handler, for zones that do what a key does.
func keyCmd(handle func(string) tea.Cmd, key string) func() tea.Cmd {
	return func() tea.Cmd { return handle(key) }
}

// wheelKeys turns the wheel into up and down on a key handler.
func wheelKeys(handle func(string) tea.Cmd) func(int) tea.Cmd {
	return func(d int) tea.Cmd { return handle(wheelKey(d).String()) }
}

// wheelKey is the key a turn of the wheel stands for.
func wheelKey(d int) tea.KeyPressMsg {
	if d < 0 {
		return keyPress("up")
	}
	return keyPress("down")
}

// keyPress is the key press named key, for handlers that take the whole
// message: a special key below, or a single character.
func keyPress(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	r := []rune(key)[0]
	return tea.KeyPressMsg{Code: r, Text: key}
}

// filterZone makes a filter line, drawn at line y, w cells wide, put the
// cursor in the filter on a click.
func (a *App) filterZone(y, w int) {
	a.addZone(zone{y: y, w: w, h: 1, click: func() tea.Cmd {
		if f := a.activeFilter(); f != nil {
			return f.Focus()
		}
		return nil
	}})
}

// Content in a boxed overlay (styles.box) starts inside its border and
// padding.
const boxLeft, boxTop = 3, 2

// boxZone records z on line of a boxed overlay's content, x cells in.
func (a *App) boxZone(line, x int, z zone) {
	z.x, z.y, z.h = boxLeft+x, boxTop+line, 1
	a.addZone(z)
}

// boxRow is listRow for a row on line of a boxed overlay's content.
func (a *App) boxRow(line, w int, pick func() bool, dbl, right func() tea.Cmd) {
	a.listRow(boxLeft, boxTop+line, w, pick, dbl, right)
}

// wheelOver lets the wheel anywhere on a rendered overlay run wheel.
func (a *App) wheelOver(s string, wheel func(int) tea.Cmd) string {
	a.addZone(zone{w: lipgloss.Width(s), h: lipgloss.Height(s), wheel: wheel})
	return s
}

// overlayZones are the zones behind an open overlay at (x, y), w×h: it's
// modal, so a click elsewhere closes it (as esc does) when that's safe,
// and nothing underneath can be reached.
func (a *App) overlayZones(x, y, w, h int) []zone {
	none := func() tea.Cmd { return nil }
	noWheel := func(int) tea.Cmd { return nil }
	outside := none
	// Text being typed isn't thrown away by a stray click: the add-folder,
	// new-stack and value dialogs stay open.
	typing := a.addRepo != nil || a.newStack != nil || a.valueDlg != nil
	if a.help.open || !typing {
		outside = func() tea.Cmd { return a.handleKey(keyPress("esc")) }
	}
	return []zone{
		{x: 0, y: 0, w: a.w, h: a.h, click: outside, right: outside, wheel: noWheel},
		{x: x, y: y, w: w, h: h, click: none, right: none, wheel: noWheel},
	}
}
