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
	"unicode"
	"unicode/utf8"

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

// maxKeyLines caps the facts under a headline: a screen, not a report.
const maxKeyLines = 6

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// shortTime prints the clock in UTC, the clock the full report prints, so the short screen
// and the details under it never disagree by a time zone.
func shortTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format("15:04")
}

// scope joins the parts of a context line that are set, so an empty report yields an
// empty scope rather than a run of separators.
func scope(parts ...string) string {
	return join("  ", parts...)
}

func join(sep string, parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// workloadName is the bare name of a workload reference such as deployment/checkout.
func workloadName(ref string) string {
	name := ref
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if strings.TrimSpace(name) == "" {
		return "this workload"
	}
	return name
}

// Why answers: what separates the failing pods, and which change made it.
func Why(r *result.WhyReport) Brief {
	if r == nil {
		return Brief{Empty: "no report"}
	}
	b := Brief{Sub: scope(r.Context, r.Namespace, r.Workload)}

	name := workloadName(r.Workload)
	total := r.Failing.Count + r.Healthy.Count

	// The separating dimension is the whole point of this screen: the first clean split,
	// or failing that the first partial one, which is named as partial and never as a split.
	var sep, part *result.Dimension
	for i := range r.Dimensions {
		d := &r.Dimensions[i]
		if d.Separation == result.Total && sep == nil {
			sep = d
		}
		if d.Separation == result.Partial && part == nil {
			part = d
		}
	}
	if sep != nil {
		part = nil
	}
	others := 0
	for i := range r.Dimensions {
		d := &r.Dimensions[i]
		if d == sep || d == part {
			continue
		}
		if d.Separation == result.Total || d.Separation == result.Partial {
			others++
		}
	}

	// The change to name: the first that splits the cohorts, else the closest in time.
	// Position never outranks the verdict, so a TEMPORAL change listed first does not hide
	// a SPLITS one under it.
	var top *result.Suspect
	for _, want := range []result.Verdict{result.Splits, result.Temporal} {
		for i := range r.Suspects {
			if r.Suspects[i].Verdict == want {
				top = &r.Suspects[i]
				break
			}
		}
		if top != nil {
			break
		}
	}
	lower := len(r.Suspects)
	if top != nil {
		lower--
	}

	fail := plural(r.Failing.Count, "fails", "fail")
	pods := plural(total, "pod", "pods")
	switch {
	case r.Mode == result.ModeRevision:
		b.Headline = fmt.Sprintf("every pod of %s is failing, so there is nothing healthy left to compare", name)
		b.Sev = result.Outage
	case sep != nil:
		b.Headline = fmt.Sprintf("%d of %d %s of %s %s, and %s tells them apart",
			r.Failing.Count, total, pods, name, fail, sep.Name)
		b.Sev = result.Disruption
	case part != nil:
		b.Headline = fmt.Sprintf("%d of %d %s of %s %s, and %s only partly tells them apart",
			r.Failing.Count, total, pods, name, fail, part.Name)
		b.Sev = result.Risk
	case r.Failing.Count > 0:
		b.Headline = fmt.Sprintf("%d of %d %s of %s %s, and nothing collected tells them apart",
			r.Failing.Count, total, pods, name, fail)
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
		b.Key = append(b.Key, Line{Label: "separates them", Value: sep.Name, Note: sides(sep), Sev: result.Disruption})
	}
	if part != nil {
		b.Key = append(b.Key, Line{Label: "partly separates them", Value: part.Name, Note: sides(part), Sev: result.Risk})
	}
	if r.Mode == result.ModeRevision && len(r.Revisions) >= 2 {
		b.Key = append(b.Key, Line{
			Label: "comparing", Value: fmt.Sprintf("revision %d against %d", r.Revisions[0].Number, r.Revisions[1].Number),
			Note: "the older revision keeps no pod, so this is weaker than a live comparison",
			Sev:  result.NotAssessed,
		})
	}
	hiddenDiff := 0
	if top != nil {
		note := top.Attribution
		if note == "" {
			note = string(top.Actor)
		}
		b.Key = append(b.Key, Line{
			Label: verdictWord(top.Verdict), Value: top.Title, Note: join(", ", shortTime(top.At), note),
			Ref: top.ID,
		})
		for i, d := range top.Diff {
			if len(b.Key) >= maxKeyLines {
				hiddenDiff = len(top.Diff) - i
				break
			}
			label := ""
			if i == 0 {
				label = "it changed"
			}
			b.Key = append(b.Key, Line{Label: label, Value: d, Ref: top.ID})
		}
	}

	var more []string
	if others > 0 {
		more = append(more, fmt.Sprintf("%d other %s", others, plural(others, "dimension differs", "dimensions differ")))
	}
	switch {
	case top != nil && lower > 0:
		more = append(more, fmt.Sprintf("%d %s ranked lower", lower, plural(lower, "change", "changes")))
	case lower > 0:
		more = append(more, fmt.Sprintf("%d %s ranked, none matched", lower, plural(lower, "change", "changes")))
	}
	if hiddenDiff > 0 {
		more = append(more, fmt.Sprintf("%d more diff %s", hiddenDiff, plural(hiddenDiff, "line", "lines")))
	}
	if n := len(r.Gaps); n > 0 {
		more = append(more, fmt.Sprintf("%d coverage %s", n, plural(n, "gap", "gaps")))
	}
	b.More = strings.Join(more, ", ")
	return b
}

// sides is the aside under a separating dimension: what each cohort carries.
func sides(d *result.Dimension) string {
	return fmt.Sprintf("%s on the failing side, %s on the healthy one", cut(d.FailingValues, 34), cut(d.HealthyValues, 34))
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
// The full sentence stays in the report and in the evidence panel. A reason that ends where
// its consequence should start is returned whole, never as nothing.
func Note(reason string) string {
	if i := strings.Index(reason, ", so "); i >= 0 {
		if rest := strings.TrimSpace(reason[i+len(", so "):]); rest != "" {
			return rest
		}
	}
	if i := strings.Index(reason, ", which "); i >= 0 {
		if rest := strings.TrimSpace(reason[i+2:]); rest != "" {
			return rest
		}
	}
	return strings.TrimSpace(reason)
}

// cut shortens a value to n characters. It counts runes, not bytes, so a multi-byte
// character is never split in half, and it never strands a base letter from the combining
// marks that follow it.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	keep := n - 1
	if keep < 0 {
		keep = 0
	}
	for keep < len(runes) && unicode.Is(unicode.M, runes[keep]) {
		keep++
	}
	return strings.TrimRight(string(runes[:keep]), " ") + "..."
}

// Impact answers: what breaks if this happens.
func Impact(r *result.ImpactReport) Brief {
	if r == nil {
		return Brief{Empty: "no report"}
	}
	// An absent verdict is NOT ASSESSED, never an implicit pass.
	verdict := r.Verdict
	if strings.TrimSpace(string(verdict)) == "" {
		verdict = result.NotAssessed
	}
	b := Brief{Sub: scope(r.Context, r.Source), Sev: verdict}
	counts := map[result.Severity]int{}
	var worst []result.Finding
	for _, f := range r.Findings {
		counts[f.Severity]++
		if f.Severity == verdict && len(worst) < 3 {
			worst = append(worst, f)
		}
	}
	switch verdict {
	case result.Outage:
		n := counts[result.Outage]
		b.Headline = fmt.Sprintf("%d %s would lose every replica", n, plural(n, "workload", "workloads"))
	case result.Disruption:
		n := counts[result.Disruption]
		b.Headline = fmt.Sprintf("nothing goes fully down, but %d %s would be disrupted", n, plural(n, "thing", "things"))
	case result.Risk:
		n := counts[result.Risk]
		b.Headline = fmt.Sprintf("nothing goes down, but %d %s would be left at risk", n, plural(n, "thing", "things"))
	case result.NotAssessed:
		n := counts[result.NotAssessed]
		switch {
		case n == 0:
			b.Headline = "nothing was assessed, so this is not a pass"
		case n == len(r.Findings):
			// Nothing else was observed, so the simulation never ran.
			b.Headline = "nothing was simulated, so this is not a pass"
		default:
			b.Headline = fmt.Sprintf("%d %s could not be assessed, so this is not a pass", n, plural(n, "thing", "things"))
		}
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
	if hidden := counts[verdict] - len(worst); hidden > 0 {
		more = append(more, fmt.Sprintf("%d more %s", hidden, strings.ToLower(string(verdict))))
	}
	for _, s := range []result.Severity{result.Outage, result.Disruption, result.Risk, result.NotAssessed, result.Info} {
		if n := counts[s]; n > 0 && s != verdict {
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
	case unassessed > 0:
		// Something could not be assessed: not fragile as far as was read, and not a pass.
		b.Headline = fmt.Sprintf("nothing fragile in what was read, but %d %s could not be assessed", unassessed, plural(unassessed, "thing", "things"))
		b.Sev = result.NotAssessed
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
	if n < 0 {
		n = 0
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
