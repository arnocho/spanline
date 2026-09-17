package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/arnocho/spanline/internal/brief"
	"github.com/arnocho/spanline/internal/result"
)

// Each view answers one question and shows only what supports the answer. Everything else is
// one keystroke away: right opens the evidence, d expands the whole list.

func sectionRow(t Theme, label string) row {
	return staticRow("  " + t.Muted(label))
}

func shortSev(s result.Severity) string {
	switch s {
	case result.Outage:
		return "outage"
	case result.Disruption:
		return "disrupt"
	case result.Risk:
		return "risk"
	case result.NotAssessed:
		return "unknown"
	default:
		return "info"
	}
}

func findingDetail(f result.Finding) []string {
	out := []string{f.Reason}
	if f.Context != "" {
		out = append(out, "context: "+f.Context)
	}
	if f.Kind != "" {
		out = append(out, "kind: "+f.Kind)
	}
	if len(f.Evidence) > 0 {
		out = append(out, "", "evidence")
		for _, e := range f.Evidence {
			out = append(out, "  "+e)
		}
	}
	return out
}

// overviewRows answers: where should I look first, and who owns what.
func overviewRows(t Theme, r *result.EstateReport, expanded bool, actions bool) []row {
	if r == nil {
		return nil
	}
	var rows []row

	limit := 3
	if expanded {
		limit = len(r.Risks)
	}
	prio := brief.Priorities(r, limit)
	if len(prio) > 0 {
		rows = append(rows, sectionRow(t, "look at this first"))
		byRef := map[string]result.Finding{}
		for _, f := range r.Risks {
			byRef[f.Context+"/"+f.Object] = f
		}
		for _, p := range prio {
			f := byRef[p.Ref]
			line := p
			rows = append(rows, row{
				render: func(th Theme, sel bool) string {
					return th.Card(sel, line.Sev, shortSev(line.Sev), line.Value, line.Note)
				},
				title:      line.Value,
				ref:        line.Ref,
				sev:        line.Sev,
				detail:     findingDetail(f),
				selectable: true,
				act:        workloadAction(f, actions),
			})
		}
		rows = append(rows, spacer())
	}

	rows = append(rows, sectionRow(t, "clusters"))
	for _, c := range r.Clusters {
		c := c
		risky := c.Risks
		sev := result.Info
		switch {
		case c.Outages > 0:
			sev = result.Outage
		case risky > 0:
			sev = result.Risk
		}
		rows = append(rows, row{
			render: func(th Theme, sel bool) string {
				caret := th.caret(sel)
				namePlain := pad(cut(c.Context, 18), 18)
				statsPlain := fmt.Sprintf("%2d nodes  %3d pods  %2d workloads", c.Nodes, c.Pods, c.Workloads)
				if th.Narrow() {
					statsPlain = fmt.Sprintf("%2d nodes %3d pods", c.Nodes, c.Pods)
				}
				tailPlain := "nothing fragile"
				if risky > 0 || c.Outages > 0 {
					tailPlain = fmt.Sprintf("%d to look at", risky)
				}
				if c.NodesNotReady > 0 {
					tailPlain += fmt.Sprintf(", %d not ready", c.NodesNotReady)
				}
				avail := th.Inner() - 2 - len([]rune(namePlain)) - len([]rune(statsPlain)) - 3
				if avail < 4 {
					statsPlain = cut(statsPlain, len([]rune(statsPlain))+avail-6)
					avail = 6
				}
				tailPlain = cut(tailPlain, avail)
				name := th.paint(colText, namePlain)
				if sel {
					name = th.bold(colText, namePlain)
				}
				tail := th.Faint(tailPlain)
				if risky > 0 || c.Outages > 0 || c.NodesNotReady > 0 {
					tail = th.paint(sevColor(sev), tailPlain)
				}
				return caret + name + th.Muted(statsPlain) + "   " + tail
			},
			title:      c.Context,
			ref:        c.Context,
			sev:        sev,
			selectable: true,
			detail: []string{
				fmt.Sprintf("context %s", c.Context),
				fmt.Sprintf("nodes %d, of which not ready %d", c.Nodes, c.NodesNotReady),
				fmt.Sprintf("pods %d across %d namespaces", c.Pods, c.Namespaces),
				fmt.Sprintf("workloads %d", c.Workloads),
				fmt.Sprintf("findings %d, of which outages %d", c.Risks, c.Outages),
				"collected at " + c.CollectedAt.Format("15:04:05"),
			},
		})
	}
	rows = append(rows, spacer())

	rows = append(rows, sectionRow(t, "node pools"))
	for _, p := range r.Pools {
		p := p
		owner := p.TerraformAddress
		ownSev := result.Info
		if owner == "" {
			owner = "no terraform owner found"
			ownSev = result.NotAssessed
		}
		frac := p.CPUPercent / 100
		meterSev := result.Info
		if p.CPUPercent > 85 || p.MemPercent > 85 {
			meterSev = result.Risk
		}
		rows = append(rows, row{
			render: func(th Theme, sel bool) string {
				caret := th.caret(sel)
				namePlain := pad(cut(p.Context+" "+p.Pool, 26), 26)
				nodesPlain := pad(fmt.Sprintf("%2d %s", p.Nodes, plural(p.Nodes, "node", "nodes")), 9)
				meterW := 8
				if th.Narrow() {
					meterW = 5
				}
				cpuPlain := fmt.Sprintf(" %3.0f%% cpu", p.CPUPercent)
				used := 2 + len([]rune(namePlain)) + len([]rune(nodesPlain)) + meterW + len([]rune(cpuPlain)) + 2
				ownerPlain := cut(shortAddr(owner), th.Inner()-used)
				name := th.paint(colText, namePlain)
				if sel {
					name = th.bold(colText, namePlain)
				}
				return caret + name + th.Muted(nodesPlain) + th.Meter(frac, meterW, meterSev) +
					th.Faint(cpuPlain) + th.paint(sevColor(ownSev), "  "+ownerPlain)
			},
			title:      p.Context + " " + p.Pool,
			ref:        p.Pool,
			sev:        ownSev,
			selectable: true,
			act:        poolAction(p, actions),
			detail: []string{
				fmt.Sprintf("pool %s in context %s", p.Pool, p.Context),
				fmt.Sprintf("nodes %d, zones %s", p.Nodes, orNone(p.Zones)),
				fmt.Sprintf("requests: cpu %.1f%%, memory %.1f%% of allocatable", p.CPUPercent, p.MemPercent),
				fmt.Sprintf("workloads on it %d, of which at risk %d", p.Workloads, p.AtRisk),
				"terraform: " + owner,
				"state file: " + orNone(p.StateFile),
			},
		})
	}

	if expanded {
		rows = append(rows, spacer(), sectionRow(t, "coverage"))
		if len(r.Gaps) == 0 {
			rows = append(rows, staticRow("  "+t.Faint("nothing was left unread")))
		}
		for _, g := range r.Gaps {
			rows = append(rows, staticRow("  "+t.Faint(wrapIndent(g, t.Inner()-2, "    "))))
		}
		rows = append(rows, spacer(), sectionRow(t, "terraform state"))
		for _, s := range r.States {
			rows = append(rows, staticRow("  "+t.Muted(fmt.Sprintf("%s  %d resources, %d pools, %d matched",
				cut(s.Path, 40), s.Resources, s.NodePools, s.Matched))))
		}
	}
	return rows
}

