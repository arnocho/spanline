package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/arnocho/spanline/internal/result"
	"github.com/charmbracelet/lipgloss"
)

// The dashboard layout: a row of stat tiles under the headline, then the interactive list on
// the left and a context panel on the right. Narrow terminals fall back to the single column.
// Tiles count up and meters fill in when a view arrives; everything still lands on the same
// still frame, so a screenshot never shows a half drawn number.

const (
	wideFrom  = 104                    // columns from which the two column layout is used
	tallFrom  = 22                     // rows from which the tiles are shown
	tileFill  = 520 * time.Millisecond // how long a tile takes to count up and fill
	panelFade = 260 * time.Millisecond // how long the side panel stays dim after arriving
)

// ease maps elapsed time to a 0..1 progress with a soft landing.
func ease(elapsed, over time.Duration) float64 {
	if over <= 0 || elapsed >= over {
		return 1
	}
	if elapsed <= 0 {
		return 0
	}
	x := float64(elapsed) / float64(over)
	return 1 - (1-x)*(1-x)
}

// countUp is the number a tile shows while it animates toward n.
func countUp(n int, progress float64) int {
	v := int(float64(n)*progress + 0.5)
	if v > n {
		v = n
	}
	if v < 0 {
		v = 0
	}
	return v
}

// fit makes a styled line exactly w cells wide: padded, or cut back to plain text when too long.
func fit(line string, w int) string {
	if lw := lipgloss.Width(line); lw <= w {
		return line + strings.Repeat(" ", w-lw)
	}
	return pad(cut(stripANSI(line), w), w)
}

// Panel draws a bordered box with its title on the top edge, exactly width by height cells.
func (t Theme) Panel(title string, lines []string, width, height int) string {
	if width < 14 {
		width = 14
	}
	if height < 3 {
		height = 3
	}
	inner := width - 4
	title = cut(title, inner-2)
	dashes := width - 5 - lipgloss.Width(title)
	if dashes < 0 {
		dashes = 0
	}
	var b strings.Builder
	b.WriteString(t.paint(colLine, "╭─ ") + t.Muted(title) + t.paint(colLine, " "+strings.Repeat("─", dashes)+"╮") + "\n")
	for i := 0; i < height-2; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		b.WriteString(t.paint(colLine, "│ ") + fit(line, inner) + t.paint(colLine, " │") + "\n")
	}
	b.WriteString(t.paint(colLine, "╰"+strings.Repeat("─", width-2)+"╯"))
	return b.String()
}

// tile is one stat box: a label on the border, a value with an optional meter, a note.
type tile struct {
	label string
	value string
	note  string
	sev   result.Severity
	frac  float64 // meter fill, negative for no meter
}

// Tiles renders a row of equal tiles across width, with the animation progress applied.
func (t Theme) Tiles(tiles []tile, width int, progress float64) string {
	if len(tiles) == 0 {
		return ""
	}
	gap := 1
	tw := (width - gap*(len(tiles)-1)) / len(tiles)
	if tw < 16 {
		return ""
	}
	var boxes []string
	for _, x := range tiles {
		inner := tw - 4
		value := t.bold(sevColor(x.sev), x.value)
		if x.sev == "" || x.sev == result.Info {
			value = t.bold(colText, x.value)
		}
		if n, err := strconv.Atoi(x.value); err == nil {
			shown := fmt.Sprint(countUp(n, progress))
			value = t.bold(sevColor(x.sev), shown)
			if x.sev == "" || x.sev == result.Info {
				value = t.bold(colText, shown)
			}
		}
		line1 := value
		if x.frac >= 0 {
			mw := inner - lipgloss.Width(x.value) - 2
			if mw >= 4 {
				if mw > 14 {
					mw = 14
				}
				line1 += "  " + t.Meter(x.frac*progress, mw, x.sev)
			}
		}
		lines := []string{line1, t.Faint(cut(x.note, inner))}
		boxes = append(boxes, t.Panel(x.label, lines, tw, 4))
	}
	sep := strings.Repeat(" ", gap)
	return lipgloss.JoinHorizontal(lipgloss.Top, interleave(boxes, sep)...)
}

func interleave(parts []string, sep string) []string {
	var out []string
	for i, p := range parts {
		if i > 0 {
			out = append(out, sep)
		}
		out = append(out, p)
	}
	return out
}

