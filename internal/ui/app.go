package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/arnocho/spanline/internal/brief"
	"github.com/arnocho/spanline/internal/render"
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

// Actions lets the interface run an analysis on demand, so a finding in the cockpit can be
// followed into an incident or an impact without leaving the application. Each function reads
// the same read only source the cockpit was built from. Nil means the action is unavailable.
type Actions struct {
	Why    func(kubeContext, namespace, workload string) (*result.WhyReport, error)
	Impact func(kubeContext, selector string) (*result.ImpactReport, error)
}

// Data is everything the interface can display. Any report may be nil: the matching view then
// says how to fill it rather than showing an empty screen.
type Data struct {
	Estate     *result.EstateReport
	Why        *result.WhyReport
	Impact     *result.ImpactReport
	Source     string // "fixtures: estate" or "kubectl, read only"
	Live       bool
	Start      View
	SnapshotAt time.Time
	Actions    *Actions
	// RefreshEvery makes a live cockpit reload itself on a timer, with a countdown in the
	// header and r to reload now. Zero, and the default for recorded data, means never.
	RefreshEvery time.Duration
}

// Loader produces the data while reporting what it is doing. Every progress call becomes one
// line of the reading screen the moment it happens, so on twenty clusters the operator watches
// real progress rather than a decoration.
type Loader func(progress func(what, note string)) (Data, error)

// Step is one line of the reading screen.
type Step struct {
	What string
	Note string
	At   time.Duration // when it appeared, on the interface clock
	Done bool
}

// rowAction is what the w and i keys do on the selected row.
type rowAction struct {
	kind      string // "why" or "impact"
	context   string
	namespace string
	name      string
	selector  string
}

// row is one selectable thing on screen. Views build rows, the app draws them, so selection,
// reveal, the evidence panel and the drill down keys behave identically everywhere.
type row struct {
	render     func(t Theme, selected bool) string
	title      string
	ref        string
	sev        result.Severity
	detail     []string
	selectable bool
	act        *rowAction
}

func staticRow(s string) row {
	return row{render: func(Theme, bool) string { return s }}
}

func spacer() row { return staticRow("") }

// messages that cross from goroutines into the interface; tickMsg lives in anim.go
type (
	progressMsg Step
	loadedMsg   struct{ data Data }
	failedMsg   struct{ err error }
	whyDoneMsg  struct {
		rep *result.WhyReport
		err error
	}
	impactDoneMsg struct {
		rep *result.ImpactReport
		err error
	}
)

type app struct {
	t    Theme
	data Data
	view View

	// the clock is injected so a recording, or a test, can ask for any instant
	now     func() time.Time
	started time.Time
	entered time.Time // when the current view was entered, drives the reveal
	picked  time.Time // when the selection last moved, drives the pulse

	cursor map[View]int
	w, h   int

	// loading
	loader   Loader
	events   chan tea.Msg
	loading  bool
	loaded   bool
	loadedAt time.Duration // on the interface clock, when the data arrived
	steps    []Step
	loadErr  error

	// in flight analysis started from a row
	busy      string
	busySince time.Time

	// live refresh
	refreshing bool
	lastLoad   time.Time

	// transient message above the footer
	toast   string
	toastAt time.Time

	detail   bool
	detailAt int
	help     bool
	expanded bool
	coach    bool

	filtering bool
	filter    string
}

