package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/arnocho/spanline/internal/result"
)

const verdictWidth = 9

type whyKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Expand key.Binding
	Help   key.Binding
	Quit   key.Binding
}

func defaultWhyKeys() whyKeyMap {
	return whyKeyMap{
		Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("up/k", "previous suspect")),
		Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("down/j", "next suspect")),
		Expand: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "expand evidence")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k whyKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Expand, k.Help, k.Quit}
}

func (k whyKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down}, {k.Expand}, {k.Help, k.Quit}}
}

// whyModel shows the incident split: onset, the two cohorts, the dimensions, the ranked suspects.
type whyModel struct {
	ch       chrome
	report   *result.WhyReport
	keys     whyKeyMap
	cursor   int
	expanded map[int]bool
}

func newWhyModel(r *result.WhyReport) whyModel {
	if r == nil {
		r = &result.WhyReport{}
	}
	return whyModel{
		ch:       newChrome("why", orNone(r.Context), whySource(r), r.GeneratedAt),
		report:   r,
		keys:     defaultWhyKeys(),
		expanded: map[int]bool{},
	}
}

func whySource(r *result.WhyReport) string {
	target := strings.TrimSpace(r.Namespace + "/" + r.Workload)
	if target == "/" {
		target = "not recorded"
	}
	mode := strings.TrimSpace(string(r.Mode))
	if mode == "" {
		return target
	}
	return target + " · mode " + mode
}

func (m whyModel) Init() tea.Cmd { return nil }

func (m whyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.ch.resize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Help):
			m.ch.help.ShowAll = !m.ch.help.ShowAll
		case key.Matches(msg, m.keys.Up):
			m.cursor = clampIndex(m.cursor-1, len(m.report.Suspects))
		case key.Matches(msg, m.keys.Down):
			m.cursor = clampIndex(m.cursor+1, len(m.report.Suspects))
		case key.Matches(msg, m.keys.Expand):
			if len(m.report.Suspects) > 0 {
				i := clampIndex(m.cursor, len(m.report.Suspects))
				m.expanded[i] = !m.expanded[i]
			}
		}
	}
	return m, nil
}

func (m whyModel) View() string {
	head := m.ch.headerView()
	foot := m.ch.footerView(m.keys, []string{m.statusLine()})
	return m.ch.assemble(head, m.bodyView(m.ch.bodyHeight(head, foot)), foot)
}

func (m whyModel) statusLine() string {
	r := m.report
	parts := []string{
		fmt.Sprintf("failing %d %s", r.Failing.Count, plural(r.Failing.Count, "pod", "pods")),
		fmt.Sprintf("healthy %d %s", r.Healthy.Count, plural(r.Healthy.Count, "pod", "pods")),
		fmt.Sprintf("%d ranked %s", len(r.Suspects), plural(len(r.Suspects), "suspect", "suspects")),
	}
	if len(r.Gaps) > 0 {
		parts = append(parts, fmt.Sprintf("%d coverage %s", len(r.Gaps), plural(len(r.Gaps), "gap", "gaps")))
	}
	return stMuted.Render(strings.Join(parts, "   ·   "))
}

