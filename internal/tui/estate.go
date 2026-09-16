package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/arnocho/spanline/internal/result"
)

// pane identifies one focusable region of the estate cockpit.
type pane int

const (
	paneList pane = iota
	paneDetail
	paneRisks
	paneCount
)

func (p pane) String() string {
	switch p {
	case paneList:
		return "clusters and node pools"
	case paneDetail:
		return "detail"
	default:
		return "risks"
	}
}

type rowKind int

const (
	rowCluster rowKind = iota
	rowPool
)

// estateRow is one line of the left pane. It carries a copy of the report entry it shows.
type estateRow struct {
	id      int
	kind    rowKind
	name    string
	stats   string
	search  string
	risky   bool
	cluster result.ClusterSummary
	pool    result.PoolSummary
}

type estateKeyMap struct {
	Up        key.Binding
	Down      key.Binding
	NextPane  key.Binding
	PrevPane  key.Binding
	Expand    key.Binding
	Filter    key.Binding
	RisksOnly key.Binding
	Help      key.Binding
	Quit      key.Binding
}

func defaultEstateKeys() estateKeyMap {
	return estateKeyMap{
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		NextPane:  key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "pane")),
		PrevPane:  key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous pane")),
		Expand:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "expand")),
		Filter:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		RisksOnly: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "risks only")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k estateKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.NextPane, k.Expand, k.Filter, k.RisksOnly, k.Help, k.Quit}
}

func (k estateKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down},
		{k.NextPane, k.PrevPane},
		{k.Expand, k.Filter, k.RisksOnly},
		{k.Help, k.Quit},
	}
}

// estateModel is the cockpit: clusters and node pools on the left, detail on the right, risks below.
type estateModel struct {
	ch           chrome
	report       *result.EstateReport
	keys         estateKeyMap
	rows         []estateRow
	focus        pane
	cursor       int
	riskCursor   int
	detailTop    int
	expanded     map[int]bool
	riskExpanded map[int]bool
	filter       string
	filtering    bool
	risksOnly    bool
}

func newEstateModel(r *result.EstateReport) estateModel {
	if r == nil {
		r = &result.EstateReport{}
	}
	return estateModel{
		ch:           newChrome("estate", estateContext(r), estateSource(r), r.GeneratedAt),
		report:       r,
		keys:         defaultEstateKeys(),
		rows:         estateRows(r),
		expanded:     map[int]bool{},
		riskExpanded: map[int]bool{},
	}
}

func estateContext(r *result.EstateReport) string {
	switch len(r.Clusters) {
	case 0:
		return "no cluster in this snapshot"
	case 1:
		return r.Clusters[0].Context
	default:
		return fmt.Sprintf("%d clusters", len(r.Clusters))
	}
}

func estateSource(r *result.EstateReport) string {
	if len(r.States) == 0 {
		return "no terraform state read"
	}
	resources, unmatched := 0, 0
	for _, s := range r.States {
		resources += s.Resources
		unmatched += s.Unmatched
	}
	out := fmt.Sprintf("%d %s, %d %s", len(r.States), plural(len(r.States), "state file", "state files"),
		resources, plural(resources, "resource", "resources"))
	if unmatched > 0 {
		out += fmt.Sprintf(", %d unmatched", unmatched)
	}
	return out
}

// estateRows lists every cluster followed by its pools, in report order. Pools whose context
// matches no cluster are listed last. Nothing is sorted or re-ranked here.
func estateRows(r *result.EstateReport) []estateRow {
	rows := make([]estateRow, 0, len(r.Clusters)+len(r.Pools))
	used := make([]bool, len(r.Pools))

	add := func(row estateRow) {
		row.id = len(rows)
		rows = append(rows, row)
	}
	for _, c := range r.Clusters {
		add(clusterRow(c))
		for i, p := range r.Pools {
			if used[i] || p.Context != c.Context {
				continue
			}
			used[i] = true
			add(poolRow(p))
		}
	}
	for i, p := range r.Pools {
		if used[i] {
			continue
		}
		add(poolRow(p))
	}
	return rows
}