// Run opens the interface. With a loader, the reading screen shows real progress and the views
// arrive when the data does. With a nil loader, the data must already be in d.
func Run(d Data, l Loader) error {
	m := newModel(d, 96, 30)
	m.loader = l
	if l != nil {
		m.events = make(chan tea.Msg, 64)
		m.loading, m.loaded = true, false
		m.steps = nil
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

func newModel(d Data, w, h int) app {
	m := app{
		t:      NewTheme(w, true),
		data:   d,
		view:   d.Start,
		now:    time.Now,
		cursor: map[View]int{},
		w:      w,
		h:      h,
		coach:  true,
	}
	// Without a loader the data is already here: the reading screen still plays once, from a
	// static list, because it is the fastest explanation of what the tool touches.
	m.steps = staticSteps(d)
	m.loading, m.loaded = true, true
	if !m.enabled(m.view) && m.anyEnabled() {
		m.view = m.firstEnabled()
	}
	return m
}

// staticSteps names every read the data implies, in order, for a run with no live loader.
func staticSteps(d Data) []Step {
	var s []Step
	add := func(what, note string) {
		s = append(s, Step{What: what, Note: note, At: time.Duration(len(s)) * stepEvery, Done: true})
	}
	if d.Live {
		add("kube contexts", "read from your kubeconfig")
	} else {
		add("recorded scenario", "no cluster is contacted")
	}
	add("nodes and pools", "labels, zones, allocatable")
	add("workloads and pods", "replicas, restarts, readiness")
	add("disruption budgets", "what a drain would block")
	add("volumes", "which ones are pinned to a zone")
	if d.Estate != nil && len(d.Estate.States) > 0 {
		add("terraform state", "who owns each node pool")
	}
	if d.Impact != nil && d.Impact.PlanSummary != "" {
		add("terraform plan", d.Impact.PlanSummary)
	}
	return s
}

// closingStep is the last line of every reading screen, the one promise worth repeating.
var closingStep = Step{What: "secrets", Note: "never read, by construction", Done: true}

func (m app) Init() tea.Cmd {
	if m.loader != nil {
		return tea.Batch(tick(), m.startLoad(), waitFor(m.events))
	}
	return tick()
}

// startLoad runs the loader in its own goroutine and streams its progress into the program.
func (m app) startLoad() tea.Cmd {
	l, ch := m.loader, m.events
	return func() tea.Msg {
		go func() {
			d, err := l(func(what, note string) { ch <- progressMsg(Step{What: what, Note: note}) })
			if err != nil {
				ch <- failedMsg{err}
				return
			}
			ch <- loadedMsg{d}
		}()
		return nil
	}
}

func waitFor(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func (m app) since(t time.Time) time.Duration {
	if t.IsZero() {
		return 0
	}
	return m.now().Sub(t)
}

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

func (m app) anyEnabled() bool {
	return m.enabled(ViewOverview) || m.enabled(ViewIncident) || m.enabled(ViewImpact)
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
		return incidentRows(m.t, m.data.Why, m.expanded, m.wide() && !m.expanded)
	case ViewImpact:
		return impactRows(m.t, m.data.Impact, m.expanded, m.wide() && !m.expanded)
	default:
		return overviewRows(m.t, m.data.Estate, m.expanded, m.data.Actions != nil)
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

// selected returns the row under the cursor, or nil when nothing selectable is there.
func (m app) selected() *row {
	rows := m.filtered()
	c := m.cursor[m.view]
	if c < 0 || c >= len(rows) || !rows[c].selectable {
		return nil
	}
	r := rows[c]
	return &r
}

// settle moves the cursor onto a selectable row when the list under it changed.
func (m *app) settle() {
	rows := m.filtered()
	idx := selectableIndexes(rows)
	if len(idx) == 0 {
		m.cursor[m.view] = 0
		return
	}
	c := m.cursor[m.view]
	for _, v := range idx {
		if v == c {
			return
		}
	}
	m.cursor[m.view] = idx[0]
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
	if idx[pos] != cur {
		m.picked = m.now()
	}
	m.cursor[m.view] = idx[pos]
}

func (m *app) goTo(v View) {
	if !m.enabled(v) || v == m.view {
		return
	}
	m.view = v
	m.entered = m.now()
	m.detail = false
	m.settle()
}

func (m *app) switchView(delta int) {
	order := []View{ViewOverview, ViewIncident, ViewImpact}
	cur := int(m.view)
	for i := 0; i < len(order); i++ {
		cur = (cur + delta + len(order)) % len(order)
		if m.enabled(View(cur)) {
			m.goTo(View(cur))
			return
		}
	}
}

func (m *app) say(s string) {
	m.toast, m.toastAt = s, m.now()
}

// loadedHold keeps the finished reading list on screen long enough to read its last line, the
// one that says secrets are never read, before the views arrive.
const loadedHold = 700 * time.Millisecond

// loadingDone reports when the reading screen may leave: the data is here, every step has been
// on screen long enough to be read, and the finished list has been held for a beat.
func (m app) loadingDone(since time.Duration) bool {
	if !m.loaded {
		return false
	}
	end := m.loadedAt + loadedHold
	if len(m.steps) > 0 {
		if e := m.steps[len(m.steps)-1].At + stepEvery + loadedHold; e > end {
			end = e
		}
	}
	if e := time.Duration(len(m.steps)+1) * stepEvery; e > end {
		end = e
	}
	return since >= end
}

// runAction starts the analysis the selected row offers, and reports it when it lands.
func (m *app) runAction(kind string) tea.Cmd {
	r := m.selected()
	if r == nil || r.act == nil || r.act.kind != kind || m.data.Actions == nil || m.busy != "" {
		return nil
	}
	a := *r.act
	acts := m.data.Actions
	switch kind {
	case "why":
		if acts.Why == nil {
			return nil
		}
		m.busy, m.busySince = fmt.Sprintf("reading %s in %s", a.name, a.context), m.now()
		return func() tea.Msg {
			rep, err := acts.Why(a.context, a.namespace, a.name)
			return whyDoneMsg{rep, err}
		}
	case "impact":
		if acts.Impact == nil {
			return nil
		}
		m.busy, m.busySince = fmt.Sprintf("simulating the loss of %s in %s", a.selector, a.context), m.now()
		return func() tea.Msg {
			rep, err := acts.Impact(a.context, a.selector)
			return impactDoneMsg{rep, err}
		}
	}
	return nil
}

// refreshDue reports whether a live cockpit should reload itself now.
func (m app) refreshDue() bool {
	return m.data.Live && m.data.RefreshEvery > 0 && m.loader != nil &&
		!m.loading && !m.refreshing && m.loadErr == nil && !m.lastLoad.IsZero() &&
		m.since(m.lastLoad) >= m.data.RefreshEvery
}

// startRefresh runs the loader again while the views stay on screen.
func (m *app) startRefresh() tea.Cmd {
	m.refreshing = true
	m.busy, m.busySince = "refreshing", m.now()
	return tea.Batch(m.startLoad(), waitFor(m.events))
}

// applyRefresh swaps in reloaded data and says what changed, in one line.
func (m *app) applyRefresh(d Data) {
	before := countFindings(m.data.Estate)
	if d.Estate != nil {
		m.data.Estate = d.Estate
	}
	if d.Why != nil {
		m.data.Why = d.Why
	}
	if d.Impact != nil {
		m.data.Impact = d.Impact
	}
	if !d.SnapshotAt.IsZero() {
		m.data.SnapshotAt = d.SnapshotAt
	}
	after := countFindings(m.data.Estate)
	m.refreshing, m.busy = false, ""
	m.lastLoad = m.now()
	m.settle()
	switch {
	case after > before:
		m.say(fmt.Sprintf("refreshed: %d findings, %d new", after, after-before))
	case after < before:
		m.say(fmt.Sprintf("refreshed: %d findings, %d fewer", after, before-after))
	default:
		m.say(fmt.Sprintf("refreshed: %d findings, no change", after))
	}
}

func countFindings(r *result.EstateReport) int {
	if r == nil {
		return 0
	}
	n := 0
	for _, f := range r.Risks {
		if f.Severity != result.Info {
			n++
		}
	}
	return n
}

// export writes the current view as markdown next to the working directory, for a ticket.
func (m *app) export() {
	var text, name string
	switch m.view {
	case ViewIncident:
		if m.data.Why == nil {
			return
		}
		text, name = render.WhyMarkdown(m.data.Why), "incident"
	case ViewImpact:
		if m.data.Impact == nil {
			return
		}
		text, name = render.ImpactMarkdown(m.data.Impact), "impact"
	default:
		if m.data.Estate == nil {
			return
		}
		text, name = render.EstateMarkdown(m.data.Estate), "overview"
	}
	path := fmt.Sprintf("spanline-%s-%s.md", name, m.now().Format("20060102-1504"))
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		m.say("export failed: " + err.Error())
		return
	}
	m.say("exported to " + path)
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
			m.started, m.entered = now, now
		}
		if m.loading && m.loadingDone(m.since(m.started)) {
			m.loading = false
			m.entered = now
			m.settle()
		}
		if m.coach && m.since(m.started) > 8*time.Second {
			m.coach = false
		}
		if m.toast != "" && m.since(m.toastAt) > 3500*time.Millisecond {
			m.toast = ""
		}
		if m.refreshDue() {
			return m, tea.Batch(tick(), m.startRefresh())
		}
		return m, tick()

	case progressMsg:
		if m.refreshing {
			// a reload keeps the views on screen; its progress is not a new reading screen
			return m, waitFor(m.events)
		}
		s := Step(msg)
		s.At = m.since(m.started)
		for i := range m.steps {
			m.steps[i].Done = true
		}
		m.steps = append(m.steps, s)
		return m, waitFor(m.events)

	case loadedMsg:
		if m.refreshing {
			m.applyRefresh(msg.data)
			return m, nil
		}
		acts := m.data.Actions
		refresh := m.data.RefreshEvery
		m.data = msg.data
		if m.data.Actions == nil {
			m.data.Actions = acts
		}
		if m.data.RefreshEvery == 0 {
			m.data.RefreshEvery = refresh
		}
		for i := range m.steps {
			m.steps[i].Done = true
		}
		m.loaded = true
		m.loadedAt = m.since(m.started)
		m.lastLoad = m.now()
		if !m.enabled(m.view) && m.anyEnabled() {
			m.view = m.firstEnabled()
		}
		return m, nil

	case failedMsg:
		if m.refreshing {
			m.refreshing, m.busy = false, ""
			m.lastLoad = m.now()
			m.say("refresh failed: " + msg.err.Error())
			return m, nil
		}
		m.loadErr = msg.err
		m.loaded, m.loading = true, false
		return m, nil

	case whyDoneMsg:
		m.busy = ""
		if msg.err != nil {
			m.say("incident: " + msg.err.Error())
			return m, nil
		}
		m.data.Why = msg.rep
		m.view, m.entered, m.detail = ViewIncident, m.now(), false
		m.cursor[ViewIncident] = 0
		m.settle()
		m.say("incident loaded, tab goes back to the overview")
		return m, nil

	case impactDoneMsg:
		m.busy = ""
		if msg.err != nil {
			m.say("impact: " + msg.err.Error())
			return m, nil
		}
		m.data.Impact = msg.rep
		m.view, m.entered, m.detail = ViewImpact, m.now(), false
		m.cursor[ViewImpact] = 0
		m.settle()
		m.say("impact loaded, tab goes back to the overview")
		return m, nil

	case tea.MouseMsg:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if m.detail {
				if m.detailAt > 0 {
					m.detailAt--
				}
			} else {
				m.move(-1)
			}
		case tea.MouseButtonWheelDown:
			if m.detail {
				m.detailAt++
			} else {
				m.move(1)
			}
		}
		return m, nil

	case tea.KeyMsg:
		m.coach = false
		if m.loading || m.loadErr != nil {
			if k := msg.String(); k == "q" || k == "ctrl+c" {
				return m, tea.Quit
			}
			return m, nil
		}
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
			m.settle()
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
				m.settle()
			}
		case "enter", "right", "l":
			if !m.help {
				if r := m.selected(); r != nil && len(r.detail) > 0 {
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
			m.goTo(ViewOverview)
		case "2":
			m.goTo(ViewIncident)
		case "3":
			m.goTo(ViewImpact)
		case "d":
			m.expanded = !m.expanded
			m.entered = m.now()
			m.settle()
		case "/":
			m.filtering, m.filter = true, ""
		case "g":
			m.cursor[m.view] = 0
			m.settle()
		case "e":
			m.export()
		case "r":
			if m.data.Live && m.loader != nil && !m.refreshing {
				return m, m.startRefresh()
			}
		case "w":
			if cmd := m.runAction("why"); cmd != nil {
				return m, cmd
			}
		case "i":
			if cmd := m.runAction("impact"); cmd != nil {
				return m, cmd
			}
		}
		return m, nil
	}
	return m, nil
}

