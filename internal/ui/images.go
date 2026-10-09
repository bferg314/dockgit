package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"

	"github.com/bferg314/dockgit/internal/dock"
)

// What the Docker tab lists.
type dockerMode int

const (
	modeContainers dockerMode = iota
	modeImages
	modeVolumes
	modeDisk
)

// diskState is the disk usage view.
type diskState struct {
	listState
	usage []dock.Usage
}

// listState is a cursor over a filtered list of n items.
type listState struct {
	cursor, offset int
	loading        bool
	loaded         bool
	err            error
}

func (l *listState) move(delta, n int) {
	l.cursor = max(0, min(n-1, l.cursor+delta))
}

// scroll keeps the cursor within h visible rows.
func (l *listState) scroll(h, n int) {
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+h {
		l.offset = l.cursor - h + 1
	}
	l.offset = max(0, min(l.offset, n-h))
}

type (
	imagesMsg struct {
		imgs []*dock.Image
		err  error
	}
	volumesMsg struct {
		vols []*dock.Volume
		err  error
	}
	// opDoneMsg reports an image or volume operation; reload names the
	// list to refresh afterwards.
	opDoneMsg struct {
		done   string
		err    error
		reload dockerMode
	}
)

func (a *App) setDockerMode(m dockerMode) tea.Cmd {
	t := &a.docker
	if t.mode == m {
		m = modeContainers // the same key again goes back
	}
	t.mode = m
	t.filter.SetValue("")
	t.filter.Blur()
	t.refresh()
	switch m {
	case modeImages:
		return a.loadImages()
	case modeVolumes:
		return a.loadVolumes()
	case modeDisk:
		return a.loadDisk()
	}
	return nil
}

func (a *App) loadImages() tea.Cmd {
	a.docker.images.loading = true
	r := a.runner
	return tea.Batch(a.startSpinner(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		imgs, err := dock.Images(ctx, r)
		return imagesMsg{imgs: imgs, err: err}
	})
}

func (a *App) loadVolumes() tea.Cmd {
	a.docker.volumes.loading = true
	r := a.runner
	return tea.Batch(a.startSpinner(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		vols, err := dock.Volumes(ctx, r)
		return volumesMsg{vols: vols, err: err}
	})
}

// imageUsers and volumeUsers name the containers (running or not) using
// an image or volume, from the container list.
func (t *dockerTab) imageUsers(id string) []string {
	var names []string
	for _, c := range t.all {
		if c.ImageID == id {
			names = append(names, displayName(c))
		}
	}
	sort.Strings(names)
	return names
}

func (t *dockerTab) volumeUsers(name string) []string {
	var names []string
	for _, c := range t.all {
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name == name {
				names = append(names, displayName(c))
				break
			}
		}
	}
	sort.Strings(names)
	return names
}

// imageView and volumeView are the filtered lists shown.
func (t *dockerTab) imageView() []*dock.Image {
	q := t.filter.Value()
	if q == "" {
		return t.imgs
	}
	refs := make([]string, len(t.imgs))
	for i, img := range t.imgs {
		refs[i] = img.Ref()
	}
	var out []*dock.Image
	for _, m := range fuzzy.Find(q, refs) {
		out = append(out, t.imgs[m.Index])
	}
	return out
}

func (t *dockerTab) volumeView() []*dock.Volume {
	q := t.filter.Value()
	if q == "" {
		return t.vols
	}
	names := make([]string, len(t.vols))
	for i, v := range t.vols {
		names[i] = v.Name
	}
	var out []*dock.Volume
	for _, m := range fuzzy.Find(q, names) {
		out = append(out, t.vols[m.Index])
	}
	return out
}

func (a *App) selectedImage() *dock.Image {
	v := a.docker.imageView()
	if c := a.docker.images.cursor; c >= 0 && c < len(v) {
		return v[c]
	}
	return nil
}

func (a *App) selectedVolume() *dock.Volume {
	v := a.docker.volumeView()
	if c := a.docker.volumes.cursor; c >= 0 && c < len(v) {
		return v[c]
	}
	return nil
}

