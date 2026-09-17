package render

import (
	"strings"

	"github.com/arnocho/spanline/internal/brief"
	"github.com/arnocho/spanline/internal/result"
	"github.com/charmbracelet/lipgloss"
)

// The short screen is what every text renderer prints first: one dim context line, the
// answer, the few facts that support it, and one line naming what is being held back.
// Nothing is deleted, only deferred. Options.Details prints the whole report underneath.
//
// The short screen never wraps a fact into a paragraph. A value too wide for the terminal
// is clipped, deterministically, so the screen stays the same height on every run.

const (
	detailsHint    = "run with --details for evidence."
	detailsBelow   = "shown below."
	nothingHeldMsg = "nothing else recorded"

	minNoteWidth    = 20
	maxClusterLines = 6
)

// plural picks the singular or the plural word for a count, so no line reads "1 risks".
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// clip shortens text to a visible width instead of wrapping it. It cuts on rune boundaries
// and measures display width, so a wide character is never split in half. Plain text only:
// style is applied after clipping, never before, so an escape sequence is never cut open.
func clip(s string, w int) string {
	if w < 8 {
		w = 8
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > w-3 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return strings.TrimRight(b.String(), " ") + "..."
}

// briefHead writes the context line and the headline. The context is dim because it is not
// the answer; the headline carries the severity of what it reports.
func briefHead(b *strings.Builder, p palette, o Options, context string, br brief.Brief) {
	b.WriteString(p.dim(clip(context, o.width())) + "\n")
	b.WriteString("\n")
	b.WriteString(p.headline(br.Sev, clip(br.Headline, o.width())) + "\n")
	if note := strings.TrimSpace(br.Empty); note != "" {
		b.WriteString(p.dim(clip(note, o.width())) + "\n")
	}
}

// briefKeys prints the supporting facts, labels aligned left. A label that carries a severity
// is coloured by it; every other label is dim, so the values are what the eye lands on.
func briefKeys(b *strings.Builder, p palette, o Options, lines []brief.Line) {
	if len(lines) == 0 {
		return
	}
	// Every column is measured after clipping, never before: a value wider than the
	// terminal is cut here, so the screen keeps its height instead of wrapping.
	labelBudget := o.width() / 3
	labels := make([]string, len(lines))
	labelWidth := 0
	for i, l := range lines {
		labels[i] = clip(l.Label, labelBudget)
		if w := lipgloss.Width(labels[i]); w > labelWidth {
			labelWidth = w
		}
	}

	// The value is the fact and the note is an aside, but a screen with no asides reads
	// worse than one with short asides: room for the note is reserved first, and the
	// values are clipped into what is left. Neither may run past the terminal width.
	// A value with no note, such as a diff continuation line, may use the whole width.
	// Only values that share their line with a note set the column width, so one long
	// value can no longer squeeze every note down to nothing.
	full := o.width() - len(indent1) - labelWidth - len(gutter)
	if full < minNoteWidth {
		full = minNoteWidth
	}
	shared := full * 45 / 100
	if shared < minNoteWidth {
		shared = minNoteWidth
	}
	values := make([]string, len(lines))
	valueWidth := 0
	for i, l := range lines {
		budget := full
		if strings.TrimSpace(l.Note) != "" {
			budget = shared
		}
		// An empty value prints as the same placeholder every table uses, never as a
		// label followed by nothing.
		values[i] = clip(orEmpty(l.Value), budget)
		if strings.TrimSpace(l.Note) == "" {
			continue
		}
		if w := lipgloss.Width(values[i]); w > valueWidth {
			valueWidth = w
		}
	}

	// On a terminal too narrow to carry both, the note moves under its value instead of
	// being dropped: the screen grows by one line per note, and nothing goes unsaid.
	room := full - valueWidth - len(gutter)
	beside := room >= minNoteWidth

	b.WriteString("\n")
	for i, l := range lines {
		label := padRight(labels[i], labelWidth)
		if strings.TrimSpace(string(l.Sev)) != "" {
			label = p.apply(severityStyle(l.Sev), label)
		} else {
			label = p.dim(label)
		}
		note := strings.TrimSpace(l.Note)
		carries := note != "" && beside
		value := values[i]
		if carries {
			value = padRight(value, valueWidth)
		}
		line := indent1 + label + gutter + value
		if carries {
			line += gutter + p.dim(clip(note, room))
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
		if note != "" && !beside {
			b.WriteString(indent1 + strings.Repeat(" ", labelWidth) + gutter + p.dim(clip(note, full)) + "\n")
		}
	}
}

// heldBack closes the short screen by naming what it is not showing, and how to see it.
// An empty list is stated as such: a blank line here would read as "that was everything".
// When the list is too long for one line, the pointer to the rest takes a line of its own,
// so it is never broken in the middle of the flag it names.
func heldBack(b *strings.Builder, p palette, o Options, more string) {
	text := strings.TrimSpace(more)
	hint := ""
	switch {
	case text == "":
		text = nothingHeldMsg
	case o.Details:
		hint = detailsBelow
	default:
		hint = detailsHint
	}
	b.WriteString("\n")
	if hint != "" {
		if line := text + ". " + hint; lipgloss.Width(line) <= o.width() {
			b.WriteString(p.dim(line) + "\n")
			return
		}
		text += "."
	}
	for _, line := range wrap(text, o.width()) {
		b.WriteString(p.dim(line) + "\n")
	}
	if hint != "" {
		b.WriteString(p.dim(hint) + "\n")
	}
}

// withGaps adds the coverage gaps to what is held back. The impact brief counts findings,
// not gaps, and an incident screen must never hide the fact that something went unread.
func withGaps(more string, n int) string {
	if n == 0 {
		return more
	}
	gaps := itoa(n) + " coverage " + plural(n, "gap", "gaps")
	if strings.TrimSpace(more) == "" {
		return gaps
	}
	return more + ", " + gaps
}

// mdBrief writes the brief headline as the first sentence under a markdown title. Markdown
// is read in a merge request, not on a screen during an incident, so it still carries
// everything: the headline leads the document, it never replaces a part of it.
func mdBrief(b *strings.Builder, br brief.Brief) {
	if text := strings.TrimSpace(br.Headline); text != "" {
		b.WriteString("\n**" + text + "**\n\n")
	}
}

// clusterLines summarises each cluster in one sentence: what it holds, and what it has
// recorded against it. Words, not a table, because this is read during an incident.
func clusterLines(b *strings.Builder, p palette, o Options, clusters []result.ClusterSummary) {
	b.WriteString("\n")
	if len(clusters) == 0 {
		b.WriteString(indent1 + p.dim("no cluster read") + "\n")
		return
	}

	shown, rest := clusters, 0
	if len(shown) > maxClusterLines {
		rest = len(shown) - maxClusterLines
		shown = shown[:maxClusterLines]
	}
	nameBudget := o.width() / 3
	names := make([]string, len(shown))
	nameWidth := 0
	for i, c := range shown {
		names[i] = clip(c.Context, nameBudget)
		if w := lipgloss.Width(names[i]); w > nameWidth {
			nameWidth = w
		}
	}
	room := o.width() - len(indent1) - nameWidth - len(gutter)

	for i, c := range shown {
		parts := []string{itoa(c.Nodes) + " " + plural(c.Nodes, "node", "nodes")}
		if c.NodesNotReady > 0 {
			parts = append(parts, itoa(c.NodesNotReady)+" not ready")
		}
		parts = append(parts, itoa(c.Pods)+" "+plural(c.Pods, "pod", "pods"))
		switch {
		case c.Outages > 0 || c.Risks > 0:
			if c.Outages > 0 {
				parts = append(parts, itoa(c.Outages)+" "+plural(c.Outages, "outage", "outages"))
			}
			if c.Risks > 0 {
				parts = append(parts, itoa(c.Risks)+" "+plural(c.Risks, "risk", "risks"))
			}
		default:
			parts = append(parts, "no outage or risk recorded")
		}
		b.WriteString(indent1 + p.dim(padRight(names[i], nameWidth)) + gutter + clip(strings.Join(parts, ", "), room) + "\n")
	}
	if rest > 0 {
		b.WriteString(indent1 + p.dim(itoa(rest)+" more "+plural(rest, "cluster", "clusters")+" not shown") + "\n")
	}
}