func (m app) View() string {
	return m.frame(m.since(m.started), m.since(m.entered))
}

// frame draws everything. It takes the two clocks explicitly so a test, or a recording, can
// ask for any moment of any animation and get the same bytes every time.
func (m app) frame(sinceStart, sinceView time.Duration) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(m.header(sinceStart))
	b.WriteString("\n\n")

	switch {
	case m.loadErr != nil:
		b.WriteString(m.errorView())
		return b.String()
	case m.loading:
		b.WriteString(m.collectView(sinceStart))
		return b.String()
	case m.help:
		b.WriteString(m.helpView())
		return b.String()
	case m.detail:
		b.WriteString(m.detailView())
		return b.String()
	}

	b.WriteString(m.tabs())
	b.WriteString("\n\n")

	if !m.enabled(m.view) {
		b.WriteString(m.emptyView())
		return b.String()
	}

	b.WriteString(m.headline())
	b.WriteString("\n\n")

	// the stat tiles count up and fill in as the view arrives
	used := 8 // header, tabs, headline, and their blank lines
	if m.tall() && !m.expanded {
		if tiles := m.t.Tiles(m.tiles(sinceView), m.t.Inner(), ease(sinceView, tileFill)); tiles != "" {
			b.WriteString(indentBlock(tiles, "  "))
			b.WriteString("\n")
			used += 4
		}
	}

	rows := m.filtered()
	footerLines := 3
	if m.view == ViewImpact {
		footerLines++
	}
	bodyH := m.h - used - footerLines
	if bodyH < 6 {
		bodyH = 6
	}
	if m.expanded {
		bodyH = 10000
	}

	listW := m.t.Inner()
	sideW := 0
	if m.wide() && !m.expanded {
		listW = m.t.Inner() * 58 / 100
		sideW = m.t.Inner() - listW - 2
	}
	list := m.renderList(rows, listW, bodyH, sinceView)
	if sideW > 0 {
		title, lines := m.sideLines(sideW - 4)
		panel := m.t.Panel(title, lines, sideW, bodyH)
		if sinceView < time.Duration(len(rows))*revealStep/2+panelFade {
			panel = m.t.Faint(stripANSI(panel))
		}
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, blockOf(list, listW, bodyH), "  ", panel))
	} else {
		b.WriteString(strings.Join(list, "\n"))
	}

	b.WriteString("\n\n")
	b.WriteString(m.statusLine(sinceStart))
	b.WriteString(m.footer())
	return b.String()
}