func (m whyModel) bodyView(avail int) string {
	r := m.report
	width := m.ch.safeWidth()

	onset := m.onsetLine(width)
	remaining := avail - 2
	if remaining < 6 {
		remaining = 6
	}

	var blocks []string

	cohortH := 7
	if !m.ch.wide() {
		// Narrow terminal: the cohorts collapse to two plain lines above the tables.
		cohortH = 0
	}
	if cohortH > 0 && remaining < cohortH+10 {
		cohortH = 5
	}

	dimRows := len(r.Dimensions)
	if dimRows == 0 {
		dimRows = 1
	}
	dimH := dimRows + 5
	if dimH > 10 {
		dimH = 10
	}

	suspectH := remaining - cohortH - dimH
	for suspectH < 6 && dimH > 5 {
		dimH--
		suspectH++
	}
	for suspectH < 6 && cohortH > 5 {
		cohortH--
		suspectH++
	}
	if suspectH < 4 {
		suspectH = 4
	}

	if cohortH > 0 {
		leftW := width / 2
		rightW := width - leftW
		blocks = append(blocks, lipgloss.JoinHorizontal(lipgloss.Top,
			renderPane("failing cohort", fmt.Sprintf("%d %s", r.Failing.Count, plural(r.Failing.Count, "pod", "pods")),
				cohortLines(r.Failing, paneInnerWidth(leftW), paneRowCapacity(cohortH)), leftW, cohortH, false),
			renderPane("healthy cohort", fmt.Sprintf("%d %s", r.Healthy.Count, plural(r.Healthy.Count, "pod", "pods")),
				cohortLines(r.Healthy, paneInnerWidth(rightW), paneRowCapacity(cohortH)), rightW, cohortH, false),
		))
	} else {
		blocks = append(blocks,
			clipTo(stLabel.Render("failing cohort ")+stValue.Render(cohortSummary(r.Failing)), width),
			clipTo(stLabel.Render("healthy cohort ")+stValue.Render(cohortSummary(r.Healthy)), width))
		suspectH = remaining - dimH - 2
		if suspectH < 4 {
			suspectH = 4
		}
	}

	dimTitle, dimHint, dimLines := m.dimensionPane(paneInnerWidth(width), paneRowCapacity(dimH))
	blocks = append(blocks, renderPane(dimTitle, dimHint, dimLines, width, dimH, false))
	blocks = append(blocks, renderPane("ranked suspects", m.suspectHint(),
		m.suspectLines(paneInnerWidth(width), paneRowCapacity(suspectH)), width, suspectH, true))

	return lipgloss.JoinVertical(lipgloss.Left, append([]string{onset, ""}, blocks...)...)
}

func (m whyModel) onsetLine(w int) string {
	r := m.report
	line := stLabel.Render("onset ") + stValue.Render(shortTime(r.OnsetAt)) +
		stFaint.Render("   ·   ") + stLabel.Render("signal ") + stValue.Render(orNone(r.OnsetSignal))
	if strings.TrimSpace(r.CohortKey) != "" {
		line += stFaint.Render("   ·   ") + stLabel.Render("cohort key ") + stValue.Render(r.CohortKey)
	}
	return clipTo(line, w)
}

func cohortSummary(c result.Cohort) string {
	if c.Count == 0 && len(c.Pods) == 0 {
		return "no pod recorded in this cohort"
	}
	out := fmt.Sprintf("%d %s", c.Count, plural(c.Count, "pod", "pods"))
	if strings.TrimSpace(c.Sample) != "" {
		out += ", sample " + c.Sample
	}
	return out
}

func cohortLines(c result.Cohort, w, capacity int) []string {
	lines := []string{kvLine("pods", fmt.Sprintf("%d", c.Count), w)}
	if strings.TrimSpace(c.Sample) != "" {
		lines = append(lines, kvLine("sample", c.Sample, w))
	}
	if len(c.Pods) == 0 {
		lines = append(lines, stMuted.Render("no pod name recorded in this cohort"))
		return window(lines, 0, capacity)
	}
	room := capacity - len(lines)
	for i, p := range c.Pods {
		if room <= 1 && len(c.Pods)-i > 1 {
			left := len(c.Pods) - i
			lines = append(lines, stFaint.Render(fmt.Sprintf("%d more %s not shown", left, plural(left, "pod", "pods"))))
			break
		}
		lines = append(lines, "  "+stValue.Render(truncate(p, w-2)))
		room--
	}
	return window(lines, 0, capacity)
}

// dimensionPane renders the dimension comparison. When the report ran in revision fallback and
// carries no dimension, the controller revisions it compared are shown instead.
func (m whyModel) dimensionPane(w, capacity int) (string, string, []string) {
	r := m.report
	if len(r.Dimensions) == 0 && len(r.Revisions) > 0 {
		return "controller revisions", fmt.Sprintf("%d compared", len(r.Revisions)), revisionLines(r.Revisions, w, capacity)
	}

	hint := ""
	for _, d := range r.Dimensions {
		if d.Separation == result.Total {
			hint = "▶ separates the two cohorts"
			break
		}
	}
	return "dimensions", hint, dimensionLines(r.Dimensions, w, capacity)
}

