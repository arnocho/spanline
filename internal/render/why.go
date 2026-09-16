package render

import (
	"strings"

	"github.com/arnocho/spanline/internal/result"
)

// WhyText renders the cohort report for a terminal: what separates the failing pods from
// the healthy ones, and which change lines up with it. Suspects keep the report's ranking.
func WhyText(r *result.WhyReport, o Options) string {
	p := newPalette(o)
	var b strings.Builder
	if r == nil {
		b.WriteString(p.bold("spanline why") + "\n")
		b.WriteString(indent1 + "no report to render\n")
		return out(&b)
	}
	w := o.width()

	sub := []string{kv("generated", ts(r.GeneratedAt))}
	if strings.TrimSpace(r.CohortKey) != "" {
		sub = append(sub, kv("cohort key", r.CohortKey))
	}
	header(&b, p, o, "spanline why", []string{
		kv("context", r.Context),
		kv("namespace", r.Namespace),
		kv("workload", r.Workload),
		kv("source", string(r.Mode)),
	}, sub)

	section(&b, p, "onset")
	writeFields(&b, [][2]string{
		{"at", ts(r.OnsetAt)},
		{"signal", orEmpty(r.OnsetSignal)},
	}, indent1)

	section(&b, p, "cohorts")
	writeTable(&b, p, []string{"COHORT", "PODS", "SAMPLE"}, []bool{false, true, false}, [][]string{
		{"failing", itoa(r.Failing.Count), orEmpty(r.Failing.Sample)},
		{"healthy", itoa(r.Healthy.Count), orEmpty(r.Healthy.Sample)},
	}, indent1)
	writeInline(&b, "failing pods", r.Failing.Pods, w, indent1)
	writeInline(&b, "healthy pods", r.Healthy.Pods, w, indent1)

	section(&b, p, "dimensions")
	if len(r.Dimensions) == 0 {
		b.WriteString(indent1 + "no dimension compared\n")
	} else {
		rows := make([][]string, 0, len(r.Dimensions))
		for _, d := range r.Dimensions {
			rows = append(rows, []string{
				d.Name,
				orEmpty(d.FailingValues),
				orEmpty(d.HealthyValues),
				orEmpty(string(d.Separation)),
				ratio(d.Purity),
			})
		}
		writeTable(&b, p,
			[]string{"DIMENSION", "FAILING", "HEALTHY", "SEPARATION", "PURITY"},
			[]bool{false, false, false, false, true}, rows, indent1)
	}

	if len(r.Revisions) > 0 {
		section(&b, p, "revisions")
		rows := make([][]string, 0, len(r.Revisions))
		for _, rev := range r.Revisions {
			rows = append(rows, []string{
				rev.Name,
				itoa(rev.Number),
				orEmpty(rev.Active),
				itoa(rev.Replicas),
				ts(rev.Created),
				orEmpty(rev.Signals),
			})
		}
		writeTable(&b, p,
			[]string{"REVISION", "NUM", "ACTIVE", "REPLICAS", "CREATED", "SIGNALS"},
			[]bool{false, true, false, true, false, false}, rows, indent1)
	}

	section(&b, p, "suspects (ranked)")
	writeSuspects(&b, p, o, r.Suspects)

	writeNarrative(&b, p, o, r.Narrative)
	writeGaps(&b, p, o, r.Gaps)
	return out(&b)
}

// writeSuspects prints the ranked list in report order, one block per suspect, with the
// diff lines indented under the entry they belong to.
func writeSuspects(b *strings.Builder, p palette, o Options, suspects []result.Suspect) {
	if len(suspects) == 0 {
		b.WriteString(indent1 + "no suspect recorded\n")
		return
	}
	heads := make([][]string, 0, len(suspects))
	for i, s := range suspects {
		heads = append(heads, []string{itoa(i+1) + ".", p.verdict(s.Verdict), ts(s.At), orEmpty(s.Title)})
	}
	widths := columnWidths(nil, heads)

	for i, s := range suspects {
		b.WriteString(formatRow(heads[i], widths, []bool{true, false, false, false}, indent1))
		b.WriteString("\n")

		meta := []string{kv("id", s.ID)}
		if strings.TrimSpace(s.Dimension) != "" {
			meta = append(meta, kv("dimension", s.Dimension))
		}
		meta = append(meta, kv("actor", string(s.Actor)))
		if strings.TrimSpace(s.Attribution) != "" {
			meta = append(meta, kv("attribution", s.Attribution))
		}
		b.WriteString(indent2 + p.dim(strings.Join(meta, gutter)) + "\n")

		if len(s.Diff) > 0 {
			b.WriteString(indent2 + p.dim("diff") + "\n")
			for _, line := range s.Diff {
				b.WriteString(indent3 + line + "\n")
			}
		}
		writeEvidence(b, p, o, s.Evidence, indent2)
	}
}