// renderList draws the interactive rows into a column of the given width, with the reveal
// stagger and the selection pulse applied, and a hint when the column cannot hold everything.
func (m app) renderList(rows []row, width, budget int, sinceView time.Duration) []string {
	th := NewTheme(width+4, m.t.Color)
	cursor := m.cursor[m.view]
	pulse := pulsing(m.since(m.picked))
	var lines []string
	shown := 0
	for i, r := range rows {
		state := reveal(i, sinceView)
		if state == revealHidden {
			break
		}
		sel := r.selectable && i == cursor
		rt := th
		rt.Pulse = sel && pulse
		line := r.render(rt, sel)
		if state == revealDim {
			line = th.Faint(stripANSI(line))
		}
		n := strings.Count(line, "\n") + 1
		last := i == len(rows)-1
		// the hint that rows are hidden takes the last line itself, so it is never pushed off
		if shown+n > budget || (!last && shown+n > budget-1) {
			left := len(rows) - i
			more := fmt.Sprintf("%d more below, press d to expand or / to filter", left)
			if th.Narrow() {
				more = fmt.Sprintf("%d more, press d", left)
			}
			lines = append(lines, "  "+th.Faint(cut(more, width)))
			break
		}
		lines = append(lines, strings.Split(line, "\n")...)
		shown += n
	}
	return lines
}

