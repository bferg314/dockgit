// Package cache keeps state that isn't configuration: what dockgit built,
// and when. Losing it is harmless.
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Build records one build dockgit ran for a repo.
type Build struct {
	Branch   string    `json:"branch"`
	Commit   string    `json:"commit"`
	Finished time.Time `json:"finished"`
	OK       bool      `json:"ok"`
}

// Builds is keyed by repo path (as written in the config).
type Builds map[string]Build

// BuildsPath is where builds are kept, in the OS cache folder.
func BuildsPath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dockgit", "builds.json"), nil
}

// LoadBuilds reads path. A missing or unreadable file is an empty record.
func LoadBuilds(path string) Builds {
	b := Builds{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &b)
	}
	return b
}

// Save writes the builds atomically (temp file and rename).
func (b Builds) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "builds-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
