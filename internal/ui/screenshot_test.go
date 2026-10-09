package ui

import (
	"crypto/sha256"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/dock"
	"github.com/bferg314/dockgit/internal/updates"
)

// TestScreenshots draws the README's screenshots from a demo state:
//
//	DOCKGIT_SCREENSHOTS=docs go test -run TestScreenshots ./internal/ui
func TestScreenshots(t *testing.T) {
	dir := os.Getenv("DOCKGIT_SCREENSHOTS")
	if dir == "" {
		t.Skip("set DOCKGIT_SCREENSHOTS to the folder to write them to")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join("..", "..", dir)
	}
	// A home folder of its own, so paths read ~/composes/...
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := filepath.Join(home, "composes")
	writeFiles(t, root, map[string]string{
		"stacks/global.env.example":       "PUID=1000\n",
		"stacks/clock/compose.yaml":       "services:\n  clock:\n    image: ghcr.io/bferg314/clock:1.4.0\n    ports: [\"8090:80\"]\nx-dockgit:\n  description: Wall clock web app\n",
		"stacks/dockge/compose.yaml":      "services:\n  dockge:\n    image: louislam/dockge:1.5.0\n    ports: [\"5001:5001\"]\nx-dockgit:\n  description: Web UI for these stacks\n",
		"stacks/monitoring/compose.yaml":  "services:\n  prometheus:\n    image: prom/prometheus:v3.5.0\n    ports: [\"9090:9090\"]\n  grafana:\n    image: grafana/grafana:12.2.0\n    ports: [\"3002:3000\"]\n    environment:\n      - GF_ADMIN_PASSWORD=${GRAFANA_PASSWORD}\n",
		"stacks/homepage/compose.yaml":    "services:\n  homepage:\n    image: ghcr.io/gethomepage/homepage:v2.4.0\n    ports: [\"3000:3000\"]\nx-dockgit:\n  description: Start page\n",
		"stacks/notes/compose.yaml":       "services:\n  app:\n    image: ghcr.io/example/notes:2.3.1\n    ports: [\"8000:8000\"]\n    environment:\n      - DB_PASSWORD=${DB_PASSWORD}\n  db:\n    image: postgres:17.6\n",
		"stacks/notes/.env.example":       "DB_PASSWORD=\n",
		"stacks/uptime-kuma/compose.yaml": "services:\n  uptime-kuma:\n    image: louislam/uptime-kuma:2.5.4\n    ports: [\"3001:3001\"]\nx-dockgit:\n  description: Uptime monitoring\n",
		"old/services/web.yml":            "services:\n  web:\n    image: nginx:latest\n    container_name: web\n    network_mode: service:proxy\n",
		"old/services/proxy.yml":          "services:\n  proxy:\n    image: traefik\n    container_name: proxy\n    ports: [\"8090:80\"]\n",
	})

	// Files written long before the containers started, so nothing reads
	// "changed since up".
	month := time.Now().Add(-30 * 24 * time.Hour)
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			os.Chtimes(p, month, month)
		}
		return nil
	})

	now := time.Now()
	c := func(stack, svc, image, state, health string, up time.Duration, ports ...string) *dock.Container {
		file := filepath.Join(root, "stacks", stack, "compose.yaml")
		cn := &dock.Container{ID: fmt.Sprintf("%x", sha256.Sum256([]byte(stack+svc))), Name: stack + "-" + svc + "-1", Image: image, ImageID: "sha256:3d110bb937bd75aab905",
			State: state, Health: health, Created: now.Add(-up - time.Minute), StartedAt: now.Add(-up),
			Command: "/init", Networks: []string{stack + "_default"},
			Labels: map[string]string{dock.LabelProject: stack, dock.LabelService: svc, dock.LabelConfigFiles: file, dock.LabelWorkingDir: filepath.Dir(file)},
			Env:    []string{"PUID=1000", "TZ=America/New_York"}}
		for _, p := range ports {
			hostPort, cport, _ := strings.Cut(p, ":")
			cn.Ports = append(cn.Ports, dock.Port{Container: cport + "/tcp", HostIP: "0.0.0.0", HostPort: hostPort})
		}
		return cn
	}
	day := 24 * time.Hour
	containers := []*dock.Container{
		c("clock", "clock", "ghcr.io/bferg314/clock:1.4.0", dock.StateRunning, "", 2*time.Hour, "8090:80"),
		c("dockge", "dockge", "louislam/dockge:1.5.0", dock.StateRunning, "", 9*day, "5001:5001"),
		c("monitoring", "grafana", "grafana/grafana:12.2.0", dock.StateRunning, "healthy", 3*day, "3002:3000"),
		c("monitoring", "prometheus", "prom/prometheus:v3.5.0", dock.StateRunning, "", 3*day, "9090:9090"),
		c("homepage", "homepage", "ghcr.io/gethomepage/homepage:v2.4.0", dock.StateRunning, "", 9*day, "3000:3000"),
		c("notes", "app", "ghcr.io/example/notes:2.3.1", dock.StateRunning, "healthy", 26*time.Hour, "8000:8000"),
		c("notes", "db", "postgres:17.6", dock.StateRunning, "", 26*time.Hour),
		c("uptime-kuma", "uptime-kuma", "louislam/uptime-kuma:2.5.4", dock.StateRunning, "starting", 40*time.Second, "3001:3001"),
		{ID: "9a1b2c3d4e5f", Name: "scratch", Image: "nginx:alpine", State: dock.StateExited, ExitCode: 137, FinishedAt: now.Add(-6 * day), Labels: map[string]string{}},
	}

	cfg := config.Default()
	cfg.ComposeRoots = []string{root}
	a := New(cfg, filepath.Join(home, "config.toml"), &fakeRunner{})
	a.Update(tea.WindowSizeMsg{Width: 132, Height: 30})
	a.Update(tea.BackgroundColorMsg{Color: hexColor("#18181B")})
	a.Update(dockerInfoMsg{info: dock.Info{Context: "desktop-linux", Version: "29.8.2"}})
	a.Update(containersMsg{cs: containers})
	a.Update(loadRoot(root, root)())
	for i, r := range a.docker.view {
		if r.c != nil && r.c.Name == "monitoring-grafana-1" {
			a.docker.cursor = i
		}
	}
	a.Update(logsMsg{id: a.docker.selected().ID, lines: []string{
		`logger=settings level=info msg="Starting Grafana" version=12.2.0`,
		`logger=sqlstore level=info msg="Connecting to DB" dbtype=sqlite3`,
		`logger=http.server level=info msg="HTTP Server Listen" address=[::]:3000`,
		`logger=context level=info msg="Request Completed" method=GET path=/api/health status=200`,
	}})
	writeSVG(t, filepath.Join(dir, "docker.svg"), a)

	press(a, '3')
	for i, row := range a.compose.view {
		if row.s != nil && row.s.Name() == "stacks/uptime-kuma" {
			a.compose.cursor = i
			row.s.updates = []updates.Result{{Service: "uptime-kuma", Image: "louislam/uptime-kuma:2.5.4", State: updates.NewVersion, Suggest: "louislam/uptime-kuma:2.5.5"}}
		}
	}
	for i, row := range a.compose.view {
		if row.s != nil && row.s.Name() == "old/services/web.yml" {
			a.compose.cursor = i
		}
	}
	writeSVG(t, filepath.Join(dir, "compose.svg"), a)
}

