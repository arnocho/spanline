// Package render turns the report structs the analyses produce into non-interactive
// output: plain text for a terminal, markdown for a merge request or a ticket, and JSON.
//
// The rule of this package: it never computes, re-ranks or re-orders anything. A report
// arrives fully decided and render prints exactly what it holds, in the order it holds it.
// Grouping estate risks by severity is presentation only, and the report order is preserved
// inside every group.
//
// Output is deterministic: no wall clock, no randomness, and no map iterated without a fixed
// or sorted key order. With Options.Color false, not a single escape sequence is written,
// because no style is ever applied on that path.
package render

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/arnocho/spanline/internal/result"
	"github.com/charmbracelet/lipgloss"
)

// Options controls presentation only. It never changes a verdict, an order or an exit code.
type Options struct {
	Color   bool // ANSI colour on
	Width   int  // terminal width, 0 means 100
	Compact bool // drop evidence lines
	Details bool // print the full report under the short screen, instead of holding it back
}

// DefaultOptions is the safe default: no colour, 100 columns, evidence kept, short screen.
// A caller that knows it writes to a terminal sets Color itself, and a caller that was asked
// for everything sets Details.
func DefaultOptions() Options {
	return Options{Color: false, Width: defaultWidth, Compact: false, Details: false}
}

const (
	defaultWidth = 100
	minWidth     = 40

	gutter  = "  "
	indent1 = "  "
	indent2 = "    "
	indent3 = "      "

	unknownValue = "unknown"
	emptyValue   = "-"
)

// width resolves the usable line width.
func (o Options) width() int {
	if o.Width <= 0 {
		return defaultWidth
	}
	if o.Width < minWidth {
		return minWidth
	}
	return o.Width
}

// Styles. Severity: OUTAGE red, DISRUPTION orange, RISK yellow, NOT ASSESSED grey, INFO dim.
// Verdict: SPLITS bold, TEMPORAL normal, NO-SPLIT and UNKNOWN dim.
var (
	styleOutage      = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	styleDisruption  = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	styleRisk        = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	styleNotAssessed = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleInfo        = lipgloss.NewStyle().Faint(true)
	styleBold        = lipgloss.NewStyle().Bold(true)
	styleDim         = lipgloss.NewStyle().Faint(true)
)

// palette is the only place that decides whether an escape sequence may be written.
type palette struct{ on bool }

func newPalette(o Options) palette { return palette{on: o.Color} }

// apply renders text through a style, or returns it untouched when colour is off.
func (p palette) apply(s lipgloss.Style, text string) string {
	if !p.on || text == "" {
		return text
	}
	return s.Render(text)
}

func (p palette) bold(text string) string { return p.apply(styleBold, text) }
func (p palette) dim(text string) string  { return p.apply(styleDim, text) }

// severityStyle maps a severity to its colour. An unknown severity gets no style at all,
// so an unfamiliar verdict is never dressed up as one of ours.
func severityStyle(s result.Severity) lipgloss.Style {
	switch s {
	case result.Outage:
		return styleOutage
	case result.Disruption:
		return styleDisruption
	case result.Risk:
		return styleRisk
	case result.NotAssessed:
		return styleNotAssessed
	case result.Info:
		return styleInfo
	default:
		return lipgloss.NewStyle()
	}
}

// severity spells the severity out in full. An absent severity prints as NOT ASSESSED,
// never as a blank column, since NOT ASSESSED is never an implicit pass.
func (p palette) severity(s result.Severity) string {
	text := string(s)
	if strings.TrimSpace(text) == "" {
		s = result.NotAssessed
		text = string(result.NotAssessed)
	}
	return p.apply(severityStyle(s), text)
}

// headline styles the one sentence that is the answer: bold, and coloured by its severity.
// INFO stays bold rather than faint, because the answer is never the dimmest line on screen.
func (p palette) headline(s result.Severity, text string) string {
	switch s {
	case result.Outage, result.Disruption, result.Risk, result.NotAssessed:
		return p.apply(severityStyle(s).Bold(true), text)
	default:
		return p.bold(text)
	}
}

// verdict styles a suspect verdict. An absent verdict prints as UNKNOWN.
func (p palette) verdict(v result.Verdict) string {
	text := string(v)
	if strings.TrimSpace(text) == "" {
		v = result.Unknown
		text = string(result.Unknown)
	}
	switch v {
	case result.Splits:
		return p.apply(styleBold, text)
	case result.Temporal:
		return text
	case result.NoSplit, result.Unknown:
		return p.apply(styleDim, text)
	default:
		return text
	}
}

