package render

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/arnocho/spanline/internal/brief"
	"github.com/arnocho/spanline/internal/cohort"
	"github.com/arnocho/spanline/internal/collect"
	"github.com/arnocho/spanline/internal/estate"
	"github.com/arnocho/spanline/internal/impact"
	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
	"github.com/arnocho/spanline/internal/tfplan"
	"github.com/charmbracelet/lipgloss"
)

var auditWidths = []int{60, 80, 100, 140}

// forbiddenWord is the verdict word spanline never prints, as a whole word: the analyses
// legitimately write "because" in their evidence.
var forbiddenWord = regexp.MustCompile(`\bcauses?\b`)

// sectionLines returns the lines between a section title and the next blank line.
func sectionLines(t *testing.T, out, title string) []string {
	t.Helper()
	i := strings.Index(out, "\n"+title+"\n")
	if i < 0 {
		t.Fatalf("section %q not found in:\n%s", title, out)
	}
	rest := out[i+len(title)+2:]
	if j := strings.Index(rest, "\n\n"); j >= 0 {
		rest = rest[:j]
	}
	return strings.Split(strings.TrimRight(rest, "\n"), "\n")
}

func widestLine(s string) int {
	w := 0
	for _, line := range strings.Split(s, "\n") {
		if v := lipgloss.Width(line); v > w {
			w = v
		}
	}
	return w
}

// liveDoctor mirrors the vocabulary internal/doctor emits on a live cluster: a read probe
// that is refused carries the status "no", and so does a capability the identity lacks.
func liveDoctor() *result.DoctorReport {
	return &result.DoctorReport{
		Context:  "prod-eu",
		Identity: "system:serviceaccount:ops:spanline",
		Profile:  "standard",
		Checks: []result.Check{
			{Name: "source", Status: "live", Detail: "kubectl (read subcommands only)"},
			{Name: "read pods", Status: "yes"},
			{Name: "list nodes", Status: "no"},
			{Name: "list replicasets", Status: "yes"},
			{Name: "list events", Status: "no"},
			{Name: "read Argo applications", Status: "no"},
			{Name: "write capability", Status: "no", Detail: "this identity cannot change the cluster"},
			{Name: "secret read capability", Status: "no", Detail: "this identity cannot read Secrets"},
			{Name: "tool kubectl", Status: "present", Detail: "reads the cluster, read subcommands only"},
		},
		Egress:      []string{"cockpit, why, impact: the Kubernetes API of the selected contexts"},
		AuditFoot:   []string{"get and list on the resources listed above, under your own identity"},
		Gaps:        []string{"list nodes is refused for this identity, so anything derived from it is NOT ASSESSED"},
		GeneratedAt: fixedTime,
	}
}

func TestDoctorShortFormNamesTheReadsTheClusterRefused(t *testing.T) {
	got := DoctorText(liveDoctor(), shortOptions())
	refused := strings.Join(sectionLines(t, got, "refused reads"), "\n")
	if strings.Contains(refused, "none recorded") {
		t.Fatalf("three reads were refused, the short form says none:\n%s", got)
	}
	for _, want := range []string{"list nodes", "list events", "read Argo applications"} {
		if !strings.Contains(refused, want) {
			t.Errorf("refused read %q is missing:\n%s", want, refused)
		}
	}
	for _, notWant := range []string{"read pods", "list replicasets", "write capability", "secret read capability", "tool kubectl"} {
		if strings.Contains(refused, notWant) {
			t.Errorf("%q is not a refused read:\n%s", notWant, refused)
		}
	}
	// The older vocabulary still counts as a refusal.
	r := liveDoctor()
	r.Checks = append(r.Checks, result.Check{Name: "secrets", Status: "denied", Detail: "get secrets refused, as expected"})
	if got := DoctorText(r, shortOptions()); !strings.Contains(got, "get secrets refused, as expected") {
		t.Errorf("a denied check dropped out of the refused reads:\n%s", got)
	}
}

