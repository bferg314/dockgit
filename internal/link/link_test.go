package link

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bferg314/dockgit/internal/compose"
	"github.com/bferg314/dockgit/internal/dock"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// container builds a compose container started from file.
func container(name, image, file, state string, ports ...dock.Port) *dock.Container {
	c := &dock.Container{ID: name, Name: name, Image: image, State: state, Ports: ports, Labels: map[string]string{}}
	if file != "" {
		c.Labels[dock.LabelConfigFiles] = file
		c.Labels[dock.LabelWorkingDir] = filepath.Dir(file)
		c.Labels[dock.LabelProject] = filepath.Base(filepath.Dir(file))
		svc := name
		if i := strings.Index(name, "-"); i >= 0 {
			svc = strings.TrimSuffix(strings.TrimPrefix(name, name[:i+1]), "-1")
		}
		c.Labels[dock.LabelService] = svc
	}
	return c
}

func setup(t *testing.T) (*Index, string, string) {
	t.Helper()
	code := t.TempDir()
	repo := filepath.Join(code, "clock")
	write(t, repo, map[string]string{"docker-compose.yml": "services:\n  clock:\n    build: .\n    ports: [\"8080:80\"]\n"})
	composes := filepath.Join(code, "composes")
	write(t, composes, map[string]string{
		"stacks/clock/compose.yaml":     "services:\n  clock:\n    image: ghcr.io/bferg314/clock:1.4.0\n    ports: [\"8090:80\"]\n",
		"stacks/downloads/compose.yaml": "services:\n  gluetun:\n    image: qmcgaw/gluetun:v3.41.3\n    ports: [\"8080:8080\"]\n  qbittorrent:\n    image: linuxserver/qbittorrent:5.2.4\n    network_mode: service:gluetun\n",
		"stacks/plex/compose.yaml":      "services:\n  plex:\n    image: linuxserver/plex:1.43.4\n    container_name: plex\n",
		"stacks/clocktwo/compose.yaml":  "services:\n  clock:\n    image: busybox:1\nx-dockgit:\n  repo: " + filepath.ToSlash(repo) + "\n",
	})
	stacks, err := compose.FindStacks(composes, 4)
	if err != nil {
		t.Fatal(err)
	}
	ix := &Index{
		Repos: []Repo{{Key: "~/code/clock", Name: "clock",
			Project: compose.Project{Dir: repo, Files: []string{"docker-compose.yml"}},
			Remotes: []string{"git@github.com:bferg314/clock.git"}}},
		Stacks: stacks,
	}
	return ix, repo, composes
}

func TestSourceOf(t *testing.T) {
	ix, repo, composes := setup(t)
	cases := []struct {
		c    *dock.Container
		want string
	}{
		{container("clock-clock-1", "clock-clock", filepath.Join(repo, "docker-compose.yml"), dock.StateRunning), "repo clock"},
		{container("downloads-gluetun-1", "qmcgaw/gluetun:v3.41.3", filepath.Join(composes, "stacks", "downloads", "compose.yaml"), dock.StateRunning), "stack stacks/downloads"},
		{container("arr-plex-1", "linuxserver/plex", filepath.Join(t.TempDir(), "arr", "plex.yml"), dock.StateRunning), "/arr/plex.yml"},
		{container("x", "busybox", "", dock.StateRunning), ""},
	}
	// A compose project dockgit doesn't list, whose file exists.
	elsewhere := filepath.Join(t.TempDir(), "calliope", "docker-compose.yml")
	write(t, filepath.Dir(elsewhere), map[string]string{"docker-compose.yml": "services: {}\n"})
	cases = append(cases, struct {
		c    *dock.Container
		want string
	}{container("calliope-app-1", "calliope-app", elsewhere, dock.StateRunning), "compose calliope"})

	for _, c := range cases {
		if got := ix.SourceOf(c.c).Label(); !strings.Contains(got, c.want) || (c.want == "" && got != "") {
			t.Errorf("%s: got %q, want %q", c.c.Name, got, c.want)
		}
	}
}

func TestRepoForStack(t *testing.T) {
	ix, _, _ := setup(t)
	byName := map[string]compose.Stack{}
	for _, s := range ix.Stacks {
		byName[s.Name()] = s
	}
	// The image ghcr.io/bferg314/clock matches the repo's remote.
	if r := ix.RepoForStack(byName["stacks/clock"]); r == nil || r.Key != "~/code/clock" {
		t.Errorf("by image: %+v", r)
	}
	// x-dockgit repo names it directly.
	if r := ix.RepoForStack(byName["stacks/clocktwo"]); r == nil || r.Key != "~/code/clock" {
		t.Errorf("by x-dockgit: %+v", r)
	}
	if r := ix.RepoForStack(byName["stacks/plex"]); r != nil {
		t.Errorf("plex has no repo: %+v", r)
	}
	if got := ix.StacksForRepo("~/code/clock"); len(got) != 2 {
		t.Errorf("stacks for clock: %d", len(got))
	}
}

