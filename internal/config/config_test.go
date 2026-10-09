package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakePath makes a directory with fake executables for names and puts only
// that directory on PATH.
func fakePath(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	fakeInstall(t, dir, names...)
	t.Setenv("PATH", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("PATHEXT", ".EXE")
	}
	return dir
}

func fakeInstall(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		file := filepath.Join(dir, n)
		if runtime.GOOS == "windows" {
			file += ".exe"
		}
		if err := os.WriteFile(file, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// The first run writes defaults with only installed tools switched on.
func TestLoadCreatesDefaults(t *testing.T) {
	fakePath(t, "code", "lazydocker")
	path := filepath.Join(t.TempDir(), "dockgit", "config.toml")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != configVersion || c.Cleanup.Mode != CleanupAfterBuild || c.LogTail != 500 {
		t.Fatalf("defaults not applied: %+v", c)
	}
	for _, tool := range c.Tools {
		want := tool.Cmd == "code" || tool.Cmd == "lazydocker"
		if tool.Enabled != want || tool.AutoDisabled == want {
			t.Errorf("%s: enabled=%v auto=%v", tool.Name, tool.Enabled, tool.AutoDisabled)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "# dockgit configuration") {
		t.Fatalf("config not written: %v\n%s", err, data)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	fakePath(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	c := Default()
	c.ComposeRoots = []string{"~/code/stacks"}
	c.Repos = []Repo{{Path: "~/code/clock", ComposeFiles: []string{"docker-compose.yml"}}}
	c.ComposeOverrides = map[string]ComposeOverride{"~/code/stacks/services/proxy.yml": {Project: "proxy"}}
	c.Cleanup.Mode = CleanupThreshold
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ComposeRoots) != 1 || got.ComposeRoots[0] != "~/code/stacks" {
		t.Errorf("compose roots: %v", got.ComposeRoots)
	}
	if len(got.Repos) != 1 || got.Repos[0].ComposeFiles[0] != "docker-compose.yml" {
		t.Errorf("repos: %+v", got.Repos)
	}
	if got.ComposeOverrides["~/code/stacks/services/proxy.yml"].Project != "proxy" {
		t.Errorf("overrides: %+v", got.ComposeOverrides)
	}
	if got.Cleanup.Mode != CleanupThreshold || len(got.Tools) != len(c.Tools) {
		t.Errorf("cleanup=%q tools=%d", got.Cleanup.Mode, len(got.Tools))
	}
}

// Hand-edited values that make no sense fall back to the defaults.
func TestLoadRepairsBadValues(t *testing.T) {
	fakePath(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("version = 1\nlog_tail = -3\n[cleanup]\nmode = \"sometimes\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.LogTail != 500 || c.Cleanup.Mode != CleanupAfterBuild {
		t.Fatalf("log_tail=%d cleanup=%q", c.LogTail, c.Cleanup.Mode)
	}
}

func TestEnableInstalled(t *testing.T) {
	dir := fakePath(t, "code")
	c := &Config{Version: configVersion, Tools: []Tool{
		{Name: "VS Code", Cmd: "code"},
		{Name: "lazydocker", Cmd: "lazydocker"},
		{Name: "lazygit", Cmd: "lazygit"},
	}}
	c.DetectTools()
	if !c.Tools[0].Enabled || c.Tools[1].Enabled || !c.Tools[1].AutoDisabled {
		t.Fatalf("detect: %+v", c.Tools)
	}

	// The user turns lazygit off by hand (clears AutoDisabled), then
	// installs lazydocker and lazygit.
	c.Tools[2].AutoDisabled = false
	fakeInstall(t, dir, "lazydocker", "lazygit")

	got := c.EnableInstalled()
	if len(got) != 1 || got[0] != "lazydocker" || !c.Tools[1].Enabled || c.Tools[1].AutoDisabled {
		t.Fatalf("lazydocker should be enabled, got %v %+v", got, c.Tools[1])
	}
	if c.Tools[2].Enabled {
		t.Fatal("a tool the user turned off must stay off")
	}
	if again := c.EnableInstalled(); len(again) != 0 {
		t.Fatalf("second call should be a no-op, got %v", again)
	}
}