// runOp runs an image or volume operation in the background.
func (a *App) runOp(reload dockerMode, f func(context.Context, dock.Runner) (string, error)) tea.Cmd {
	r := a.runner
	return tea.Batch(a.startSpinner(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		done, err := f(ctx, r)
		return opDoneMsg{done: done, err: err, reload: reload}
	})
}

func (a *App) imagesKey(key string) tea.Cmd {
	t := &a.docker
	n := len(t.imageView())
	switch key {
	case "up", "k":
		t.images.move(-1, n)
	case "down", "j":
		t.images.move(1, n)
	case "pgup":
		t.images.move(-max(1, a.h-8), n)
	case "pgdown":
		t.images.move(max(1, a.h-8), n)
	case "home":
		t.images.cursor = 0
	case "end":
		t.images.cursor = max(0, n-1)
	case "r":
		return a.loadImages()
	case "y":
		if img := a.selectedImage(); img != nil {
			return a.copyText("image "+img.Ref(), img.Ref())
		}
	case "x":
		img := a.selectedImage()
		if img == nil {
			return nil
		}
		if users := t.imageUsers(img.ID); len(users) > 0 {
			return a.notify(2, "%s is used by %s: remove %s first", img.Ref(), strings.Join(users, ", "), plural(len(users), "that container", "those containers"))
		}
		return a.confirm("Remove image "+img.Ref()+"?", []string{img.ShortID() + "  " + img.Size}, func() tea.Cmd {
			return a.runOp(modeImages, func(ctx context.Context, r dock.Runner) (string, error) {
				return "Removed " + img.Ref(), dock.RemoveImage(ctx, r, img.ID)
			})
		})
	case "P":
		var n int
		for _, img := range t.imgs {
			if img.Dangling() && len(t.imageUsers(img.ID)) == 0 {
				n++
			}
		}
		if n == 0 {
			return a.notify(0, "No dangling images to prune")
		}
		return a.confirm(fmt.Sprintf("Prune %d dangling %s?", n, plural(n, "image", "images")),
			[]string{"Unnamed images left over from rebuilds, not used by any container."}, func() tea.Cmd {
				return a.runOp(modeImages, func(ctx context.Context, r dock.Runner) (string, error) {
					freed, err := dock.PruneDanglingImages(ctx, r)
					return "Pruned dangling images, freed " + freed, err
				})
			})
	}
	return nil
}

func (a *App) volumesKey(key string) tea.Cmd {
	t := &a.docker
	n := len(t.volumeView())
	switch key {
	case "up", "k":
		t.volumes.move(-1, n)
	case "down", "j":
		t.volumes.move(1, n)
	case "pgup":
		t.volumes.move(-max(1, a.h-8), n)
	case "pgdown":
		t.volumes.move(max(1, a.h-8), n)
	case "home":
		t.volumes.cursor = 0
	case "end":
		t.volumes.cursor = max(0, n-1)
	case "r":
		return a.loadVolumes()
	case "y":
		if v := a.selectedVolume(); v != nil {
			return a.copyText("volume name", v.Name)
		}
	case "x":
		v := a.selectedVolume()
		if v == nil {
			return nil
		}
		if users := t.volumeUsers(v.Name); len(users) > 0 {
			return a.notify(2, "%s is used by %s: remove %s first", v.Name, strings.Join(users, ", "), plural(len(users), "that container", "those containers"))
		}
		return a.confirm("Remove volume "+v.Name+"?", []string{"Its data (" + v.Size + ") is deleted for good."}, func() tea.Cmd {
			return a.runOp(modeVolumes, func(ctx context.Context, r dock.Runner) (string, error) {
				return "Removed volume " + v.Name, dock.RemoveVolume(ctx, r, v.Name)
			})
		})
	}
	return nil
}

