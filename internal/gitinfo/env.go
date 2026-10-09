package gitinfo

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// NonInteractiveEnv is the environment to add for git commands dockgit runs
// in the background (pull, switch, fetch): they have no terminal, so a
// prompt would hang. HTTPS prompts are turned off outright. SSH is put in
// batch mode (an unlocked agent still works) unless the user has their own
// SSH command configured, which must not be overridden.
func NonInteractiveEnv(ctx context.Context, dir string) []string {
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	if os.Getenv("GIT_SSH_COMMAND") != "" || os.Getenv("GIT_SSH") != "" {
		return env
	}
	check := exec.CommandContext(ctx, "git", "-C", dir, "config", "--get", "core.sshCommand")
	if out, err := check.Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		return env
	}
	return append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
}

// Commit returns the short hash of HEAD.
func Commit(ctx context.Context, dir string) (string, error) {
	out, err := Git(ctx, dir, "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(string(out)), err
}

// TopLevel returns the root of the repository containing dir.
func TopLevel(ctx context.Context, dir string) (string, error) {
	out, err := Git(ctx, dir, "rev-parse", "--show-toplevel")
	return strings.TrimSpace(string(out)), err
}