// shortAddr keeps the end of a Terraform address, which is the part that identifies the
// resource, instead of truncating the provider prefix and losing the name.
func shortAddr(a string) string {
	parts := strings.Split(a, ".")
	if len(parts) < 2 {
		return a
	}
	typ := parts[len(parts)-2]
	words := strings.Split(typ, "_")
	if len(words) > 2 {
		typ = strings.Join(words[len(words)-2:], "_")
	}
	return typ + "." + parts[len(parts)-1]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" || s == "-" {
		return "none"
	}
	return s
}

// incidentRows answers: what separates the failing pods, and which change made it.
func incidentRows(t Theme, r *result.WhyReport, expanded bool) []row {
	if r == nil {
		return nil
	}
	var rows []row
	b := brief.Why(r)
	for _, k := range b.Key {
		k := k
		rows = append(rows, row{render: func(th Theme, _ bool) string {
			return th.KeyLine(k.Label, k.Value, k.Note, k.Sev)
		}})
	}
	rows = append(rows, spacer())
	if tl := timelineRows(t, r); len(tl) > 0 {
		rows = append(rows, tl...)
		rows = append(rows, spacer())
	}

	// Only the dimensions that actually differ earn a line. The rest is one dim sentence.
	var differ []result.Dimension
	identical := 0
	for _, d := range r.Dimensions {
		if d.Separation == result.Total || d.Separation == result.Partial {
			differ = append(differ, d)
			continue
		}
		identical++
	}
	if len(differ) > 0 {
		rows = append(rows, sectionRow(t, "what differs between the two groups"))
		limit := 3
		if expanded {
			limit = len(differ)
		}
		for i, d := range differ {
			if i >= limit {
				rows = append(rows, staticRow("  "+t.Faint(fmt.Sprintf("%d more differ, press d", len(differ)-limit))))
				break
			}
			d := d
			sev := result.Disruption
			if d.Separation == result.Partial {
				sev = result.Risk
			}
			rows = append(rows, row{
				render: func(th Theme, sel bool) string {
					caret := th.caret(sel)
					name := pad(d.Name, 24)
					if sel {
						name = th.bold(colText, name)
					} else {
						name = th.paint(colText, name)
					}
					return caret + name + th.paint(sevColor(sev), pad(string(d.Separation), 10)) +
						th.Faint(cut(d.FailingValues+"  against  "+d.HealthyValues, th.Inner()-42))
				},
				title:      d.Name,
				ref:        d.Name,
				sev:        sev,
				selectable: true,
				detail: []string{
					"dimension " + d.Name,
					"separation " + string(d.Separation),
					fmt.Sprintf("purity %.2f, the share of failing pods carrying the discriminating value", d.Purity),
					"",
					"failing side: " + d.FailingValues,
					"healthy side: " + d.HealthyValues,
				},
			})
		}
	}
	if identical > 0 {
		rows = append(rows, staticRow("  "+t.Faint(fmt.Sprintf("%d other attributes are identical on both sides, so they explain nothing", identical))))
	}
	rows = append(rows, spacer())

	if len(r.Revisions) > 0 {
		rows = append(rows, sectionRow(t, "revisions compared"))
		for _, rev := range r.Revisions {
			rev := rev
			rows = append(rows, staticRow("  "+t.paint(colText, pad(fmt.Sprintf("revision %d", rev.Number), 16))+
				t.Muted(pad(rev.Name, 22))+t.Faint(rev.Signals)))
		}
		rows = append(rows, spacer())
	}

	rows = append(rows, sectionRow(t, "changes, ranked"))
	limit := 3
	if expanded {
		limit = len(r.Suspects)
	}
	for i, s := range r.Suspects {
		if i >= limit {
			rows = append(rows, staticRow("  "+t.Faint(fmt.Sprintf("%d more ranked lower, press d", len(r.Suspects)-limit))))
			break
		}
		s := s
		sev := result.Info
		switch s.Verdict {
		case result.Splits:
			sev = result.Disruption
		case result.Temporal:
			sev = result.Risk
		case result.Unknown:
			sev = result.NotAssessed
		}
		note := s.At.Format("15:04")
		if s.Attribution != "" {
			note += "  " + s.Attribution
		} else {
			note += "  " + string(s.Actor)
		}
		detail := []string{s.Title, "verdict " + string(s.Verdict), "actor " + string(s.Actor)}
		if s.Dimension != "" {
			detail = append(detail, "created the discriminating value of "+s.Dimension)
		}
		if len(s.Diff) > 0 {
			detail = append(detail, "", "changed")
			for _, d := range s.Diff {
				detail = append(detail, "  "+d)
			}
		}
		if len(s.Evidence) > 0 {
			detail = append(detail, "", "evidence")
			for _, e := range s.Evidence {
				detail = append(detail, "  "+e)
			}
		}
		rows = append(rows, row{
			render: func(th Theme, sel bool) string {
				return th.Card(sel, sev, string(s.Verdict), s.Title, note)
			},
			title:      s.Title,
			ref:        s.ID,
			sev:        sev,
			detail:     detail,
			selectable: true,
		})
	}

	if expanded && len(r.Gaps) > 0 {
		rows = append(rows, spacer(), sectionRow(t, "coverage"))
		for _, g := range r.Gaps {
			rows = append(rows, staticRow("  "+t.Faint(wrapIndent(g, t.Inner()-2, "    "))))
		}
	}
	return rows
}

