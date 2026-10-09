package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	t.Setenv("DOCKGIT_TEST_DIR", filepath.Join(home, "x"))
	cases := map[string]string{
		"":                            "",
		"~":                           home,
		"~/code":                      filepath.Join(home, "code"),
		"$DOCKGIT_TEST_DIR/y":         filepath.Join(home, "x", "y"),
		"${DOCKGIT_TEST_DIR}/y":       filepath.Join(home, "x", "y"),
		"%DOCKGIT_TEST_DIR%/y":        filepath.Join(home, "x", "y"),
		"%DOCKGIT_NOT_SET_XYZ%/y":     filepath.Clean("%DOCKGIT_NOT_SET_XYZ%/y"),
		"  ~/spaced  ":                filepath.Join(home, "spaced"),
		filepath.Join("a", "..", "b"): "b",
	}
	if runtime.GOOS == "windows" {
		cases[`~\code`] = filepath.Join(home, "code")
	}
	for in, want := range cases {
		if got := ExpandPath(in); got != want {
			t.Errorf("ExpandPath(%q) = %q, want %q", in, got, want)
		}
	}
}