// wide reports whether the dashboard layout applies at this size.
func (m app) wide() bool { return m.t.Width >= wideFrom && m.h >= 16 }

// tall reports whether there is room for the tile row.
func (m app) tall() bool { return m.h >= tallFrom }

// tiles builds the stat row for the current view.
func (m app) tiles(sinceView time.Duration) []tile {
	switch m.view {
	case ViewIncident:
		return incidentTiles(m.data.Why)
	case ViewImpact:
		return impactTiles(m.data.Impact)
	default:
		return overviewTiles(m.data.Estate)
	}
}

// sideLines builds the context panel for the current view.
func (m app) sideLines(w int) (string, []string) {
	switch m.view {
	case ViewIncident:
		return incidentSide(m.t, m.data.Why, w)
	case ViewImpact:
		return impactSide(m.t, m.data.Impact, w)
	default:
		return overviewSide(m.t, m.data, w)
	}
}

func overviewTiles(r *result.EstateReport) []tile {
	if r == nil {
		return nil
	}
	outages, risks, unowned, notReady := 0, 0, 0, 0
	for _, f := range r.Risks {
		switch f.Severity {
		case result.Outage:
			outages++
		case result.Disruption, result.Risk:
			risks++
		}
	}
	for _, p := range r.Pools {
		if p.TerraformAddress == "" {
			unowned++
		}
	}
	for _, c := range r.Clusters {
		notReady += c.NodesNotReady
	}
	nodes := 0
	for _, c := range r.Clusters {
		nodes += c.Nodes
	}
	nodeNote := fmt.Sprintf("%d %s", nodes, plural(nodes, "node", "nodes"))
	if notReady > 0 {
		nodeNote += fmt.Sprintf(", %d not ready", notReady)
	}
	ownedFrac := 0.0
	if len(r.Pools) > 0 {
		ownedFrac = float64(len(r.Pools)-unowned) / float64(len(r.Pools))
	}
	outSev := result.Info
	if outages > 0 {
		outSev = result.Outage
	}
	riskSev := result.Info
	if risks > 0 {
		riskSev = result.Risk
	}
	return []tile{
		{label: "clusters", value: fmt.Sprint(len(r.Clusters)), note: nodeNote, frac: -1},
		{label: "node pools", value: fmt.Sprint(len(r.Pools)), note: fmt.Sprintf("%d owned by terraform", len(r.Pools)-unowned), frac: ownedFrac},
		{label: "outages", value: fmt.Sprint(outages), note: "serving nothing", sev: outSev, frac: -1},
		{label: "risks", value: fmt.Sprint(risks), note: "one node loss away", sev: riskSev, frac: -1},
	}
}

func overviewSide(t Theme, d Data, w int) (string, []string) {
	r := d.Estate
	if r == nil {
		return "context", nil
	}
	counts := map[result.Severity]int{}
	for _, f := range r.Risks {
		counts[f.Severity]++
	}
	max := 1
	for _, n := range counts {
		if n > max {
			max = n
		}
	}
	var lines []string
	lines = append(lines, t.Muted("findings"))
	for _, s := range []result.Severity{result.Outage, result.Disruption, result.Risk, result.NotAssessed, result.Info} {
		n := counts[s]
		if n == 0 {
			continue
		}
		bar := t.paint(sevColor(s), strings.Repeat("▮", n*8/max))
		lines = append(lines, fmt.Sprintf("  %s %s %s", t.paint(sevColor(s), pad(strings.ToLower(string(s)), 12)), pad(fmt.Sprint(n), 3), bar))
	}
	lines = append(lines, "", t.Muted("terraform"))
	owned := 0
	for _, p := range r.Pools {
		if p.TerraformAddress != "" {
			owned++
		}
	}
	lines = append(lines, fmt.Sprintf("  %d of %d pools owned", owned, len(r.Pools)))
	if len(r.States) == 0 {
		lines = append(lines, t.Faint("  no state file read"))
	}
	for _, s := range r.States {
		lines = append(lines, t.Faint(fmt.Sprintf("  %s: %d resources", cut(s.Path, w-22), s.Resources)))
	}
	lines = append(lines, "", t.Muted("coverage"))
	if len(r.Gaps) == 0 {
		lines = append(lines, t.Faint("  nothing was left unread"))
	} else {
		lines = append(lines, t.paint(sevColor(result.NotAssessed), fmt.Sprintf("  %d %s, press d", len(r.Gaps), plural(len(r.Gaps), "gap", "gaps"))))
	}
	return "context", lines
}

