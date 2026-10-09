package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// fit pads or truncates a styled string to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

// ago formats t relative to now: "just now", "5m ago", "3d ago", ...
func ago(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
	}
}

// agoStyle makes recent activity stand out and old activity recede.
func (s styles) agoStyle(t time.Time) lipgloss.Style {
	switch d := time.Since(t); {
	case t.IsZero():
		return s.faintText
	case d < 24*time.Hour:
		return s.ok
	case d < 30*24*time.Hour:
		return s.textS
	default:
		return s.dim
	}
}

// visibleName decides which runes of a path-like name fit in w cells:
// runes[from:to], preceded by "…/" when dirs is true and followed by "…"
// when cut is true. Names read from the start, so:
//
//   - if it fits, everything is shown
//   - otherwise leading directories are dropped ("…/repo") so the repo's
//     own name stays whole
//   - if even that doesn't fit, the base name is cut at the end ("repo-na…")
func visibleName(runes []rune, baseStart, w int) (from, to int, dirs, cut bool) {
	n := len(runes)
	switch {
	case n <= w:
		return 0, n, false, false
	case baseStart > 0 && n-baseStart+2 <= w:
		return baseStart, n, true, false
	default:
		return baseStart, baseStart + max(0, w-1), false, true
	}
}

// highlightName renders a path-like name in w cells: the directory part is
// muted, the base name is normal (or accent when selected), and fuzzy-matched
// characters are highlighted. Long names are shortened by visibleName.
// matches are byte offsets into name.
func (s styles) highlightName(name string, w int, matches []int, selected bool) string {
	runes := []rune(name)
	matched := make(map[int]bool, len(matches))
	for _, b := range matches {
		matched[utf8.RuneCountInString(name[:b])] = true
	}
	baseStart := strings.LastIndex(name, "/") + 1
	baseStart = utf8.RuneCountInString(name[:baseStart])

	offset, end, dirs, cut := visibleName(runes, baseStart, w)
	runes = runes[:end]
	prefix, suffix := "", ""
	if dirs {
		prefix = s.dim.Render("…/")
	}
	if cut {
		suffix = s.dim.Render("…")
	}

	base := s.textS
	if selected {
		base = s.selName
	}
	styleFor := [...]lipgloss.Style{base, s.dim, s.match}
	category := func(i int) int {
		switch {
		case matched[i]:
			return 2
		case i < baseStart:
			return 1
		}
		return 0
	}

	var b strings.Builder
	b.WriteString(prefix)
	start := offset
	for i := offset; i <= len(runes); i++ {
		if i == len(runes) || category(i) != category(start) {
			if i > start {
				b.WriteString(styleFor[category(start)].Render(string(runes[start:i])))
			}
			start = i
		}
	}
	b.WriteString(suffix)
	return fit(b.String(), w)
}

// plural picks one or many to agree with n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
