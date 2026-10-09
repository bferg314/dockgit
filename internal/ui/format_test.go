package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestHighlightNameTruncation(t *testing.T) {
	st := newStyles(true)
	cases := []struct {
		name string
		w    int
		want string
	}{
		{"dockgit", 10, "dockgit"},
		{"euterpe-solitaire", 17, "euterpe-solitaire"},
		{"euterpe-solitaire", 12, "euterpe-sol…"},
		{"work/clients/very-long-client-project", 30, "…/very-long-client-project"},
		{"work/clients/very-long-client-project", 20, "very-long-client-pr…"},
		{"bferg314/calliope-poker", 18, "…/calliope-poker"},
		{"a/b", 3, "a/b"},
	}
	for _, c := range cases {
		got := strings.TrimRight(ansi.Strip(st.highlightName(c.name, c.w, nil, false)), " ")
		if got != c.want {
			t.Errorf("highlightName(%q, %d) = %q, want %q", c.name, c.w, got, c.want)
		}
		if ansi.StringWidth(st.highlightName(c.name, c.w, nil, false)) != c.w {
			t.Errorf("highlightName(%q, %d) is not exactly %d cells wide", c.name, c.w, c.w)
		}
	}
}

// Fuzzy-match highlights must stay on the right characters after the
// directories are dropped.
func TestHighlightNameMatchesAfterTruncation(t *testing.T) {
	st := newStyles(true)
	name := "work/clients/api"
	// "api" at byte offsets 13, 14, 15.
	out := st.highlightName(name, 8, []int{13, 14, 15}, false)
	if got := strings.TrimRight(ansi.Strip(out), " "); got != "…/api" {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(out, st.match.Render("api")) {
		t.Fatalf("match highlight lost: %q", out)
	}
}
