package brief

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/arnocho/spanline/internal/cohort"
	"github.com/arnocho/spanline/internal/collect"
	"github.com/arnocho/spanline/internal/estate"
	"github.com/arnocho/spanline/internal/impact"
	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
	"github.com/arnocho/spanline/internal/tfplan"
)

var fixedTime = time.Date(2026, 3, 4, 9, 15, 0, 0, time.UTC)

// whyReport is a small cohort report with one clean split and one ranked change.
func whyReport() *result.WhyReport {
	return &result.WhyReport{
		Context: "prod-eu", Namespace: "payments", Workload: "deployment/checkout",
		Mode: result.ModeCohort, OnsetAt: fixedTime, OnsetSignal: "OOMKilled",
		Failing: result.Cohort{Count: 6}, Healthy: result.Cohort{Count: 4},
		Dimensions: []result.Dimension{
			{Name: "pod-template-hash", FailingValues: "7d9", HealthyValues: "6c1", Separation: result.Total, Purity: 1},
		},
		Suspects: []result.Suspect{
			{ID: "rs/checkout-7d9", Verdict: result.Splits, At: fixedTime, Title: "image bumped", Actor: result.ActorPipeline, Attribution: "argocd"},
		},
	}
}

func onlyLine(t *testing.T, lines []Line, label string) Line {
	t.Helper()
	for _, l := range lines {
		if l.Label == label {
			return l
		}
	}
	t.Fatalf("no key line labelled %q in %+v", label, lines)
	return Line{}
}

func TestImpactRiskVerdictIsNotAPass(t *testing.T) {
	r := &result.ImpactReport{
		Context: "prod-eu", Source: "nodes pool=apps", Verdict: result.Risk,
		Findings: []result.Finding{
			{Severity: result.Risk, Kind: "Deployment", Object: "payments/checkout", Reason: "this workload drops from 2 live pods to 1, so one more failure takes it out entirely"},
			{Severity: result.Risk, Kind: "Deployment", Object: "orders/orders", Reason: "this workload drops from 3 live pods to 1, so one more failure takes it out entirely"},
			{Severity: result.Info, Kind: "Simulation", Object: "pool=apps", Reason: "2 of 9 nodes go away"},
		},
	}
	b := Impact(r)
	if strings.Contains(b.Headline, "nothing in this snapshot breaks") {
		t.Fatalf("a RISK verdict reads as a pass: %q", b.Headline)
	}
	if b.Empty != "" {
		t.Errorf("Empty = %q, want nothing: two findings are at risk", b.Empty)
	}
	if b.Sev != result.Risk {
		t.Errorf("Sev = %q, want RISK", b.Sev)
	}
	if !strings.Contains(b.Headline, "2 things") || !strings.Contains(b.Headline, "at risk") {
		t.Errorf("headline must count what is at risk: %q", b.Headline)
	}
	if len(b.Key) != 2 || b.Key[0].Label != "risk" || b.Key[1].Label != "risk" {
		t.Errorf("the risk findings must be the key lines: %+v", b.Key)
	}
	if b.More != "1 info" {
		t.Errorf("More = %q, want the info finding counted", b.More)
	}
}

func TestImpactEmptyVerdictReadsNotAssessed(t *testing.T) {
	b := Impact(&result.ImpactReport{})
	if strings.Contains(b.Headline, "nothing in this snapshot breaks") {
		t.Fatalf("an absent verdict reads as a pass: %q", b.Headline)
	}
	if b.Sev != result.NotAssessed {
		t.Errorf("Sev = %q, want NOT ASSESSED", b.Sev)
	}
	if !strings.Contains(b.Headline, "not a pass") {
		t.Errorf("headline must say it is not a pass: %q", b.Headline)
	}
	if b.Empty != "" {
		t.Errorf("Empty = %q, want nothing: nothing was assessed", b.Empty)
	}
}