func incidentTiles(r *result.WhyReport) []tile {
	if r == nil {
		return nil
	}
	total := r.Failing.Count + r.Healthy.Count
	frac := 0.0
	if total > 0 {
		frac = float64(r.Failing.Count) / float64(total)
	}
	sep, sepNote := "none", "no attribute tells them apart"
	for _, d := range r.Dimensions {
		if d.Separation == result.Total {
			sep, sepNote = d.Name, "total separation"
			break
		}
		if d.Separation == result.Partial && sep == "none" {
			sep, sepNote = d.Name, "partial separation"
		}
	}
	started, startedNote := "unknown", "no onset signal retained"
	if !r.OnsetAt.IsZero() {
		started = r.OnsetAt.UTC().Format("15:04")
		startedNote = brief_onset(r.OnsetSignal)
	}
	failSev := result.Disruption
	if r.Healthy.Count == 0 {
		failSev = result.Outage
	}
	return []tile{
		{label: "failing", value: fmt.Sprintf("%d of %d", r.Failing.Count, total), note: "pods failing", sev: failSev, frac: frac},
		{label: "healthy", value: fmt.Sprint(r.Healthy.Count), note: "pods holding", sev: result.Info, frac: -1},
		{label: "started", value: started, note: startedNote, frac: -1},
		{label: "separated by", value: sep, note: sepNote, sev: result.Disruption, frac: -1},
	}
}

// brief_onset names the onset signal in the few words a tile can hold.
func brief_onset(signal string) string {
	switch {
	case strings.Contains(signal, "OOMKilled"):
		return "killed for memory"
	case strings.Contains(signal, "Unhealthy"):
		return "readiness failed"
	case strings.Contains(signal, "Ready"):
		return "reported not ready"
	case signal == "":
		return ""
	default:
		return "first failure signal"
	}
}

// shortChange compresses a change title into the few words a timeline entry can hold. The
// most specific shapes are matched first, so an Argo sync is never called a revision.
func shortChange(s result.Suspect) string {
	t := s.Title
	switch {
	case strings.HasPrefix(t, "Argo CD application "):
		rest := strings.TrimPrefix(t, "Argo CD application ")
		if f := strings.Fields(rest); len(f) > 0 {
			return "argo sync of " + f[0]
		}
	case strings.HasPrefix(t, "field manager "):
		rest := strings.TrimPrefix(t, "field manager ")
		if f := strings.Fields(rest); len(f) > 0 {
			return f[0] + " wrote to it"
		}
	case strings.Contains(t, "node(s)"):
		return "nodes joined the cluster"
	case strings.Contains(t, "revision "):
		i := strings.Index(t, "revision ")
		if f := strings.Fields(t[i+len("revision "):]); len(f) > 0 {
			return "revision " + f[0] + " rolled out"
		}
	}
	return t
}

func incidentSide(t Theme, r *result.WhyReport, w int) (string, []string) {
	if r == nil {
		return "timeline", nil
	}
	type moment struct {
		at    time.Time
		glyph string
		sev   result.Severity
		what  string
	}
	var ms []moment
	for _, s := range r.Suspects {
		if s.At.IsZero() {
			continue
		}
		sev := result.Severity("")
		switch s.Verdict {
		case result.Splits:
			sev = result.Disruption
		case result.Temporal:
			sev = result.Risk
		}
		ms = append(ms, moment{s.At, "●", sev, shortChange(s)})
	}
	if !r.OnsetAt.IsZero() {
		ms = append(ms, moment{r.OnsetAt, "✖", result.Outage, "onset"})
	}
	// oldest first, one line per distinct moment
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && ms[j].at.Before(ms[j-1].at); j-- {
			ms[j], ms[j-1] = ms[j-1], ms[j]
		}
	}
	var lines []string
	lines = append(lines, t.Muted("timeline"))
	seen := map[string]bool{}
	for _, m := range ms {
		key := m.at.Format("15:04") + m.what
		if seen[key] {
			continue
		}
		seen[key] = true
		mark := t.paint(colMuted, m.glyph) // a change that explains nothing stays neutral
		if m.sev != "" {
			mark = t.paint(sevColor(m.sev), m.glyph)
		}
		lines = append(lines, "  "+t.Faint(m.at.UTC().Format("15:04"))+"  "+mark+" "+cut(m.what, w-14))
	}
	var top *result.Suspect
	for i := range r.Suspects {
		if r.Suspects[i].Verdict == result.Splits || r.Suspects[i].Verdict == result.Temporal {
			top = &r.Suspects[i]
			break
		}
	}
	if top != nil && len(top.Diff) > 0 {
		lines = append(lines, "", t.Muted("it changed"))
		for _, d := range top.Diff {
			lines = append(lines, "  "+cut(d, w-6))
		}
	}
	if r.Mode == result.ModeRevision {
		lines = append(lines, "", t.paint(sevColor(result.NotAssessed), "  no healthy pod is left, so this"), t.paint(sevColor(result.NotAssessed), "  compares revisions, a weaker signal"))
	}
	if n := len(r.Gaps); n > 0 {
		lines = append(lines, "", t.Muted("coverage"), t.Faint(fmt.Sprintf("  %d %s, press d", n, plural(n, "gap", "gaps"))))
	}
	return "context", lines
}

