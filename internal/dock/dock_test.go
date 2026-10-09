package dock

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeRunner answers commands from a table keyed by the joined args.
type fakeRunner struct {
	out    map[string]string
	errs   map[string]error
	stream string
	calls  []string
}

func (f *fakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	return []byte(f.out[key]), f.errs[key]
}

func (f *fakeRunner) Combined(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return f.Run(ctx, dir, args...)
}

func (f *fakeRunner) Stream(_ context.Context, _ string, args ...string) (io.ReadCloser, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	return io.NopCloser(strings.NewReader(f.stream)), nil
}

func (f *fakeRunner) StreamCombined(ctx context.Context, dir string, args ...string) (io.ReadCloser, error) {
	return f.Stream(ctx, dir, args...)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	appID   = "fc468a524c81e8d4e8b53f201d60273a63a0d30ef783b92ca5adb80870913e2a"
	pgID    = "e811f33c6809cb5705b89e4cd2b78add8e0cf210ae4303d761b5f997cf60ace9"
	redisID = "0f491f956edd486ef5b8e287cb63b5e7ae775bf52b2dd366c311b395071189e0"
	nginxID = "9a1b2c3d4e5f0000000000000000000000000000000000000000000000000000"
)

func listRunner(t *testing.T) *fakeRunner {
	ids := []string{appID, pgID, redisID, nginxID}
	return &fakeRunner{out: map[string]string{
		"ps -aq --no-trunc": strings.Join(ids, "\n") + "\n",
		"inspect --type container " + strings.Join(ids, " "): fixture(t, "inspect.json"),
	}}
}

