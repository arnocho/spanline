package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/arnocho/spanline/internal/brief"
	"github.com/arnocho/spanline/internal/result"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// View names the three things spanline can show. They are always in the same order, so the
// muscle memory of 1, 2, 3 never changes between runs.
type View int

const (
	ViewOverview View = iota
	ViewIncident
	ViewImpact
)

var viewNames = []string{"overview", "incident", "impact"}

// Data is everything the interface can display. Any field may be nil: the matching view then
// shows how to fill it rather than an empty screen.
type Data struct {
	Estate *result.EstateReport
	Why    *result.WhyReport
	Impact *result.ImpactReport
	Source string // "fixtures: estate" or "kubectl, read only"
	Live   bool
	Start  View
}

// row is one selectable thing on screen. Views build rows, the app draws them, so selection,
// reveal and the detail panel behave identically everywhere.
type row struct {
	render     func(t Theme, selected bool) string
	title      string
	ref        string
	sev        result.Severity
	detail     []string
	selectable bool
}

func staticRow(s string) row {
	return row{render: func(Theme, bool) string { return s }}
}

func spacer() row { return staticRow("") }

type app struct {
	t    Theme
	data Data
	view View

	cursor  map[View]int
	scroll  map[View]int
	w, h    int
	started time.Time
	entered time.Time // when the current view was entered, drives the reveal
	picked  time.Time // when the selection last moved, drives the pulse

	collecting bool
	steps      []step

	detail   bool
	detailAt int
	help     bool
	expanded bool
	coach    bool

	filtering bool
	filter    string
}

