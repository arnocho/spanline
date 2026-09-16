// Package brief turns a full report into the one answer a tired operator needs first.
//
// Every surface uses it, so the terminal interface, the plain text output and the markdown
// all lead with the same sentence. Detail is never deleted, only deferred: the reports keep
// everything, and a brief is what is shown before anyone asks for more.
package brief

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arnocho/spanline/internal/result"
)

// Line is one key fact under the headline: a label, a value, and an optional aside.
type Line struct {
	Label string
	Value string
	Note  string
	Sev   result.Severity
	Ref   string // identifier of the underlying object, for drill down
}

// Brief is a whole screen's worth of answer, and nothing more.
type Brief struct {
	Headline string // the answer, one sentence, no jargon
	Sub      string // where this was observed
	Key      []Line // two to four facts that support the headline
	More     string // what is being held back, and how to see it
	Sev      result.Severity
	Empty    string // set when there is genuinely nothing to report
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func shortTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format("15:04")
}

// Why answers: what separates the failing pods, and which change made it.
func Why(r *result.WhyReport) Brief {
	if r == nil {
		return Brief{Empty: "no report"}
	}
	b := Brief{Sub: fmt.Sprintf("%s  %s  %s", r.Context, r.Namespace, r.Workload)}

	name := r.Workload
	if i := strings.IndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	total := r.Failing.Count + r.Healthy.Count

	// The separating dimension is the whole point of this screen.
	var sep *result.Dimension
	others := 0
	for i := range r.Dimensions {
		d := r.Dimensions[i]
		if d.Separation == result.Total && sep == nil {
			sep = &r.Dimensions[i]
			continue
		}
		if d.Separation == result.Total || d.Separation == result.Partial {
			others++
		}
	}

	var top *result.Suspect
	lower := 0
	for i := range r.Suspects {
		s := r.Suspects[i]
		if top == nil && (s.Verdict == result.Splits || s.Verdict == result.Temporal) {
			top = &r.Suspects[i]
			continue
		}
		lower++
	}

	switch {
	case r.Mode == result.ModeRevision:
		b.Headline = fmt.Sprintf("every pod of %s is failing, so there is nothing healthy left to compare", name)
		b.Sev = result.Outage
	case sep != nil:
		b.Headline = fmt.Sprintf("%d of %d pods of %s fail, and %s tells them apart",
			r.Failing.Count, total, name, sep.Name)
		b.Sev = result.Disruption
	case r.Failing.Count > 0:
		b.Headline = fmt.Sprintf("%d of %d pods of %s fail, and nothing collected tells them apart",
			r.Failing.Count, total, name)
		b.Sev = result.NotAssessed
	default:
		b.Headline = fmt.Sprintf("no pod of %s is failing in this snapshot", name)
		b.Sev = result.Info
		b.Empty = "nothing to explain"
		return b
	}

	if !r.OnsetAt.IsZero() {
		b.Key = append(b.Key, Line{Label: "started", Value: shortTime(r.OnsetAt), Note: onsetWord(r.OnsetSignal)})
	}
	if sep != nil {
		b.Key = append(b.Key, Line{
			Label: "separates them", Value: sep.Name,
			Note: fmt.Sprintf("%s on the failing side, %s on the healthy one", cut(sep.FailingValues, 34), cut(sep.HealthyValues, 34)),
			Sev:  result.Disruption,
		})
	}
	if r.Mode == result.ModeRevision && len(r.Revisions) >= 2 {
		b.Key = append(b.Key, Line{
			Label: "comparing", Value: fmt.Sprintf("revision %d against %d", r.Revisions[0].Number, r.Revisions[1].Number),
			Note: "the older revision keeps no pod, so this is weaker than a live comparison",
			Sev:  result.NotAssessed,
		})
	}
	if top != nil {
		note := top.Attribution
		if note == "" {
			note = string(top.Actor)
		}
		b.Key = append(b.Key, Line{
			Label: verdictWord(top.Verdict), Value: top.Title, Note: fmt.Sprintf("%s, %s", shortTime(top.At), note),
			Ref: top.ID,
		})
		for i, d := range top.Diff {
			label := ""
			if i == 0 {
				label = "it changed"
			}
			b.Key = append(b.Key, Line{Label: label, Value: d, Ref: top.ID})
			if len(b.Key) >= 6 {
				break
			}
		}
	}

	var more []string
	if others > 0 {
		more = append(more, fmt.Sprintf("%d other %s differ", others, plural(others, "dimension", "dimensions")))
	}
	if lower > 0 {
		more = append(more, fmt.Sprintf("%d %s ranked lower", lower, plural(lower, "change", "changes")))
	}
	if n := len(r.Gaps); n > 0 {
		more = append(more, fmt.Sprintf("%d coverage %s", n, plural(n, "gap", "gaps")))
	}
	b.More = strings.Join(more, ", ")
	return b
}

func onsetWord(signal string) string {
	switch {
	case strings.Contains(signal, "OOMKilled"):
		return "first container killed for memory"
	case strings.Contains(signal, "Unhealthy"):
		return "first readiness failure"
	case strings.Contains(signal, "Ready"):
		return "first pod to report not ready"
	case signal == "":
		return ""
	default:
		return "first failure signal"
	}
}

func verdictWord(v result.Verdict) string {
	switch v {
	case result.Splits:
		return "the change"
	case result.Temporal:
		return "closest change"
	default:
		return "change"
	}
}

