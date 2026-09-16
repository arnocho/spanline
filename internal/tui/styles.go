// Package tui renders already computed spanline reports in an interactive terminal interface.
//
// The views display reports produced by the analyses. They never compute, re-rank or mutate
// anything: ordering comes from the report, verdicts come from the report, and an absent value
// is shown as absent rather than filled in.
package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/lipgloss"

	"github.com/arnocho/spanline/internal/result"
)

const (
	// wideThreshold is the column count below which every view drops to a single column.
	wideThreshold = 80
	defaultWidth  = 100
	defaultHeight = 32
	labelWidth    = 18
	severityWidth = 12
)

// severityOrder is the fixed display order used when findings are grouped. Highest first.
var severityOrder = []result.Severity{
	result.Outage,
	result.Disruption,
	result.Risk,
	result.NotAssessed,
	result.Info,
}

// Palette. One place, reused by every view.
var (
	colAccent      = lipgloss.AdaptiveColor{Light: "#1B4FD8", Dark: "#7AA2F7"}
	colText        = lipgloss.AdaptiveColor{Light: "#16181D", Dark: "#D6DBE5"}
	colMuted       = lipgloss.AdaptiveColor{Light: "#5F6673", Dark: "#8A93A3"}
	colFaint       = lipgloss.AdaptiveColor{Light: "#8A919E", Dark: "#5C6472"}
	colBorder      = lipgloss.AdaptiveColor{Light: "#CFD4DC", Dark: "#3A4048"}
	colOutage      = lipgloss.AdaptiveColor{Light: "#C0262B", Dark: "#F07178"}
	colDisruption  = lipgloss.AdaptiveColor{Light: "#A85A00", Dark: "#F0A45D"}
	colRisk        = lipgloss.AdaptiveColor{Light: "#8A7100", Dark: "#E3D06A"}
	colNotAssessed = lipgloss.AdaptiveColor{Light: "#6A7180", Dark: "#9AA3B2"}
)

// Text styles. Every view styles through these, never with ad hoc colours.
var (
	stTitle    = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stStrong   = lipgloss.NewStyle().Bold(true).Foreground(colText)
	stValue    = lipgloss.NewStyle().Foreground(colText)
	stLabel    = lipgloss.NewStyle().Foreground(colMuted)
	stMuted    = lipgloss.NewStyle().Foreground(colMuted)
	stFaint    = lipgloss.NewStyle().Foreground(colFaint)
	stAccent   = lipgloss.NewStyle().Foreground(colAccent)
	stSelected = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stRule     = lipgloss.NewStyle().Foreground(colBorder)
)

// severityWord always spells the severity out. Colour is never the only signal.
// An unset severity is shown as NOT ASSESSED, which is the conservative reading of an
// absent verdict: it is never an implicit pass.
func severityWord(s result.Severity) string {
	if strings.TrimSpace(string(s)) == "" {
		return string(result.NotAssessed)
	}
	return string(s)
}

func severityStyle(s result.Severity) lipgloss.Style {
	switch s {
	case result.Outage:
		return lipgloss.NewStyle().Bold(true).Foreground(colOutage)
	case result.Disruption:
		return lipgloss.NewStyle().Bold(true).Foreground(colDisruption)
	case result.Risk:
		return lipgloss.NewStyle().Foreground(colRisk)
	case result.Info:
		return stFaint
	default:
		return lipgloss.NewStyle().Foreground(colNotAssessed)
	}
}

// severityTag is the padded, coloured, spelled out severity prefix.
func severityTag(s result.Severity) string {
	return severityStyle(s).Render(padRight(severityWord(s), severityWidth))
}

// chrome is the layout shared by every view: a header bar, a body, a footer with key hints.
type chrome struct {
	command string
	context string
	source  string
	stamp   string
	width   int
	height  int
	help    help.Model
}

func newChrome(command, context, source string, generatedAt time.Time) chrome {
	h := help.New()
	h.ShortSeparator = "   "
	h.FullSeparator = "   "
	h.Styles.ShortKey = stAccent
	h.Styles.ShortDesc = stMuted
	h.Styles.ShortSeparator = stFaint
	h.Styles.FullKey = stAccent
	h.Styles.FullDesc = stMuted
	h.Styles.FullSeparator = stFaint
	h.Styles.Ellipsis = stFaint
	h.Width = defaultWidth

	return chrome{
		command: command,
		context: context,
		source:  source,
		stamp:   shortTime(generatedAt),
		width:   defaultWidth,
		height:  defaultHeight,
		help:    h,
	}
}

func (c *chrome) resize(width, height int) {
	if width > 0 {
		c.width = width
	}
	if height > 0 {
		c.height = height
	}
	c.help.Width = c.width
}

// wide reports whether there is room for a multi column layout.
func (c chrome) wide() bool { return c.width >= wideThreshold }

func (c chrome) safeWidth() int {
	if c.width < 24 {
		return 24
	}
	return c.width
}

