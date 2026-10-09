package updates

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bferg314/dockgit/internal/compose"
	"github.com/bferg314/dockgit/internal/registry"
)

type fakeRegistry struct {
	tags    map[string][]string
	digests map[string]string
}

func (f fakeRegistry) Tags(_ context.Context, ref registry.Ref) ([]string, error) {
	if t, ok := f.tags[ref.Repo]; ok {
		return t, nil
	}
	return nil, errors.New("registry token: 401 (private image?)")
}

func (f fakeRegistry) Digest(_ context.Context, ref registry.Ref) (string, error) {
	return f.digests[ref.Repo+":"+ref.Tag], nil
}

func TestCheck(t *testing.T) {
	reg := fakeRegistry{
		tags: map[string][]string{
			"louislam/dockge":         {"1.4.2", "1.5.0", "1.5.1", "2.0.0", "latest"},
			"bferg314/clock":          {"1.4.0"},
			"linuxserver/qbittorrent": {"5.2.4", "20.04.1"},
		},
		digests: map[string]string{"library/redis:7-alpine": "sha256:new", "itzg/minecraft-server:java17": "sha256:same"},
	}
	local := func(_ context.Context, image string) ([]string, error) {
		switch image {
		case "redis:7-alpine":
			return []string{"redis@sha256:old"}, nil
		case "itzg/minecraft-server:java17":
			return []string{"itzg/minecraft-server@sha256:same"}, nil
		case "ghcr.io/bferg314/clock:1.4.0", "linuxserver/qbittorrent:5.2.4":
			return []string{"x@sha256:1"}, nil
		}
		return nil, nil
	}
	services := []compose.Service{
		{Name: "dockge", Image: "louislam/dockge:1.5.0"},
		{Name: "clock", Image: "ghcr.io/bferg314/clock:1.4.0"},
		{Name: "redis", Image: "redis:7-alpine"},
		{Name: "mc", Image: "itzg/minecraft-server:java17"},
		{Name: "qbit", Image: "linuxserver/qbittorrent:5.2.4"},
		{Name: "new", Image: "ghcr.io/me/new:latest"},
		{Name: "private", Image: "ghcr.io/me/private:1.0.0"},
		{Name: "built", Build: true},
		{Name: "var", Image: "x:${TAG}"},
	}
	got := Check(context.Background(), reg, local, services)
	want := []struct {
		svc     string
		state   State
		suggest string
		major   string
	}{
		{"dockge", NewVersion, "louislam/dockge:1.5.1", "2.0.0"},
		{"clock", UpToDate, "", ""},
		{"redis", NewImage, "", ""},
		{"mc", UpToDate, "", ""},
		{"qbit", UpToDate, "", ""}, // 20.04.1 is not an update
		{"new", NotPulled, "", ""},
		{"private", Unknown, "", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d results: %+v", len(got), got)
	}
	for i, w := range want {
		r := got[i]
		if r.Service != w.svc || r.State != w.state || r.Suggest != w.suggest || r.Major != w.major {
			t.Errorf("%d: got %+v, want %+v", i, r, w)
		}
	}
}

func TestBump(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	os.WriteFile(path, []byte(`services:
  dockge:
    image: louislam/dockge:1.5.0 # the UI
  other:
    image: "louislam/dockge:1.5.0"
  not-this:
    environment:
      NOTE: louislam/dockge:1.5.0
`), 0o644)
	if err := Bump(path, map[string]string{"louislam/dockge:1.5.0": "louislam/dockge:1.5.1"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if strings.Count(s, "louislam/dockge:1.5.1") != 2 || !strings.Contains(s, "# the UI") || !strings.Contains(s, "NOTE: louislam/dockge:1.5.0") {
		t.Fatalf("got:\n%s", s)
	}
	if err := Bump(path, map[string]string{"nope:1": "nope:2"}); err == nil {
		t.Fatal("a missing image should be an error")
	}
	if withTag("ghcr.io/me/app:1.0@sha256:x", "1.1") != "ghcr.io/me/app:1.1" || withTag("localhost:5000/app", "2") != "localhost:5000/app:2" {
		t.Fatal("withTag")
	}
}
