package gitinfo

import (
	"strings"
	"testing"
)

func TestParseStatus(t *testing.T) {
	out := `# branch.oid 1234567890abcdef
# branch.head main
# branch.upstream origin/main
# branch.ab +2 -3
# stash 1
1 M. N... 100644 100644 100644 aaa bbb staged.go
1 .M N... 100644 100644 100644 aaa bbb unstaged.go
1 MM N... 100644 100644 100644 aaa bbb both.go
2 R. N... 100644 100644 100644 aaa bbb R100 new.go	old.go
u UU N... 100644 100644 100644 100644 aaa bbb ccc conflict.go
? untracked.txt
? other.txt
`
	var s Status
	parseStatus([]byte(out), &s)

	want := Status{Branch: "main", Head: "1234567", Upstream: "origin/main", Ahead: 2, Behind: 3,
		Stashes: 1, Staged: 3, Unstaged: 2, Conflicts: 1, Untracked: 2}
	if s.Branch != want.Branch || s.Head != want.Head || s.Upstream != want.Upstream ||
		s.Ahead != want.Ahead || s.Behind != want.Behind || s.Stashes != want.Stashes ||
		s.Staged != want.Staged || s.Unstaged != want.Unstaged ||
		s.Conflicts != want.Conflicts || s.Untracked != want.Untracked {
		t.Fatalf("got %+v\nwant %+v", s, want)
	}
}

func TestParseStatusDetachedInitial(t *testing.T) {
	var s Status
	parseStatus([]byte("# branch.oid (initial)\n# branch.head (detached)\n"), &s)
	if s.Branch != "" || s.Head != "" || s.Dirty() {
		t.Fatalf("got %+v", s)
	}
}

func TestParseBranches(t *testing.T) {
	out := "refs/heads/main\torigin/main\t300\n" +
		"refs/heads/old\t\t100\n" +
		"refs/remotes/origin/HEAD\t\t300\n" +
		"refs/remotes/origin/main\t\t300\n" +
		"refs/remotes/origin/feature/login\t\t400\n" +
		"refs/remotes/upstream/feature/login\t\t400\n" +
		"refs/remotes/upstream/spike\t\t200\n"
	got := parseBranches(out)
	var names []string
	for _, b := range got {
		names = append(names, b.Name+"@"+b.Remote)
	}
	if want := "feature/login@origin main@ spike@upstream old@"; strings.Join(names, " ") != want {
		t.Fatalf("got %q, want %q", strings.Join(names, " "), want)
	}
	if got[1].Upstream != "origin/main" || got[1].RemoteOnly() || !got[0].RemoteOnly() {
		t.Fatalf("got %+v", got)
	}
}