// status colours a doctor check status by reusing the severity palette. Anything that is not
// a recognised failure word stays plain, so an unfamiliar status is never dressed up.
func (p palette) status(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fail", "failed", "error", "denied":
		return p.apply(styleOutage, s)
	case "warn", "warning", "degraded":
		return p.apply(styleRisk, s)
	case "skip", "skipped", "not assessed", "unknown":
		return p.apply(styleNotAssessed, s)
	default:
		return s
	}
}

// header writes the banner: one line naming the run and its scope, a rule, then a dim
// sub line. Fields are already formatted as key=value.
func header(b *strings.Builder, p palette, o Options, title string, fields, sub []string) {
	line := title
	if len(fields) > 0 {
		line += gutter + strings.Join(fields, gutter)
	}
	subLine := strings.Join(sub, gutter)

	b.WriteString(p.bold(line))
	b.WriteString("\n")

	rule := lipgloss.Width(line)
	if w := lipgloss.Width(subLine); w > rule {
		rule = w
	}
	if rule > o.width() {
		rule = o.width()
	}
	b.WriteString(p.dim(strings.Repeat("-", rule)))
	b.WriteString("\n")

	if subLine != "" {
		b.WriteString(p.dim(subLine))
		b.WriteString("\n")
	}
}

// section writes a blank line and a bold section title.
func section(b *strings.Builder, p palette, title string) {
	b.WriteString("\n")
	b.WriteString(p.bold(title))
	b.WriteString("\n")
}

// kv formats one header field.
func kv(key, value string) string { return key + "=" + orEmpty(value) }

func orEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return emptyValue
	}
	return s
}

