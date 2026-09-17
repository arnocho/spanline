// Package ui is spanline's terminal interface: one answer per screen, detail on demand,
// and enough motion to show what the tool is doing without ever getting in the way.
package ui

import (
	"strings"

	"github.com/arnocho/spanline/internal/result"
	"github.com/charmbracelet/lipgloss"
)

// The palette is deliberately small. One accent for the product, one ramp of greys for
// structure, and colour reserved for severity so a colour never means two things.
var (
	colAccent = lipgloss.AdaptiveColor{Light: "#0F766E", Dark: "#5EEAD4"}
	colText   = lipgloss.AdaptiveColor{Light: "#18181B", Dark: "#E4E4E7"}
	colMuted  = lipgloss.AdaptiveColor{Light: "#52525B", Dark: "#A1A1AA"}
	colFaint  = lipgloss.AdaptiveColor{Light: "#A1A1AA", Dark: "#52525B"}
	colLine   = lipgloss.AdaptiveColor{Light: "#D4D4D8", Dark: "#3F3F46"}

	colOutage     = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	colDisruption = lipgloss.AdaptiveColor{Light: "#C2410C", Dark: "#FB923C"}
	colRisk       = lipgloss.AdaptiveColor{Light: "#A16207", Dark: "#FCD34D"}
	colUnknown    = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	colOK         = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
)

// Theme carries the only two things that change how anything is drawn.
type Theme struct {
	Width int
	Color bool
	Pulse bool // set by the app on the selected row for a moment after the selection moves
}

// NewTheme clamps the width to something readable and remembers whether colour is allowed.
func NewTheme(width int, color bool) Theme {
	if width <= 0 {
		width = 96
	}
	if width < 40 {
		width = 40
	}
	return Theme{Width: width, Color: color}
}

// Narrow reports a terminal too small for two columns.
func (t Theme) Narrow() bool { return t.Width < 76 }

// Inner is the writable width inside the two column gutter.
func (t Theme) Inner() int {
	w := t.Width - 4
	if w > 132 {
		w = 132
	}
	if w < 30 {
		w = 30
	}
	return w
}

func (t Theme) paint(c lipgloss.TerminalColor, s string) string {
	if !t.Color {
		return s
	}
	return lipgloss.NewStyle().Foreground(c).Render(s)
}

func (t Theme) bold(c lipgloss.TerminalColor, s string) string {
	if !t.Color {
		return s
	}
	return lipgloss.NewStyle().Foreground(c).Bold(true).Render(s)
}

// sevColor maps a severity to its one colour.
func sevColor(s result.Severity) lipgloss.TerminalColor {
	switch s {
	case result.Outage:
		return colOutage
	case result.Disruption:
		return colDisruption
	case result.Risk:
		return colRisk
	case result.NotAssessed:
		return colUnknown
	default:
		return colOK
	}
}

// sevWord is always printed, so colour is never the only signal.
func sevWord(s result.Severity) string {
	if s == "" {
		return "NOT ASSESSED"
	}
	return string(s)
}

// Chip is a severity marker: a dot plus the word, never the word alone.
func (t Theme) Chip(s result.Severity) string {
	return t.paint(sevColor(s), "● "+sevWord(s))
}

// Accent renders product chrome, never data.
func (t Theme) Accent(s string) string { return t.paint(colAccent, s) }

// Muted renders secondary text.
func (t Theme) Muted(s string) string { return t.paint(colMuted, s) }

// Faint renders text that should recede almost entirely, used by the reveal animation.
func (t Theme) Faint(s string) string { return t.paint(colFaint, s) }

// Headline is the answer. One line, severity coloured, never truncated mid word.
func (t Theme) Headline(s string, sev result.Severity) string {
	return t.bold(sevColor(sev), wrapTo(s, t.Inner()))
}

// Rule draws the only horizontal line the interface uses.
func (t Theme) Rule() string {
	return t.paint(colLine, strings.Repeat("─", t.Inner()))
}

