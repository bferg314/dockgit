package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bferg314/dockgit/internal/compose"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// A loose layout, shaped like a compose repo that grew file by file.
var loose = map[string]string{
	"apps/clock/docker-compose.yml": "services:\n  clock:\n    image: ghcr.io/me/clock:latest\n    container_name: clock\n    ports:\n      - \"8080:80\"\n",
	"apps/minecraft/compose.yml":    "services:\n  minecraft:\n    image: itzg/minecraft-server:java17\n    environment:\n      TZ: ${TZ}\n      RCON: ${MC_RCON_PASSWORD}\n",
	"apps/minecraft/.env.example":   "TZ=America/New_York\n",
	"arr/.env.example":              "PUID=1000\nPIAUSERNAME=x\n",
	"arr/gluetun.yml":               "services:\n  gluetun:\n    image: qmcgaw/gluetun:v3\n    ports:\n      - 8080:8080\n    environment:\n      - USER=$USERNAME\n",
	"arr/qbit.yml":                  "services:\n  qbittorrent:\n    image: linuxserver/qbittorrent:4.6.0\n    network_mode: \"service:gluetun\"\n    environment:\n      - PUID=${PUID}\n",
	"watchtower/compose.yaml":       "services:\n  watchtower:\n    image: nickfedor/watchtower:1.9\n    environment:\n      - HOOK=${DISCORD_WEBHOOK_ID}\n",
	"broken/compose.yaml":           "services:\n  a: [unclosed\n",
}

func check(t *testing.T, files map[string]string, containers ...Container) (string, []Finding) {
	t.Helper()
	root := writeTree(t, files)
	stacks, err := compose.FindStacks(root, 4)
	if err != nil {
		t.Fatal(err)
	}
	return root, Check(root, stacks, containers)
}

func find(fs []Finding, stack, substr string) *Finding {
	for i, f := range fs {
		if f.Stack == stack && strings.Contains(f.Msg, substr) {
			return &fs[i]
		}
	}
	return nil
}

func TestLooseLayout(t *testing.T) {
	root, fs := check(t, loose)
	expect := []struct {
		sev           Severity
		stack, substr string
	}{
		{Problem, "arr/qbit.yml", "network_mode service:gluetun from another file"},
		{Problem, "", "host port 8080 is published by apps/clock and arr/gluetun.yml"},
		{Problem, "broken", "can't read compose.yaml"},
		{Warning, "arr/", `2 compose files share this folder, so compose gives them all the project name "arr"`},
		{Warning, "arr/gluetun.yml", "uses USERNAME, not in .env.example"},
		{Warning, "apps/minecraft", "uses MC_RCON_PASSWORD, not in .env.example"},
		{Warning, "watchtower", "uses DISCORD_WEBHOOK_ID but there's no .env.example"},
		{Note, "apps/clock", "is named docker-compose.yml"},
		{Note, "apps/clock", "sets container_name: clock"},
		{Note, "apps/clock", "no pinned version for ghcr.io/me/clock"},

		{Note, "apps/minecraft", "has no .env yet"},
	}
	for _, e := range expect {
		f := find(fs, e.stack, e.substr)
		if f == nil {
			t.Errorf("missing %q on %q", e.substr, e.stack)
			continue
		}
		if f.Severity != e.sev || f.Fix == "" {
			t.Errorf("%q: severity %d, fix %q", e.substr, f.Severity, f.Fix)
		}
	}
	// Things that are fine aren't flagged.
	if f := find(fs, "arr/qbit.yml", "uses PUID"); f != nil {
		t.Errorf("PUID is in arr/.env.example: %+v", f)
	}
	if f := find(fs, "arr/gluetun.yml", "pinned"); f != nil {
		t.Errorf("gluetun:v3 is pinned: %+v", f)
	}
	for i := 1; i < len(fs); i++ {
		if fs[i].Severity < fs[i-1].Severity {
			t.Fatal("findings should be ordered by severity")
		}
	}
	_ = root
}