// impactRows answers: what breaks if this happens.
func impactRows(t Theme, r *result.ImpactReport, expanded bool) []row {
	if r == nil {
		return nil
	}
	var rows []row
	b := brief.Impact(r)
	for _, k := range b.Key {
		if k.Sev != "" {
			continue // severity lines are shown as cards below, not twice
		}
		k := k
		rows = append(rows, row{render: func(th Theme, _ bool) string {
			return th.KeyLine(k.Label, k.Value, k.Note, k.Sev)
		}})
	}
	if len(r.Mapping) > 0 {
		rows = append(rows, spacer(), sectionRow(t, "what the plan moves"))
		for _, mline := range r.Mapping {
			if i := strings.Index(mline, " [state"); i >= 0 {
				mline = mline[:i]
			}
			mline := mline
			rows = append(rows, staticRow("  "+t.Muted(wrapIndent(mline, t.Inner()-2, "    "))))
		}
	}
	rows = append(rows, spacer(), sectionRow(t, "what that breaks"))
	limit := 6
	if expanded {
		limit = len(r.Findings)
	}
	shown := 0
	for _, f := range r.Findings {
		if !expanded && f.Severity == result.Info {
			continue
		}
		if shown >= limit {
			rows = append(rows, staticRow("  "+t.Faint(fmt.Sprintf("%d more findings, press d", len(r.Findings)-shown))))
			break
		}
		f := f
		rows = append(rows, row{
			render: func(th Theme, sel bool) string {
				return th.Card(sel, f.Severity, shortSev(f.Severity), f.Object, brief.Note(f.Reason))
			},
			title:      f.Object,
			ref:        f.Object,
			sev:        f.Severity,
			detail:     findingDetail(f),
			selectable: true,
		})
		shown++
	}
	if shown == 0 {
		rows = append(rows, staticRow("  "+t.Faint("nothing in the scope that was read would break")))
	}

	if expanded {
		if len(r.Ignored) > 0 {
			rows = append(rows, spacer(), sectionRow(t, "not modelled, so not claimed"))
			for _, ig := range r.Ignored {
				rows = append(rows, staticRow("  "+t.Faint(ig)))
			}
		}
		if len(r.Gaps) > 0 {
			rows = append(rows, spacer(), sectionRow(t, "coverage"))
			for _, g := range r.Gaps {
				rows = append(rows, staticRow("  "+t.Faint(wrapIndent(g, t.Inner()-2, "    "))))
			}
		}
	}
	return rows
}

