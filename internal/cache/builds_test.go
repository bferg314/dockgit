package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dockgit", "builds.json")
	if got := LoadBuilds(path); len(got) != 0 {
		t.Fatalf("missing file should be empty: %v", got)
	}
	when := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	b := Builds{"~/code/clock": {Branch: "main", Commit: "abc1234", Finished: when, OK: true}}
	if err := b.Save(path); err != nil {
		t.Fatal(err)
	}
	got := LoadBuilds(path)["~/code/clock"]
	if got.Branch != "main" || got.Commit != "abc1234" || !got.Finished.Equal(when) || !got.OK {
		t.Fatalf("got %+v", got)
	}

	// A corrupt file is an empty record, not an error.
	os.WriteFile(path, []byte("{nope"), 0o644)
	if got := LoadBuilds(path); len(got) != 0 {
		t.Fatalf("corrupt file: %v", got)
	}
}
