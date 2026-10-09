package gitinfo

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Branch is a branch that can be checked out: a local branch, or a branch
// that exists only on a remote (checking it out creates a tracking branch).
type Branch struct {
	Name     string // "feature", never "origin/feature"
	Remote   string // for remote-only branches: "origin"
	Upstream string // for local branches with one: "origin/feature"
	When     time.Time
}

// RemoteOnly reports whether the branch exists only on a remote.
func (b Branch) RemoteOnly() bool { return b.Remote != "" }

// Branches lists local branches and remote branches with no local
// counterpart, most recently committed first.
func Branches(ctx context.Context, dir string) ([]Branch, error) {
	out, err := Git(ctx, dir, "for-each-ref",
		"--format=%(refname)%09%(upstream:short)%09%(committerdate:unix)",
		"refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	return parseBranches(string(out)), nil
}

func parseBranches(out string) []Branch {
	local := map[string]bool{}
	var branches, remote []Branch
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		ts, _ := strconv.ParseInt(f[2], 10, 64)
		when := time.Unix(ts, 0)
		switch ref := f[0]; {
		case strings.HasPrefix(ref, "refs/heads/"):
			name := strings.TrimPrefix(ref, "refs/heads/")
			local[name] = true
			branches = append(branches, Branch{Name: name, Upstream: f[1], When: when})
		case strings.HasPrefix(ref, "refs/remotes/"):
			rest := strings.TrimPrefix(ref, "refs/remotes/")
			r, name, ok := strings.Cut(rest, "/")
			if !ok || name == "HEAD" {
				continue
			}
			remote = append(remote, Branch{Name: name, Remote: r, When: when})
		}
	}
	// A remote branch is listed only if no local branch has its name, and
	// only once if several remotes have it (origin first).
	sort.SliceStable(remote, func(i, j int) bool { return remote[i].Remote == "origin" && remote[j].Remote != "origin" })
	for _, b := range remote {
		if !local[b.Name] {
			local[b.Name] = true
			branches = append(branches, b)
		}
	}
	sort.SliceStable(branches, func(i, j int) bool { return branches[i].When.After(branches[j].When) })
	return branches
}