// blockOf pads a column to an exact width and height so it sits cleanly next to a panel.
func blockOf(lines []string, width, height int) string {
	out := make([]string, 0, height)
	for i := 0; i < height && i < len(lines); i++ {
		out = append(out, fit(lines[i], width))
	}
	for len(out) < height {
		out = append(out, strings.Repeat(" ", width))
	}
	return strings.Join(out, "\n")
}

// indentBlock prefixes every line of a block, so a multi line box lands inside the gutter.
func indentBlock(s, prefix string) string {
	parts := strings.Split(s, "\n")
	for i := range parts {
		parts[i] = prefix + parts[i]
	}
	return strings.Join(parts, "\n")
}

// statusLine is the one line above the footer: a filter being typed, work in flight, a toast,
// or the coach mark. At most one of them, in that order of urgency.
func (m app) statusLine(sinceStart time.Duration) string {
	switch {
	case m.filtering || m.filter != "":
		return "  " + m.t.Accent("filter: ") + m.filter + m.t.Faint("  esc clears") + "\n"
	case m.busy != "":
		return "  " + m.t.Accent(spinner(m.since(m.busySince))) + " " + m.t.Muted(m.busy) + "\n"
	case m.toast != "":
		return "  " + m.t.Accent("✓ ") + m.t.Muted(cut(m.toast, m.t.Inner()-4)) + "\n"
	case m.coach:
		return "  " + m.t.Faint("move with up and down, press right for the evidence behind a line, tab to change view") + "\n"
	}
	return ""
}

