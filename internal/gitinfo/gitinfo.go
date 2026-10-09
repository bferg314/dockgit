// Package gitinfo reads the state of a local repository by shelling out to
// git, so the user's own git config, credential helpers and SSH setup apply.
package gitinfo

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Status is a snapshot of one repository.
type Status struct {
	Branch     string // empty when detached
	Head       string // short commit hash
	Upstream   string // empty when no upstream is configured
	Ahead      int
	Behind     int
	Staged     int
	Unstaged   int
	Untracked  int
	Conflicts  int
	Stashes    int
	LastCommit time.Time // zero for repos without commits
	Remotes    []string  // remote URLs
	Err        error     `json:"-"`
}

// Changes is the total number of changed paths.
func (s Status) Changes() int { return s.Staged + s.Unstaged + s.Untracked + s.Conflicts }

// Dirty reports whether the working tree has any changes.
func (s Status) Dirty() bool { return s.Changes() > 0 }

// Get collects the status of the repository at dir.
func Get(ctx context.Context, dir string) Status {
	var s Status

	out, err := Git(ctx, dir, "status", "--porcelain=v2", "--branch", "--show-stash")
	if err != nil {
		s.Err = err
		return s
	}
	parseStatus(out, &s)

	if out, err := Git(ctx, dir, "log", "-1", "--format=%ct"); err == nil {
		if ts, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
			s.LastCommit = time.Unix(ts, 0)
		}
	}

	// Exits 1 when there are no remotes; that's fine.
	if out, err := Git(ctx, dir, "config", "--get-regexp", `^remote\..*\.url$`); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if _, url, ok := strings.Cut(line, " "); ok {
				s.Remotes = append(s.Remotes, url)
			}
		}
	}
	return s
}

// Git runs a read-only git command in dir and returns stdout.
func Git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	// --no-optional-locks keeps us from fighting editors over index.lock.
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, &Error{Msg: msg, Err: err}
		}
		return out, err
	}
	return out, nil
}

// Error carries git's stderr alongside the exit error.
type Error struct {
	Msg string
	Err error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

// parseStatus parses `git status --porcelain=v2 --branch --show-stash`.
func parseStatus(out []byte, s *Status) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			if oid := strings.TrimPrefix(line, "# branch.oid "); oid != "(initial)" && len(oid) >= 7 {
				s.Head = oid[:7]
			}
		case strings.HasPrefix(line, "# branch.head "):
			if head := strings.TrimPrefix(line, "# branch.head "); head != "(detached)" {
				s.Branch = head
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			s.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
		case strings.HasPrefix(line, "# branch.ab "):
			for _, f := range strings.Fields(strings.TrimPrefix(line, "# branch.ab ")) {
				n, _ := strconv.Atoi(f[1:])
				if f[0] == '+' {
					s.Ahead = n
				} else {
					s.Behind = n
				}
			}
		case strings.HasPrefix(line, "# stash "):
			s.Stashes, _ = strconv.Atoi(strings.TrimPrefix(line, "# stash "))
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "):
			if len(line) >= 4 {
				if line[2] != '.' {
					s.Staged++
				}
				if line[3] != '.' {
					s.Unstaged++
				}
			}
		case strings.HasPrefix(line, "u "):
			s.Conflicts++
		case strings.HasPrefix(line, "? "):
			s.Untracked++
		}
	}
}