func TestList(t *testing.T) {
	cs, err := List(context.Background(), listRunner(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 4 {
		t.Fatalf("got %d containers", len(cs))
	}
	byName := map[string]*Container{}
	for _, c := range cs {
		byName[c.Name] = c
	}

	app := byName["calliope-poker-app-1"]
	if app == nil || app.ID != appID || app.Image != "calliope-poker-app" || !app.Running() {
		t.Fatalf("app: %+v", app)
	}
	if app.Project() != "calliope-poker" || app.Service() != "app" ||
		app.WorkingDir() != `C:\Users\me\code\calliope-poker` ||
		!slices.Equal(app.ConfigFiles(), []string{`C:\Users\me\code\calliope-poker\docker-compose.yml`}) {
		t.Errorf("compose labels: project=%q service=%q dir=%q files=%q", app.Project(), app.Service(), app.WorkingDir(), app.ConfigFiles())
	}
	if app.Labels["com.docker.compose.depends_on"] != "postgres:service_healthy:false,redis:service_healthy:false" {
		t.Errorf("labels with commas must survive: %q", app.Labels["com.docker.compose.depends_on"])
	}
	// IPv4 and IPv6 bindings of one port show once.
	if pp := app.PublishedPorts(); len(pp) != 1 || pp[0].HostPort != "8080" || pp[0].HostIP != "" || pp[0].Container != "3000/tcp" {
		t.Errorf("app ports: %+v", pp)
	}
	if app.StartedAt.IsZero() || app.Created.IsZero() || !app.FinishedAt.IsZero() {
		t.Errorf("times: %v %v %v", app.Created, app.StartedAt, app.FinishedAt)
	}
	if !strings.HasPrefix(app.Command, "docker-entrypoint.sh node_modules/.bin/tsx") {
		t.Errorf("command: %q", app.Command)
	}

	pg := byName["calliope-poker-postgres-1"]
	if pg.Health != "healthy" {
		t.Errorf("postgres health: %q", pg.Health)
	}
	if pp := pg.PublishedPorts(); len(pp) != 1 || pp[0].HostIP != "127.0.0.1" || pp[0].HostPort != "5432" {
		t.Errorf("postgres ports: %+v", pp)
	}
	if len(pg.Mounts) != 1 || pg.Mounts[0].Type != "volume" || pg.Mounts[0].Name != "calliope-poker_pgdata" {
		t.Errorf("postgres mounts: %+v", pg.Mounts)
	}
	if !slices.Equal(pg.Networks, []string{"calliope-poker_default"}) {
		t.Errorf("networks: %v", pg.Networks)
	}

	nginx := byName["old-nginx"]
	if nginx.State != StateExited || nginx.ExitCode != 137 || nginx.Project() != "" || nginx.Health != "" {
		t.Errorf("nginx: %+v", nginx)
	}
	// Exposed but not published.
	if len(nginx.Ports) != 1 || nginx.Ports[0].Published() || len(nginx.PublishedPorts()) != 0 {
		t.Errorf("nginx ports: %+v", nginx.Ports)
	}
}

func TestListEmpty(t *testing.T) {
	f := &fakeRunner{out: map[string]string{"ps -aq --no-trunc": "\n"}}
	cs, err := List(context.Background(), f)
	if err != nil || len(cs) != 0 || len(f.calls) != 1 {
		t.Fatalf("cs=%v err=%v calls=%v", cs, err, f.calls)
	}
}

// docker inspect fails when one of the IDs is gone but still prints the
// others; the missing one is reported as gone, not as an error.
func TestInspectReportsRemoved(t *testing.T) {
	inspect := fixture(t, "inspect.json")
	gone := "deadbeef0000"
	key := "inspect --type container " + appID + " " + gone
	f := &fakeRunner{
		out:  map[string]string{key: inspect},
		errs: map[string]error{key: errors.New("Error: No such container: deadbeef0000")},
	}
	cs, missing, err := Inspect(context.Background(), f, appID, gone)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) == 0 || !slices.Equal(missing, []string{gone}) {
		t.Fatalf("cs=%d missing=%v", len(cs), missing)
	}

	// Everything gone: docker prints "[]".
	key = "inspect --type container " + gone
	f = &fakeRunner{
		out:  map[string]string{key: "[]\n"},
		errs: map[string]error{key: errors.New("Error: No such container: deadbeef0000")},
	}
	cs, missing, err = Inspect(context.Background(), f, gone)
	if err != nil || len(cs) != 0 || !slices.Equal(missing, []string{gone}) {
		t.Fatalf("cs=%v missing=%v err=%v", cs, missing, err)
	}
}

func TestInspectDaemonDown(t *testing.T) {
	key := "inspect --type container " + appID
	down := errors.New("error during connect: the docker daemon is not running")
	f := &fakeRunner{errs: map[string]error{key: down}}
	if _, _, err := Inspect(context.Background(), f, appID); err != down {
		t.Fatalf("want the daemon error, got %v", err)
	}
}

func TestEvents(t *testing.T) {
	f := &fakeRunner{stream: fixture(t, "events.jsonl") + "not json\n"}
	ch, errc := Events(context.Background(), f)
	var got []string
	for e := range ch {
		got = append(got, e.Action)
		if e.ID == "" || e.Name != "dockgit-probe" || e.Time.Before(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("event: %+v", e)
		}
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	want := []string{"create", "start", "health_status", "kill", "stop", "die", "destroy"}
	if !slices.Equal(got, want) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	call := f.calls[0]
	for _, w := range []string{"events --format json", "--filter type=container", "--filter event=health_status"} {
		if !strings.Contains(call, w) {
			t.Errorf("command missing %q: %s", w, call)
		}
	}
	if strings.Contains(call, "exec_") {
		t.Errorf("exec events must be filtered out: %s", call)
	}
}

func TestGetInfo(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		"context show":                         "desktop-linux\n",
		"version --format {{.Server.Version}}": "29.8.2\n",
	}}
	info, err := GetInfo(context.Background(), f)
	if err != nil || info.Context != "desktop-linux" || info.Version != "29.8.2" {
		t.Fatalf("%+v %v", info, err)
	}

	down := errors.New("error during connect: open //./pipe/dockerDesktopLinuxEngine: The system cannot find the file specified.")
	f.errs = map[string]error{"version --format {{.Server.Version}}": down}
	if info, err := GetInfo(context.Background(), f); err != down || info.Context != "desktop-linux" {
		t.Fatalf("daemon down: %+v %v", info, err)
	}
}