// Run opens the interface. It restores the terminal on every exit path.
func Run(d Data) error {
	m := newModel(d, 96, 30)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func newModel(d Data, w, h int) app {
	m := app{
		t:          NewTheme(w, true),
		data:       d,
		view:       d.Start,
		cursor:     map[View]int{},
		scroll:     map[View]int{},
		w:          w,
		h:          h,
		collecting: true,
		steps:      collectSteps(d),
		coach:      true,
	}
	if !m.enabled(m.view) && m.anyEnabled() {
		m.view = m.firstEnabled()
	}
	return m
}

// anyEnabled reports whether any view holds a report. With none, the interface keeps the view
// the user asked for and explains how to fill it, instead of silently showing another one.
func (m app) anyEnabled() bool {
	return m.enabled(ViewOverview) || m.enabled(ViewIncident) || m.enabled(ViewImpact)
}

// collectSteps is the opening animation. It is not decoration: it names every read spanline
// performs, in order, which is the fastest way to teach what the tool touches.
func collectSteps(d Data) []step {
	var s []step
	if d.Live {
		s = append(s, step{"kube contexts", "read from your kubeconfig"})
	} else {
		s = append(s, step{"recorded scenario", "no cluster is contacted"})
	}
	s = append(s,
		step{"nodes and pools", "labels, zones, allocatable"},
		step{"workloads and pods", "replicas, restarts, readiness"},
		step{"disruption budgets", "what a drain would block"},
		step{"volumes", "which ones are pinned to a zone"},
	)
	if d.Estate != nil && len(d.Estate.States) > 0 {
		s = append(s, step{"terraform state", "who owns each node pool"})
	}
	if d.Impact != nil && d.Impact.PlanSummary != "" {
		s = append(s, step{"terraform plan", d.Impact.PlanSummary})
	}
	s = append(s, step{"secrets", "never read, by construction"})
	return s
}

func (m app) Init() tea.Cmd { return tick() }

func (m app) enabled(v View) bool {
	switch v {
	case ViewOverview:
		return m.data.Estate != nil
	case ViewIncident:
		return m.data.Why != nil
	case ViewImpact:
		return m.data.Impact != nil
	}
	return false
}

func (m app) firstEnabled() View {
	for _, v := range []View{ViewOverview, ViewIncident, ViewImpact} {
		if m.enabled(v) {
			return v
		}
	}
	return ViewOverview
}

func (m app) rows() []row {
	switch m.view {
	case ViewIncident:
		return incidentRows(m.t, m.data.Why, m.expanded)
	case ViewImpact:
		return impactRows(m.t, m.data.Impact, m.expanded)
	default:
		return overviewRows(m.t, m.data.Estate, m.expanded)
	}
}

func (m app) filtered() []row {
	rows := m.rows()
	if m.filter == "" {
		return rows
	}
	f := strings.ToLower(m.filter)
	var out []row
	for _, r := range rows {
		if !r.selectable {
			continue
		}
		if strings.Contains(strings.ToLower(r.title), f) || strings.Contains(strings.ToLower(r.ref), f) {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return []row{staticRow(m.t.Muted("  nothing matches " + m.filter))}
	}
	return out
}

func selectableIndexes(rows []row) []int {
	var idx []int
	for i, r := range rows {
		if r.selectable {
			idx = append(idx, i)
		}
	}
	return idx
}

func (m *app) move(delta int) {
	rows := m.filtered()
	idx := selectableIndexes(rows)
	if len(idx) == 0 {
		return
	}
	cur := m.cursor[m.view]
	pos := 0
	for i, v := range idx {
		if v == cur {
			pos = i
		}
	}
	pos += delta
	if pos < 0 {
		pos = 0
	}
	if pos >= len(idx) {
		pos = len(idx) - 1
	}
	m.cursor[m.view] = idx[pos]
	m.picked = time.Now()
}

func (m *app) switchView(delta int) {
	order := []View{ViewOverview, ViewIncident, ViewImpact}
	cur := int(m.view)
	for i := 0; i < len(order); i++ {
		cur = (cur + delta + len(order)) % len(order)
		if m.enabled(View(cur)) {
			m.view = View(cur)
			m.entered = time.Now()
			m.detail = false
			return
		}
	}
}

func (m app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.t = NewTheme(msg.Width, m.t.Color)
		return m, nil

	case tickMsg:
		now := time.Time(msg)
		if m.started.IsZero() {
			m.started = now
			m.entered = now
		}
		if m.collecting {
			_, running := stepState(len(m.steps), now.Sub(m.started))
			if !running {
				m.collecting = false
				m.entered = now
			}
		}
		if m.coach && now.Sub(m.started) > 6*time.Second {
			m.coach = false
		}
		return m, tick()

	case tea.KeyMsg:
		m.coach = false
		if m.filtering {
			switch msg.String() {
			case "enter":
				m.filtering = false
			case "esc":
				m.filtering, m.filter = false, ""
			case "backspace":
				if m.filter != "" {
					m.filter = m.filter[:len(m.filter)-1]
				}
			default:
				if len(msg.String()) == 1 {
					m.filter += msg.String()
				}
			}
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "?":
			m.help = !m.help
		case "esc", "left", "h":
			switch {
			case m.help:
				m.help = false
			case m.detail:
				m.detail = false
			case m.filter != "":
				m.filter = ""
			}
		case "enter", "right", "l":
			if !m.help {
				rows := m.filtered()
				if c := m.cursor[m.view]; c < len(rows) && rows[c].selectable && len(rows[c].detail) > 0 {
					m.detail, m.detailAt = true, 0
				}
			}
		case "up", "k":
			if m.detail {
				if m.detailAt > 0 {
					m.detailAt--
				}
			} else {
				m.move(-1)
			}
		case "down", "j":
			if m.detail {
				m.detailAt++
			} else {
				m.move(1)
			}
		case "tab":
			m.switchView(1)
		case "shift+tab":
			m.switchView(-1)
		case "1":
			if m.enabled(ViewOverview) {
				m.view, m.entered, m.detail = ViewOverview, time.Now(), false
			}
		case "2":
			if m.enabled(ViewIncident) {
				m.view, m.entered, m.detail = ViewIncident, time.Now(), false
			}
		case "3":
			if m.enabled(ViewImpact) {
				m.view, m.entered, m.detail = ViewImpact, time.Now(), false
			}
		case "d":
			m.expanded = !m.expanded
			m.entered = time.Now()
		case "/":
			m.filtering, m.filter = true, ""
		case "g":
			m.cursor[m.view] = 0
			m.move(0)
		}
		return m, nil
	}
	return m, nil
}

func (m app) since() time.Duration {
	if m.started.IsZero() {
		return 0
	}
	return time.Since(m.entered)
}

func (m app) View() string {
	return m.frame(time.Since(m.started), m.since())
}

// frame draws everything. It takes the two clocks explicitly so a test, or a screenshot,
// can ask for any moment of any animation and get the same bytes every time.
func (m app) frame(sinceStart, sinceView time.Duration) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(m.header())
	b.WriteString("\n\n")

	if m.collecting {
		b.WriteString(m.collectView(sinceStart))
		return b.String()
	}
	if m.help {
		b.WriteString(m.helpView())
		return b.String()
	}
	if m.detail {
		b.WriteString(m.detailView())
		return b.String()
	}

	b.WriteString(m.t.Tabs(viewNames, int(m.view), []bool{
		m.enabled(ViewOverview), m.enabled(ViewIncident), m.enabled(ViewImpact),
	}))
	b.WriteString("\n\n")

	if !m.enabled(m.view) {
		b.WriteString(m.emptyView())
		return b.String()
	}

	b.WriteString(m.headline())
	b.WriteString("\n\n")

	rows := m.filtered()
	cursor := m.cursor[m.view]
	shown := 0
	budget := m.bodyBudget()
	for i, r := range rows {
		if shown >= budget {
			left := len(rows) - i
			more := fmt.Sprintf("%d more below, press d to expand or / to filter", left)
			if m.t.Narrow() {
				more = fmt.Sprintf("%d more, press d", left)
			}
			b.WriteString("  " + m.t.Faint(cut(more, m.t.Inner())) + "\n")
			break
		}
		state := reveal(i, sinceView)
		if state == revealHidden {
			break
		}
		line := r.render(m.t, r.selectable && i == cursor)
		if state == revealDim {
			line = m.t.Faint(stripANSI(line))
		}
		b.WriteString(line + "\n")
		shown += strings.Count(line, "\n") + 1
	}

	b.WriteString("\n")
	if m.filter != "" || m.filtering {
		b.WriteString("  " + m.t.Accent("filter: ") + m.filter + m.t.Faint("  esc clears") + "\n")
	}
	if m.coach {
		b.WriteString("  " + m.t.Faint("move with up and down, press right for the evidence behind a line, tab to change view") + "\n")
	}
	b.WriteString(m.footer())
	return b.String()
}