func TestDoctorWithNoChecksSaysSo(t *testing.T) {
	got := DoctorText(&result.DoctorReport{Egress: []string{}, GeneratedAt: fixedTime}, shortOptions())
	refused := strings.Join(sectionLines(t, got, "refused reads"), "\n")
	if strings.Contains(refused, "none recorded") || !strings.Contains(refused, "no check run") {
		t.Errorf("with no check run, the refused reads must say so, not read as no refusal:\n%s", got)
	}
	if egress := strings.Join(sectionLines(t, got, "egress"), "\n"); !strings.Contains(egress, "none recorded") {
		t.Errorf("an empty egress list must be stated:\n%s", got)
	}
	if !strings.Contains(got, "source=unknown") {
		t.Errorf("with no source check the header must not claim a source:\n%s", got)
	}

	r := liveDoctor()
	r.CanWrite, r.CanReadSec = true, true
	if got := DoctorText(r, shortOptions()); !strings.Contains(got, "can write         yes") {
		t.Errorf("a writable identity must be stated plainly:\n%s", got)
	}
}

func TestDoctorHeaderStaysWithinWidth(t *testing.T) {
	r := liveDoctor()
	r.Context = "gke_platform-prod_europe-west1_apps"
	r.Identity = "system:serviceaccount:platform-observability:spanline-read-only-probe"
	for _, w := range auditWidths {
		for _, details := range []bool{false, true} {
			got := DoctorText(r, Options{Width: w, Details: details})
			// The banner is everything above the first section. Tables below it may be
			// wider than the terminal, the banner may not.
			banner := strings.SplitN(got, "\naccess\n", 2)[0]
			if widestLine(banner) > w {
				t.Errorf("width %d details=%v: banner wider than the terminal:\n%s", w, details, banner)
			}
			joined := strings.ReplaceAll(got, "\n"+indent1, "")
			for _, want := range []string{"identity=" + r.Identity, "context=" + r.Context, "generated=2026-03-04T09:15:00Z"} {
				if !strings.Contains(joined, want) {
					t.Errorf("width %d: the header lost %q:\n%s", w, want, got)
				}
			}
		}
	}
	// A single field wider than the terminal is broken across lines, never clipped.
	r.Identity = strings.Repeat("abcdefghij", 9)
	got := DoctorText(r, Options{Width: 60})
	header := strings.SplitN(got, "\naccess\n", 2)[0]
	if widestLine(header) > 60 {
		t.Errorf("a wide identity ran past the terminal:\n%s", header)
	}
	if !strings.Contains(strings.ReplaceAll(header, "\n"+indent1, ""), "identity="+r.Identity) {
		t.Errorf("a wide identity was cut:\n%s", header)
	}
}

// TestShortFormNotesSurviveEveryWidth is the regression guard for the value column that
// once swallowed the notes: at every width the note is on screen, next to its value when
// the terminal has room, on its own line under the value when it has not.
func TestShortFormNotesSurviveEveryWidth(t *testing.T) {
	cases := []struct {
		name string
		key  []brief.Line
		text func(Options) string
	}{
		{"why", brief.Why(realisticWhy()).Key, func(o Options) string { return WhyText(realisticWhy(), o) }},
		{"impact", brief.Impact(realisticImpact()).Key, func(o Options) string { return ImpactText(realisticImpact(), o) }},
		{"estate", brief.Estate(realisticEstate()).Key, func(o Options) string { return EstateText(realisticEstate(), o) }},
	}
	for _, tc := range cases {
		for _, w := range append([]int{40}, auditWidths...) {
			t.Run(tc.name, func(t *testing.T) {
				got := tc.text(Options{Width: w})
				for _, line := range strings.Split(got, "\n") {
					if lipgloss.Width(line) > w {
						t.Errorf("width %d: line wider than the terminal: %q", w, line)
					}
				}
				lines := strings.Split(got, "\n")
				for _, k := range tc.key {
					note := strings.TrimSpace(k.Note)
					if note == "" {
						continue
					}
					prefix := note
					if len(prefix) > 12 {
						prefix = prefix[:12]
					}
					if !strings.Contains(got, prefix) {
						t.Errorf("width %d: the note %q was dropped without a word:\n%s", w, note, got)
						continue
					}
					if w < 60 {
						continue
					}
					// With room on the line, the note sits beside its label and value, and
					// carries at least minNoteWidth cells.
					found := false
					for _, line := range lines {
						if strings.Contains(line, indent1+k.Label) && strings.Contains(line, prefix) {
							found = true
							tail := line[strings.Index(line, prefix):]
							if lipgloss.Width(tail) < minNoteWidth && lipgloss.Width(tail) < lipgloss.Width(note) {
								t.Errorf("width %d: the note was squeezed to %q", w, tail)
							}
						}
					}
					if !found {
						t.Errorf("width %d: note %q is not on the line of label %q:\n%s", w, note, k.Label, got)
					}
				}
				if lineCount(got) >= maxShortLines {
					t.Errorf("width %d: short form is %d lines:\n%s", w, lineCount(got), got)
				}
			})
		}
	}
}

