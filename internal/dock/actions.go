package dock

import (
	"context"
	"errors"
	"strings"
)

// Start, Stop, Restart and Remove act on one container. Their results show
// up through the event stream; the error is for reporting failures.
func Start(ctx context.Context, r Runner, id string) error {
	_, err := r.Run(ctx, "", "start", id)
	return err
}

func Stop(ctx context.Context, r Runner, id string) error {
	_, err := r.Run(ctx, "", "stop", id)
	return err
}

func Restart(ctx context.Context, r Runner, id string) error {
	_, err := r.Run(ctx, "", "restart", id)
	return err
}

// Remove deletes a container. force also stops a running one.
func Remove(ctx context.Context, r Runner, id string, force bool) error {
	args := []string{"rm", id}
	if force {
		args = []string{"rm", "-f", id}
	}
	_, err := r.Run(ctx, "", args...)
	return err
}

// ErrNoShell means a container has no sh to run a shell with (distroless
// and scratch images).
var ErrNoShell = errors.New("no shell in this container")

// Shell finds the best interactive shell in a running container: bash if
// it has one, otherwise sh.
func Shell(ctx context.Context, r Runner, id string) (string, error) {
	out, err := r.Run(ctx, "", "exec", id, "sh", "-c", "command -v bash || command -v sh")
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") || strings.Contains(err.Error(), "no such file") {
			return "", ErrNoShell
		}
		return "", err
	}
	sh := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0])
	if sh == "" {
		return "", ErrNoShell
	}
	return sh, nil
}

// ExecArgs is the docker command line for an interactive shell.
func ExecArgs(id, shell string) []string {
	return []string{"exec", "-it", id, shell}
}

// LogArgs is the docker command line that follows a container's logs.
func LogArgs(id string, tail int) []string {
	return []string{"logs", "--follow", "--timestamps", "--tail", itoa(tail), id}
}