// ts formats a timestamp in UTC. A zero time is reported as unknown, never as a fake date.
func ts(t time.Time) string {
	if t.IsZero() {
		return unknownValue
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

func itoa(n int) string { return strconv.Itoa(n) }

// ratio formats a purity or a percentage with fixed precision, so runs compare byte for byte.
func ratio(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

func pct(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) + "%" }

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// columnWidths measures every column by its widest visible cell, ignoring escape sequences.
func columnWidths(head []string, rows [][]string) []int {
	n := len(head)
	for _, r := range rows {
		if len(r) > n {
			n = len(r)
		}
	}
	w := make([]int, n)
	measure := func(cells []string) {
		for i, c := range cells {
			if v := lipgloss.Width(c); v > w[i] {
				w[i] = v
			}
		}
	}
	measure(head)
	for _, r := range rows {
		measure(r)
	}
	return w
}

// formatRow pads a row to the measured widths. right marks the columns to right align.
// The last column is never padded, so no line carries trailing spaces.
func formatRow(cells []string, widths []int, right []bool, indent string) string {
	var b strings.Builder
	b.WriteString(indent)
	for i, c := range cells {
		last := i == len(cells)-1
		w := 0
		if i < len(widths) {
			w = widths[i]
		}
		switch {
		case last && (i >= len(right) || !right[i]):
			b.WriteString(c)
		case i < len(right) && right[i]:
			b.WriteString(padLeft(c, w))
		default:
			b.WriteString(padRight(c, w))
		}
		if !last {
			b.WriteString(gutter)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

func padRight(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func padLeft(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// writeTable prints an aligned table with a bold header row.
func writeTable(b *strings.Builder, p palette, head []string, right []bool, rows [][]string, indent string) {
	widths := columnWidths(head, rows)
	b.WriteString(p.bold(formatRow(head, widths, right, indent)))
	b.WriteString("\n")
	for _, r := range rows {
		b.WriteString(formatRow(r, widths, right, indent))
		b.WriteString("\n")
	}
}

// writeFields prints label and value pairs with the labels aligned, no header row.
func writeFields(b *strings.Builder, pairs [][2]string, indent string) {
	rows := make([][]string, 0, len(pairs))
	for _, pair := range pairs {
		rows = append(rows, []string{pair[0], pair[1]})
	}
	widths := columnWidths(nil, rows)
	for _, r := range rows {
		b.WriteString(formatRow(r, widths, nil, indent))
		b.WriteString("\n")
	}
}

// writeList prints one bullet per item, wrapped to the line width.
func writeList(b *strings.Builder, items []string, width int, indent string) {
	for _, it := range items {
		for i, line := range wrap(it, width-len(indent)-2) {
			if i == 0 {
				b.WriteString(indent + "- " + line + "\n")
				continue
			}
			b.WriteString(indent + "  " + line + "\n")
		}
	}
}

// writeInline prints a labelled, comma separated list wrapped to the line width.
func writeInline(b *strings.Builder, label string, items []string, width int, indent string) {
	if len(items) == 0 {
		return
	}
	text := strings.Join(items, ", ")
	lines := wrap(text, width-len(indent)-len(label)-2)
	for i, line := range lines {
		if i == 0 {
			b.WriteString(indent + label + ": " + line + "\n")
			continue
		}
		b.WriteString(indent + strings.Repeat(" ", len(label)+2) + line + "\n")
	}
}

// writeEvidence prints the fields a finding was derived from. Compact drops it.
func writeEvidence(b *strings.Builder, p palette, o Options, evidence []string, indent string) {
	if o.Compact || len(evidence) == 0 {
		return
	}
	b.WriteString(indent + p.dim("evidence") + "\n")
	writeList(b, evidence, o.width(), indent+"  ")
}

// writeGaps always prints the coverage section. An empty list is stated as such: an absent
// gap list must never read as full coverage.
func writeGaps(b *strings.Builder, p palette, o Options, gaps []string) {
	section(b, p, "coverage and gaps")
	if len(gaps) == 0 {
		b.WriteString(indent1 + "gaps: none recorded\n")
		return
	}
	writeList(b, gaps, o.width(), indent1)
}

// writeNarrative prints optional model prose, always labelled, never mixed into the findings.
func writeNarrative(b *strings.Builder, p palette, o Options, n *result.Narrative) {
	if n == nil || strings.TrimSpace(n.Text) == "" {
		return
	}
	section(b, p, "narrative")
	meta := []string{kv("model", n.Model)}
	if n.Unverified {
		meta = append(meta, "unverified: yes")
	}
	if n.Dropped > 0 {
		meta = append(meta, kv("dropped sentences", itoa(n.Dropped)))
	}
	b.WriteString(indent1 + p.dim(strings.Join(meta, gutter)) + "\n")
	for _, line := range wrap(n.Text, o.width()-len(indent1)) {
		b.WriteString(indent1 + line + "\n")
	}
	if len(n.Citations) > 0 {
		b.WriteString(indent1 + p.dim("citations") + "\n")
		writeList(b, n.Citations, o.width(), indent2)
	}
}

// wrap breaks text on spaces at the given width. It never splits a word and never
// reflows an already short line, so output stays byte stable.
func wrap(text string, width int) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if width < 20 {
		width = 20
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		fields := strings.Fields(para)
		if len(fields) == 0 {
			out = append(out, "")
			continue
		}
		line := fields[0]
		for _, f := range fields[1:] {
			if len(line)+1+len(f) > width {
				out = append(out, line)
				line = f
				continue
			}
			line += " " + f
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// JSON marshals any report with stable indentation. Go's encoder sorts map keys, and the
// report structs carry fixed field order, so two runs on the same report are byte identical.
func JSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// Markdown helpers. No ANSI, no emoji, and every table carries a header row.

func mdHeading(b *strings.Builder, level int, text string) {
	b.WriteString("\n" + strings.Repeat("#", level) + " " + text + "\n\n")
}

// mdTable writes a pipe table with its header and separator rows.
func mdTable(b *strings.Builder, head []string, rows [][]string) {
	b.WriteString("| " + strings.Join(head, " | ") + " |\n")
	seps := make([]string, len(head))
	for i := range seps {
		seps[i] = "---"
	}
	b.WriteString("| " + strings.Join(seps, " | ") + " |\n")
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, c := range r {
			cells[i] = mdCell(c)
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
}

// mdCell keeps a value inside one table cell: pipes escaped, newlines flattened.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.TrimSpace(s)
	if s == "" {
		return emptyValue
	}
	return s
}

func mdList(b *strings.Builder, items []string) {
	for _, it := range items {
		b.WriteString("- " + strings.TrimSpace(it) + "\n")
	}
}

// mdGaps is the markdown twin of writeGaps, with the same refusal to imply full coverage.
func mdGaps(b *strings.Builder, gaps []string) {
	mdHeading(b, 2, "Coverage and gaps")
	if len(gaps) == 0 {
		b.WriteString("gaps: none recorded\n")
		return
	}
	mdList(b, gaps)
}

func mdNarrative(b *strings.Builder, n *result.Narrative) {
	if n == nil || strings.TrimSpace(n.Text) == "" {
		return
	}
	mdHeading(b, 2, "Narrative")
	meta := "Model: " + orEmpty(n.Model)
	if n.Unverified {
		meta += ". Unverified."
	}
	if n.Dropped > 0 {
		meta += " Dropped sentences: " + itoa(n.Dropped) + "."
	}
	b.WriteString(meta + "\n\n")
	b.WriteString(strings.TrimSpace(n.Text) + "\n")
	if len(n.Citations) > 0 {
		b.WriteString("\nCitations:\n\n")
		mdList(b, n.Citations)
	}
}

// out finishes a builder: exactly one trailing newline.
func out(b *strings.Builder) string {
	return strings.TrimRight(b.String(), "\n") + "\n"
}
