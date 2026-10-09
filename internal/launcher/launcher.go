// Package launcher starts external tools in a repo or stack folder.
package launcher

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bferg314/dockgit/internal/config"
)

// Command builds the exec.Cmd for tool t on path, a folder or a file,
// expanding {path}. It runs in the folder (or the file's folder).
func Command(t config.Tool, path string) *exec.Cmd {
	args := make([]string, len(t.Args))
	for i, a := range t.Args {
		args[i] = strings.ReplaceAll(a, "{path}", path)
	}
	cmd := exec.Command(t.Cmd, args...)
	cmd.Dir = path
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		cmd.Dir = filepath.Dir(path)
	}
	return cmd
}

// InZellij reports whether dockgit is running inside a zellij session.
func InZellij() bool {
	return os.Getenv("ZELLIJ") != ""
}

// ZellijTab builds a command that runs tool t in a new zellij tab in dir.
// It blocks until the tool exits, and the tab closes with it.
func ZellijTab(t config.Tool, dir string) *exec.Cmd {
	tool := Command(t, dir)
	args := []string{"action", "new-tab",
		"--name", filepath.Base(dir) + " · " + t.Name,
		"--cwd", dir,
		"--close-on-exit", "--block-until-exit",
		// The resolved path, so zellij runs what dockgit found on PATH.
		"--", tool.Path}
	cmd := exec.Command("zellij", append(args, tool.Args[1:]...)...)
	cmd.Dir = dir
	return cmd
}

// ZellijError turns a failed zellij action's output into an error.
func ZellijError(out []byte, err error) error {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if line = strings.TrimSpace(line); line != "" {
		return errors.New(line)
	}
	return err
}

// Detach starts cmd without waiting and without tying it to our terminal.
func Detach(cmd *exec.Cmd) error {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detachAttrs(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// OpenURL opens url in the default browser.
func OpenURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// Avoids cmd.exe's "start", which mangles URLs containing "&".
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return Detach(cmd)
}