func (m app) bodyBudget() int {
	budget := m.h - 12
	if m.expanded {
		budget = 10000
	}
	if budget < 6 {
		budget = 6
	}
	return budget
}

func (m app) header() string {
	name := m.t.Accent("spanline")
	src := m.t.Faint(m.data.Source)
	mode := m.t.Muted("recorded")
	if m.data.Live {
		mode = m.t.paint(colOK, "live, read only")
	}
	left := "  " + name + "  " + mode
	right := src
	gap := m.t.Inner() - lipgloss.Width(left) - lipgloss.Width(right) + 2
	if gap < 2 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m app) headline() string {
	var b brief.Brief
	switch m.view {
	case ViewIncident:
		b = brief.Why(m.data.Why)
	case ViewImpact:
		b = brief.Impact(m.data.Impact)
	default:
		b = brief.Estate(m.data.Estate)
	}
	out := "  " + m.t.Headline(b.Headline, b.Sev)
	if b.Sub != "" {
		out += "\n  " + m.t.Faint(cut(b.Sub, m.t.Inner()))
	}
	return out
}

func (m app) footer() string {
	hints := []Hint{{"↑↓", "move"}, {"→", "evidence"}, {"tab", "view"}, {"d", "expand"}, {"/", "filter"}, {"?", "keys"}, {"q", "quit"}}
	if m.detail {
		hints = []Hint{{"↑↓", "scroll"}, {"esc", "back"}, {"q", "quit"}}
	}
	if m.t.Narrow() && len(hints) > 4 {
		// trimming the footer must never remove the way out
		hints = append(append([]Hint{}, hints[:3]...), Hint{"q", "quit"})
	}
	line := m.t.Footer(hints)
	if m.view == ViewImpact && m.data.Impact != nil {
		v := m.data.Impact
		line = "  " + m.t.Chip(v.Verdict) + m.t.Muted(fmt.Sprintf("   exit code %d", v.ExitCode)) + "\n" + line
	}
	return line
}

