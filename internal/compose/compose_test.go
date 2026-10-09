package compose

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func touch(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("services: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	// face-to-face's layout, plus noise.
	touch(t, dir, "docker-compose.yml", "docker-compose.build.yml", "compose.override.yaml", "Dockerfile", "notes.yml")
	os.Mkdir(filepath.Join(dir, "compose.dir.yml"), 0o755)
	got, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"docker-compose.yml", "compose.override.yaml", "docker-compose.build.yml"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if pick := Pick(got); !slices.Equal(pick, []string{"docker-compose.yml"}) {
		t.Errorf("pick: %q", pick)
	}
	if pick := Pick([]string{"docker-compose.build.yml"}); pick != nil {
		t.Errorf("only variants should ask: %q", pick)
	}

	// compose.yaml wins over docker-compose.yml, as in compose itself.
	dir2 := t.TempDir()
	touch(t, dir2, "docker-compose.yml", "compose.yaml")
	if got, _ := Discover(dir2); !slices.Equal(Pick(got), []string{"compose.yaml"}) {
		t.Errorf("got %q", got)
	}
}

func TestArgs(t *testing.T) {
	p := Project{Dir: "/x", Files: []string{"docker-compose.yml", "docker-compose.build.yml"}, Name: "ff", EnvFiles: []string{"../.env", ".env.local"}}
	got := strings.Join(p.Args("up", "-d", "--build"), " ")
	want := "compose -f docker-compose.yml -f docker-compose.build.yml -p ff --env-file ../.env --env-file .env.local --progress plain up -d --build"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := strings.Join(Project{Dir: "/x"}.Args("down"), " "); got != "compose --progress plain down" {
		t.Fatalf("defaults: %s", got)
	}
}

func TestNormPath(t *testing.T) {
	cases := map[string]string{
		`C:\Users\me\code\clock`:                      "c:/users/me/code/clock",
		`C:\Users\me\code\clock\`:                     "c:/users/me/code/clock",
		"/mnt/c/Users/me/code/clock":                  "c:/users/me/code/clock",
		"/run/desktop/mnt/host/c/Users/me/code/clock": "c:/users/me/code/clock",
		"/host_mnt/c/Users/me/code/clock":             "c:/users/me/code/clock",
		`C:\`:                                         "c:/",
		"/home/me/code/clock/":                        "/home/me/code/clock",
	}
	for in, want := range cases {
		if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
			// Case is kept where file systems care about it.
			if strings.HasPrefix(want, "c:") {
				continue
			}
		}
		if got := NormPath(in); got != want {
			t.Errorf("NormPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOwns(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("uses Windows paths from real compose labels")
	}
	p := Project{Dir: `C:\Users\me\code\calliope-poker`}
	// The labels recorded from calliope-poker's containers.
	if !p.Owns([]string{`C:\Users\me\code\calliope-poker\docker-compose.yml`}, `C:\Users\me\code\calliope-poker`) {
		t.Error("should own its own containers")
	}
	// Started from WSL: same folder, other form.
	if !p.Owns([]string{"/mnt/c/Users/me/code/calliope-poker/docker-compose.yml"}, "") {
		t.Error("should own containers started from WSL")
	}
	// A different folder whose name starts the same.
	if p.Owns([]string{`C:\Users\me\code\calliope-poker-old\docker-compose.yml`}, `C:\Users\me\code\calliope-poker-old`) {
		t.Error("must not own a sibling folder's containers")
	}
	if p.Owns(nil, "") {
		t.Error("unmanaged containers belong to no project")
	}
}

func TestFilePaths(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "compose.yaml")
	if got := (Project{Dir: dir}).FilePaths(); !slices.Equal(got, []string{filepath.Join(dir, "compose.yaml")}) {
		t.Fatalf("default: %q", got)
	}
	if got := (Project{Dir: dir, Files: []string{"a.yml"}}).FilePaths(); !slices.Equal(got, []string{filepath.Join(dir, "a.yml")}) {
		t.Fatalf("explicit: %q", got)
	}
}
