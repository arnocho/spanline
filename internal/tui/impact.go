package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/arnocho/spanline/internal/result"
)

type impactKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Expand key.Binding
	Help   key.Binding
	Quit   key.Binding
}

func defaultImpactKeys() impactKeyMap {
	return impactKeyMap{
		Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("up/k", "previous finding")),
		Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("down/j", "next finding")),
		Expand: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "expand evidence")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k impactKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Expand, k.Help, k.Quit}
}

func (k impactKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down}, {k.Expand}, {k.Help, k.Quit}}
}

// impactModel shows the pre change findings grouped by severity, with the verdict pinned below.
type impactModel struct {
	ch       chrome
	report   *result.ImpactReport
	keys     impactKeyMap
	findings []result.Finding
	cursor   int
	expanded map[int]bool
}

func newImpactModel(r *result.ImpactReport) impactModel {
	if r == nil {
		r = &result.ImpactReport{}
	}
	return impactModel{
		ch:       newChrome("impact", orNone(r.Context), impactSource(r), r.GeneratedAt),
		report:   r,
		keys:     defaultImpactKeys(),
		findings: groupBySeverity(r.Findings),
		expanded: map[int]bool{},
	}
}

func impactSource(r *result.ImpactReport) string {
	out := orNone(r.Source)
	if sha := strings.TrimSpace(r.PlanSHA); sha != "" {
		out += " · plan " + truncate(sha, 12)
	}
	return out
}

// groupBySeverity copies the findings into display groups, highest severity first. Order inside a
// group is the report's own order: nothing is re-ranked.
func groupBySeverity(findings []result.Finding) []result.Finding {
	out := make([]result.Finding, 0, len(findings))
	for _, sev := range severityOrder {
		for _, f := range findings {
			if normalizeSeverity(f.Severity) == sev {
				out = append(out, f)
			}
		}
	}
	for _, f := range findings {
		if !knownSeverity(f.Severity) {
			out = append(out, f)
		}
	}
	return out
}

func normalizeSeverity(s result.Severity) result.Severity {
	if strings.TrimSpace(string(s)) == "" {
		return result.NotAssessed
	}
	return s
}

func knownSeverity(s result.Severity) bool {
	n := normalizeSeverity(s)
	for _, sev := range severityOrder {
		if n == sev {
			return true
		}
	}
	return false
}

func (m impactModel) Init() tea.Cmd { return nil }

func (m impactModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			m.cursor = clampIndex(m.cursor-1, len(m.findings))
		case key.Matches(msg, m.keys.Down):
			m.cursor = clampIndex(m.cursor+1, len(m.findings))
		case key.Matches(msg, m.keys.Expand):
			if len(m.findings) > 0 {
				i := clampIndex(m.cursor, len(m.findings))
				m.expanded[i] = !m.expanded[i]
			}
		}
	}
	return m, nil
}

func (m impactModel) View() string {
	head := m.ch.headerView()
	foot := m.ch.footerView(m.keys, []string{m.verdictLine(), m.snapshotLine()})
	return m.ch.assemble(head, m.bodyView(m.ch.bodyHeight(head, foot)), foot)
}

// verdictLine is pinned in the footer: the verdict and the exit code the report carries.
func (m impactModel) verdictLine() string {
	r := m.report
	line := stLabel.Render("verdict ") + severityStyle(r.Verdict).Render(severityWord(r.Verdict)) +
		stFaint.Render("   ·   ") + stLabel.Render("exit code ") + stStrong.Render(fmt.Sprintf("%d", r.ExitCode)) +
		stFaint.Render("   ·   ") + stLabel.Render("findings ") + stValue.Render(fmt.Sprintf("%d", len(r.Findings)))
	return clipTo(line, m.ch.safeWidth())
}

func (m impactModel) snapshotLine() string {
	r := m.report
	line := stLabel.Render("snapshot ") + stValue.Render(shortTime(r.SnapshotAt)) +
		stFaint.Render("   ·   ") + stLabel.Render("expires ") + stValue.Render(shortTime(r.ExpiresAt))
	return clipTo(line, m.ch.safeWidth())
}

func (m impactModel) bodyView(avail int) string {
	width := m.ch.safeWidth()

	changeH := 8
	if !m.ch.wide() {
		changeH = 6
	}
	if avail-changeH < 6 {
		changeH = 5
	}
	findingsH := avail - changeH
	if findingsH < 4 {
		findingsH = 4
	}

	change := renderPane("change under review", "", m.changeLines(paneInnerWidth(width), paneRowCapacity(changeH)),
		width, changeH, false)
	findings := renderPane("findings by severity", m.findingsHint(),
		m.findingLines(paneInnerWidth(width), paneRowCapacity(findingsH)), width, findingsH, true)

	return lipgloss.JoinVertical(lipgloss.Left, change, findings)
}

func (m impactModel) findingsHint() string {
	if len(m.findings) == 0 {
		return "none"
	}
	return fmt.Sprintf("%d of %d", clampIndex(m.cursor, len(m.findings))+1, len(m.findings))
}

func (m impactModel) changeLines(w, capacity int) []string {
	r := m.report
	lines := []string{
		kvLine("source", orNone(r.Source), w),
		kvLine("plan sha", orSnapshot(r.PlanSHA), w),
		kvLine("plan summary", orSnapshot(r.PlanSummary), w),
	}
	if len(r.Nodes) == 0 {
		lines = append(lines, kvLine("nodes", "no node listed in this report", w))
	} else {
		lines = append(lines, kvLine("nodes", strings.Join(r.Nodes, ", "), w))
	}
	if len(r.Mapping) == 0 {
		lines = append(lines, kvLine("mapping", "no mapping recorded in this report", w))
	} else {
		for i, mp := range r.Mapping {
			label := "mapping"
			if i > 0 {
				label = ""
			}
			lines = append(lines, kvLine(label, mp, w))
		}
	}
	if len(r.Ignored) > 0 {
		for i, ig := range r.Ignored {
			label := "ignored"
			if i > 0 {
				label = ""
			}
			lines = append(lines, kvLine(label, ig, w))
		}
	}
	return window(lines, 0, capacity)
}

func (m impactModel) findingLines(w, capacity int) []string {
	r := m.report
	var lines []string
	focus := 0

	if len(m.findings) == 0 {
		lines = append(lines, stMuted.Render("no finding recorded in this report"))
	} else {
		cursor := clampIndex(m.cursor, len(m.findings))
		counts := severityCounts(m.findings)
		current := result.Severity("")
		for i, f := range m.findings {
			sev := normalizeSeverity(f.Severity)
			if sev != current {
				current = sev
				if i > 0 {
					lines = append(lines, "")
				}
				head := severityStyle(sev).Render(severityWord(sev)) +
					stFaint.Render(fmt.Sprintf("  %d %s", counts[sev], plural(counts[sev], "finding", "findings")))
				lines = append(lines, head)
			}
			if i == cursor {
				focus = len(lines)
			}
			lines = append(lines, findingLine(f, i == cursor, w))
			if m.expanded[i] {
				lines = append(lines, findingDetailLines(f, w)...)
			}
		}
	}

	lines = append(lines, "", stLabel.Render("coverage gaps"))
	if len(r.Gaps) == 0 {
		lines = append(lines, stMuted.Render("no coverage gap recorded in this report"))
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

// severityCounts counts the grouped findings per severity. The map is only read by key, so the
// rendering stays deterministic.
func severityCounts(findings []result.Finding) map[result.Severity]int {
	counts := make(map[result.Severity]int, len(severityOrder))
	for _, f := range findings {
		counts[normalizeSeverity(f.Severity)]++
	}
	return counts
}