func TestLogsClean(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		"logs --tail 20 x": "\x1b[32mready\x1b[0m\r\n\tindented\nloading 10%\rloading 100%\n",
	}}
	got, err := Logs(context.Background(), f, "x", 20)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ready", "    indented", "loading 100%"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCLIError(t *testing.T) {
	stderr := "\nError response from daemon: No such container: x\n\nRun 'docker --help' for usage.\n"
	if got := cliError(stderr, errors.New("exit status 1")).Error(); got != "Error response from daemon: No such container: x" {
		t.Fatalf("got %q", got)
	}
	if got := cliError("", errors.New("exit status 1")).Error(); got != "exit status 1" {
		t.Fatalf("got %q", got)
	}
}

func TestImages(t *testing.T) {
	f := &fakeRunner{out: map[string]string{"image ls --no-trunc --format json": fixture(t, "images.jsonl")}}
	imgs, err := Images(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 13 {
		t.Fatalf("got %d images", len(imgs))
	}
	for i := 1; i < len(imgs); i++ {
		if imgs[i].Created.After(imgs[i-1].Created) {
			t.Fatal("images should be newest first")
		}
	}
	var app, dangling *Image
	for _, img := range imgs {
		switch {
		case img.Repository == "calliope-poker-app":
			app = img
		case img.Dangling():
			dangling = img
		}
	}
	if app == nil || app.Ref() != "calliope-poker-app:latest" || app.Containers != 1 || app.Size != "322MB" ||
		app.ShortID() != "da2a281d96b4" || app.Created.IsZero() {
		t.Errorf("app image: %+v", app)
	}
	if dangling == nil || dangling.Ref() != "<none> cdcdcdcdcdcd" || dangling.Containers != 0 {
		t.Errorf("dangling image: %+v", dangling)
	}
}

func TestVolumes(t *testing.T) {
	f := &fakeRunner{out: map[string]string{"system df -v --format json": fixture(t, "df.json")}}
	vs, err := Volumes(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	var pg *Volume
	for _, v := range vs {
		if v.Name == "calliope-poker_pgdata" {
			pg = v
		}
	}
	if len(vs) != 8 || pg == nil || pg.Size != "194MB" || pg.Links != 1 || pg.Driver != "local" {
		t.Fatalf("volumes: %d, pgdata %+v", len(vs), pg)
	}
	if !slices.IsSortedFunc(vs, func(a, b *Volume) int { return strings.Compare(a.Name, b.Name) }) {
		t.Error("volumes should be sorted by name")
	}
}

func TestGetStats(t *testing.T) {
	f := &fakeRunner{out: map[string]string{"stats --no-stream --no-trunc --format json": fixture(t, "stats.jsonl")}}
	stats, err := GetStats(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if s := stats[appID]; len(stats) != 3 || s.CPU != "0.06%" || !strings.HasSuffix(s.Mem, "GiB") {
		t.Fatalf("stats: %v", stats)
	}
}

func TestPruneReportsReclaimed(t *testing.T) {
	f := &fakeRunner{out: map[string]string{"image prune -f": "Deleted Images:\ndeleted: sha256:ab\n\nTotal reclaimed space: 412MB\n"}}
	if got, err := PruneDanglingImages(context.Background(), f); err != nil || got != "412MB" {
		t.Fatalf("got %q %v", got, err)
	}
	f.out["image prune -f"] = "Total reclaimed space: 0B\n"
	if got, _ := PruneDanglingImages(context.Background(), f); got != "0B" {
		t.Fatalf("got %q", got)
	}
}

func TestShell(t *testing.T) {
	key := "exec x sh -c command -v bash || command -v sh"
	f := &fakeRunner{out: map[string]string{key: "/bin/bash\n"}}
	if sh, err := Shell(context.Background(), f, "x"); err != nil || sh != "/bin/bash" {
		t.Fatalf("got %q %v", sh, err)
	}
	f.out[key] = "/bin/sh\n"
	if sh, _ := Shell(context.Background(), f, "x"); sh != "/bin/sh" {
		t.Fatalf("got %q", sh)
	}
	f.errs = map[string]error{key: errors.New(`Error response from daemon: exec: "sh": executable file not found in $PATH`)}
	if _, err := Shell(context.Background(), f, "x"); err != ErrNoShell {
		t.Fatalf("distroless: got %v", err)
	}
}

func TestFollow(t *testing.T) {
	f := &fakeRunner{stream: "2026-10-08T14:12:16.813046562Z \x1b[32mready\x1b[0m\n2026-10-08T14:12:17.000000000Z second\n"}
	ch, errc := Follow(context.Background(), f, "x", 100)
	var got []string
	for l := range ch {
		got = append(got, l)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %q", got)
	}
	ts, text := TrimTimestamp(got[0])
	if ts != "2026-10-08T14:12:16.813046562Z" || text != "ready" {
		t.Fatalf("TrimTimestamp: %q %q", ts, text)
	}
	if f.calls[0] != "logs --follow --timestamps --tail 100 x" {
		t.Fatalf("command: %s", f.calls[0])
	}
	if ts, text := TrimTimestamp("no timestamp here"); ts != "" || text != "no timestamp here" {
		t.Fatalf("plain line: %q %q", ts, text)
	}
}

func TestDiskUsage(t *testing.T) {
	f := &fakeRunner{out: map[string]string{"system df --format json": fixture(t, "df-summary.jsonl")}}
	us, err := DiskUsage(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 4 || us[0].Type != "Images" || us[3].Type != "Build Cache" || us[0].Size == "" || us[0].Total == 0 {
		t.Fatalf("got %+v", us)
	}
}

func TestPruneBuildCache(t *testing.T) {
	key := "builder prune --force --reserved-space 10GB --filter until=168h"
	f := &fakeRunner{out: map[string]string{key: "ID\tRECLAIMABLE\tSIZE\nabc\ttrue\t1.2GB\nTotal:\t1.664GB\n"}}
	got, err := PruneBuildCache(context.Background(), f, "10GB", "168h")
	if err != nil || got != "1.664GB" {
		t.Fatalf("got %q %v", got, err)
	}
	f.out = map[string]string{"builder prune --force": "Total:\t0B\n"}
	if got, _ := PruneBuildCache(context.Background(), f, "", ""); got != "0B" {
		t.Fatalf("no limits: %q", got)
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"0B": 0, "458.8kB": 458800, "6.997GB": 6997000000, "10GB": 10_000_000_000,
		"1GiB": 1 << 30, "6.796GB (88%)": 6796000000, "512": 512, "1.5 MB": 1500000,
	}
	for in, want := range cases {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "lots", "10XB"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) should fail", bad)
		}
	}
}

func TestRepoDigests(t *testing.T) {
	key := "image inspect --format {{json .RepoDigests}} redis:7-alpine"
	f := &fakeRunner{out: map[string]string{key: "[\"redis@sha256:858f\"]\n"}}
	if got, err := RepoDigests(context.Background(), f, "redis:7-alpine"); err != nil || len(got) != 1 || got[0] != "redis@sha256:858f" {
		t.Fatalf("got %q %v", got, err)
	}
	f.errs = map[string]error{"image inspect --format {{json .RepoDigests}} nope:1": errors.New("Error response from daemon: No such image: nope:1")}
	if got, err := RepoDigests(context.Background(), f, "nope:1"); err != nil || got != nil {
		t.Fatalf("missing image: %q %v", got, err)
	}
}