func TestImpactNotAssessedHeadlineCountsWhatWasNotAssessed(t *testing.T) {
	r := &result.ImpactReport{
		Verdict: result.NotAssessed,
		Findings: []result.Finding{
			{Severity: result.NotAssessed, Kind: "PersistentVolumeClaim", Object: "payments/ledger", Reason: "this claim has no spec.volumeName, so spanline cannot follow it to a PersistentVolume"},
			{Severity: result.Info, Kind: "Simulation", Object: "pool=apps", Reason: "2 of 9 nodes go away, 7 stay"},
		},
	}
	b := Impact(r)
	if strings.Contains(b.Headline, "nothing was simulated") {
		t.Fatalf("the simulation ran, the headline must not deny it: %q", b.Headline)
	}
	if !strings.Contains(b.Headline, "1 thing could not be assessed") || !strings.Contains(b.Headline, "not a pass") {
		t.Errorf("headline = %q, want the count of what was not assessed and that it is not a pass", b.Headline)
	}

	// Nothing at all was simulated: the report holds no finding.
	none := Impact(&result.ImpactReport{Verdict: result.NotAssessed})
	if !strings.Contains(none.Headline, "nothing was assessed") || !strings.Contains(none.Headline, "not a pass") {
		t.Errorf("headline = %q, want it to say nothing was assessed and that it is not a pass", none.Headline)
	}
}

func TestImpactMoreNamesHiddenFindingsOfTheVerdictSeverity(t *testing.T) {
	r := &result.ImpactReport{Verdict: result.Outage}
	for _, o := range []string{"a/a", "b/b", "c/c", "d/d", "e/e"} {
		r.Findings = append(r.Findings, result.Finding{Severity: result.Outage, Kind: "Deployment", Object: o, Reason: "every live pod stands on a node that goes away, so it drops from 2 to 0"})
	}
	r.Findings = append(r.Findings, result.Finding{Severity: result.Info, Kind: "Simulation", Object: "pool=apps", Reason: "5 of 9 nodes go away"})
	b := Impact(r)
	if len(b.Key) != 3 {
		t.Fatalf("want 3 outage key lines, got %d", len(b.Key))
	}
	if !strings.HasPrefix(b.More, "2 more outage") {
		t.Errorf("More = %q, want the two hidden outages named first", b.More)
	}
	if strings.HasSuffix(strings.TrimSpace(b.More), ",") {
		t.Errorf("More ends with a stray comma: %q", b.More)
	}
}

func TestEstateNotAssessedOnlyIsNotAPass(t *testing.T) {
	r := &result.EstateReport{
		Clusters: []result.ClusterSummary{{Context: "prod-eu", Nodes: 3, Pods: 10}},
		Pools:    []result.PoolSummary{{Context: "prod-eu", Pool: "system", Nodes: 3}},
		Risks: []result.Finding{
			{Severity: result.NotAssessed, Kind: "NodePool", Object: "system", Reason: "no state read declares a node pool of this name, so who owns these nodes was not assessed", Context: "prod-eu"},
			{Severity: result.NotAssessed, Kind: "PersistentVolumeClaim", Object: "data/kafka-0", Reason: "the volume this claim is bound to was not read, so whether it pins the pod to one zone is unknown", Context: "prod-eu"},
		},
	}
	b := Estate(r)
	if b.Headline == "nothing fragile in what was read" {
		t.Fatalf("two findings were not assessed, the headline reads as a pass: %q", b.Headline)
	}
	if b.Empty != "" {
		t.Errorf("Empty = %q, want nothing", b.Empty)
	}
	if b.Sev != result.NotAssessed {
		t.Errorf("Sev = %q, want NOT ASSESSED", b.Sev)
	}
	if !strings.Contains(b.Headline, "2 things could not be assessed") {
		t.Errorf("headline = %q, want the count of what was not assessed", b.Headline)
	}
	if len(b.Key) != 2 {
		t.Errorf("both findings must be key lines, got %+v", b.Key)
	}
}