func (m app) header(sinceStart time.Duration) string {
	name := m.t.Accent("spanline")
	mode := m.t.Muted("recorded")
	if m.data.Live {
		mode = m.t.paint(colOK, "live, read only")
	}
	age := ""
	if !m.data.SnapshotAt.IsZero() {
		if m.data.Live {
			age = m.t.Faint("  ·  snapshot " + humanAge(m.now().Sub(m.data.SnapshotAt)) + " ago")
		} else {
			age = m.t.Faint("  ·  " + m.data.SnapshotAt.Format("2006-01-02 15:04"))
		}
	}
	if m.data.Live && m.data.RefreshEvery > 0 && m.loader != nil && !m.loading {
		switch {
		case m.refreshing:
			age += m.t.Faint("  ·  ") + m.t.Accent(spinner(m.since(m.busySince))) + m.t.Faint(" refreshing")
		case !m.lastLoad.IsZero():
			left := m.data.RefreshEvery - m.since(m.lastLoad)
			if left < 0 {
				left = 0
			}
			age += m.t.Faint(fmt.Sprintf("  ·  refresh in %ds, r for now", int(left.Seconds()+0.5)))
		}
	}
	left := "  " + name + "  " + mode + age
	right := m.t.Faint(m.data.Source)
	gap := m.t.Inner() - lipgloss.Width(left) - lipgloss.Width(right) + 2
	if gap < 2 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// tabs shows the three views with a count each, so the switcher is also a summary.
func (m app) tabs() string {
	counts := []string{"", "", ""}
	if m.data.Estate != nil {
		n := 0
		for _, f := range m.data.Estate.Risks {
			if f.Severity != result.Info {
				n++
			}
		}
		counts[0] = fmt.Sprint(n)
	}
	if m.data.Why != nil {
		counts[1] = fmt.Sprint(len(m.data.Why.Suspects))
	}
	if m.data.Impact != nil {
		counts[2] = fmt.Sprint(len(m.data.Impact.Findings))
	}
	names := make([]string, 3)
	for i, n := range viewNames {
		names[i] = n
		if counts[i] != "" {
			names[i] = n + " " + counts[i]
		}
	}
	return m.t.Tabs(names, int(m.view), []bool{
		m.enabled(ViewOverview), m.enabled(ViewIncident), m.enabled(ViewImpact),
	})
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
	hints := []Hint{{"↑↓", "move"}, {"→", "evidence"}, {"tab", "view"}}
	if r := m.selected(); r != nil && r.act != nil && m.data.Actions != nil {
		switch r.act.kind {
		case "why":
			hints = append(hints, Hint{"w", "why"})
		case "impact":
			hints = append(hints, Hint{"i", "impact"})
		}
	}
	hints = append(hints, Hint{"d", "expand"}, Hint{"/", "filter"}, Hint{"e", "export"}, Hint{"?", "keys"}, Hint{"q", "quit"})
	if m.detail {
		hints = []Hint{{"↑↓", "scroll"}, {"esc", "back"}, {"q", "quit"}}
	}
	if m.t.Narrow() && len(hints) > 4 {
		// trimming the footer must never remove the way out
		hints = append(append([]Hint{}, hints[:3]...), Hint{"q", "quit"})
	}
	line := m.t.Footer(hints)
	if m.view == ViewImpact && m.data.Impact != nil && !m.detail {
		v := m.data.Impact
		line = "  " + m.t.Chip(v.Verdict) + m.t.Muted(fmt.Sprintf("   exit code %d", v.ExitCode)) + "\n" + line
	}
	return line
}

func (m app) collectView(since time.Duration) string {
	var b strings.Builder
	b.WriteString("  " + m.t.Muted("reading") + "\n\n")

	// the note column starts after the longest visible step name, so nothing runs together
	labelW := 12
	visible := 0
	for _, s := range m.steps {
		if since < s.At {
			continue
		}
		visible++
		if w := lipgloss.Width(s.What) + 2; w > labelW {
			labelW = w
		}
	}
	if labelW > 48 {
		labelW = 48
	}
	noteW := m.t.Inner() - labelW - 4
	if noteW < 8 {
		noteW = 8
	}
	line := func(mark, what, note string) {
		b.WriteString("  " + mark + " " + m.t.paint(colText, pad(cut(what, labelW-2), labelW)) + m.t.Faint(cut(note, noteW)) + "\n")
	}

	for _, s := range m.steps {
		if since < s.At {
			continue
		}
		// with a live loader a step is done when the loader says so; on a replay, after a beat
		done := s.Done
		if m.loader == nil {
			done = since >= s.At+stepEvery
		}
		if done {
			line(m.t.paint(colOK, "✓"), s.What, s.Note)
		} else {
			line(m.t.Accent(spinner(since)), s.What, s.Note)
		}
	}
	if !m.loaded && len(m.steps) == 0 {
		line(m.t.Accent(spinner(since)), "waiting for the source", "")
	}
	if m.loaded && visible == len(m.steps) {
		line(m.t.paint(colOK, "✓"), closingStep.What, closingStep.Note)
	}
	b.WriteString("\n  " + m.t.Faint("q quits at any time") + "\n")
	return b.String()
}

func (m app) errorView() string {
	var b strings.Builder
	b.WriteString("  " + m.t.Chip(result.NotAssessed) + "  " + m.t.bold(colText, "nothing could be read") + "\n\n")
	b.WriteString("  " + m.t.paint(colMuted, wrapIndent(m.loadErr.Error(), m.t.Inner()-2, "  ")) + "\n\n")
	b.WriteString("  " + m.t.Faint("to see the tool on recorded data: ") + m.t.Accent("spanline demo") + "\n\n")
	b.WriteString(m.t.Footer([]Hint{{"q", "quit"}}))
	return b.String()
}

func (m app) emptyView() string {
	var what, how string
	switch m.view {
	case ViewIncident:
		what, how = "no incident is loaded", "select a workload in the overview and press w, or run: spanline why deploy/checkout -n payments"
	case ViewImpact:
		what, how = "no change is loaded", "select a node pool in the overview and press i, or run: spanline impact --nodes pool=apps"
	default:
		what, how = "no cluster was read", "run: spanline cockpit"
	}
	return "  " + m.t.Muted(what) + "\n  " + m.t.Faint(wrapIndent(how, m.t.Inner()-2, "  ")) + "\n\n" + m.footer()
}

func (m app) detailView() string {
	r := m.selected()
	if r == nil {
		return m.footer()
	}
	var b strings.Builder
	b.WriteString("  " + m.t.Chip(r.sev) + "  " + m.t.bold(colText, cut(r.title, m.t.Inner()-16)) + "\n")
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
		{"↑ ↓ or j k", "move through the list, the mouse wheel works too"},
		{"→ or enter", "open the evidence behind the selected line"},
		{"← or esc", "go back, close the panel, clear the filter"},
		{"tab", "next view, shift tab for the previous one"},
		{"1 2 3", "jump straight to overview, incident or impact"},
		{"w", "on a workload: explain why its pods fail, right here"},
		{"i", "on a node pool: simulate losing it, right here"},
		{"d", "expand: show every row and every detail inline"},
		{"/", "filter the list, enter keeps it, esc clears it"},
		{"e", "export this view as markdown, for a ticket"},
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

// wrapIndent wraps a line and keeps its own leading indentation on every piece, so an
// indented evidence line stays indented when it is too long for the panel.
func wrapIndent(s string, width int, indent string) string {
	body := strings.TrimLeft(s, " ")
	lead := s[:len(s)-len(body)]
	n := width - len(lead)
	if n < 10 {
		n = 10
	}
	parts := strings.Split(wrapTo(body, n), "\n")
	for i := range parts {
		if i == 0 {
			parts[i] = lead + parts[i]
		} else {
			parts[i] = lead + indent + parts[i]
		}
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