func dimensionLines(dims []result.Dimension, w, capacity int) []string {
	if len(dims) == 0 {
		return []string{stMuted.Render("no dimension compared in this snapshot")}
	}

	nameW, sepW, purW := 18, 12, 6
	valW := (w - 2 - nameW - sepW - purW - 4) / 2
	if valW < 10 {
		// Narrow: one block of three lines per dimension instead of a table.
		var lines []string
		for _, d := range dims {
			marker := "  "
			style := stValue
			if d.Separation == result.Total {
				marker = "▶ "
				style = stSelected
			}
			lines = append(lines, style.Render(truncate(marker+orNone(d.Name)+"  "+string(d.Separation), w)))
			lines = append(lines, "    "+stFaint.Render(truncate("failing "+orNone(d.FailingValues), w-4)))
			lines = append(lines, "    "+stFaint.Render(truncate("healthy "+orNone(d.HealthyValues), w-4)))
		}
		return window(lines, 0, capacity)
	}

	header := "  " + padRight("dimension", nameW) + " " + padRight("failing", valW) + " " +
		padRight("healthy", valW) + " " + padRight("separation", sepW) + " " + padRight("purity", purW)
	lines := []string{stLabel.Render(truncate(header, w))}

	for _, d := range dims {
		row := "  " + padRight(truncate(orNone(d.Name), nameW), nameW) + " " +
			padRight(truncate(orNone(d.FailingValues), valW), valW) + " " +
			padRight(truncate(orNone(d.HealthyValues), valW), valW) + " " +
			padRight(truncate(separationWord(d.Separation), sepW), sepW) + " " +
			padRight(fmt.Sprintf("%.2f", d.Purity), purW)

		switch d.Separation {
		case result.Total:
			lines = append(lines, stSelected.Render(truncate("▶ "+strings.TrimPrefix(row, "  "), w)))
		case result.Partial:
			lines = append(lines, stValue.Render(truncate(row, w)))
		default:
			lines = append(lines, stMuted.Render(truncate(row, w)))
		}
	}
	return window(lines, 0, capacity)
}

func separationWord(s result.Separation) string {
	if strings.TrimSpace(string(s)) == "" {
		return string(result.Unresolved)
	}
	return string(s)
}

func revisionLines(revs []result.Revision, w, capacity int) []string {
	if len(revs) == 0 {
		return []string{stMuted.Render("no controller revision recorded in this snapshot")}
	}
	nameW := 28
	header := "  " + padRight("revision", nameW) + " " + padRight("number", 7) + " " +
		padRight("replicas", 9) + " " + padRight("active", 8) + " created"
	lines := []string{stLabel.Render(truncate(header, w))}
	for _, rev := range revs {
		row := "  " + padRight(truncate(orNone(rev.Name), nameW), nameW) + " " +
			padRight(fmt.Sprintf("%d", rev.Number), 7) + " " +
			padRight(fmt.Sprintf("%d", rev.Replicas), 9) + " " +
			padRight(truncate(orNone(rev.Active), 8), 8) + " " + shortTime(rev.Created)
		lines = append(lines, stValue.Render(truncate(row, w)))
		if strings.TrimSpace(rev.Signals) != "" {
			lines = append(lines, "    "+stFaint.Render(truncate("signals "+rev.Signals, w-4)))
		}
	}
	return window(lines, 0, capacity)
}

func (m whyModel) suspectHint() string {
	if len(m.report.Suspects) == 0 {
		return "none"
	}
	return fmt.Sprintf("%d of %d", clampIndex(m.cursor, len(m.report.Suspects))+1, len(m.report.Suspects))
}

func (m whyModel) suspectLines(w, capacity int) []string {
	r := m.report
	var lines []string
	focus := 0

	if len(r.Suspects) == 0 {
		lines = append(lines, stMuted.Render("no suspect recorded in this snapshot"))
	} else {
		cursor := clampIndex(m.cursor, len(r.Suspects))
		for i, s := range r.Suspects {
			if i == cursor {
				focus = len(lines)
			}
			lines = append(lines, suspectLine(i, s, i == cursor, w))
			lines = append(lines, suspectMetaLine(s, w))
			lines = append(lines, suspectDiffLines(s, m.expanded[i], w)...)
			if m.expanded[i] {
				lines = append(lines, suspectEvidenceLines(s, w)...)
			}
		}
	}

	lines = append(lines, "", stLabel.Render("coverage gaps"))
	if len(r.Gaps) == 0 {
		lines = append(lines, stMuted.Render("no coverage gap recorded in this snapshot"))
	} else {
		for _, g := range r.Gaps {
			lines = append(lines, gapLine(g, w))
		}
	}
	if n := narrativeLines(r.Narrative, w); len(n) > 0 {
		lines = append(lines, "")
		lines = append(lines, n...)
	}

	return window(lines, focus, capacity)
}

