package render

import (
	"sort"
	"strings"

	"github.com/arnocho/spanline/internal/brief"
	"github.com/arnocho/spanline/internal/result"
)

// severityOrder is the fixed print order for grouped risks. Grouping is presentation only:
// the report's own order is preserved inside every group.
var severityOrder = []result.Severity{
	result.Outage, result.Disruption, result.Risk, result.NotAssessed, result.Info,
}

type severityGroup struct {
	severity result.Severity
	findings []result.Finding
}

// groupBySeverity buckets findings without re-ranking them. Known severities print in the
// fixed order above, any unknown severity follows, sorted by name, so output stays stable.
func groupBySeverity(findings []result.Finding) []severityGroup {
	buckets := make(map[result.Severity][]result.Finding, len(severityOrder))
	var extra []string
	seen := make(map[result.Severity]bool)

	for _, f := range findings {
		s := f.Severity
		if strings.TrimSpace(string(s)) == "" {
			s = result.NotAssessed
		}
		buckets[s] = append(buckets[s], f)
		if !knownSeverity(s) && !seen[s] {
			seen[s] = true
			extra = append(extra, string(s))
		}
	}
	sort.Strings(extra)

	groups := make([]severityGroup, 0, len(severityOrder)+len(extra))
	for _, s := range severityOrder {
		if len(buckets[s]) > 0 {
			groups = append(groups, severityGroup{severity: s, findings: buckets[s]})
		}
	}
	for _, name := range extra {
		s := result.Severity(name)
		groups = append(groups, severityGroup{severity: s, findings: buckets[s]})
	}
	return groups
}

func knownSeverity(s result.Severity) bool {
	for _, k := range severityOrder {
		if k == s {
			return true
		}
	}
	return false
}

// EstateText renders the cockpit for a terminal. By default it prints the short screen:
// where to look first, then one line per cluster in words. With Options.Details it is
// followed by the tables, clusters, pools with the Terraform address that owns them, and
// risks grouped by severity.
func EstateText(r *result.EstateReport, o Options) string {
	p := newPalette(o)
	var b strings.Builder
	if r == nil {
		b.WriteString(p.bold("spanline estate") + "\n")
		b.WriteString(indent1 + "no report to render\n")
		return out(&b)
	}

	br := brief.Estate(r)
	briefHead(&b, p, o, "spanline estate"+gutter+br.Sub, br)
	briefKeys(&b, p, o, br.Key)
	clusterLines(&b, p, o, r.Clusters)
	heldBack(&b, p, o, br.More)
	if !o.Details {
		return out(&b)
	}

	section(&b, p, "details")
	writeFields(&b, [][2]string{
		{"generated", ts(r.GeneratedAt)},
		{"risks", itoa(len(r.Risks))},
		{"state files", itoa(len(r.States))},
		{"source", "terraform state and kubernetes"},
	}, indent1)

	section(&b, p, "clusters")
	if len(r.Clusters) == 0 {
		b.WriteString(indent1 + "no cluster read\n")
	} else {
		rows := make([][]string, 0, len(r.Clusters))
		for _, c := range r.Clusters {
			rows = append(rows, []string{
				c.Context, itoa(c.Nodes), itoa(c.NodesNotReady), itoa(c.Pods),
				itoa(c.Namespaces), itoa(c.Workloads), itoa(c.Risks), itoa(c.Outages),
				ts(c.CollectedAt),
			})
		}
		writeTable(&b, p,
			[]string{"CONTEXT", "NODES", "NOT READY", "PODS", "NAMESPACES", "WORKLOADS", "RISKS", "OUTAGES", "COLLECTED"},
			[]bool{false, true, true, true, true, true, true, true, false}, rows, indent1)
	}

	section(&b, p, "pools")
	if len(r.Pools) == 0 {
		b.WriteString(indent1 + "no pool read\n")
	} else {
		rows := make([][]string, 0, len(r.Pools))
		for _, pool := range r.Pools {
			address := strings.TrimSpace(pool.TerraformAddress)
			if address == "" {
				address = "not matched to terraform"
			}
			rows = append(rows, []string{
				pool.Context, pool.Pool, itoa(pool.Nodes), orEmpty(pool.Zones),
				pct(pool.CPUPercent), pct(pool.MemPercent),
				itoa(pool.Workloads), itoa(pool.AtRisk),
				address, orEmpty(pool.StateFile),
			})
		}
		writeTable(&b, p,
			[]string{"CONTEXT", "POOL", "NODES", "ZONES", "CPU", "MEM", "WORKLOADS", "AT RISK", "TERRAFORM ADDRESS", "STATE FILE"},
			[]bool{false, false, true, false, true, true, true, true, false, false}, rows, indent1)
	}

	if len(r.States) > 0 {
		section(&b, p, "state files")
		rows := make([][]string, 0, len(r.States))
		for _, s := range r.States {
			rows = append(rows, []string{
				s.Path, itoa(s.Resources), itoa(s.NodePools), itoa(s.Clusters),
				itoa(s.Matched), itoa(s.Unmatched),
			})
		}
		writeTable(&b, p,
			[]string{"PATH", "RESOURCES", "POOLS", "CLUSTERS", "MATCHED", "UNMATCHED"},
			[]bool{false, true, true, true, true, true}, rows, indent1)
	}

	section(&b, p, "risks")
	if len(r.Risks) == 0 {
		b.WriteString(indent1 + "no risk recorded\n")
	} else {
		for _, g := range groupBySeverity(r.Risks) {
			b.WriteString("\n")
			b.WriteString(indent1 + p.severity(g.severity) + gutter + p.dim("("+itoa(len(g.findings))+")") + "\n")
			head := []string{"KIND", "OBJECT", "REASON"}
			rows := make([][]string, 0, len(g.findings))
			for _, f := range g.findings {
				rows = append(rows, []string{orEmpty(f.Kind), orEmpty(f.Object), orEmpty(f.Reason)})
			}
			widths := columnWidths(head, rows)
			b.WriteString(p.bold(formatRow(head, widths, nil, indent2)))
			b.WriteString("\n")
			for i, f := range g.findings {
				b.WriteString(formatRow(rows[i], widths, nil, indent2))
				b.WriteString("\n")
				if strings.TrimSpace(f.Context) != "" {
					b.WriteString(indent3 + p.dim(kv("context", f.Context)) + "\n")
				}
				writeEvidence(&b, p, o, f.Evidence, indent3)
			}
		}
	}

	writeNarrative(&b, p, o, r.Narrative)
	writeGaps(&b, p, o, r.Gaps)
	return out(&b)
}

