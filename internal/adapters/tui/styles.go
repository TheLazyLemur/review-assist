package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	purple   = lipgloss.Color("#7D56F4")
	green    = lipgloss.Color("#04B575")
	red      = lipgloss.Color("#FF4672")
	yellow   = lipgloss.Color("#F1FA8C")
	orange   = lipgloss.Color("#FFB86C")
	blue     = lipgloss.Color("#8BE9FD")
	pink     = lipgloss.Color("#FF79C6")
	grey     = lipgloss.Color("#626262")
	midGrey  = lipgloss.Color("#999999")
	selectBg = lipgloss.Color("#44475A")
	cursorBg = lipgloss.Color("#343746")
	addBg    = lipgloss.Color("#1E3A2A")
	delBg    = lipgloss.Color("#4A1E28")

	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(purple)
	subtleStyle   = lipgloss.NewStyle().Foreground(midGrey)
	faintStyle    = lipgloss.NewStyle().Foreground(grey)
	errStyle      = lipgloss.NewStyle().Foreground(red)
	okStyle       = lipgloss.NewStyle().Foreground(green)
	addStyle      = lipgloss.NewStyle().Foreground(green)
	delStyle      = lipgloss.NewStyle().Foreground(red)
	hunkStyle     = lipgloss.NewStyle().Foreground(blue).Faint(true)
	authorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#BD93F9")).Bold(true)
	commentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#F8F8F2"))
	keyStyle      = lipgloss.NewStyle().Foreground(pink).Bold(true)
	selectedStyle = lipgloss.NewStyle().Background(purple).Foreground(lipgloss.Color("#FAFAFA")).Bold(true)
	tabStyle      = lipgloss.NewStyle().Padding(0, 1).Foreground(midGrey)
	activeTab     = lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color("#FAFAFA")).Background(purple).Bold(true)
	modalStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(purple).Padding(0, 1)
	sidebarStyle  = lipgloss.NewStyle().BorderStyle(lipgloss.NormalBorder()).BorderRight(true).BorderForeground(grey)
)

func severityColor(sev string) color.Color {
	switch sev {
	case "blocking":
		return red
	case "major":
		return orange
	default:
		return yellow
	}
}

func severityBadge(sev string) string {
	return lipgloss.NewStyle().Foreground(severityColor(sev)).Bold(true).Render("◆ " + sev)
}

func stateBadge(state string, draft bool) string {
	switch {
	case draft:
		return lipgloss.NewStyle().Foreground(midGrey).Render("draft")
	case state == "OPEN":
		return okStyle.Render("open")
	case state == "MERGED":
		return lipgloss.NewStyle().Foreground(purple).Render("merged")
	case state == "CLOSED":
		return errStyle.Render("closed")
	default:
		return strings.ToLower(state)
	}
}

func decisionBadge(d string) string {
	switch d {
	case "APPROVED":
		return okStyle.Render("✓ approved")
	case "CHANGES_REQUESTED":
		return errStyle.Render("✗ changes requested")
	case "REVIEW_REQUIRED":
		return lipgloss.NewStyle().Foreground(yellow).Render("● review required")
	default:
		return ""
	}
}

// fit truncates or pads an ANSI string to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func expandTabs(s string) string { return strings.ReplaceAll(s, "\t", "    ") }

// oneLine flattens text for compact single-line display.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func helpLine(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, keyStyle.Render(pairs[i])+" "+faintStyle.Render(pairs[i+1]))
	}
	return strings.Join(parts, faintStyle.Render(" · "))
}