func TestShortFormEmptyValuePrintsAPlaceholder(t *testing.T) {
	var b strings.Builder
	briefKeys(&b, newPalette(Options{}), Options{Width: 80}, []brief.Line{
		{Label: "plan", Value: "", Note: "no summary was read"},
		{Label: "nodes affected", Value: "3"},
	})
	got := b.String()
	if !strings.Contains(got, indent1+"plan            -  no summary was read") {
		t.Errorf("an empty value must print as %q, got:\n%q", emptyValue, got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("trailing whitespace: %q", line)
		}
	}
}

func TestEstateMarkdownCarriesTheFindingContext(t *testing.T) {
	r := realisticEstate()
	for i := range r.Risks {
		r.Risks[i].Context = "prod-eu"
	}
	r.Risks[1].Context = "onprem-int"
	got := EstateMarkdown(r)
	if !strings.Contains(got, "| kind | object | reason | evidence | context |") {
		t.Fatalf("the risks table has no context column:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "| node | node-c |") && !strings.HasSuffix(line, "| onprem-int |") {
			t.Errorf("the finding lost its cluster: %q", line)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("markdown carries an escape sequence")
	}
}

func TestWhyMarkdownCarriesTheSuspectID(t *testing.T) {
	got := WhyMarkdown(whyFixture())
	if !strings.Contains(got, "| rank | verdict | at | title | dimension | actor | attribution | id |") {
		t.Fatalf("the suspects table has no id column:\n%s", got)
	}
	if !strings.Contains(got, "| rs/checkout-7d9 |") || !strings.Contains(got, "| cm/checkout-flags |") {
		t.Errorf("suspect ids are missing:\n%s", got)
	}
}

func TestImpactMarkdownEvidenceHeadingsAreUnambiguous(t *testing.T) {
	r := &result.ImpactReport{
		Context: "prod-eu", Source: "nodes pool=apps", Verdict: result.Outage, ExitCode: 3,
		Findings: []result.Finding{
			{Severity: result.Outage, Kind: "Deployment", Object: "payments/payments-api", Reason: "every live pod stands on a node that goes away, so it drops from 4 to 0", Evidence: []string{"spec.replicas: 4"}},
			{Severity: result.Disruption, Kind: "PodDisruptionBudget", Object: "payments/payments-api", Reason: "this budget allows 0 disruptions, so every eviction is refused", Evidence: []string{"status.disruptionsAllowed: 0"}},
		},
	}
	got := ImpactMarkdown(r)
	for _, want := range []string{"Deployment payments/payments-api:", "PodDisruptionBudget payments/payments-api:"} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence heading %q missing, two findings share the object name:\n%s", want, got)
		}
	}
}

func TestRevisionModeAlwaysShowsTheRevisionsSection(t *testing.T) {
	r := whyFixture()
	r.Mode = result.ModeRevision
	r.Healthy = result.Cohort{}
	r.Dimensions = nil
	r.Revisions = nil
	text := WhyText(r, detailOptions())
	if !strings.Contains(text, "\nrevisions\n") || !strings.Contains(text, "no revision retained") {
		t.Errorf("the revision fallback ran with nothing to compare, and the details do not say so:\n%s", text)
	}
	md := WhyMarkdown(r)
	if !strings.Contains(md, "## Revisions") || !strings.Contains(md, "No revision retained.") {
		t.Errorf("the markdown hides the empty revisions section:\n%s", md)
	}
	// A cohort run has no revisions section at all: nothing was compared that way.
	if got := WhyText(whyFixture(), detailOptions()); strings.Contains(got, "\nrevisions\n") {
		t.Errorf("a cohort run must not print a revisions section:\n%s", got)
	}
}