func suspectLine(i int, s result.Suspect, selected bool, w int) string {
	gutter := "  "
	if selected {
		gutter = "› "
	}
	rank := fmt.Sprintf("#%-2d ", i+1)
	stamp := shortTime(s.At)
	rest := w - len(gutter) - len(rank) - verdictWidth - 1 - len([]rune(stamp)) - 2
	if rest < 6 {
		rest = 6
	}
	titleStyle := stStrong
	if selected {
		titleStyle = stSelected
	}
	return gutter + stFaint.Render(rank) + verdictTag(s.Verdict) + " " +
		stFaint.Render(stamp) + "  " + titleStyle.Render(truncate(orNone(s.Title), rest))
}

func suspectMetaLine(s result.Suspect, w int) string {
	parts := []string{"id " + orNone(s.ID)}
	if strings.TrimSpace(s.Dimension) != "" {
		parts = append(parts, "dimension "+s.Dimension)
	}
	actor := strings.TrimSpace(string(s.Actor))
	if actor == "" {
		actor = string(result.ActorUnknown)
	}
	parts = append(parts, "actor "+actor)
	if strings.TrimSpace(s.Attribution) != "" {
		parts = append(parts, "attribution "+s.Attribution)
	}
	return "      " + stFaint.Render(truncate(strings.Join(parts, " · "), w-6))
}

func suspectDiffLines(s result.Suspect, expanded bool, w int) []string {
	indent := "      "
	inner := w - len(indent)
	if inner < 8 {
		inner = 8
	}
	if len(s.Diff) == 0 {
		return []string{indent + stMuted.Render("no diff line recorded for this change")}
	}
	limit := len(s.Diff)
	if !expanded && limit > 2 {
		limit = 2
	}
	out := make([]string, 0, limit+1)
	for _, d := range s.Diff[:limit] {
		out = append(out, indent+diffStyle(d).Render(truncate(d, inner)))
	}
	if limit < len(s.Diff) {
		left := len(s.Diff) - limit
		out = append(out, indent+stFaint.Render(fmt.Sprintf("%d more diff %s, enter to expand", left, plural(left, "line", "lines"))))
	}
	return out
}

// diffStyle colours a unified diff line by its marker. The marker itself stays visible.
func diffStyle(line string) lipgloss.Style {
	switch {
	case strings.HasPrefix(line, "+"):
		return lipgloss.NewStyle().Foreground(colAccent)
	case strings.HasPrefix(line, "-"):
		return lipgloss.NewStyle().Foreground(colDisruption)
	default:
		return stValue
	}
}

func suspectEvidenceLines(s result.Suspect, w int) []string {
	indent := "      "
	inner := w - len(indent)
	if inner < 8 {
		inner = 8
	}
	if len(s.Evidence) == 0 {
		return []string{indent + stMuted.Render("no evidence recorded for this suspect")}
	}
	out := make([]string, 0, len(s.Evidence))
	for _, e := range s.Evidence {
		out = append(out, indent+stFaint.Render(truncate("evidence "+e, inner)))
	}
	return out
}

func verdictTag(v result.Verdict) string {
	word := strings.TrimSpace(string(v))
	if word == "" {
		word = string(result.Unknown)
	}
	var style lipgloss.Style
	switch v {
	case result.Splits:
		style = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	case result.Temporal:
		style = lipgloss.NewStyle().Foreground(colDisruption)
	case result.NoSplit:
		style = stMuted
	default:
		style = lipgloss.NewStyle().Foreground(colNotAssessed)
	}
	return style.Render(padRight(word, verdictWidth))
}
