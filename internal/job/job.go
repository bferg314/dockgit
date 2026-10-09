// Package job runs a sequence of commands (git switch, docker compose up,
// ...) as one task, streaming their combined output line by line and
// stopping at the first failure.
package job

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Step is one command.
type Step struct {
	Dir  string
	Cmd  string
	Args []string
	Env  []string // added to the environment
}

// String is the step as a shell would show it: "$ git switch main".
func (s Step) String() string {
	parts := []string{s.Cmd}
	for _, a := range s.Args {
		if strings.ContainsAny(a, " \t\"'") {
			a = fmt.Sprintf("%q", a)
		}
		parts = append(parts, a)
	}
	return "$ " + strings.Join(parts, " ")
}

// Exec starts a step and returns its combined stdout and stderr. When the
// command fails, the reader ends with its error instead of io.EOF.
type Exec func(ctx context.Context, s Step) (io.ReadCloser, error)

// Run runs steps in order, sending each one's "$ command" line and then
// its output. The channel closes when all succeeded, one failed or ctx was
// cancelled; then errc holds the failure, if any.
func Run(ctx context.Context, exec Exec, steps []Step) (<-chan string, <-chan error) {
	ch := make(chan string, 256)
	errc := make(chan error, 1)
	send := func(l string) {
		select {
		case ch <- l:
		case <-ctx.Done():
		}
	}
	go func() {
		defer close(ch)
		for _, s := range steps {
			send(s.String())
			out, err := exec(ctx, s)
			if err != nil {
				errc <- err
				return
			}
			sc := bufio.NewScanner(out)
			sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
			for sc.Scan() {
				send(clean(sc.Text()))
			}
			err = sc.Err()
			out.Close()
			if ctx.Err() != nil {
				errc <- ctx.Err()
				return
			}
			if err != nil {
				errc <- fmt.Errorf("%s: %w", strings.TrimPrefix(s.String(), "$ "), err)
				return
			}
		}
		errc <- nil
	}()
	return ch, errc
}

// clean strips escape codes and keeps the last state of \r-redrawn lines.
func clean(l string) string {
	l = strings.TrimRight(ansi.Strip(l), "\r")
	if i := strings.LastIndex(l, "\r"); i >= 0 {
		l = l[i+1:]
	}
	return strings.ReplaceAll(l, "\t", "    ")
}

// OS runs steps as real processes.
func OS(ctx context.Context, s Step) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, s.Cmd, s.Args...)
	cmd.Dir = s.Dir
	if len(s.Env) > 0 {
		cmd.Env = append(os.Environ(), s.Env...)
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		pw.CloseWithError(cmd.Wait())
		close(done)
	}()
	return &proc{PipeReader: pr, cmd: cmd, done: done}, nil
}

type proc struct {
	*io.PipeReader
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *proc) Close() error {
	p.PipeReader.Close()
	p.cmd.Process.Kill() // already exited is fine
	<-p.done
	return nil
}
