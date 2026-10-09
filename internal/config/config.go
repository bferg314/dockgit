// Package config loads and saves dockgit's TOML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
)

// Tool launch modes.
const (
	// ModeTerminal suspends the TUI and hands the terminal to the tool
	// (lazydocker, lazygit, claude, ...). dockgit resumes when it exits.
	ModeTerminal = "terminal"
	// ModeDetach starts the tool in the background and returns immediately
	// (VS Code, file managers, other GUI apps).
	ModeDetach = "detach"
)

// Build cache cleanup modes.
const (
	CleanupOff        = "off"
	CleanupAfterBuild = "after_build"
	CleanupThreshold  = "threshold"
)

// CleanupModes lists the cleanup modes in the order Settings cycles them.
var CleanupModes = []string{CleanupOff, CleanupAfterBuild, CleanupThreshold}

// Tool is an external program that can be launched on a repo or stack.
type Tool struct {
	Name    string   `toml:"name"`
	Cmd     string   `toml:"cmd"`
	Args    []string `toml:"args"` // "{path}" is replaced with the folder path
	Key     string   `toml:"key"`  // picks the tool in the open-with popup
	Mode    string   `toml:"mode"`
	Enabled bool     `toml:"enabled"`
	// InPlace keeps a terminal tool in dockgit's own terminal even when
	// ZellijTabs would open it in a new tab.
	InPlace bool `toml:"in_place,omitempty"`
	// AutoDisabled marks a tool dockgit switched off because its command
	// wasn't installed. It is switched back on once the command appears.
	// Toggling a tool by hand clears it, so user choices stick.
	AutoDisabled bool `toml:"auto_disabled,omitempty"`
}

// Cleanup controls pruning of the build cache and dangling images.
type Cleanup struct {
	Mode string `toml:"mode"`
	// KeepStorage and OlderThan are passed to docker builder prune as
	// --keep-storage and --filter until=.
	KeepStorage         string `toml:"keep_storage"`
	OlderThan           string `toml:"older_than"`
	PruneDanglingImages bool   `toml:"prune_dangling_images"`
	// Threshold is the build cache size that triggers a prune in
	// threshold mode.
	Threshold string `toml:"threshold"`
}

// Repo is a git repository on the Repos tab.
type Repo struct {
	Path string `toml:"path"`
	// ComposeFiles are relative to Path. Empty means compose's default.
	ComposeFiles []string `toml:"compose_files"`
	// Project overrides the compose project name. Empty means compose's
	// default (the folder name).
	Project string `toml:"project,omitempty"`
	EnvFile string `toml:"env_file,omitempty"`
}

// ComposeOverride adjusts how one compose file in a loose layout is run.
type ComposeOverride struct {
	Project  string   `toml:"project,omitempty"`
	EnvFiles []string `toml:"env_files,omitempty"`
}

// Config is the full on-disk configuration.
type Config struct {
	// Version is the config format; see Load.
	Version int `toml:"version"`
	// ComposeRoots are folders or repos holding compose stacks. Supports ~
	// and environment variables.
	ComposeRoots []string `toml:"compose_roots"`
	// Powerline draws the status bar with arrow separators, which need a
	// Nerd Font or Powerline-patched font.
	Powerline bool `toml:"powerline"`
	// ZellijTabs opens terminal tools in a new zellij tab, instead of
	// suspending dockgit, when dockgit runs inside zellij.
	ZellijTabs  bool `toml:"zellij_tabs"`
	ShowStopped bool `toml:"show_stopped"`
	Stats       bool `toml:"stats"`
	// MaskEnv hides environment variable values until asked.
	MaskEnv            bool `toml:"mask_env"`
	LogTail            int  `toml:"log_tail"`
	ConfirmDestructive bool `toml:"confirm_destructive"`

	Cleanup Cleanup `toml:"cleanup"`
	Repos   []Repo  `toml:"repos"`
	// ComposeOverrides is keyed by compose file path.
	ComposeOverrides map[string]ComposeOverride `toml:"compose_overrides"`
	Tools            []Tool                     `toml:"tools"`
}

// Path returns the location of the config file.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dockgit", "config.toml"), nil
}

// configVersion is the current config format.
//
//	1: first release
const configVersion = 1

// Load reads the config at path. If it does not exist, defaults are created,
// installed tools are enabled, and the result is written to disk.
func Load(path string) (*Config, error) {
	cfg := Default()
	_, err := toml.DecodeFile(path, cfg)
	if errors.Is(err, fs.ErrNotExist) {
		cfg.DetectTools()
		return cfg, cfg.Save(path)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if cfg.LogTail <= 0 {
		cfg.LogTail = Default().LogTail
	}
	if !validCleanupMode(cfg.Cleanup.Mode) {
		cfg.Cleanup.Mode = Default().Cleanup.Mode
	}
	if len(cfg.EnableInstalled()) > 0 {
		if err := cfg.Save(path); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func validCleanupMode(m string) bool {
	for _, c := range CleanupModes {
		if m == c {
			return true
		}
	}
	return false
}

// Save writes the config to path, creating parent directories as needed.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# dockgit configuration\n# Tool args: \"{path}\" is replaced with the repo or stack folder.\n# Tool modes: \"terminal\" (suspends dockgit) or \"detach\" (runs in background).\n\n")
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// DetectTools enables every configured tool whose command is on PATH and
// marks the rest as auto-disabled, so they switch on once installed.
func (c *Config) DetectTools() {
	for i := range c.Tools {
		ok := Available(c.Tools[i].Cmd)
		c.Tools[i].Enabled, c.Tools[i].AutoDisabled = ok, !ok
	}
}

// EnableInstalled switches on auto-disabled tools whose command is now on
// PATH and returns their names. Tools the user turned off are untouched.
func (c *Config) EnableInstalled() []string {
	var names []string
	for i := range c.Tools {
		t := &c.Tools[i]
		if t.AutoDisabled && !t.Enabled && Available(t.Cmd) {
			t.Enabled, t.AutoDisabled = true, false
			names = append(names, t.Name)
		}
	}
	return names
}

// Default returns the built-in configuration.
func Default() *Config {
	return &Config{
		Version:            configVersion,
		ZellijTabs:         true,
		ShowStopped:        true,
		MaskEnv:            true,
		LogTail:            500,
		ConfirmDestructive: true,
		Cleanup: Cleanup{
			Mode:                CleanupAfterBuild,
			KeepStorage:         "10GB",
			OlderThan:           "168h",
			PruneDanglingImages: true,
			Threshold:           "20GB",
		},
		Tools: []Tool{
			{Name: "VS Code", Cmd: "code", Args: []string{"{path}"}, Key: "o", Mode: ModeDetach},
			{Name: "Cursor", Cmd: "cursor", Args: []string{"{path}"}, Key: "u", Mode: ModeDetach},
			{Name: "Claude Code", Cmd: "claude", Key: "c", Mode: ModeTerminal},
			{Name: "lazydocker", Cmd: "lazydocker", Key: "d", Mode: ModeTerminal},
			{Name: "lazygit", Cmd: "lazygit", Key: "l", Mode: ModeTerminal},
			fileManager(),
		},
	}
}

func fileManager() Tool {
	t := Tool{Name: "File manager", Args: []string{"{path}"}, Key: "e", Mode: ModeDetach}
	switch runtime.GOOS {
	case "windows":
		t.Cmd = "explorer"
	case "darwin":
		t.Cmd = "open"
	default:
		t.Cmd = "xdg-open"
	}
	return t
}