func TestCutKeepsRunesWhole(t *testing.T) {
	r := whyReport()
	r.Dimensions[0].FailingValues = strings.Repeat("é", 40)
	r.Dimensions[0].HealthyValues = strings.Repeat("e\u0301", 30)
	line := onlyLine(t, Why(r).Key, "separates them")
	if !utf8.ValidString(line.Note) {
		t.Fatalf("the note is not valid UTF-8: %q", line.Note)
	}
	failing, rest, ok := strings.Cut(line.Note, "... on the failing side, ")
	if !ok {
		t.Fatalf("note = %q, want the failing value shortened with an ellipsis", line.Note)
	}
	if utf8.RuneCountInString(failing) != 33 {
		t.Errorf("failing side keeps %d runes, want 33", utf8.RuneCountInString(failing))
	}
	healthy, _, ok := strings.Cut(rest, "... on the healthy one")
	if !ok {
		t.Fatalf("note = %q, want the healthy value shortened with an ellipsis", line.Note)
	}
	if !strings.HasSuffix(healthy, "e\u0301") {
		t.Errorf("the cut stranded a base letter from its combining mark: %q", healthy)
	}
	// A short value is returned untouched, and an ASCII value is cut where it always was.
	if got := cut("short", 34); got != "short" {
		t.Errorf("cut(short) = %q", got)
	}
	if got := cut(strings.Repeat("a", 40), 34); got != strings.Repeat("a", 33)+"..." {
		t.Errorf("cut(ascii) = %q", got)
	}
}

func TestWhyMoreDoesNotSayRankedLowerWithoutATopChange(t *testing.T) {
	r := whyReport()
	r.Suspects = []result.Suspect{
		{ID: "cm/flags", Verdict: result.NoSplit, At: fixedTime, Title: "flag flipped", Actor: result.ActorHuman},
		{ID: "hpa/checkout", Verdict: result.NoSplit, At: fixedTime, Title: "max replicas raised", Actor: result.ActorController},
	}
	b := Why(r)
	if strings.Contains(b.More, "ranked lower") {
		t.Fatalf("nothing is shown above them, More = %q", b.More)
	}
	if !strings.Contains(b.More, "2 changes ranked, none matched") {
		t.Errorf("More = %q, want the changes counted and their verdict stated", b.More)
	}
	for _, l := range b.Key {
		if l.Label == "the change" || l.Label == "closest change" {
			t.Errorf("a NO-SPLIT change was promoted to a key line: %+v", l)
		}
	}
}

func TestWhyMoreNamesHeldBackDiffLines(t *testing.T) {
	r := whyReport()
	r.Suspects[0].Diff = []string{"d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"}
	b := Why(r)
	if len(b.Key) > 6 {
		t.Fatalf("%d key lines, want at most 6", len(b.Key))
	}
	shown := 0
	for _, l := range b.Key {
		if strings.HasPrefix(l.Value, "d") && l.Ref == "rs/checkout-7d9" {
			shown++
		}
	}
	if shown != 3 {
		t.Fatalf("%d diff lines shown, want 3", shown)
	}
	if !strings.Contains(b.More, "5 more diff lines") {
		t.Errorf("More = %q, want the five held back diff lines named", b.More)
	}
	// A single held back line is singular.
	r.Suspects[0].Diff = []string{"d1", "d2", "d3", "d4"}
	if b := Why(r); !strings.Contains(b.More, "1 more diff line,") && !strings.HasSuffix(b.More, "1 more diff line") {
		t.Errorf("More = %q, want one held back diff line in the singular", b.More)
	}
}

func TestWhyDiffLabelIsNotRepeated(t *testing.T) {
	r := whyReport()
	r.Suspects[0].Diff = []string{"d1", "d2", "d3"}
	labelled := 0
	for _, l := range Why(r).Key {
		if l.Label == "it changed" {
			labelled++
		}
	}
	if labelled != 1 {
		t.Errorf("the diff label appears %d times, want once", labelled)
	}
}

func TestWhyPrefersASplitsChangeOverATemporalOne(t *testing.T) {
	r := whyReport()
	r.Suspects = []result.Suspect{
		{ID: "cm/flags", Verdict: result.Temporal, At: fixedTime, Title: "flag flipped", Actor: result.ActorHuman},
		{ID: "rs/checkout-7d9", Verdict: result.Splits, At: fixedTime, Title: "image bumped", Actor: result.ActorPipeline},
		{ID: "np/batch", Verdict: result.NoSplit, At: fixedTime, Title: "pool scaled", Actor: result.ActorPipeline},
	}
	b := Why(r)
	top := onlyLine(t, b.Key, "the change")
	if top.Value != "image bumped" {
		t.Errorf("top change = %q, want the SPLITS one", top.Value)
	}
	for _, l := range b.Key {
		if l.Label == "closest change" {
			t.Errorf("the TEMPORAL change was promoted next to the SPLITS one: %+v", l)
		}
	}
	if !strings.Contains(b.More, "2 changes ranked lower") {
		t.Errorf("More = %q, want the other two counted", b.More)
	}
}

