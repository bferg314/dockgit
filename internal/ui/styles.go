package ui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// styles holds the palette, rebuilt when the terminal reports its background.
type styles struct {
	accent, accentFg, text, muted, faint, green, yellow, red, blue, magenta color.Color
	barMid, barLow                                                          color.Color // status bar backgrounds

	logo, tabOn, tabOff, colHead, dim, faintText, textS, bold lipgloss.Style
	marker, selName, branch, match, rule, box, boxTitle, key  lipgloss.Style
	ok, warn, bad, ahead, behind, pin                         lipgloss.Style
}

func newStyles(dark bool) styles {
	ld := lipgloss.LightDark(dark)
	c := lipgloss.Color
	s := styles{
		accent:   ld(c("#7C3AED"), c("#A78BFA")),
		accentFg: ld(c("#FFFFFF"), c("#1E1B2E")),
		text:     ld(c("#27272A"), c("#E4E4E7")),
		muted:    ld(c("#71717A"), c("#A1A1AA")),
		faint:    ld(c("#D4D4D8"), c("#3F3F46")),
		green:    ld(c("#16A34A"), c("#4ADE80")),
		yellow:   ld(c("#B45309"), c("#FBBF24")),
		red:      ld(c("#DC2626"), c("#F87171")),
		blue:     ld(c("#0369A1"), c("#7DD3FC")),
		magenta:  ld(c("#BE185D"), c("#F9A8D4")),
		barMid:   ld(c("#D4D4D8"), c("#3F3F46")),
		barLow:   ld(c("#F4F4F5"), c("#27272A")),
	}
	fg := func(col color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(col) }

	s.logo = fg(s.accent).Bold(true)
	s.tabOn = lipgloss.NewStyle().Foreground(s.accentFg).Background(s.accent).Bold(true).Padding(0, 1)
	s.tabOff = fg(s.muted).Padding(0, 1)
	s.colHead = fg(s.muted).Bold(true)
	s.dim = fg(s.muted)
	s.faintText = fg(s.faint)
	s.textS = fg(s.text)
	s.bold = fg(s.text).Bold(true)
	s.marker = fg(s.accent).Bold(true)
	s.selName = fg(s.accent).Bold(true)
	s.branch = fg(s.blue)
	s.match = fg(s.yellow).Bold(true).Underline(true)
	s.rule = fg(s.faint)
	s.box = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(s.accent).Padding(1, 2)
	s.boxTitle = fg(s.accent).Bold(true)
	s.key = fg(s.accent).Bold(true)
	s.ok = fg(s.green)
	s.warn = fg(s.yellow)
	s.bad = fg(s.red).Bold(true)
	s.ahead = fg(s.blue)
	s.behind = fg(s.magenta)
	s.pin = fg(s.yellow)
	return s
}