func TestImageName(t *testing.T) {
	cases := map[string]string{
		"postgres:17-alpine":                "docker.io/library/postgres",
		"linuxserver/plex":                  "docker.io/linuxserver/plex",
		"ghcr.io/bferg314/clock:1.4.0":      "ghcr.io/bferg314/clock",
		"localhost:5000/app:dev":            "localhost:5000/app",
		"qmcgaw/gluetun@sha256:abc":         "docker.io/qmcgaw/gluetun",
		"Docker.io/LinuxServer/Plex:latest": "docker.io/linuxserver/plex",
	}
	for in, want := range cases {
		if got := ImageName(in); got != want {
			t.Errorf("ImageName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPreflight(t *testing.T) {
	ix, repo, composes := setup(t)
	var downloads compose.Stack
	for _, s := range ix.Stacks {
		if s.Name() == "stacks/downloads" {
			downloads = s
		}
	}
	file := filepath.Join(composes, "stacks", "downloads", "compose.yaml")
	repoClock := container("clock-clock-1", "clock-clock", filepath.Join(repo, "docker-compose.yml"), dock.StateRunning,
		dock.Port{Container: "80/tcp", HostIP: "0.0.0.0", HostPort: "8080"}, dock.Port{Container: "80/tcp", HostIP: "::", HostPort: "8080"})
	oldGluetun := container("arr-gluetun-1", "qmcgaw/gluetun", filepath.Join(t.TempDir(), "arr", "gluetun.yml"), dock.StateRunning)
	oldGluetun.Labels[dock.LabelService] = "gluetun"
	ownOld := container("downloads-gluetun-1", "qmcgaw/gluetun:v3.41.2", file, dock.StateExited)
	redis := container("calliope-redis-1", "redis:7", filepath.Join(t.TempDir(), "x", "compose.yaml"), dock.StateRunning)

	target := Target{Name: "downloads", Services: downloads.Services, Missing: []string{"PIA_USERNAME"},
		Owns: func(c *dock.Container) bool { return downloads.Owns(c.ConfigFiles()) }}
	ps := Preflight(target, []*dock.Container{repoClock, oldGluetun, ownOld, redis})

	kinds := map[string]Problem{}
	for _, p := range ps {
		kinds[p.Kind] = p
	}
	if p := kinds[PortTaken]; !strings.Contains(p.Msg, "port 8080 (gluetun) is held by clock-clock-1") || len(p.Holders) != 1 || p.Remove {
		t.Errorf("port: %+v", p)
	}
	if p := kinds[AlreadyRuns]; !strings.Contains(p.Msg, "arr-gluetun-1 already runs gluetun:v3.41.3") || !p.Remove {
		t.Errorf("duplicate: %+v", p)
	}
	if p := kinds[Unset]; !strings.Contains(p.Msg, "PIA_USERNAME") {
		t.Errorf("unset: %+v", p)
	}
	// qbittorrent's network_mode points inside the stack: not a problem.
	if _, ok := kinds[NeedsOther]; ok {
		t.Errorf("service:gluetun is in the same stack: %+v", kinds[NeedsOther])
	}
	if len(ps) != 3 {
		t.Errorf("got %d problems: %+v", len(ps), ps)
	}
}

func TestPreflightNamesAndAddresses(t *testing.T) {
	plex := compose.Service{Name: "plex", Image: "linuxserver/plex:1.43.4", ContainerName: "plex", Ports: []string{"127.0.0.1:5432", "53/udp"}}
	stoppedPlex := container("plex", "lscr.io/linuxserver/plex", "", dock.StateExited)
	pg := container("pg", "postgres", "", dock.StateRunning, dock.Port{Container: "5432/tcp", HostIP: "127.0.0.2", HostPort: "5432"})
	dns := container("dns", "coredns", "", dock.StateRunning, dock.Port{Container: "53/tcp", HostPort: "53"})
	ps := Preflight(Target{Services: []compose.Service{plex}}, []*dock.Container{stoppedPlex, pg, dns})
	if len(ps) != 1 || ps[0].Kind != NameTaken || !ps[0].Remove {
		t.Fatalf("only the name should clash (a stopped container still holds it; 127.0.0.2 isn't 127.0.0.1; tcp isn't udp): %+v", ps)
	}
}