func TestWhyChangeNoteHasNoStrayComma(t *testing.T) {
	r := whyReport()
	r.Suspects[0].Attribution = ""
	r.Suspects[0].Actor = ""
	line := onlyLine(t, Why(r).Key, "the change")
	if line.Note != "09:15" {
		t.Errorf("note = %q, want just the time", line.Note)
	}
	r.Suspects[0].At = time.Time{}
	r.Suspects[0].Actor = result.ActorPipeline
	if line := onlyLine(t, Why(r).Key, "the change"); line.Note != "unknown, pipeline" {
		t.Errorf("note = %q, want the unknown time and the actor", line.Note)
	}
}

func TestWhyHeadlineGrammarAtEdgeCounts(t *testing.T) {
	r := whyReport()
	r.Failing.Count, r.Healthy.Count = 1, 1
	if got := Why(r).Headline; !strings.HasPrefix(got, "1 of 2 pods of checkout fails,") {
		t.Errorf("headline = %q, want a singular verb for one pod", got)
	}
	r.Failing.Count, r.Healthy.Count = 6, 4
	if got := Why(r).Headline; !strings.HasPrefix(got, "6 of 10 pods of checkout fail,") {
		t.Errorf("headline = %q, want a plural verb for six pods", got)
	}
	r.Dimensions = nil
	r.Failing.Count, r.Healthy.Count = 1, 0
	if got := Why(r).Headline; strings.Contains(got, "1 of 1 pods") {
		t.Errorf("headline = %q, want one pod in the singular", got)
	}

	empty := Why(&result.WhyReport{})
	if strings.Contains(empty.Headline, "of  is") || strings.Contains(empty.Headline, "  ") {
		t.Errorf("headline = %q, want no blank workload name", empty.Headline)
	}
	if empty.Sub != "" {
		t.Errorf("Sub = %q, want an empty scope for an empty report", empty.Sub)
	}

	slash := whyReport()
	slash.Workload = "payments/deployment/checkout"
	if got := Why(slash).Headline; !strings.Contains(got, "pods of checkout fail") {
		t.Errorf("headline = %q, want the bare workload name", got)
	}
}

func TestWhyPartialSeparationIsNamedHonestly(t *testing.T) {
	r := whyReport()
	r.Dimensions = []result.Dimension{
		{Name: "zone", FailingValues: "1, 2", HealthyValues: "1", Separation: result.None, Purity: 0},
		{Name: "node name", FailingValues: "node-a, node-b", HealthyValues: "node-a", Separation: result.Partial, Purity: 0.5},
		{Name: "node pool", FailingValues: "batch", HealthyValues: "batch, general", Separation: result.Partial, Purity: 0.4},
	}
	b := Why(r)
	if strings.Contains(b.Headline, "nothing collected tells them apart") {
		t.Fatalf("a PARTIAL split exists, the headline denies it: %q", b.Headline)
	}
	if !strings.Contains(b.Headline, "node name") || !strings.Contains(b.Headline, "partly tells them apart") {
		t.Errorf("headline = %q, want the partial dimension named as partial", b.Headline)
	}
	if strings.Contains(b.Headline, "and node name tells them apart") {
		t.Errorf("headline = %q claims a clean split for a PARTIAL dimension", b.Headline)
	}
	line := onlyLine(t, b.Key, "partly separates them")
	if line.Value != "node name" || line.Sev != result.Risk {
		t.Errorf("key line = %+v, want the partial dimension at RISK", line)
	}
	if !strings.Contains(b.More, "1 other dimension differs") {
		t.Errorf("More = %q, want the other partial dimension counted, in the singular", b.More)
	}
	for _, l := range b.Key {
		if l.Label == "separates them" {
			t.Errorf("a PARTIAL dimension was presented as a clean split: %+v", l)
		}
	}
}