func clusterRow(c result.ClusterSummary) estateRow {
	return estateRow{
		kind:    rowCluster,
		name:    orNone(c.Context),
		stats:   fmt.Sprintf("%d nodes · %d %s", c.Nodes, c.Risks, plural(c.Risks, "risk", "risks")),
		search:  strings.ToLower(c.Context),
		risky:   c.Risks > 0 || c.Outages > 0,
		cluster: c,
	}
}

func poolRow(p result.PoolSummary) estateRow {
	return estateRow{
		kind:   rowPool,
		name:   orNone(p.Pool),
		stats:  fmt.Sprintf("%d nodes · %d at risk", p.Nodes, p.AtRisk),
		search: strings.ToLower(p.Pool + " " + p.Context + " " + p.TerraformAddress),
		risky:  p.AtRisk > 0,
		pool:   p,
	}
}

func (m estateModel) Init() tea.Cmd { return nil }

func (m estateModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.ch.resize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		if m.filtering {
			return m.updateFilter(msg), nil
		}
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Help):
			m.ch.help.ShowAll = !m.ch.help.ShowAll
		case key.Matches(msg, m.keys.NextPane):
			m.focus = (m.focus + 1) % paneCount
		case key.Matches(msg, m.keys.PrevPane):
			m.focus = (m.focus + paneCount - 1) % paneCount
		case key.Matches(msg, m.keys.Up):
			m.move(-1)
		case key.Matches(msg, m.keys.Down):
			m.move(1)
		case key.Matches(msg, m.keys.Expand):
			m.toggleExpand()
		case key.Matches(msg, m.keys.Filter):
			m.filtering = true
			m.focus = paneList
		case key.Matches(msg, m.keys.RisksOnly):
			m.risksOnly = !m.risksOnly
			m.cursor = 0
		}
	}
	return m, nil
}

// updateFilter edits the substring filter. Enter keeps it, escape clears it.
func (m estateModel) updateFilter(msg tea.KeyMsg) tea.Model {
	switch msg.Type {
	case tea.KeyEsc:
		m.filtering = false
		m.filter = ""
		m.cursor = 0
	case tea.KeyEnter:
		m.filtering = false
		m.cursor = 0
	case tea.KeyBackspace:
		r := []rune(m.filter)
		if len(r) > 0 {
			m.filter = string(r[:len(r)-1])
			m.cursor = 0
		}
	case tea.KeySpace:
		m.filter += " "
		m.cursor = 0
	case tea.KeyRunes:
		m.filter += string(msg.Runes)
		m.cursor = 0
	}
	return m
}

func (m *estateModel) move(delta int) {
	switch m.focus {
	case paneList:
		n := len(m.visibleRows())
		m.cursor = clampIndex(m.cursor+delta, n)
	case paneDetail:
		m.detailTop += delta
		if m.detailTop < 0 {
			m.detailTop = 0
		}
	case paneRisks:
		m.riskCursor = clampIndex(m.riskCursor+delta, len(m.report.Risks))
	}
}

func (m *estateModel) toggleExpand() {
	switch m.focus {
	case paneList:
		rows := m.visibleRows()
		if len(rows) == 0 {
			return
		}
		row := rows[clampIndex(m.cursor, len(rows))]
		m.expanded[row.id] = !m.expanded[row.id]
	case paneRisks:
		if len(m.report.Risks) == 0 {
			return
		}
		i := clampIndex(m.riskCursor, len(m.report.Risks))
		m.riskExpanded[i] = !m.riskExpanded[i]
	}
}