// EstateMarkdown renders the cockpit for a merge request or a ticket.
func EstateMarkdown(r *result.EstateReport) string {
	var b strings.Builder
	if r == nil {
		b.WriteString("# spanline estate\n\nNo report to render.\n")
		return out(&b)
	}

	b.WriteString("# spanline estate\n")
	mdBrief(&b, brief.Estate(r))
	b.WriteString("Generated " + ts(r.GeneratedAt) + ". Clusters: " + itoa(len(r.Clusters)) +
		". Pools: " + itoa(len(r.Pools)) + ". Risks: " + itoa(len(r.Risks)) + ".\n")

	mdHeading(&b, 2, "Clusters")
	if len(r.Clusters) == 0 {
		b.WriteString("No cluster read.\n")
	} else {
		rows := make([][]string, 0, len(r.Clusters))
		for _, c := range r.Clusters {
			rows = append(rows, []string{
				c.Context, itoa(c.Nodes), itoa(c.NodesNotReady), itoa(c.Pods),
				itoa(c.Namespaces), itoa(c.Workloads), itoa(c.Risks), itoa(c.Outages), ts(c.CollectedAt),
			})
		}
		mdTable(&b, []string{"context", "nodes", "not ready", "pods", "namespaces", "workloads", "risks", "outages", "collected"}, rows)
	}

	mdHeading(&b, 2, "Pools")
	if len(r.Pools) == 0 {
		b.WriteString("No pool read.\n")
	} else {
		rows := make([][]string, 0, len(r.Pools))
		for _, pool := range r.Pools {
			address := strings.TrimSpace(pool.TerraformAddress)
			if address == "" {
				address = "not matched to terraform"
			}
			rows = append(rows, []string{
				pool.Context, pool.Pool, itoa(pool.Nodes), pool.Zones,
				pct(pool.CPUPercent), pct(pool.MemPercent),
				itoa(pool.Workloads), itoa(pool.AtRisk), address, pool.StateFile,
			})
		}
		mdTable(&b, []string{"context", "pool", "nodes", "zones", "cpu", "mem", "workloads", "at risk", "terraform address", "state file"}, rows)
	}

	if len(r.States) > 0 {
		mdHeading(&b, 2, "State files")
		rows := make([][]string, 0, len(r.States))
		for _, s := range r.States {
			rows = append(rows, []string{
				s.Path, itoa(s.Resources), itoa(s.NodePools), itoa(s.Clusters), itoa(s.Matched), itoa(s.Unmatched),
			})
		}
		mdTable(&b, []string{"path", "resources", "pools", "clusters", "matched", "unmatched"}, rows)
	}

	mdHeading(&b, 2, "Risks")
	if len(r.Risks) == 0 {
		b.WriteString("No risk recorded.\n")
	} else {
		for _, g := range groupBySeverity(r.Risks) {
			mdHeading(&b, 3, string(g.severity)+" ("+itoa(len(g.findings))+")")
			rows := make([][]string, 0, len(g.findings))
			for _, f := range g.findings {
				rows = append(rows, []string{f.Kind, f.Object, f.Reason, strings.Join(f.Evidence, "; ")})
			}
			mdTable(&b, []string{"kind", "object", "reason", "evidence"}, rows)
		}
	}

	mdNarrative(&b, r.Narrative)
	mdGaps(&b, r.Gaps)
	return out(&b)
}
