package dock

import (
	"bufio"
	"context"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Logs returns the last n lines a container wrote, stdout and stderr
// together, with terminal escape codes removed.
func Logs(ctx context.Context, r Runner, id string, n int) ([]string, error) {
	out, err := r.Combined(ctx, "", "logs", "--tail", itoa(n), id)
	if err != nil {
		return nil, err
	}
	return cleanLines(string(out)), nil
}

// cleanLines splits output into display-safe lines: no escape codes, no
// carriage returns, tabs expanded.
func cleanLines(s string) []string {
	s = strings.TrimRight(ansi.Strip(s), "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		if j := strings.LastIndex(l, "\r"); j >= 0 {
			l = l[j+1:] // progress bars redraw with \r: keep the last state
		}
		lines[i] = strings.ReplaceAll(l, "\t", "    ")
	}
	return lines
}

// Follow streams a container's log lines, starting with the last tail
// lines, each prefixed with its RFC 3339 timestamp (see TrimTimestamp).
// The channel closes when the container stops or ctx is cancelled; then
// errc holds the error, if any.
func Follow(ctx context.Context, r Runner, id string, tail int) (<-chan string, <-chan error) {
	return FollowArgs(ctx, r, "", LogArgs(id, tail)...)
}

// FollowArgs streams the output of any docker command that follows logs,
// such as `compose logs --follow`, run in dir.
func FollowArgs(ctx context.Context, r Runner, dir string, args ...string) (<-chan string, <-chan error) {
	ch := make(chan string, 256)
	errc := make(chan error, 1)
	go func() {
		defer close(ch)
		out, err := r.StreamCombined(ctx, dir, args...)
		if err != nil {
			errc <- err
			return
		}
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			for _, l := range cleanLines(sc.Text()) {
				select {
				case ch <- l:
				case <-ctx.Done():
				}
			}
		}
		err = sc.Err()
		out.Close()
		if ctx.Err() != nil {
			err = nil
		}
		errc <- err
	}()
	return ch, errc
}
