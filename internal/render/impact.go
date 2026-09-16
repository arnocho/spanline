package render

import (
	"strings"

	"github.com/arnocho/spanline/internal/brief"
	"github.com/arnocho/spanline/internal/result"
)

// ImpactText renders the impact report for a terminal: what breaks if these nodes go away,
// or if this plan is applied. By default it prints the short screen only; Options.Details
// adds the findings, their evidence and the coverage underneath. The last line is always
// the verdict and its exit code, in both forms.
func ImpactText(r *result.ImpactReport, o Options) string {
	p := newPalette(o)
	var b strings.Builder
	if r == nil {
		b.WriteString(p.bold("spanline impact") + "\n")
		b.WriteString(indent1 + "no report to render\n")
		return out(&b)
	}
	w := o.width()

	br := brief.Impact(r)
	briefHead(&b, p, o, "spanline impact"+gutter+strings.Join([]string{
		kv("context", r.Context),
		kv("source", r.Source),
		kv("snapshot", ts(r.SnapshotAt)),
	}, gutter), br)
	briefKeys(&b, p, o, br.Key)
	heldBack(&b, p, o, withGaps(br.More, len(r.Gaps)))

	if o.Details {
		section(&b, p, "details")
		writeFields(&b, [][2]string{
			{"snapshot", ts(r.SnapshotAt)},
			{"expires", ts(r.ExpiresAt)},
			{"generated", ts(r.GeneratedAt)},
		}, indent1)

		section(&b, p, "scope")
		fields := [][2]string{{"nodes", itoa(len(r.Nodes))}}
		if strings.TrimSpace(r.PlanSHA) != "" {
			fields = append(fields, [2]string{"plan sha", r.PlanSHA})
		}
		if strings.TrimSpace(r.PlanSummary) != "" {
			fields = append(fields, [2]string{"plan", r.PlanSummary})
		}
		writeFields(&b, fields, indent1)
		writeInline(&b, "node list", r.Nodes, w, indent1)

		if len(r.Mapping) > 0 {
			section(&b, p, "mapping")
			writeList(&b, r.Mapping, w, indent1)
		}

		section(&b, p, "findings")
		writeFindings(&b, p, o, r.Findings)

		if len(r.Ignored) > 0 {
			section(&b, p, "ignored")
			writeList(&b, r.Ignored, w, indent1)
		}

		writeNarrative(&b, p, o, r.Narrative)
		writeGaps(&b, p, o, r.Gaps)
	}

	// The verdict closes the report, so a truncated terminal still shows the exit code.
	b.WriteString("\n")
	b.WriteString(p.bold("verdict") + gutter + p.severity(r.Verdict) + gutter + "exit code " + itoa(r.ExitCode) + "\n")
	return out(&b)
}

// writeFindings prints findings in report order, with each one's evidence under it.
func writeFindings(b *strings.Builder, p palette, o Options, findings []result.Finding) {
	if len(findings) == 0 {
		b.WriteString(indent1 + "no finding recorded\n")
		return
	}
	head := []string{"SEVERITY", "KIND", "OBJECT", "REASON"}
	rows := make([][]string, 0, len(findings))
	for _, f := range findings {
		rows = append(rows, []string{
			p.severity(f.Severity), orEmpty(f.Kind), orEmpty(f.Object), orEmpty(f.Reason),
		})
	}
	widths := columnWidths(head, rows)

	b.WriteString(p.bold(formatRow(head, widths, nil, indent1)))
	b.WriteString("\n")
	for i, f := range findings {
		b.WriteString(formatRow(rows[i], widths, nil, indent1))
		b.WriteString("\n")
		if strings.TrimSpace(f.Context) != "" {
			b.WriteString(indent2 + p.dim(kv("context", f.Context)) + "\n")
		}
		writeEvidence(b, p, o, f.Evidence, indent2)
	}
}

// ImpactMarkdown renders the impact report for a merge request or a ticket.
func ImpactMarkdown(r *result.ImpactReport) string {
	var b strings.Builder
	if r == nil {
		b.WriteString("# spanline impact\n\nNo report to render.\n")
		return out(&b)
	}

	b.WriteString("# spanline impact: " + orEmpty(r.Context) + "\n")
	mdBrief(&b, brief.Impact(r))

	rows := [][]string{
		{"context", r.Context},
		{"source", r.Source},
		{"snapshot", ts(r.SnapshotAt)},
		{"expires", ts(r.ExpiresAt)},
		{"generated", ts(r.GeneratedAt)},
		{"nodes", itoa(len(r.Nodes))},
	}
	if strings.TrimSpace(r.PlanSHA) != "" {
		rows = append(rows, []string{"plan sha", r.PlanSHA})
	}
	if strings.TrimSpace(r.PlanSummary) != "" {
		rows = append(rows, []string{"plan", r.PlanSummary})
	}
	mdTable(&b, []string{"field", "value"}, rows)

	if len(r.Nodes) > 0 {
		b.WriteString("\nNodes: " + strings.Join(r.Nodes, ", ") + "\n")
	}

	if len(r.Mapping) > 0 {
		mdHeading(&b, 2, "Mapping")
		mdList(&b, r.Mapping)
	}

	mdHeading(&b, 2, "Findings")
	if len(r.Findings) == 0 {
		b.WriteString("No finding recorded.\n")
	} else {
		fr := make([][]string, 0, len(r.Findings))
		for _, f := range r.Findings {
			fr = append(fr, []string{string(f.Severity), f.Kind, f.Object, f.Reason, f.Context})
		}
		mdTable(&b, []string{"severity", "kind", "object", "reason", "context"}, fr)

		var hasEvidence bool
		for _, f := range r.Findings {
			if len(f.Evidence) > 0 {
				hasEvidence = true
				break
			}
		}
		if hasEvidence {
			mdHeading(&b, 3, "Evidence")
			for _, f := range r.Findings {
				if len(f.Evidence) == 0 {
					continue
				}
				b.WriteString(orEmpty(f.Object) + ":\n\n")
				mdList(&b, f.Evidence)
				b.WriteString("\n")
			}
		}
	}

	if len(r.Ignored) > 0 {
		mdHeading(&b, 2, "Ignored")
		mdList(&b, r.Ignored)
	}

	mdNarrative(&b, r.Narrative)
	mdGaps(&b, r.Gaps)

	mdHeading(&b, 2, "Verdict")
	b.WriteString("**" + string(r.Verdict) + "**, exit code " + itoa(r.ExitCode) + "\n")
	return out(&b)
}