// headerView is the status header: the command, the context, the source, the generation time.
func (c chrome) headerView() string {
	w := c.safeWidth()
	inner := w - 4

	left := stTitle.Render("spanline") + " " + stStrong.Render(c.command)
	right := stFaint.Render("generated " + c.stamp)
	top := left
	if gap := inner - lipgloss.Width(left) - lipgloss.Width(right); gap >= 2 {
		top = left + strings.Repeat(" ", gap) + right
	}

	contextValue := orNone(c.context)
	sourceValue := orNone(c.source)
	budget := inner - 21
	if budget < 12 {
		budget = 12
	}
	if lipgloss.Width(contextValue)+lipgloss.Width(sourceValue) > budget {
		half := budget / 2
		contextValue = truncate(contextValue, half)
		sourceValue = truncate(sourceValue, budget-half)
	}
	bottom := stLabel.Render("context ") + stValue.Render(contextValue) +
		stFaint.Render("   ·   ") + stLabel.Render("source ") + stValue.Render(sourceValue)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBorder).
		Padding(0, 1).
		Width(w - 2)

	return box.Render(clipTo(top, inner) + "\n" + clipTo(bottom, inner))
}

// footerView is the key hint bar. pinned lines sit above the hints and stay visible.
func (c chrome) footerView(km help.KeyMap, pinned []string) string {
	w := c.safeWidth()
	lines := []string{stRule.Render(strings.Repeat("─", w))}
	for _, p := range pinned {
		if strings.TrimSpace(p) == "" {
			continue
		}
		lines = append(lines, clipTo(p, w))
	}
	lines = append(lines, clipTo(c.help.View(km), w))
	return strings.Join(lines, "\n")
}

// bodyHeight is the room left for the body once the header and the footer are placed.
func (c chrome) bodyHeight(head, foot string) int {
	h := c.height - lipgloss.Height(head) - lipgloss.Height(foot)
	if h < 4 {
		h = 4
	}
	return h
}

// assemble stacks the three bands and pins the footer to the bottom of the frame.
func (c chrome) assemble(head, body, foot string) string {
	avail := c.bodyHeight(head, foot)
	return strings.Join([]string{head, fitHeight(body, avail), foot}, "\n")
}

// renderPane draws one bordered pane. lines must already be built for the pane's inner width,
// which is paneInnerWidth(w); at most paneRowCapacity(h) of them are shown.
func renderPane(title, hint string, lines []string, w, h int, focused bool) string {
	if w < 12 {
		w = 12
	}
	if h < 4 {
		h = 4
	}
	inner := paneInnerWidth(w)
	rows := paneRowCapacity(h)

	border := colBorder
	name := stStrong.Render(title)
	if focused {
		border = colAccent
		name = stSelected.Render("› " + title)
	}
	head := name
	if hint != "" {
		if gap := inner - lipgloss.Width(name) - lipgloss.Width(hint); gap >= 1 {
			head = name + strings.Repeat(" ", gap) + stFaint.Render(hint)
		}
	}

	content := make([]string, 0, rows+1)
	content = append(content, clipTo(head, inner))
	for _, l := range lines {
		if len(content) > rows {
			break
		}
		content = append(content, clipTo(l, inner))
	}
	for len(content) < rows+1 {
		content = append(content, "")
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1).
		Width(w - 2).
		Height(h - 2)

	return box.Render(strings.Join(content, "\n"))
}

// paneInnerWidth is the text width inside a pane of total width w.
func paneInnerWidth(w int) int {
	if w-4 < 4 {
		return 4
	}
	return w - 4
}

// paneRowCapacity is the number of content rows inside a pane of total height h.
func paneRowCapacity(h int) int {
	if h-3 < 1 {
		return 1
	}
	return h - 3
}

// kvLine renders one aligned label and value pair.
func kvLine(label, value string, w int) string {
	lw := labelWidth
	if w < 44 {
		lw = 12
	}
	rest := w - lw
	if rest < 1 {
		rest = 1
	}
	return stLabel.Render(padRight(label, lw)) + stValue.Render(truncate(value, rest))
}

// splitLine puts a plain left part and a plain right part on the same row, right aligned.
func splitLine(left, right string, w int, leftStyle, rightStyle lipgloss.Style) string {
	if right == "" {
		return leftStyle.Render(truncate(left, w))
	}
	gap := w - len([]rune(left)) - len([]rune(right))
	if gap < 1 {
		return leftStyle.Render(truncate(left, w))
	}
	return leftStyle.Render(left) + strings.Repeat(" ", gap) + rightStyle.Render(right)
}

// clipTo truncates an already styled line to w cells without breaking its styling.
func clipTo(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

// truncate shortens plain text to w cells. Never call it on styled text.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

func padRight(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// window returns at most capacity lines, keeping the focus line visible.
func window(lines []string, focus, capacity int) []string {
	if capacity <= 0 || len(lines) == 0 {
		return nil
	}
	if len(lines) <= capacity {
		return lines
	}
	start := 0
	if focus >= capacity {
		start = focus - capacity + 1
	}
	if start > len(lines)-capacity {
		start = len(lines) - capacity
	}
	if start < 0 {
		start = 0
	}
	return lines[start : start+capacity]
}

// scrollFrom returns at most capacity lines starting at top, clamped to the slice.
func scrollFrom(lines []string, top, capacity int) []string {
	if capacity <= 0 || len(lines) == 0 {
		return nil
	}
	if top > len(lines)-capacity {
		top = len(lines) - capacity
	}
	if top < 0 {
		top = 0
	}
	end := top + capacity
	if end > len(lines) {
		end = len(lines)
	}
	return lines[top:end]
}

func fitHeight(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "not recorded"
	}
	return s
}

func shortTime(t time.Time) string {
	if t.IsZero() {
		return "not recorded"
	}
	return t.UTC().Format("2006-01-02 15:04") + " UTC"
}

// plural picks the singular or plural noun for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
