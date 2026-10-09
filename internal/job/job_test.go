package job

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeExec prints canned output per command and fails the ones in fail.
func fakeExec(out map[string]string, fail map[string]bool) Exec {
	return func(_ context.Context, s Step) (io.ReadCloser, error) {
		pr, pw := io.Pipe()
		go func() {
			io.WriteString(pw, out[s.Cmd])
			if fail[s.Cmd] {
				pw.CloseWithError(errors.New("exit status 1"))
				return
			}
			pw.Close()
		}()
		return pr, nil
	}
}

func collect(ch <-chan string) []string {
	var lines []string
	for l := range ch {
		lines = append(lines, l)
	}
	return lines
}

func TestRunStreamsEachStep(t *testing.T) {
	exec := fakeExec(map[string]string{
		"git":    "Switched to branch 'main'\n",
		"docker": "\x1b[1m#1 building\x1b[0m\nprogress 10%\rprogress 100%\n\tdone\n",
	}, nil)
	ch, errc := Run(context.Background(), exec, []Step{
		{Cmd: "git", Args: []string{"switch", "main"}},
		{Cmd: "docker", Args: []string{"compose", "up", "-d", "--build"}},
	})
	got := collect(ch)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	want := []string{
		"$ git switch main", "Switched to branch 'main'",
		"$ docker compose up -d --build", "#1 building", "progress 100%", "    done",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestRunStopsAtFirstFailure(t *testing.T) {
	exec := fakeExec(map[string]string{"git": "error: Your local changes would be overwritten\n"}, map[string]bool{"git": true})
	ch, errc := Run(context.Background(), exec, []Step{
		{Cmd: "git", Args: []string{"switch", "feature"}},
		{Cmd: "docker", Args: []string{"compose", "up"}},
	})
	got := collect(ch)
	err := <-errc
	if err == nil || !strings.Contains(err.Error(), "git switch feature") {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 2 || strings.Contains(strings.Join(got, "|"), "docker") {
		t.Fatalf("the docker step must not run: %q", got)
	}
}

func TestStepString(t *testing.T) {
	s := Step{Cmd: "git", Args: []string{"stash", "push", "-m", "dockgit: before feature"}}
	if got := s.String(); got != `$ git stash push -m "dockgit: before feature"` {
		t.Fatalf("got %s", got)
	}
}

// The real runner reports a failing command's exit through the stream.
func TestOSFailure(t *testing.T) {
	ch, errc := Run(context.Background(), OS, []Step{{Cmd: "git", Args: []string{"definitely-not-a-git-command"}}})
	got := collect(ch)
	if err := <-errc; err == nil {
		t.Fatalf("expected a failure, output %q", got)
	}
}
