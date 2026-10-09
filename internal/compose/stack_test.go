package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeTree creates files (path -> content) under a new temp root.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// LooseTree is shaped like a compose repo that grew file by file: several
// stacks per folder, mixed file names, cross-file network_mode.
var LooseTree = map[string]string{
	"apps/clock/docker-compose.yml": "services:\n  clock:\n    image: ghcr.io/me/clock:latest\n    container_name: clock\n    ports:\n      - \"8080:80\"\n",
	"apps/minecraft/compose.yml": `services:
  minecraft:
    image: itzg/minecraft-server:java17
    ports:
      - "25565:25565"
    environment:
      TZ: "${TZ}"
      RCON_PASSWORD: "${MC_RCON_PASSWORD}"   # not in the example
      MOTD: "it's $$5"
`,
	"apps/minecraft/.env.example": "TZ=America/New_York\n",
	"arr/.env.example":            "PUID=1000\nPGID=1000\nPIAUSERNAME=x\n",
	"arr/gluetun.yml": `# a VPN
services:
  gluetun:
    image: qmcgaw/gluetun
    ports:
      - 8080:8080    # qBittorrent WebUI
      - 21527:21527/udp
    environment:
      - USER=$USERNAME
      - LOG=${LOG_LEVEL:-info}
`,
	"arr/qbit.yml": `services:
  qbittorrent:
    image: linuxserver/qbittorrent:latest
    network_mode: "service:gluetun"
    environment:
      - PUID=${PUID}
`,
	"arr/notes.yml":               "just: notes\n",
	"README.md":                   "# composes\n",
	".git/config":                 "[core]\n",
	"deep/a/b/c/d/e/compose.yaml": "services:\n  too-deep:\n    image: x\n",
}

func TestFindStacks(t *testing.T) {
	root := writeTree(t, LooseTree)
	stacks, err := FindStacks(root, 4)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, s := range stacks {
		rels = append(rels, s.Rel)
	}
	if want := []string{"apps/clock/docker-compose.yml", "apps/minecraft/compose.yml", "arr/gluetun.yml", "arr/qbit.yml"}; !slices.Equal(rels, want) {
		t.Fatalf("got %q, want %q", rels, want)
	}
	byRel := map[string]Stack{}
	for _, s := range stacks {
		byRel[s.Rel] = s
	}

	clock := byRel["apps/clock/docker-compose.yml"]
	if clock.Name() != "apps/clock" || clock.Siblings != 0 || len(clock.Services) != 1 {
		t.Errorf("clock: %+v", clock)
	}
	if svc := clock.Services[0]; svc.ContainerName != "clock" || !slices.Equal(svc.Ports, []string{"8080"}) || svc.Image != "ghcr.io/me/clock:latest" {
		t.Errorf("clock service: %+v", svc)
	}

	gl := byRel["arr/gluetun.yml"]
	if gl.Name() != "arr/gluetun.yml" || gl.Siblings != 1 {
		t.Errorf("loose files are listed by file: %q siblings=%d", gl.Name(), gl.Siblings)
	}
	if got := gl.Services[0].Ports; !slices.Equal(got, []string{"8080", "21527/udp"}) {
		t.Errorf("gluetun ports: %q", got)
	}
	if got := varNames(gl.Vars); got != "USERNAME LOG_LEVEL(default)" {
		t.Errorf("gluetun vars: %s", got)
	}

	mc := byRel["apps/minecraft/compose.yml"]
	if got := varNames(mc.Vars); got != "TZ MC_RCON_PASSWORD" {
		t.Errorf("minecraft vars ($$ is not a variable): %s", got)
	}
	if qb := byRel["arr/qbit.yml"]; qb.Services[0].NetworkMode != "service:gluetun" {
		t.Errorf("qbit: %+v", qb.Services[0])
	}
}

func varNames(vs []Var) string {
	var out []string
	for _, v := range vs {
		n := v.Name
		if v.HasDefault {
			n += "(default)"
		}
		if v.Required {
			n += "(required)"
		}
		out = append(out, n)
	}
	return strings.Join(out, " ")
}

func TestFindVars(t *testing.T) {
	text := `x: ${A} $B ${C:-c} ${D-d} ${E:?set E} ${F:+f}
# ${COMMENTED}
y: "a # not a comment ${G}"
z: $$H ${A:-again}
`
	if got := varNames(findVars(text)); got != "A B C(default) D(default) E(required) F G" {
		t.Fatalf("got %s", got)
	}
}