// WhyMarkdown renders the cohort report for a merge request or a ticket.
func WhyMarkdown(r *result.WhyReport) string {
	var b strings.Builder
	if r == nil {
		b.WriteString("# spanline why\n\nNo report to render.\n")
		return out(&b)
	}

	b.WriteString("# spanline why: " + orEmpty(r.Namespace) + "/" + orEmpty(r.Workload) + "\n")

	rows := [][]string{
		{"context", r.Context},
		{"namespace", r.Namespace},
		{"workload", r.Workload},
		{"source", string(r.Mode)},
	}
	if strings.TrimSpace(r.CohortKey) != "" {
		rows = append(rows, []string{"cohort key", r.CohortKey})
	}
	rows = append(rows,
		[]string{"onset", ts(r.OnsetAt)},
		[]string{"onset signal", r.OnsetSignal},
		[]string{"generated", ts(r.GeneratedAt)},
	)
	b.WriteString("\n")
	mdTable(&b, []string{"field", "value"}, rows)

	mdHeading(&b, 2, "Cohorts")
	mdTable(&b, []string{"cohort", "pods", "sample"}, [][]string{
		{"failing", itoa(r.Failing.Count), r.Failing.Sample},
		{"healthy", itoa(r.Healthy.Count), r.Healthy.Sample},
	})
	if len(r.Failing.Pods) > 0 {
		b.WriteString("\nFailing pods: " + strings.Join(r.Failing.Pods, ", ") + "\n")
	}
	if len(r.Healthy.Pods) > 0 {
		b.WriteString("\nHealthy pods: " + strings.Join(r.Healthy.Pods, ", ") + "\n")
	}

	mdHeading(&b, 2, "Dimensions")
	if len(r.Dimensions) == 0 {
		b.WriteString("No dimension compared.\n")
	} else {
		dims := make([][]string, 0, len(r.Dimensions))
		for _, d := range r.Dimensions {
			dims = append(dims, []string{
				d.Name, d.FailingValues, d.HealthyValues, string(d.Separation), ratio(d.Purity),
			})
		}
		mdTable(&b, []string{"dimension", "failing values", "healthy values", "separation", "purity"}, dims)
	}

	if len(r.Revisions) > 0 {
		mdHeading(&b, 2, "Revisions")
		revs := make([][]string, 0, len(r.Revisions))
		for _, rev := range r.Revisions {
			revs = append(revs, []string{
				rev.Name, itoa(rev.Number), rev.Active, itoa(rev.Replicas), ts(rev.Created), rev.Signals,
			})
		}
		mdTable(&b, []string{"revision", "number", "active", "replicas", "created", "signals"}, revs)
	}

	mdHeading(&b, 2, "Suspects (ranked)")
	if len(r.Suspects) == 0 {
		b.WriteString("No suspect recorded.\n")
	} else {
		sus := make([][]string, 0, len(r.Suspects))
		for i, s := range r.Suspects {
			sus = append(sus, []string{
				itoa(i + 1), string(s.Verdict), ts(s.At), s.Title, s.Dimension, string(s.Actor), s.Attribution,
			})
		}
		mdTable(&b, []string{"rank", "verdict", "at", "title", "dimension", "actor", "attribution"}, sus)
		for i, s := range r.Suspects {
			if len(s.Diff) == 0 && len(s.Evidence) == 0 {
				continue
			}
			mdHeading(&b, 3, itoa(i+1)+". "+orEmpty(s.Title))
			if len(s.Diff) > 0 {
				b.WriteString("```diff\n")
				for _, line := range s.Diff {
					b.WriteString(line + "\n")
				}
				b.WriteString("```\n")
			}
			if len(s.Evidence) > 0 {
				if len(s.Diff) > 0 {
					b.WriteString("\n")
				}
				b.WriteString("Evidence:\n\n")
				mdList(&b, s.Evidence)
			}
		}
	}

	mdNarrative(&b, r.Narrative)
	mdGaps(&b, r.Gaps)
	return out(&b)
}