func TestWhyRevisionModeWithoutRevisions(t *testing.T) {
	r := whyReport()
	r.Mode = result.ModeRevision
	r.Healthy.Count = 0
	r.Dimensions = nil
	r.Revisions = nil
	r.Suspects[0].Verdict = result.Temporal
	b := Why(r)
	if !strings.HasPrefix(b.Headline, "every pod of checkout is failing") {
		t.Errorf("headline = %q", b.Headline)
	}
	for _, l := range b.Key {
		if l.Label == "comparing" {
			t.Errorf("no revision is retained, nothing is being compared: %+v", l)
		}
	}
	if b.Sev != result.Outage {
		t.Errorf("Sev = %q, want OUTAGE", b.Sev)
	}
}

func TestNoteNeverReturnsEmpty(t *testing.T) {
	cases := map[string]string{
		"nothing is ready while 1 is wanted, so this workload serves no traffic at all":        "this workload serves no traffic at all",
		"this pod is covered by 2 budgets at once, which is a documented reason for a refusal": "which is a documented reason for a refusal",
		"one replica only, so ": "one replica only, so",
		"plain reason":          "plain reason",
		"":                      "",
	}
	for reason, want := range cases {
		if got := Note(reason); got != want {
			t.Errorf("Note(%q) = %q, want %q", reason, got, want)
		}
		if reason != "" && Note(reason) == "" {
			t.Errorf("Note(%q) returned nothing", reason)
		}
	}
}

func TestSubIsEmptyWhenTheScopeIs(t *testing.T) {
	if got := Impact(&result.ImpactReport{}).Sub; got != "" {
		t.Errorf("impact Sub = %q, want empty", got)
	}
	if got := Impact(&result.ImpactReport{Context: "prod-eu"}).Sub; got != "prod-eu" {
		t.Errorf("impact Sub = %q, want the context alone", got)
	}
	if got := Why(&result.WhyReport{Context: "prod-eu", Workload: "deployment/x"}).Sub; got != "prod-eu  deployment/x" {
		t.Errorf("why Sub = %q, want the two set parts joined", got)
	}
}

func TestShortTimeMatchesTheReportClock(t *testing.T) {
	r := whyReport()
	paris := time.FixedZone("paris", 2*3600)
	r.OnsetAt = time.Date(2026, 3, 4, 11, 15, 0, 0, paris) // 09:15 UTC
	r.Suspects[0].At = r.OnsetAt
	if got := onlyLine(t, Why(r).Key, "started").Value; got != "09:15" {
		t.Errorf("started = %q, want the UTC clock the details print", got)
	}
	if got := onlyLine(t, Why(r).Key, "the change").Note; !strings.HasPrefix(got, "09:15,") {
		t.Errorf("change note = %q, want the UTC clock", got)
	}
}

func TestPrioritiesToleratesAnyLimit(t *testing.T) {
	r := &result.EstateReport{Risks: []result.Finding{
		{Severity: result.Risk, Object: "a"}, {Severity: result.Outage, Object: "b"}, {Severity: result.Info, Object: "c"},
	}}
	if got := Priorities(r, -1); len(got) != 0 {
		t.Errorf("negative limit returned %d lines", len(got))
	}
	if got := Priorities(r, 0); len(got) != 0 {
		t.Errorf("zero limit returned %d lines", len(got))
	}
	got := Priorities(r, 10)
	if len(got) != 2 || got[0].Value != "b" || got[1].Value != "a" {
		t.Errorf("want the outage first and no info line, got %+v", got)
	}
	if Priorities(nil, 3) != nil {
		t.Errorf("nil report must give nil")
	}
}