func (a *App) renderImages(w, h int) string {
	t := &a.docker
	st := a.st
	lines, h := a.filterLine(h)
	view := t.imageView()
	if msg := listMessage(st, a, &t.images, len(view), t.filter.Value(), "No images."); msg != "" {
		return strings.Join(append(lines, "", "   "+msg), "\n")
	}

	const idW, sizeW, agoW = 12, 8, 9
	refW, usersW := len("IMAGE"), len("USED BY")
	for _, img := range view {
		refW = max(refW, utf8.RuneCountInString(img.Ref()))
		usersW = max(usersW, len(strings.Join(t.imageUsers(img.ID), ", ")))
	}
	usersW = min(usersW, 30)
	refW = max(12, min(refW, w-2-idW-sizeW-agoW-usersW-8))

	lines = append(lines, "  "+fit(st.colHead.Render("IMAGE"), refW)+"  "+fit(st.colHead.Render("ID"), idW)+"  "+
		fit(st.colHead.Render("SIZE"), sizeW)+"  "+fit(st.colHead.Render("CREATED"), agoW)+"  "+st.colHead.Render("USED BY"))
	h--
	t.images.scroll(h, len(view))
	for i := t.images.offset; i < min(len(view), t.images.offset+h); i++ {
		img := view[i]
		sel := i == t.images.cursor
		marker, ref := "  ", st.textS.Render(img.Ref())
		if sel {
			marker, ref = st.marker.Render("▌")+" ", st.selName.Render(img.Ref())
		}
		if img.Dangling() && !sel {
			ref = st.dim.Render(img.Ref())
		}
		users := st.dim.Render("unused")
		if u := t.imageUsers(img.ID); len(u) > 0 {
			users = st.textS.Render(strings.Join(u, ", "))
		} else if img.Dangling() {
			users = st.warn.Render("dangling")
		}
		lines = append(lines, marker+fit(ref, refW)+"  "+fit(st.dim.Render(img.ShortID()), idW)+"  "+
			fit(st.textS.Render(img.Size), sizeW)+"  "+fit(st.agoStyle(img.Created).Render(ago(img.Created)), agoW)+"  "+fit(users, usersW))
	}
	return strings.Join(lines, "\n")
}

func (a *App) renderVolumes(w, h int) string {
	t := &a.docker
	st := a.st
	lines, h := a.filterLine(h)
	view := t.volumeView()
	if msg := listMessage(st, a, &t.volumes, len(view), t.filter.Value(), "No volumes."); msg != "" {
		if t.volumes.loading && !t.volumes.loaded {
			msg += st.dim.Render("  (measuring sizes takes a few seconds)")
		}
		return strings.Join(append(lines, "", "   "+msg), "\n")
	}

	const sizeW = 9
	nameW := len("VOLUME")
	for _, v := range view {
		nameW = max(nameW, utf8.RuneCountInString(v.Name))
	}
	nameW = max(12, min(nameW, w-2-sizeW-30-4))

	lines = append(lines, "  "+fit(st.colHead.Render("VOLUME"), nameW)+"  "+fit(st.colHead.Render("SIZE"), sizeW)+"  "+st.colHead.Render("USED BY"))
	h--
	t.volumes.scroll(h, len(view))
	for i := t.volumes.offset; i < min(len(view), t.volumes.offset+h); i++ {
		v := view[i]
		sel := i == t.volumes.cursor
		marker, name := "  ", st.textS.Render(v.Name)
		if sel {
			marker, name = st.marker.Render("▌")+" ", st.selName.Render(v.Name)
		}
		users := st.dim.Render("unused")
		if u := t.volumeUsers(v.Name); len(u) > 0 {
			users = st.textS.Render(strings.Join(u, ", "))
		}
		lines = append(lines, marker+fit(name, nameW)+"  "+fit(st.textS.Render(v.Size), sizeW)+"  "+users)
	}
	return strings.Join(lines, "\n")
}

// filterLine shows the filter input when in use, and returns the height
// left for the list.
func (a *App) filterLine(h int) ([]string, int) {
	f := &a.docker.filter
	if f.Focused() || f.Value() != "" {
		return []string{" " + a.st.key.Render("/") + " " + f.View()}, h - 1
	}
	return nil, h
}

// listMessage is what to show instead of an empty, loading or failed list.
func listMessage(st styles, a *App, l *listState, n int, filter, empty string) string {
	switch {
	case l.err != nil:
		return st.bad.Render(l.err.Error()) + st.dim.Render("  · r retries")
	case n > 0:
		return ""
	case l.loading:
		return a.spin.View() + st.dim.Render(" loading…")
	case filter != "":
		return st.dim.Render("Nothing matches that filter.")
	}
	return st.dim.Render(empty)
}