func clampIndex(i, n int) int {
	if n <= 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

func (m estateModel) visibleRows() []estateRow {
	if m.filter == "" && !m.risksOnly {
		return m.rows
	}
	needle := strings.ToLower(m.filter)
	out := make([]estateRow, 0, len(m.rows))
	for _, row := range m.rows {
		if m.risksOnly && !row.risky {
			continue
		}
		if needle != "" && !strings.Contains(row.search, needle) {
			continue
		}
		out = append(out, row)
	}
	return out
}

func (m estateModel) View() string {
	head := m.ch.headerView()
	foot := m.ch.footerView(m.keys, []string{m.statusLine()})
	return m.ch.assemble(head, m.bodyView(m.ch.bodyHeight(head, foot)), foot)
}

func (m estateModel) statusLine() string {
	rows := m.visibleRows()
	filter := "filter none"
	switch {
	case m.filtering:
		filter = "filter " + m.filter + "▌ (enter keeps it, esc clears it)"
	case m.filter != "":
		filter = "filter " + m.filter
	}
	risks := "risks only off"
	if m.risksOnly {
		risks = "risks only on"
	}
	position := fmt.Sprintf("%d of %d %s shown", len(rows), len(m.rows), plural(len(m.rows), "row", "rows"))
	return stMuted.Render(strings.Join([]string{"pane " + m.focus.String(), filter, risks, position}, "   ·   "))
}

func (m estateModel) bodyView(avail int) string {
	rows := m.visibleRows()
	cursor := clampIndex(m.cursor, len(rows))

	if !m.ch.wide() {
		// Narrow terminal: one pane at a time, tab cycles between them.
		w := m.ch.safeWidth()
		switch m.focus {
		case paneList:
			return renderPane(paneList.String(), m.listHint(rows),
				m.listLines(rows, cursor, paneInnerWidth(w), paneRowCapacity(avail)), w, avail, true)
		case paneDetail:
			return renderPane(paneDetail.String(), "",
				m.detailLines(rows, cursor, paneInnerWidth(w), paneRowCapacity(avail)), w, avail, true)
		default:
			return renderPane(paneRisks.String(), m.risksHint(),
				m.risksLines(paneInnerWidth(w), paneRowCapacity(avail)), w, avail, true)
		}
	}

	listW := m.ch.width * 42 / 100
	if listW < 32 {
		listW = 32
	}
	if m.ch.width-listW < 34 {
		listW = m.ch.width - 34
	}
	detailW := m.ch.width - listW

	topH := avail * 62 / 100
	risksH := avail - topH
	if risksH < 6 {
		risksH = 6
		topH = avail - risksH
	}
	if topH < 5 {
		topH = 5
		risksH = avail - topH
	}
	if risksH < 4 {
		risksH = 4
	}

	top := lipgloss.JoinHorizontal(lipgloss.Top,
		renderPane(paneList.String(), m.listHint(rows),
			m.listLines(rows, cursor, paneInnerWidth(listW), paneRowCapacity(topH)), listW, topH, m.focus == paneList),
		renderPane(paneDetail.String(), "",
			m.detailLines(rows, cursor, paneInnerWidth(detailW), paneRowCapacity(topH)), detailW, topH, m.focus == paneDetail),
	)
	risks := renderPane(paneRisks.String(), m.risksHint(),
		m.risksLines(paneInnerWidth(m.ch.width), paneRowCapacity(risksH)), m.ch.width, risksH, m.focus == paneRisks)

	return lipgloss.JoinVertical(lipgloss.Left, top, risks)
}

func (m estateModel) listHint(rows []estateRow) string {
	if len(rows) == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d", clampIndex(m.cursor, len(rows))+1, len(rows))
}

func (m estateModel) risksHint() string {
	if len(m.report.Risks) == 0 {
		return "none"
	}
	return fmt.Sprintf("%d of %d", clampIndex(m.riskCursor, len(m.report.Risks))+1, len(m.report.Risks))
}

func (m estateModel) listLines(rows []estateRow, cursor, w, capacity int) []string {
	if len(rows) == 0 {
		// The list pane is the narrowest one, so these sentences are wrapped rather than cut.
		if m.filter != "" || m.risksOnly {
			return wrapLines("no cluster and no node pool matches the current filter", w, stMuted)
		}
		return wrapLines("no cluster and no node pool in this snapshot", w, stMuted)
	}

	lines := make([]string, 0, len(rows)+4)
	focus := 0
	for i, row := range rows {
		selected := i == cursor && m.focus == paneList
		if i == cursor {
			focus = len(lines)
		}
		lines = append(lines, m.rowLine(row, selected, w))
		if m.expanded[row.id] {
			lines = append(lines, rowPeekLines(row, w)...)
		}
	}
	return window(lines, focus, capacity)
}

func (m estateModel) rowLine(row estateRow, selected bool, w int) string {
	gutter := "  "
	if selected {
		gutter = "› "
	}
	marker := "▸ "
	if m.expanded[row.id] {
		marker = "▾ "
	}
	indent := ""
	if row.kind == rowPool {
		indent = "  "
	}
	leftStyle, rightStyle := stValue, stFaint
	if selected {
		leftStyle, rightStyle = stSelected, stAccent
	}
	return splitLine(gutter+indent+marker+row.name, row.stats, w, leftStyle, rightStyle)
}

// rowPeekLines is the inline expansion of a row. The full record stays in the detail pane.
func rowPeekLines(row estateRow, w int) []string {
	indent := "      "
	inner := w - len(indent)
	if inner < 8 {
		inner = 8
	}
	var body []string
	if row.kind == rowCluster {
		c := row.cluster
		body = []string{
			fmt.Sprintf("%d pods · %d namespaces · %d workloads", c.Pods, c.Namespaces, c.Workloads),
			fmt.Sprintf("%d nodes not ready · %d outages", c.NodesNotReady, c.Outages),
		}
	} else {
		p := row.pool
		body = []string{
			"terraform " + orSnapshot(p.TerraformAddress),
			fmt.Sprintf("cpu %.1f%% · mem %.1f%% · %d workloads", p.CPUPercent, p.MemPercent, p.Workloads),
		}
	}
	out := make([]string, 0, len(body))
	for _, b := range body {
		out = append(out, indent+stFaint.Render(truncate(b, inner)))
	}
	return out
}

func (m estateModel) detailLines(rows []estateRow, cursor, w, capacity int) []string {
	if len(rows) == 0 {
		return wrapLines("no cluster and no node pool to detail in this snapshot", w, stMuted)
	}
	row := rows[cursor]

	var lines []string
	if row.kind == rowCluster {
		c := row.cluster
		lines = []string{
			stStrong.Render(truncate("cluster "+row.name, w)),
			"",
			kvLine("context", orNone(c.Context), w),
			kvLine("nodes", fmt.Sprintf("%d", c.Nodes), w),
			kvLine("nodes not ready", fmt.Sprintf("%d", c.NodesNotReady), w),
			kvLine("pods", fmt.Sprintf("%d", c.Pods), w),
			kvLine("namespaces", fmt.Sprintf("%d", c.Namespaces), w),
			kvLine("workloads", fmt.Sprintf("%d", c.Workloads), w),
			kvLine("risks", fmt.Sprintf("%d", c.Risks), w),
			kvLine("outages", fmt.Sprintf("%d", c.Outages), w),
			kvLine("collected at", shortTime(c.CollectedAt), w),
			"",
			stLabel.Render("node pools in this cluster"),
		}
		pools := 0
		for _, r := range m.rows {
			if r.kind == rowPool && r.pool.Context == c.Context {
				pools++
				lines = append(lines, "  "+stValue.Render(truncate(r.name+"  "+orSnapshot(r.pool.TerraformAddress), w-2)))
			}
		}
		if pools == 0 {
			lines = append(lines, "  "+stMuted.Render("no node pool recorded for this cluster"))
		}
	} else {
		p := row.pool
		lines = []string{
			stStrong.Render(truncate("node pool "+row.name, w)),
			"",
			kvLine("context", orNone(p.Context), w),
			kvLine("terraform address", orSnapshot(p.TerraformAddress), w),
			kvLine("state file", orSnapshot(p.StateFile), w),
			kvLine("nodes", fmt.Sprintf("%d", p.Nodes), w),
			kvLine("cpu", fmt.Sprintf("%.1f%%", p.CPUPercent), w),
			kvLine("memory", fmt.Sprintf("%.1f%%", p.MemPercent), w),
			kvLine("workloads", fmt.Sprintf("%d", p.Workloads), w),
			kvLine("at risk", fmt.Sprintf("%d", p.AtRisk), w),
			kvLine("zones", orSnapshot(p.Zones), w),
		}
	}

	if n := narrativeLines(m.report.Narrative, w); len(n) > 0 {
		lines = append(lines, "")
		lines = append(lines, n...)
	}
	return scrollFrom(lines, m.detailTop, capacity)
}

func (m estateModel) risksLines(w, capacity int) []string {
	var lines []string
	focus := 0

	if len(m.report.Risks) == 0 {
		lines = append(lines, wrapLines("no risk recorded in this snapshot", w, stMuted)...)
	} else {
		cursor := clampIndex(m.riskCursor, len(m.report.Risks))
		for i, f := range m.report.Risks {
			if i == cursor {
				focus = len(lines)
			}
			lines = append(lines, findingLine(f, i == cursor && m.focus == paneRisks, w))
			if m.riskExpanded[i] {
				lines = append(lines, findingDetailLines(f, w)...)
			}
		}
	}

	lines = append(lines, "", stLabel.Render("coverage gaps"))
	if len(m.report.Gaps) == 0 {
		lines = append(lines, stMuted.Render("no coverage gap recorded in this snapshot"))
	} else {
		for _, g := range m.report.Gaps {
			lines = append(lines, gapLine(g, w))
		}
	}
	return window(lines, focus, capacity)
}

// findingLine is the one line form of a finding, shared by the estate and impact views.
func findingLine(f result.Finding, selected bool, w int) string {
	gutter := "  "
	if selected {
		gutter = "› "
	}
	objectWidth := 30
	if w < 70 {
		objectWidth = 18
	}
	rest := w - len(gutter) - severityWidth - 1 - objectWidth - 1
	if rest < 4 {
		rest = 4
	}
	objectStyle := stStrong
	if selected {
		objectStyle = stSelected
	}
	return gutter + severityTag(f.Severity) + " " +
		objectStyle.Render(padRight(truncate(orNone(f.Object), objectWidth), objectWidth)) + " " +
		stValue.Render(truncate(orNone(f.Reason), rest))
}

// findingDetailLines is the expansion of a finding: what it was derived from.
func findingDetailLines(f result.Finding, w int) []string {
	indent := "    "
	inner := w - len(indent)
	if inner < 8 {
		inner = 8
	}
	lines := []string{indent + stLabel.Render("kind ") + stValue.Render(truncate(orNone(f.Kind), inner-5))}
	if strings.TrimSpace(f.Context) != "" {
		lines = append(lines, indent+stLabel.Render("context ")+stValue.Render(truncate(f.Context, inner-8)))
	}
	if len(f.Evidence) == 0 {
		lines = append(lines, indent+stMuted.Render("no evidence recorded for this finding"))
		return lines
	}
	for _, e := range f.Evidence {
		lines = append(lines, indent+stFaint.Render(truncate("evidence "+e, inner)))
	}
	return lines
}

// gapLine renders one coverage gap, aligned with the finding rows above it. A gap is never an
// implicit pass, so it carries the NOT ASSESSED severity spelled out.
func gapLine(gap string, w int) string {
	rest := w - severityWidth - 3
	if rest < 4 {
		rest = 4
	}
	return "  " + severityTag(result.NotAssessed) + " " + stValue.Render(truncate(gap, rest))
}

func orSnapshot(s string) string {
	if strings.TrimSpace(s) == "" {
		return "not recorded in this snapshot"
	}
	return s
}
