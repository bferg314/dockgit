// Package dock talks to Docker through the docker CLI, so whatever context,
// DOCKER_HOST and credentials work in the user's shell work here too.
package dock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
)

// Runner runs docker commands. The UI is tested with a fake.
type Runner interface {
	// Run runs a short command in dir ("" for the current directory) and
	// returns its stdout. A failed command still returns what it printed.
	Run(ctx context.Context, dir string, args ...string) ([]byte, error)
	// Combined is Run with stdout and stderr interleaved, for output
	// meant to be read as one stream, such as container logs.
	Combined(ctx context.Context, dir string, args ...string) ([]byte, error)
	// Stream starts a long-running command (events) and returns its
	// stdout. Cancelling ctx stops it; Close waits for it.
	Stream(ctx context.Context, dir string, args ...string) (io.ReadCloser, error)
	// StreamCombined is Stream with stdout and stderr interleaved, for
	// followed logs and builds. A failed command ends the reader with the
	// error.
	StreamCombined(ctx context.Context, dir string, args ...string) (io.ReadCloser, error)
}

// CLI runs the real docker binary.
type CLI struct{}

func (CLI) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, cliError(stderr.String(), err)
	}
	return out, nil
}

func (CLI) Combined(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, cliError(string(out), err)
	}
	return out, nil
}

func (CLI) Stream(ctx context.Context, dir string, args ...string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &stream{ReadCloser: out, cmd: cmd, stderr: &stderr}, nil
}

func (CLI) StreamCombined(ctx context.Context, dir string, args ...string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		pw.CloseWithError(cmd.Wait()) // nil ends the reader with io.EOF
		close(done)
	}()
	return &combinedStream{PipeReader: pr, cmd: cmd, done: done}, nil
}

// combinedStream stops the command on Close: a followed log that is idle
// never writes, so closing the pipe alone wouldn't end it.
type combinedStream struct {
	*io.PipeReader
	cmd  *exec.Cmd
	done chan struct{}
}

func (s *combinedStream) Close() error {
	s.PipeReader.Close()
	s.cmd.Process.Kill() // already exited is fine
	<-s.done
	return nil
}

type stream struct {
	io.ReadCloser
	cmd    *exec.Cmd
	stderr *bytes.Buffer
}

func (s *stream) Close() error {
	s.ReadCloser.Close()
	if err := s.cmd.Wait(); err != nil {
		return cliError(s.stderr.String(), err)
	}
	return nil
}

// cliError picks the most useful line from docker's stderr: the first one
// starting with "Error" (docker follows it with usage hints), or else the
// first non-empty line.
func cliError(stderr string, err error) error {
	var first string
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if first == "" {
			first = l
		}
		if strings.HasPrefix(l, "Error") || strings.HasPrefix(l, "error") {
			return errors.New(l)
		}
	}
	if first != "" {
		return errors.New(first)
	}
	return err
}