func TestControlCharactersNeverReachTheOutput(t *testing.T) {
	// Blink and red together: no style of this package ever emits that pair, so with colour
	// on, its presence can only mean the report's bytes leaked through.
	const inject = "\x1b[5;31m"
	why := realisticWhy()
	why.Dimensions[0].FailingValues = "7d9" + inject
	why.Suspects[0].Title = inject + "image bumped"
	why.Suspects[0].Diff[0] = "- image: x" + inject
	why.Suspects[0].Attribution = "argocd" + inject
	why.Gaps[0] = inject + "metrics"
	why.Narrative = &result.Narrative{Text: "The image changed.\x1b]0;title\x07 See field 3.", Model: "m" + inject, Citations: []string{"c" + inject}}
	why.OnsetSignal = "Unhealthy\x1b[2J"
	why.Workload = "deployment/checkout\t" + inject

	imp := realisticImpact()
	imp.Findings[0].Reason = "every replica sits on the drained pool" + inject
	imp.Findings[0].Evidence[0] = inject + "pdb.status.currentHealthy"
	imp.Mapping[0] = "map" + inject
	imp.PlanSummary = "replace" + inject

	est := realisticEstate()
	est.Pools[0].Pool = "batch" + inject
	est.Risks[0].Reason = "no ready replica" + inject
	est.Clusters[0].Context = "prod-eu" + inject

	doc := doctorFixture()
	doc.Identity = "user" + inject
	doc.Checks[0].Detail = "v1" + inject
	doc.Egress[0] = "none" + inject

	plain := map[string]string{
		"why":            WhyText(why, Options{Width: 100, Details: true}),
		"why short":      WhyText(why, Options{Width: 100}),
		"impact":         ImpactText(imp, Options{Width: 100, Details: true}),
		"estate":         EstateText(est, Options{Width: 100, Details: true}),
		"estate short":   EstateText(est, Options{Width: 100}),
		"doctor":         DoctorText(doc, Options{Width: 100, Details: true}),
		"why md":         WhyMarkdown(why),
		"impact md":      ImpactMarkdown(imp),
		"estate md":      EstateMarkdown(est),
		"doctor compact": DoctorText(doc, Options{Width: 100, Compact: true}),
	}
	for _, name := range sortedKeys(plain) {
		got := plain[name]
		if strings.Contains(got, "\x1b") || strings.Contains(got, "\x07") {
			t.Errorf("%s: a control character from the report reached the output:\n%q", name, got)
		}
		if strings.Contains(got, "\t") {
			t.Errorf("%s: a tab from the report reached the output", name)
		}
		if !strings.Contains(got, `\x1b`) {
			t.Errorf("%s: the control character was dropped silently instead of being made visible", name)
		}
	}
	coloured := map[string]string{
		"why":    WhyText(why, Options{Color: true, Width: 100, Details: true}),
		"impact": ImpactText(imp, Options{Color: true, Width: 100, Details: true}),
		"estate": EstateText(est, Options{Color: true, Width: 100, Details: true}),
		"doctor": DoctorText(doc, Options{Color: true, Width: 100, Details: true}),
	}
	for _, name := range sortedKeys(coloured) {
		if got := coloured[name]; strings.Contains(got, inject) || strings.Contains(got, "\x1b]") || strings.Contains(got, "\x1b[2J") {
			t.Errorf("%s: an injected sequence survived with colour on:\n%q", name, got)
		}
	}
	// The report handed in is never modified.
	if !strings.Contains(why.Suspects[0].Title, inject) || !strings.Contains(est.Pools[0].Pool, inject) || !strings.Contains(doc.Identity, inject) {
		t.Errorf("rendering mutated the caller's report")
	}
	// JSON keeps the bytes, escaped, so a machine reader sees exactly what the cluster said.
	js, err := JSON(why)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(js, "\x1b") || !strings.Contains(js, `\u001b[5;31m`) {
		t.Errorf("JSON must escape a control character, not print or drop it")
	}
}