func TestStackLayoutIsClean(t *testing.T) {
	_, fs := check(t, map[string]string{
		".env.example":                        "PUID=1000\nTZ=UTC\n",
		"stacks/media/plex/compose.yaml":      "services:\n  plex:\n    image: linuxserver/plex:${TAG:-latest}\n    environment:\n      - PUID=${PUID}\n      - CLAIM=${PLEX_CLAIM}\n",
		"stacks/media/plex/.env.example":      "PLEX_CLAIM=\n",
		"stacks/media/plex/.env":              "PLEX_CLAIM=x\n",
		"stacks/media/downloads/compose.yaml": "services:\n  gluetun:\n    image: qmcgaw/gluetun:v3\n    ports: [\"8080:8080\"]\n  qbittorrent:\n    image: linuxserver/qbittorrent:4.6.0\n    network_mode: \"service:gluetun\"\n",
	})
	if len(fs) != 0 {
		for _, f := range fs {
			t.Errorf("unexpected: %s %s: %s", f.Severity.Icon(), f.Stack, f.Msg)
		}
	}
}

// Variables listed in dockge's global.env.example count as listed.
func TestGlobalEnvExample(t *testing.T) {
	_, fs := check(t, map[string]string{
		"stacks/global.env.example": "PUID=1000\nTZ=UTC\n",
		"stacks/plex/compose.yaml":  "services:\n  plex:\n    image: p:1\n    environment:\n      - PUID=${PUID}\n      - CLAIM=${PLEX_CLAIM}\n",
		"stacks/plex/.env.example":  "PLEX_CLAIM=\n",
		"stacks/plex/.env":          "PLEX_CLAIM=x\n",
		"stacks/clock/compose.yaml": "services:\n  clock:\n    image: c:1\n    environment:\n      - TZ=${TZ}\n",
	})
	if len(fs) != 0 {
		for _, f := range fs {
			t.Errorf("unexpected: %s %s: %s", f.Severity.Icon(), f.Stack, f.Msg)
		}
	}
}

// "has no .env yet" only when the stack's own .env.example lists a
// required variable: shared ones come from global.env.
func TestNoEnvYetOnlyWhenNeeded(t *testing.T) {
	_, fs := check(t, map[string]string{
		"stacks/global.env.example": "PUID=1000\n",
		"stacks/plex/compose.yaml":  "services:\n  plex:\n    image: p:1\n    environment:\n      - PUID=${PUID}\n      - CLAIM=${PLEX_CLAIM:-}\n",
		"stacks/plex/.env.example":  "PLEX_CLAIM=\n",
		"stacks/mc/compose.yaml":    "services:\n  mc:\n    image: m:1\n    environment:\n      - PUID=${PUID}\n      - RCON=${MC_RCON_PASSWORD}\n",
		"stacks/mc/.env.example":    "MC_RCON_PASSWORD=x\n",
	})
	if f := find(fs, "stacks/plex", "has no .env yet"); f != nil {
		t.Errorf("plex needs nothing of its own: %+v", f)
	}
	if f := find(fs, "stacks/mc", "has no .env yet (needs MC_RCON_PASSWORD)"); f == nil {
		t.Errorf("mc needs MC_RCON_PASSWORD: %+v", fs)
	}
}

func TestContainerNameClash(t *testing.T) {
	_, fs := check(t, map[string]string{
		"arr/gluetun-wg.yml":   "services:\n  gluetun:\n    image: g:1\n    container_name: gluetun\n",
		"arr/gluetun-ovpn.yml": "services:\n  vpn:\n    image: g:1\n    container_name: gluetun\n",
		"apps/x/compose.yaml":  "services:\n  x:\n    image: x:1\n    container_name: x\n",
	})
	f := find(fs, "", "container_name gluetun is set by arr/gluetun-ovpn.yml and arr/gluetun-wg.yml")
	if f == nil || f.Severity != Problem {
		t.Fatalf("got %+v", fs)
	}
	if find(fs, "", "container_name x is set") != nil {
		t.Fatal("a name used once is not a clash")
	}
}

func TestRunningFromOldPath(t *testing.T) {
	root := writeTree(t, map[string]string{"stacks/apps/clock/compose.yaml": "services:\n  clock:\n    image: x:1\n"})
	stacks, _ := compose.FindStacks(root, 4)
	gone := filepath.Join(root, "apps", "clock", "docker-compose.yml")
	elsewhere := filepath.Join(t.TempDir(), "compose.yaml")
	fs := Check(root, stacks, []Container{
		{Name: "clock", ConfigFiles: []string{gone}},
		{Name: "other", ConfigFiles: []string{elsewhere}}, // not under this root
		{Name: "new", ConfigFiles: []string{filepath.Join(root, "stacks", "apps", "clock", "compose.yaml")}},
	})
	if len(fs) != 1 || !strings.Contains(fs[0].Msg, "clock was started from apps/clock/docker-compose.yml, which no longer exists") {
		t.Fatalf("got %+v", fs)
	}
}