// The fixtures are the only reports that are the shape of a real run. Every brief built
// from them must read as a sentence: no odd plural, no blank, no dash, no word we never print.
func TestBriefsFromFixturesReadAsSentences(t *testing.T) {
	briefs := map[string]Brief{}

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
		briefs["why "+scenario] = Why(rep)
	}

	est, err := collect.NewFixtureSource("estate")
	if err != nil {
		t.Fatal(err)
	}
	var snaps []*model.Snapshot
	for _, ctx := range []string{"aks-prod-weu", "onprem-int"} {
		snap, err := est.Snapshot(context.Background(), ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		snaps = append(snaps, snap)
	}
	raw, err := est.Extra("terraform/state.json")
	if err != nil {
		t.Fatal(err)
	}
	state, err := tfplan.ParseState(raw)
	if err != nil {
		t.Fatal(err)
	}
	states := []*tfplan.State{state}
	erep, err := estate.Build(snaps, states, estate.Options{Now: est.Now()})
	if err != nil {
		t.Fatal(err)
	}
	briefs["estate"] = Estate(erep)

	for _, sel := range []string{"pool=apps", "zone=2", "pool=nothing"} {
		irep, err := impact.Nodes(snaps[0], sel, impact.Options{Now: est.Now()})
		if err != nil {
			t.Fatal(err)
		}
		briefs["impact "+sel] = Impact(irep)
	}
	planRaw, err := est.Extra("terraform/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := tfplan.ParsePlan(planRaw)
	if err != nil {
		t.Fatal(err)
	}
	prep, err := impact.Plan(snaps[0], plan, states, "fixtures:terraform/plan.json", impact.Options{Now: est.Now()})
	if err != nil {
		t.Fatal(err)
	}
	briefs["impact plan"] = Impact(prep)

	oddPlural := regexp.MustCompile(`\b1 (workloads|things|changes|dimensions|gaps|findings|pods|clusters|pools|lines)\b`)
	oddSingular := regexp.MustCompile(`\b(0|[2-9]|\d{2,}) (workload|thing|change|dimension|gap|finding|pod|cluster|pool|line)\b`)
	for name, b := range briefs {
		texts := []string{b.Headline, b.Sub, b.More, b.Empty}
		for _, l := range b.Key {
			texts = append(texts, l.Label, l.Value, l.Note)
		}
		if strings.TrimSpace(b.Headline) == "" {
			t.Errorf("%s: no headline", name)
		}
		if len(b.Key) > 6 {
			t.Errorf("%s: %d key lines, want at most 6", name, len(b.Key))
		}
		if strings.HasSuffix(strings.TrimSpace(b.More), ",") {
			t.Errorf("%s: More ends with a comma: %q", name, b.More)
		}
		for _, l := range b.Key {
			if strings.TrimSpace(l.Value) == "" {
				t.Errorf("%s: key line %q has no value", name, l.Label)
			}
			if strings.HasSuffix(strings.TrimSpace(l.Note), ",") {
				t.Errorf("%s: note ends with a comma: %q", name, l.Note)
			}
		}
		for _, s := range texts {
			if strings.ContainsAny(s, "\u2014\u2013") {
				t.Errorf("%s: long dash in %q", name, s)
			}
			if strings.Contains(strings.ToLower(s), "cause") {
				t.Errorf("%s: forbidden word in %q", name, s)
			}
			if strings.Contains(s, "  ") && s != b.Sub {
				t.Errorf("%s: double space in %q", name, s)
			}
			if oddPlural.MatchString(s) {
				t.Errorf("%s: plural after one in %q", name, s)
			}
			if oddSingular.MatchString(s) {
				t.Errorf("%s: singular after a count in %q", name, s)
			}
			if !utf8.ValidString(s) {
				t.Errorf("%s: invalid UTF-8 in %q", name, s)
			}
		}
	}

	// The revision fallback fixture leads with the fact that nothing healthy is left.
	if got := briefs["why rollout-completed"].Headline; !strings.HasPrefix(got, "every pod of cart is failing") {
		t.Errorf("rollout-completed headline = %q", got)
	}
	// A selector that matches nothing is NOT ASSESSED and must never read as a pass.
	if b := briefs["impact pool=nothing"]; b.Sev != result.NotAssessed || !strings.Contains(b.Headline, "not a pass") || b.Empty != "" {
		t.Errorf("pool=nothing brief reads as a pass: %+v", b)
	}
}