func hexColor(h string) interface {
	RGBA() (r, g, b, a uint32)
} {
	n, _ := strconv.ParseUint(strings.TrimPrefix(h, "#"), 16, 32)
	return rgb{uint8(n >> 16), uint8(n >> 8), uint8(n)}
}

type rgb struct{ r, g, b uint8 }

func (c rgb) RGBA() (r, g, b, a uint32) {
	return uint32(c.r) * 0x101, uint32(c.g) * 0x101, uint32(c.b) * 0x101, 0xffff
}

// writeSVG draws the app's screen as an SVG, cell by cell.
func writeSVG(t *testing.T, path string, a *App) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(ansiToSVG(a.render(), a.w, a.h)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}

var sgr = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// ansiToSVG converts lines of text with SGR colour codes (24-bit colour,
// bold, reset) into an SVG of a terminal w cells wide and h high.
func ansiToSVG(text string, w, h int) string {
	const cw, lh, pad, fs = 8.4, 18.0, 14.0, 14
	const bg, fg = "#18181B", "#E4E4E7"
	width, height := float64(w)*cw+2*pad, float64(h)*lh+2*pad
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f">`+"\n", width, height, width, height)
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" rx="8" fill="%s"/>`+"\n", bg)
	fmt.Fprintf(&b, `<g font-family="Cascadia Mono, 'SF Mono', Menlo, Consolas, 'DejaVu Sans Mono', monospace" font-size="%d" xml:space="preserve">`+"\n", fs)

	for row, line := range strings.Split(text, "\n") {
		y := pad + float64(row)*lh
		col := 0
		cur := struct {
			fg, bg string
			bold   bool
		}{fg: fg}
		var runs strings.Builder
		flush := func(s string) {
			if s == "" {
				return
			}
			n := ansi.StringWidth(s)
			x := pad + float64(col)*cw
			if cur.bg != "" {
				fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.0f" fill="%s"/>`+"\n", x, y, float64(n)*cw, lh, cur.bg)
			}
			// Spaces are positions, not glyphs: only the text between them
			// is drawn, sized to its cells so fonts can't drift.
			trimmed := strings.TrimLeft(s, " ")
			lead := n - ansi.StringWidth(trimmed)
			trimmed = strings.TrimRight(trimmed, " ")
			if trimmed != "" {
				weight := ""
				if cur.bold {
					weight = ` font-weight="bold"`
				}
				fmt.Fprintf(&runs, `<text x="%.1f" y="%.1f" fill="%s"%s textLength="%.1f" lengthAdjust="spacingAndGlyphs">%s</text>`+"\n",
					x+float64(lead)*cw, y+lh*0.75, cur.fg, weight, float64(ansi.StringWidth(trimmed))*cw, html.EscapeString(trimmed))
			}
			col += n
		}
		rest := line
		for {
			loc := sgr.FindStringSubmatchIndex(rest)
			if loc == nil {
				flush(rest)
				break
			}
			flush(rest[:loc[0]])
			codes := strings.Split(rest[loc[2]:loc[3]], ";")
			for i := 0; i < len(codes); i++ {
				switch codes[i] {
				case "", "0":
					cur.fg, cur.bg, cur.bold = fg, "", false
				case "1":
					cur.bold = true
				case "22":
					cur.bold = false
				case "39":
					cur.fg = fg
				case "49":
					cur.bg = ""
				case "38", "48":
					if i+4 < len(codes) && codes[i+1] == "2" {
						r, _ := strconv.Atoi(codes[i+2])
						g, _ := strconv.Atoi(codes[i+3])
						bl, _ := strconv.Atoi(codes[i+4])
						c := fmt.Sprintf("#%02X%02X%02X", r, g, bl)
						if codes[i] == "38" {
							cur.fg = c
						} else {
							cur.bg = c
						}
						i += 4
					}
				}
			}
			rest = rest[loc[1]:]
		}
		b.WriteString(runs.String())
	}
	b.WriteString("</g>\n</svg>\n")
	return b.String()
}