// workloadAction offers "why" on a finding that names a workload the cohort analysis can read.
func workloadAction(f result.Finding, enabled bool) *rowAction {
	if !enabled {
		return nil
	}
	switch f.Kind {
	case "Deployment", "StatefulSet", "DaemonSet":
	default:
		return nil
	}
	ns, name := f.Object, f.Object
	if i := strings.IndexByte(f.Object, '/'); i >= 0 {
		ns, name = f.Object[:i], f.Object[i+1:]
	}
	return &rowAction{kind: "why", context: f.Context, namespace: ns, name: strings.ToLower(f.Kind) + "/" + name}
}

// poolAction offers "impact" on a node pool: what breaks if it goes away.
func poolAction(p result.PoolSummary, enabled bool) *rowAction {
	if !enabled || p.Pool == "" || p.Pool == "unlabelled" {
		return nil
	}
	return &rowAction{kind: "impact", context: p.Context, selector: "pool=" + p.Pool}
}

// timelineRows draws when each change landed relative to the onset, on one line, so the eye
// sees the order before reading a single timestamp.
func timelineRows(t Theme, r *result.WhyReport) []row {
	if r == nil || r.OnsetAt.IsZero() || len(r.Suspects) == 0 {
		return nil
	}
	type mark struct {
		at    time.Time
		glyph string
		sev   result.Severity
		label string
	}
	var marks []mark
	first := r.OnsetAt
	for _, s := range r.Suspects {
		if s.At.IsZero() {
			continue
		}
		sev := result.Info
		switch s.Verdict {
		case result.Splits:
			sev = result.Disruption
		case result.Temporal:
			sev = result.Risk
		}
		marks = append(marks, mark{at: s.At, glyph: "●", sev: sev, label: s.At.Format("15:04")})
		if s.At.Before(first) {
			first = s.At
		}
	}
	if len(marks) == 0 {
		return nil
	}
	marks = append(marks, mark{at: r.OnsetAt, glyph: "✖", sev: result.Outage, label: r.OnsetAt.Format("15:04") + " onset"})
	last := r.OnsetAt
	for _, m := range marks {
		if m.at.After(last) {
			last = m.at
		}
	}
	span := last.Sub(first)
	width := t.Inner() - 14
	if width < 20 {
		width = 20
	}
	cells := make([]string, width)
	for i := range cells {
		cells[i] = t.paint(colLine, "─")
	}
	// place the marks, later marks win a contested cell so the onset always shows
	for _, m := range marks {
		pos := 0
		if span > 0 {
			pos = int(float64(width-1) * float64(m.at.Sub(first)) / float64(span))
		}
		if pos < 0 {
			pos = 0
		}
		if pos > width-1 {
			pos = width - 1
		}
		cells[pos] = t.paint(sevColor(m.sev), m.glyph)
	}
	line := "  " + t.Muted(pad("timeline", 12)) + strings.Join(cells, "")
	// the legend names the marks in time order, clipped to the width
	var legend []string
	for _, m := range marks {
		legend = append(legend, m.glyph+" "+m.label)
	}
	leg := "  " + strings.Repeat(" ", 12) + t.Faint(cut(strings.Join(legend, "   "), width))
	return []row{staticRow(line), staticRow(leg)}
}
