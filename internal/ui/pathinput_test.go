package ui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCompletePath(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"clock", "calliope-poker", "calliope-poker-old", ".hidden", "face-to-face"} {
		os.Mkdir(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "cfile"), nil, 0o644)
	sep := string(filepath.Separator)
	base := root + sep

	if got, _ := completePath(base + "cl"); got != base+"clock"+sep {
		t.Errorf("single match: %q", got)
	}
	got, opts := completePath(base + "ca")
	if got != base+"calliope-poker" || !slices.Equal(opts, []string{"calliope-poker", "calliope-poker-old"}) {
		t.Errorf("common prefix: %q %q", got, opts)
	}
	if got, _ := completePath(base + "zz"); got != base+"zz" {
		t.Errorf("no match should leave input alone: %q", got)
	}
	if got, opts := completePath(base + "c"); got != base+"c" || len(opts) != 3 {
		t.Errorf("files are skipped: %q %q", got, opts)
	}
	if got, _ := completePath(base + ".h"); got != base+".hidden"+sep {
		t.Errorf("hidden folders when asked: %q", got)
	}
	// Forward slashes typed on Windows keep working.
	if got, _ := completePath(filepath.ToSlash(root) + "/fa"); got != filepath.ToSlash(root)+"/face-to-face"+sep {
		t.Errorf("forward slashes: %q", got)
	}
	if got, _ := completePath("~"); got != "~"+sep {
		t.Errorf("~: %q", got)
	}
}