// Card is one finding: an accent bar, a title, and a dim reason underneath.
// Selection thickens the bar and adds a gutter caret, so it survives a monochrome terminal.
func (t Theme) Card(selected bool, sev result.Severity, label, title, note string) string {
	bar := "│"
	caret := "  "
	if selected {
		bar = "┃"
		caret = t.Accent("› ")
		if t.Pulse {
			caret = t.Accent("▸ ")
		}
	}
	lab, labw := "", 0
	if label != "" {
		labw = 9
		lab = t.paint(sevColor(sev), pad(strings.ToUpper(label), 8)) + " "
	}
	titleStyled := cut(title, t.Inner()-4-labw)
	if selected {
		titleStyled = t.bold(colText, titleStyled)
	} else {
		titleStyled = t.paint(colText, titleStyled)
	}
	out := caret + t.paint(sevColor(sev), bar) + " " + lab + titleStyled
	if note != "" {
		indent := "  " + t.paint(sevColor(sev), bar) + " " + strings.Repeat(" ", labw)
		out += "\n" + indent + t.Faint(cut(note, t.Inner()-4-labw))
	}
	return out
}

// KeyLine is one supporting fact under a headline: label on the left, value, then an aside.
func (t Theme) KeyLine(label, value, note string, sev result.Severity) string {
	l := t.Muted(pad(cut(label, 15), 16))
	value = cut(value, t.Inner()-18)
	v := t.paint(colText, value)
	if sev != "" {
		v = t.paint(sevColor(sev), value)
	}
	line := "  " + l + v
	if note == "" {
		return line
	}
	room := t.Inner() - 18 - len([]rune(value)) - 3
	if room > 16 {
		return line + t.Faint("   "+cut(note, room))
	}
	return line + "\n  " + strings.Repeat(" ", 16) + t.Faint(cut(note, t.Inner()-18))
}

// Hint is one key binding shown in the footer.
type Hint struct {
	Key  string
	What string
}

// Footer renders the key map, which is always visible so the interface teaches itself.
func (t Theme) Footer(hints []Hint) string {
	var parts []string
	for _, h := range hints {
		parts = append(parts, t.Accent(h.Key)+" "+t.Muted(h.What))
	}
	sep := t.paint(colLine, "  ")
	return "  " + strings.Join(parts, sep)
}

// Tabs renders the view switcher, with disabled views shown but dimmed.
func (t Theme) Tabs(names []string, active int, enabled []bool) string {
	var parts []string
	for i, n := range names {
		switch {
		case i == active:
			parts = append(parts, t.bold(colAccent, n))
		case !enabled[i]:
			parts = append(parts, t.Faint(n))
		default:
			parts = append(parts, t.Muted(n))
		}
	}
	return "  " + strings.Join(parts, t.paint(colLine, "  ·  "))
}

// Meter draws a small proportional bar, used for pool pressure and cohort balance.
func (t Theme) Meter(frac float64, width int, sev result.Severity) string {
	if width < 4 {
		width = 4
	}
	filled := int(frac*float64(width) + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return t.paint(sevColor(sev), strings.Repeat("█", filled)) + t.paint(colLine, strings.Repeat("░", width-filled))
}

// caret marks the selected row, brighter for a moment right after the selection moved.
func (t Theme) caret(selected bool) string {
	if !selected {
		return "  "
	}
	if t.Pulse {
		return t.Accent("▸ ")
	}
	return t.Accent("› ")
}

func pad(s string, n int) string {
	if lipgloss.Width(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-lipgloss.Width(s))
}

func cut(s string, n int) string {
	if n < 6 {
		n = 6
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func wrapTo(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	words := strings.Fields(s)
	var lines []string
	cur := ""
	for _, w := range words {
		// a single token wider than the line, such as a Terraform address, is hard broken
		for len([]rune(w)) > n {
			if cur != "" {
				lines = append(lines, cur)
				cur = ""
			}
			r := []rune(w)
			lines = append(lines, string(r[:n]))
			w = string(r[n:])
		}
		if cur == "" {
			cur = w
			continue
		}
		if lipgloss.Width(cur)+1+lipgloss.Width(w) > n {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n")
}
