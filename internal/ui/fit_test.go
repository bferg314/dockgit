package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/dockgit/internal/dock"
)

// screens builds each screen dockgit can show, ready to render.
func screens(t *testing.T) map[string]func() *App {
	t.Helper()
	docker := func() *App { return dockerApp(t, 120) }
	compose := func() *App { a, _, _ := composeApp(t); return a }
	return map[string]func() *App{
		"containers": docker,
		"containers, full details": func() *App {
			a := docker()
			a.detail.full = true
			return a
		},
		"images": func() *App {
			a := docker()
			a.docker.mode = modeImages
			a.docker.imgs = imgsFixture(t)
			a.docker.images.loaded = true
			return a
		},
		"disk": func() *App {
			a := docker()
			a.runner = &fakeRunner{out: map[string]string{"system df --format json": readFixture(t, "df-summary.jsonl")}}
			a.docker.mode = modeDisk
			a.docker.disk.usage, a.docker.disk.loaded = mustUsage(t, a), true
			return a
		},
		"logs": func() *App {
			a := docker()
			press(a, 'l')
			a.Update(logLinesMsg{gen: a.logView.gen, lines: []string{"2026-10-08T14:12:16.813046562Z " + strings.Repeat("a long log line ", 20)}})
			return a
		},
		"action popup": func() *App {
			a := docker()
			a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			return a
		},
		"confirm": func() *App {
			a := docker()
			press(a, 'x')
			return a
		},
		"help": func() *App {
			a := docker()
			press(a, '?')
			return a
		},
		"repos": func() *App {
			a, _ := reposApp(t, gitRepo(t, "compose.yaml"), "compose.yaml")
			return a
		},
		"branch picker": func() *App {
			dir := gitRepo(t, "compose.yaml")
			a, _ := reposApp(t, dir, "compose.yaml")
			press(a, 'b')
			a.Update(loadBranches(dir, dir)())
			return a
		},
		"add repo": func() *App {
			a, _ := reposApp(t, gitRepo(t, "compose.yaml"), "compose.yaml")
			press(a, 'n')
			return a
		},
		"compose": compose,
		"compose, details": func() *App {
			a := compose()
			selectStack(t, a, "stacks/media/plex")
			return a
		},
		"doctor": func() *App {
			a := compose()
			press(a, '!')
			return a
		},
		"preflight": func() *App {
			a := compose()
			s := selectStack(t, a, "stacks/apps/clock")
			_ = s
			holder := &dock.Container{ID: "h", Name: "holder", State: dock.StateRunning, Labels: map[string]string{},
				Ports: []dock.Port{{Container: "80/tcp", HostPort: "8080"}}}
			a.Update(containersMsg{cs: []*dock.Container{holder}})
			press(a, 'U')
			return a
		},
		"new stack": func() *App {
			a := compose()
			press(a, 'n')
			return a
		},
		"settings": func() *App {
			a := docker()
			press(a, '4')
			return a
		},
		"settings value": func() *App {
			a := docker()
			press(a, '4')
			for i, it := range a.settingItems() {
				if it.label == "Keep build cache up to" {
					a.settings.cursor = i
				}
			}
			a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			return a
		},
	}
}

func imgsFixture(t *testing.T) []*dock.Image {
	t.Helper()
	imgs, err := dock.Images(t.Context(), &fakeRunner{out: map[string]string{"image ls --no-trunc --format json": readFixture(t, "images.jsonl")}})
	if err != nil {
		t.Fatal(err)
	}
	return imgs
}

// Every screen fills the terminal exactly: no line wider than it, no more
// lines than it has, at sizes from a small split pane to a wide monitor.
func TestEveryScreenFits(t *testing.T) {
	for name, build := range screens(t) {
		a := build()
		for _, w := range []int{40, 52, 64, 80, 99, 100, 120, 160, 200} {
			for _, h := range []int{10, 16, 24, 40} {
				a.Update(tea.WindowSizeMsg{Width: w, Height: h})
				out := a.render()
				lines := strings.Split(out, "\n")
				if len(lines) != h {
					t.Errorf("%s at %dx%d: %d lines", name, w, h, len(lines))
				}
				for i, l := range lines {
					if lw := ansi.StringWidth(l); lw > w {
						t.Errorf("%s at %dx%d: line %d is %d wide: %q", name, w, h, i, lw, ansi.Strip(l))
						break
					}
				}
			}
		}
	}
}