func (m app) collectView(since time.Duration) string {
	var b strings.Builder
	done, running := stepState(len(m.steps), since)
	b.WriteString("  " + m.t.Muted("reading") + "\n\n")
	for i, s := range m.steps {
		switch {
		case i < done:
			b.WriteString("  " + m.t.paint(colOK, "✓") + " " + m.t.paint(colText, pad(s.What, 22)) + m.t.Faint(s.Note) + "\n")
		case i == done && running:
			b.WriteString("  " + m.t.Accent(spinner(since)) + " " + m.t.paint(colText, pad(s.What, 22)) + m.t.Faint(s.Note) + "\n")
		default:
			b.WriteString("  " + m.t.Faint("·") + " " + m.t.Faint(pad(s.What, 22)) + "\n")
		}
	}
	return b.String()
}

func (m app) emptyView() string {
	var what, how string
	switch m.view {
	case ViewIncident:
		what, how = "no incident is loaded", "spanline why deploy/checkout -n payments"
	case ViewImpact:
		what, how = "no change is loaded", "spanline impact --nodes pool=apps"
	default:
		what, how = "no cluster was read", "spanline cockpit"
	}
	return "  " + m.t.Muted(what) + "\n  " + m.t.Faint("run: ") + m.t.Accent(how) + "\n\n" + m.footer()
}

func (m app) detailView() string {
	rows := m.filtered()
	c := m.cursor[m.view]
	if c >= len(rows) {
		return m.footer()
	}
	r := rows[c]
	var b strings.Builder
	b.WriteString("  " + m.t.Chip(r.sev) + "  " + m.t.bold(colText, r.title) + "\n")
	b.WriteString("  " + m.t.Rule() + "\n\n")
	lines := r.detail
	budget := m.h - 10
	if budget < 4 {
		budget = 4
	}
	at := m.detailAt
	if at > len(lines)-1 {
		at = len(lines) - 1
	}
	if at < 0 {
		at = 0
	}
	end := at + budget
	if end > len(lines) {
		end = len(lines)
	}
	for _, l := range lines[at:end] {
		b.WriteString("  " + m.t.paint(colMuted, wrapIndent(l, m.t.Inner()-2, "    ")) + "\n")
	}
	if end < len(lines) {
		b.WriteString("\n  " + m.t.Faint(fmt.Sprintf("%d more lines, press down", len(lines)-end)) + "\n")
	}
	b.WriteString("\n" + m.footer())
	return b.String()
}

func (m app) helpView() string {
	keys := [][2]string{
		{"↑ ↓ or j k", "move through the list"},
		{"→ or enter", "open the evidence behind the selected line"},
		{"← or esc", "go back, close the panel, clear the filter"},
		{"tab", "next view, shift tab for the previous one"},
		{"1 2 3", "jump straight to overview, incident or impact"},
		{"d", "expand: show every row and every detail inline"},
		{"/", "filter the list, enter keeps it, esc clears it"},
		{"?", "this help"},
		{"q", "quit"},
	}
	var b strings.Builder
	b.WriteString("  " + m.t.bold(colText, "keys") + "\n\n")
	for _, k := range keys {
		b.WriteString("  " + m.t.Accent(pad(k[0], 14)) + m.t.Muted(k[1]) + "\n")
	}
	b.WriteString("\n  " + m.t.bold(colText, "what the three views answer") + "\n\n")
	for _, k := range [][2]string{
		{"overview", "where should I look first, and who owns the node pools"},
		{"incident", "what separates the failing pods, and which change made it"},
		{"impact", "what breaks if these nodes go away or this plan runs"},
	} {
		b.WriteString("  " + m.t.Accent(pad(k[0], 14)) + m.t.Muted(k[1]) + "\n")
	}
	b.WriteString("\n" + m.footer())
	return b.String()
}

func wrapIndent(s string, width int, indent string) string {
	w := wrapTo(s, width)
	parts := strings.Split(w, "\n")
	for i := 1; i < len(parts); i++ {
		parts[i] = indent + parts[i]
	}
	return strings.Join(parts, "\n")
}

func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			in = true
		case in && (r == 'm' || r == 'K'):
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}