func impactTiles(r *result.ImpactReport) []tile {
	if r == nil {
		return nil
	}
	counts := map[result.Severity]int{}
	for _, f := range r.Findings {
		counts[f.Severity]++
	}
	sevOrInfo := func(s result.Severity) result.Severity {
		if counts[s] == 0 {
			return result.Info
		}
		return s
	}
	return []tile{
		{label: "outages", value: fmt.Sprint(counts[result.Outage]), note: "lose every replica", sev: sevOrInfo(result.Outage), frac: -1},
		{label: "disruptions", value: fmt.Sprint(counts[result.Disruption]), note: "drain, volume, capacity", sev: sevOrInfo(result.Disruption), frac: -1},
		{label: "risks", value: fmt.Sprint(counts[result.Risk]), note: "down to one replica", sev: sevOrInfo(result.Risk), frac: -1},
		{label: "not assessed", value: fmt.Sprint(counts[result.NotAssessed]), note: "never read as a pass", sev: sevOrInfo(result.NotAssessed), frac: -1},
	}
}

func impactSide(t Theme, r *result.ImpactReport, w int) (string, []string) {
	if r == nil {
		return "context", nil
	}
	var lines []string
	if len(r.Mapping) > 0 {
		lines = append(lines, t.Muted("what the plan moves"))
		for _, ml := range r.Mapping {
			if i := strings.Index(ml, " [state"); i >= 0 {
				ml = ml[:i]
			}
			for _, key := range []string{"kubernetes.azure.com/agentpool=", "cloud.google.com/gke-nodepool=", "eks.amazonaws.com/nodegroup=", "agentpool="} {
				ml = strings.ReplaceAll(ml, "nodes "+key, "pool ")
			}
			// the address is shortened to its resource, the full form stays in the evidence, and
			// what it moves goes on its own line so neither half is cut
			if f := strings.Fields(ml); len(f) > 0 && strings.Contains(f[0], ".") {
				ml = shortAddr(f[0]) + strings.TrimPrefix(ml, f[0])
			}
			head, tail := ml, ""
			if i := strings.Index(ml, " -> "); i >= 0 {
				head, tail = ml[:i], ml[i+4:]
			}
			lines = append(lines, "  "+cut(head, w-6))
			if tail != "" {
				lines = append(lines, "    "+t.Faint(cut("→ "+tail, w-8)))
			}
		}
		lines = append(lines, "")
	}
	lines = append(lines, t.Muted(fmt.Sprintf("nodes affected  %d", len(r.Nodes))))
	shown := r.Nodes
	if len(shown) > 6 {
		shown = shown[:6]
	}
	for _, n := range shown {
		lines = append(lines, "  "+t.Faint(cut(n, w-6)))
	}
	if len(r.Nodes) > 6 {
		lines = append(lines, t.Faint(fmt.Sprintf("  and %d more", len(r.Nodes)-6)))
	}
	if !r.ExpiresAt.IsZero() {
		lines = append(lines, "", t.Muted("snapshot"), t.Faint("  valid until "+r.ExpiresAt.UTC().Format("15:04 UTC")))
	}
	if len(r.Ignored) > 0 {
		lines = append(lines, "", t.Muted("not modelled"))
		for _, ig := range r.Ignored {
			lines = append(lines, "  "+t.Faint(cut(ig, w-6)))
		}
	}
	return "context", lines
}