// Note shortens a finding's reason to the part an operator reads first: the consequence.
// The full sentence stays in the report and in the evidence panel.
func Note(reason string) string {
	if i := strings.Index(reason, ", so "); i >= 0 {
		return strings.TrimSpace(reason[i+len(", so "):])
	}
	if i := strings.Index(reason, ", which "); i >= 0 {
		return strings.TrimSpace(reason[i+2:])
	}
	return reason
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "..."
}

// Impact answers: what breaks if this happens.
func Impact(r *result.ImpactReport) Brief {
	if r == nil {
		return Brief{Empty: "no report"}
	}
	b := Brief{Sub: fmt.Sprintf("%s  %s", r.Context, r.Source), Sev: r.Verdict}
	counts := map[result.Severity]int{}
	var worst []result.Finding
	for _, f := range r.Findings {
		counts[f.Severity]++
		if f.Severity == r.Verdict && len(worst) < 3 {
			worst = append(worst, f)
		}
	}
	switch r.Verdict {
	case result.Outage:
		n := counts[result.Outage]
		b.Headline = fmt.Sprintf("%d %s would lose every replica", n, plural(n, "workload", "workloads"))
	case result.Disruption:
		n := counts[result.Disruption]
		b.Headline = fmt.Sprintf("nothing goes fully down, but %d %s would be disrupted", n, plural(n, "thing", "things"))
	case result.NotAssessed:
		b.Headline = "nothing was simulated, so this is not a pass"
	default:
		b.Headline = "nothing in this snapshot breaks"
		b.Empty = "no impact found on the scope that was read"
	}
	if len(r.Nodes) > 0 {
		b.Key = append(b.Key, Line{Label: "nodes affected", Value: fmt.Sprint(len(r.Nodes)), Note: cut(strings.Join(r.Nodes, ", "), 60)})
	}
	if r.PlanSummary != "" {
		b.Key = append(b.Key, Line{Label: "plan", Value: r.PlanSummary})
	}
	for _, f := range worst {
		b.Key = append(b.Key, Line{Label: strings.ToLower(string(f.Severity)), Value: f.Object, Note: cut(Note(f.Reason), 64), Sev: f.Severity, Ref: f.Object})
	}
	var more []string
	for _, s := range []result.Severity{result.Outage, result.Disruption, result.Risk, result.NotAssessed, result.Info} {
		if n := counts[s]; n > 0 && s != r.Verdict {
			more = append(more, fmt.Sprintf("%d %s", n, strings.ToLower(string(s))))
		}
	}
	if len(more) > 0 {
		b.More = strings.Join(more, ", ")
	}
	return b
}

// Estate answers: where should I look first.
func Estate(r *result.EstateReport) Brief {
	if r == nil {
		return Brief{Empty: "no report"}
	}
	b := Brief{}
	outages, risks, unassessed := 0, 0, 0
	for _, f := range r.Risks {
		switch f.Severity {
		case result.Outage:
			outages++
		case result.Disruption, result.Risk:
			risks++
		case result.NotAssessed:
			unassessed++
		}
	}
	pools := 0
	owned := 0
	for _, p := range r.Pools {
		pools++
		if p.TerraformAddress != "" {
			owned++
		}
	}
	b.Sub = fmt.Sprintf("%d %s, %d node %s, %d owned by terraform",
		len(r.Clusters), plural(len(r.Clusters), "cluster", "clusters"),
		pools, plural(pools, "pool", "pools"), owned)
	switch {
	case outages > 0:
		b.Headline = fmt.Sprintf("%d %s serving nothing right now", outages, plural(outages, "workload is", "workloads are"))
		b.Sev = result.Outage
	case risks > 0:
		b.Headline = fmt.Sprintf("nothing is down, %d %s would not survive a node loss", risks, plural(risks, "thing", "things"))
		b.Sev = result.Risk
	default:
		b.Headline = "nothing fragile in what was read"
		b.Sev = result.Info
		b.Empty = "no risk recorded in this snapshot"
	}
	b.Key = Priorities(r, 4)
	var more []string
	if n := len(r.Risks) - len(b.Key); n > 0 {
		more = append(more, fmt.Sprintf("%d more %s", n, plural(n, "finding", "findings")))
	}
	if unassessed > 0 {
		more = append(more, fmt.Sprintf("%d not assessed", unassessed))
	}
	if n := len(r.Gaps); n > 0 {
		more = append(more, fmt.Sprintf("%d coverage %s", n, plural(n, "gap", "gaps")))
	}
	b.More = strings.Join(more, ", ")
	return b
}

// Priorities is the "look at this now" list: the worst findings, worst first, capped.
func Priorities(r *result.EstateReport, n int) []Line {
	if r == nil {
		return nil
	}
	f := make([]result.Finding, 0, len(r.Risks))
	for _, x := range r.Risks {
		if x.Severity == result.Info {
			continue
		}
		f = append(f, x)
	}
	sort.SliceStable(f, func(i, j int) bool { return f[i].Severity.Rank() > f[j].Severity.Rank() })
	if len(f) > n {
		f = f[:n]
	}
	out := make([]Line, 0, len(f))
	for _, x := range f {
		out = append(out, Line{
			Label: strings.ToLower(string(x.Severity)),
			Value: x.Object,
			Note:  cut(Note(x.Reason), 62),
			Sev:   x.Severity,
			Ref:   x.Context + "/" + x.Object,
		})
	}
	return out
}