func TestHostPort(t *testing.T) {
	root := writeTree(t, map[string]string{"compose.yaml": `services:
  a:
    image: x
    ports:
      - "80"
      - "8080:80"
      - "127.0.0.1:5432:5432"
      - "0.0.0.0:9000:9000"
      - "${WEB_PORT:-3000}:3000"
      - 53:53/udp
      - target: 443
        published: "8443"
        host_ip: 127.0.0.1
      - target: 81
        published: 8081
`})
	stacks, err := FindStacks(root, 2)
	if err != nil || len(stacks) != 1 {
		t.Fatal(err, len(stacks))
	}
	want := []string{"8080", "127.0.0.1:5432", "9000", "3000", "53/udp", "127.0.0.1:8443", "8081"}
	if got := stacks[0].Services[0].Ports; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if stacks[0].Name() != "." && stacks[0].Name() != "compose.yaml" {
		t.Logf("root stack name: %q", stacks[0].Name())
	}
}

func TestStackProjectLayersEnvFiles(t *testing.T) {
	root := writeTree(t, map[string]string{
		".env":                           "PUID=1000\n",
		"stacks/media/plex/compose.yaml": "services:\n  plex:\n    image: x\n",
		"stacks/media/plex/.env":         "PLEX_CLAIM=x\n",
		"stacks/apps/clock/compose.yaml": "services:\n  clock:\n    image: x\n",
		"x-dockgit.yaml":                 "services:\n  a:\n    image: x\nx-dockgit:\n  description: Wall clock\n  repo: ~/code/clock\n  autostart: true\n",
	})
	stacks, _ := FindStacks(root, 4)
	byName := map[string]Stack{}
	for _, s := range stacks {
		byName[s.Name()] = s
	}
	plex := byName["stacks/media/plex"]
	p := plex.Project()
	if len(p.EnvFiles) != 2 || !strings.HasSuffix(p.EnvFiles[0], filepath.Join(root, ".env")) || filepath.Base(filepath.Dir(p.EnvFiles[1])) != "plex" {
		t.Fatalf("plex env files: %q", p.EnvFiles)
	}
	if args := strings.Join(p.Args("up", "-d"), " "); !strings.Contains(args, "-f compose.yaml --env-file") {
		t.Fatalf("args: %s", args)
	}
	// clock has no .env of its own: only the root's applies.
	if p := byName["stacks/apps/clock"].Project(); len(p.EnvFiles) != 1 {
		t.Fatalf("clock env files: %q", p.EnvFiles)
	}
	meta := byName["x-dockgit.yaml"].Meta
	if meta.Description != "Wall clock" || meta.Repo != "~/code/clock" || !meta.Autostart {
		t.Fatalf("meta: %+v", meta)
	}
}

// dockge keeps shared values in global.env beside the stack folders.
func TestStackProjectUsesGlobalEnv(t *testing.T) {
	root := writeTree(t, map[string]string{
		"stacks/global.env":         "PUID=1000\n",
		"stacks/plex/compose.yaml":  "services:\n  plex:\n    image: x\n",
		"stacks/plex/.env":          "PLEX_CLAIM=x\n",
		"stacks/global.env.example": "PUID=\n",
		"stacks/clock/compose.yaml": "services:\n  clock:\n    image: x\n",
	})
	stacks, _ := FindStacks(root, 4)
	plex := stacks[1]
	if plex.Name() != "stacks/plex" {
		t.Fatalf("got %q", plex.Name())
	}
	want := []string{filepath.Join(root, ".env"), filepath.Join(root, "stacks", "global.env"), filepath.Join(root, "stacks", "plex", ".env")}
	if got := plex.EnvFiles(); !slices.Equal(got, want) {
		t.Fatalf("env files:\n%q\nwant\n%q", got, want)
	}
	// Only the ones that exist are passed, in order.
	if got := plex.Project().EnvFiles; !slices.Equal(got, want[1:]) {
		t.Fatalf("project env files: %q", got)
	}
	if got := plex.ExampleFiles()[1]; got != filepath.Join(root, "stacks", "global.env.example") {
		t.Fatalf("example files: %q", plex.ExampleFiles())
	}
}

func TestReadEnv(t *testing.T) {
	root := writeTree(t, map[string]string{".env": "# comment\nA=1\nexport B = two \nC=\"quoted value\"\nD='single'\r\nnot a line\n\nE=\n"})
	env, err := ReadEnv(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "two", "C": "quoted value", "D": "single", "E": ""}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("got %v", env)
	}
	if env, err := ReadEnv(filepath.Join(root, "missing")); err != nil || len(env) != 0 {
		t.Fatalf("missing file: %v %v", env, err)
	}
}

func TestStackOwns(t *testing.T) {
	root := writeTree(t, LooseTree)
	stacks, _ := FindStacks(root, 4)
	gl := stacks[2]
	if !gl.Owns([]string{gl.File}) || gl.Owns([]string{stacks[3].File}) {
		t.Fatal("Owns should match the exact file")
	}
}