func TestJSONTimesAreUTC(t *testing.T) {
	paris := time.FixedZone("paris", 2*3600)
	r := whyFixture()
	r.GeneratedAt = time.Date(2026, 3, 4, 11, 15, 0, 0, paris)
	r.Suspects[0].At = time.Date(2026, 3, 4, 10, 51, 0, 0, paris)
	got, err := JSON(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "+02:00") {
		t.Errorf("JSON carries the machine's zone, the text output does not:\n%s", got)
	}
	for _, want := range []string{`"generatedAt": "2026-03-04T09:15:00Z"`, `"at": "2026-03-04T08:51:00Z"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if !r.GeneratedAt.Equal(time.Date(2026, 3, 4, 9, 15, 0, 0, time.UTC)) || r.GeneratedAt.Location() != paris {
		t.Errorf("JSON mutated the caller's report")
	}
	// A zero time stays null-like, never a fake date.
	empty, err := JSON(&result.WhyReport{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty, `"generatedAt": "0001-01-01T00:00:00Z"`) {
		t.Errorf("zero time changed shape:\n%s", empty)
	}
}

// fixtureReports builds every report the embedded scenarios can produce, the way the
// commands do, so the renderers are checked against the real shape of the data.
func fixtureReports(t *testing.T) (whys []*result.WhyReport, impacts []*result.ImpactReport, est *result.EstateReport) {
	t.Helper()
	for scenario, workload := range map[string]string{
		"oom-rollout-blocked": "deploy/checkout",
		"node-image-drift":    "deploy/api",
		"rollout-completed":   "deploy/cart",
	} {
		src, err := collect.NewFixtureSource(scenario)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := src.Snapshot(context.Background(), "", "payments")
		if err != nil {
			t.Fatal(err)
		}
		rep, err := cohort.Analyze(snap, cohort.Options{Workload: workload, Namespace: "payments", Now: src.Now()})
		if err != nil {
			t.Fatal(err)
		}
		whys = append(whys, rep)
	}
	src, err := collect.NewFixtureSource("estate")
	if err != nil {
		t.Fatal(err)
	}
	var snaps []*model.Snapshot
	for _, ctx := range []string{"aks-prod-weu", "onprem-int"} {
		snap, err := src.Snapshot(context.Background(), ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		snaps = append(snaps, snap)
	}
	raw, err := src.Extra("terraform/state.json")
	if err != nil {
		t.Fatal(err)
	}
	state, err := tfplan.ParseState(raw)
	if err != nil {
		t.Fatal(err)
	}
	states := []*tfplan.State{state}
	est, err = estate.Build(snaps, states, estate.Options{Now: src.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for _, sel := range []string{"pool=apps", "zone=2", "pool=nothing"} {
		rep, err := impact.Nodes(snaps[0], sel, impact.Options{Now: src.Now()})
		if err != nil {
			t.Fatal(err)
		}
		impacts = append(impacts, rep)
	}
	planRaw, err := src.Extra("terraform/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := tfplan.ParsePlan(planRaw)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := impact.Plan(snaps[0], plan, states, "fixtures:terraform/plan.json", impact.Options{Now: src.Now()})
	if err != nil {
		t.Fatal(err)
	}
	impacts = append(impacts, rep)
	return whys, impacts, est
}

func TestFixtureReportsRenderCleanly(t *testing.T) {
	whys, impacts, est := fixtureReports(t)
	type out struct {
		name  string
		short func(Options) string
		md    string
		json  any
	}
	var outs []out
	for _, r := range whys {
		r := r
		outs = append(outs, out{"why " + r.Workload, func(o Options) string { return WhyText(r, o) }, WhyMarkdown(r), r})
	}
	for _, r := range impacts {
		r := r
		outs = append(outs, out{"impact " + r.Source, func(o Options) string { return ImpactText(r, o) }, ImpactMarkdown(r), r})
	}
	outs = append(outs, out{"estate", func(o Options) string { return EstateText(est, o) }, EstateMarkdown(est), est})

	for _, o := range outs {
		t.Run(o.name, func(t *testing.T) {
			for _, w := range auditWidths {
				short := o.short(Options{Width: w})
				for _, line := range strings.Split(short, "\n") {
					if lipgloss.Width(line) > w {
						t.Errorf("width %d: short form line wider than the terminal: %q", w, line)
					}
				}
				if lineCount(short) >= maxShortLines {
					t.Errorf("width %d: short form is %d lines", w, lineCount(short))
				}
				full := o.short(Options{Width: w, Details: true})
				if !strings.HasPrefix(full, short[:strings.Index(short, "\n")]) {
					t.Errorf("width %d: details must open like the short form", w)
				}
				for name, got := range map[string]string{"short": short, "details": full, "markdown": o.md} {
					if strings.Contains(got, esc) {
						t.Errorf("width %d %s: escape sequence with colour off", w, name)
					}
					if strings.ContainsAny(got, "\u2014\u2013") {
						t.Errorf("width %d %s: long dash", w, name)
					}
					if forbiddenWord.MatchString(strings.ToLower(got)) {
						t.Errorf("width %d %s: forbidden word", w, name)
					}
					for _, line := range strings.Split(got, "\n") {
						if strings.HasSuffix(line, " ") {
							t.Errorf("width %d %s: trailing whitespace: %q", w, name, line)
						}
					}
				}
				coloured := o.short(Options{Width: w, Color: true, Details: true})
				if !strings.Contains(coloured, esc) {
					t.Errorf("width %d: no colour with Color on", w)
				}
			}
			if _, err := JSON(o.json); err != nil {
				t.Errorf("JSON: %v", err)
			}
		})
	}
}
