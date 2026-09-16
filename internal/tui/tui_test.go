package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arnocho/spanline/internal/result"
)

const (
	testWidth  = 100
	testHeight = 40
)

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

func stripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

// render sends a window size and returns the plain text of the model's view.
func render(m tea.Model, width, height int) string {
	sized, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return stripANSI(sized.View())
}

// send feeds messages to a model in order and returns the resulting model.
func send(m tea.Model, msgs ...tea.Msg) tea.Model {
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	return m
}

func runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func keyType(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func mustContain(t *testing.T, view string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(view, w) {
			t.Errorf("view does not contain %q\n----- view -----\n%s", w, view)
		}
	}
}

func mustNotContain(t *testing.T, view string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(view, u) {
			t.Errorf("view unexpectedly contains %q\n----- view -----\n%s", u, view)
		}
	}
}

// paneColumn returns the left hand pane of a side by side view as one flat string, so that an
// assertion on a sentence the pane wrapped does not depend on where the wrap falls.
func paneColumn(view string) string {
	var b strings.Builder
	for _, line := range strings.Split(view, "\n") {
		if i := strings.Index(line, "││"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(strings.TrimSpace(strings.Trim(line, "│╭╮╰╯─")))
		b.WriteString(" ")
	}
	return b.String()
}

var testStamp = time.Date(2026, 9, 16, 9, 12, 0, 0, time.UTC)

func estateFixture() *result.EstateReport {
	return &result.EstateReport{
		Clusters: []result.ClusterSummary{{
			Context:       "prod-eu",
			Nodes:         6,
			NodesNotReady: 1,
			Pods:          120,
			Namespaces:    9,
			Workloads:     41,
			Risks:         2,
			Outages:       1,
			CollectedAt:   testStamp,
		}},
		Pools: []result.PoolSummary{
			{
				Context:          "prod-eu",
				Pool:             "pool-blue",
				Nodes:            3,
				TerraformAddress: "module.eks.pool_blue",
				StateFile:        "live/prod/terraform.tfstate",
				CPUPercent:       62.5,
				MemPercent:       48.25,
				Workloads:        18,
				AtRisk:           1,
				Zones:            "eu-west-1a",
			},
			{
				Context:    "prod-eu",
				Pool:       "pool-green",
				Nodes:      3,
				CPUPercent: 12,
				MemPercent: 20,
				Workloads:  7,
			},
		},
		Risks: []result.Finding{{
			Severity: result.Outage,
			Kind:     "single-pool",
			Object:   "deploy/api",
			Reason:   "all replicas on one pool",
			Evidence: []string{"pods on pool-blue 3 of 3"},
		}},
		States: []result.StateSummary{{
			Path: "live/prod/terraform.tfstate", Resources: 42, NodePools: 2, Clusters: 1, Matched: 1, Unmatched: 1,
		}},
		Gaps:        []string{"metrics server not reachable"},
		GeneratedAt: testStamp,
	}
}

func whyFixture() *result.WhyReport {
	return &result.WhyReport{
		Context:     "prod-eu",
		Namespace:   "payments",
		Workload:    "deploy/api",
		Mode:        result.ModeCohort,
		CohortKey:   "node.pool",
		OnsetAt:     testStamp,
		OnsetSignal: "CrashLoopBackOff on 3 pods",
		Failing:     result.Cohort{Pods: []string{"api-1", "api-2"}, Count: 2, Sample: "api-1"},
		Healthy:     result.Cohort{Pods: []string{"api-3"}, Count: 1, Sample: "api-3"},
		Dimensions: []result.Dimension{
			{Name: "node.pool", FailingValues: "pool-blue", HealthyValues: "pool-green", Separation: result.Total, Purity: 1},
			{Name: "image", FailingValues: "api:v7", HealthyValues: "api:v7", Separation: result.None, Purity: 0},
		},
		Suspects: []result.Suspect{{
			ID:          "tf-1729",
			Verdict:     result.Splits,
			At:          testStamp,
			Title:       "node pool taint changed",
			Dimension:   "node.pool",
			Diff:        []string{"+ taint dedicated=gpu", "- taint none", "+ label tier=batch"},
			Attribution: "state serial 88",
			Actor:       result.ActorPipeline,
			Evidence:    []string{"tfstate live/prod serial 88"},
		}},
		Gaps:        []string{"audit log not read"},
		GeneratedAt: testStamp,
	}
}

func impactFixture() *result.ImpactReport {
	return &result.ImpactReport{
		Context:     "prod-eu",
		Source:      "plan.json",
		SnapshotAt:  testStamp,
		ExpiresAt:   testStamp.Add(15 * time.Minute),
		Nodes:       []string{"ip-10-0-1-7"},
		PlanSHA:     "9f2c1ab7de40",
		PlanSummary: "replace 2 node pools",
		Mapping:     []string{"module.eks.pool_blue maps to pool-blue"},
		Findings: []result.Finding{
			{Severity: result.Risk, Kind: "pdb", Object: "pdb/api", Reason: "budget allows one eviction"},
			{Severity: result.Outage, Kind: "single-pool", Object: "deploy/api", Reason: "every replica on the drained pool",
				Evidence: []string{"replicas 3 of 3 on pool-blue"}},
			{Severity: result.Disruption, Kind: "topology", Object: "sts/ledger", Reason: "one zone left"},
		},
		Ignored:     []string{"job/backup"},
		Gaps:        []string{"no metrics for pool-green"},
		Verdict:     result.Outage,
		ExitCode:    3,
		GeneratedAt: testStamp,
	}
}

func TestEstateViewShowsHeaderClustersPoolsDetailAndRisks(t *testing.T) {
	view := render(newEstateModel(estateFixture()), testWidth, testHeight)

	mustContain(t, view,
		"spanline estate",
		"context prod-eu",
		"source 1 state file, 42 resources, 1 unmatched",
		"clusters and node pools",
		"detail",
		"risks",
		"prod-eu",
		"6 nodes",
		"pool-blue",
		"pool-green",
		"node pools in this cluster",
		"collected at",
		"OUTAGE",
		"deploy/api",
		"all replicas on one pool",
		"coverage gaps",
		"NOT ASSESSED",
		"metrics server not reachable",
		"tab",
		"quit",
	)
}

func TestEstateDetailShowsTerraformAddressForSelectedPool(t *testing.T) {
	m := send(newEstateModel(estateFixture()),
		tea.WindowSizeMsg{Width: testWidth, Height: testHeight},
		runes("/"), runes("blue"), keyType(tea.KeyEnter))

	view := stripANSI(m.View())
	mustContain(t, view, "filter blue", "pool-blue", "terraform address", "module.eks.pool_blue", "live/prod/terraform.tfstate")
	mustNotContain(t, view, "pool-green")
}

func TestEstateFilterWithoutMatchSaysSo(t *testing.T) {
	m := send(newEstateModel(estateFixture()),
		tea.WindowSizeMsg{Width: testWidth, Height: testHeight},
		runes("/"), runes("zzz"), keyType(tea.KeyEnter))

	// The list pane is the narrowest one, so the sentence wraps across its lines.
	mustContain(t, paneColumn(stripANSI(m.View())), "no cluster and no node pool matches the current filter")
}

func TestEstateRisksOnlyTogglesAndTabMovesFocus(t *testing.T) {
	base := send(newEstateModel(estateFixture()), tea.WindowSizeMsg{Width: testWidth, Height: testHeight})

	// The row marker is what disappears from the list. The selected cluster's detail still lists
	// every pool that cluster owns, which is what the report says.
	onlyRisks := stripANSI(send(base, runes("r")).View())
	mustContain(t, onlyRisks, "risks only on", "▸ pool-blue")
	mustNotContain(t, onlyRisks, "▸ pool-green")

	focused := stripANSI(send(base, keyType(tea.KeyTab)).View())
	mustContain(t, focused, "pane detail", "› detail")
}

func TestEstateExpandRowShowsPeekLines(t *testing.T) {
	m := send(newEstateModel(estateFixture()),
		tea.WindowSizeMsg{Width: testWidth, Height: testHeight},
		keyType(tea.KeyEnter))

	mustContain(t, stripANSI(m.View()), "▾ prod-eu", "120 pods")
}

func TestEstateEmptyReportSaysEveryListIsEmpty(t *testing.T) {
	view := render(newEstateModel(&result.EstateReport{GeneratedAt: testStamp}), testWidth, testHeight)

	mustContain(t, view,
		"no cluster in this snapshot",
		"no terraform state read",
		"no cluster and no node pool to detail in this snapshot",
		"no risk recorded in this snapshot",
		"no coverage gap recorded in this snapshot",
	)
	mustContain(t, paneColumn(view), "no cluster and no node pool in this snapshot")
}

func TestWhyViewShowsOnsetCohortsDimensionsAndSuspects(t *testing.T) {
	view := render(newWhyModel(whyFixture()), testWidth, testHeight)

	mustContain(t, view,
		"spanline why",
		"payments/deploy/api · mode cohort",
		"onset 2026-09-16 09:12 UTC",
		"signal CrashLoopBackOff on 3 pods",
		"cohort key node.pool",
		"failing cohort",
		"healthy cohort",
		"api-1",
		"api-3",
		"dimensions",
		"separation",
		"purity",
		"▶ node.pool",
		"TOTAL",
		"separates the two cohorts",
		"ranked suspects",
		"#1",
		"SPLITS",
		"node pool taint changed",
		"+ taint dedicated=gpu",
		"actor pipeline",
		"1 more diff line, enter to expand",
	)
}

func TestWhyViewNeverPrintsTheWordCause(t *testing.T) {
	view := strings.ToLower(render(newWhyModel(whyFixture()), testWidth, testHeight))
	mustNotContain(t, view, "cause")
}

func TestWhyExpandShowsEvidenceAndFullDiff(t *testing.T) {
	m := send(newWhyModel(whyFixture()),
		tea.WindowSizeMsg{Width: testWidth, Height: testHeight},
		keyType(tea.KeyEnter))

	mustContain(t, stripANSI(m.View()), "+ label tier=batch", "evidence tfstate live/prod serial 88")
}

func TestWhyEmptyReportSaysEveryListIsEmpty(t *testing.T) {
	view := render(newWhyModel(&result.WhyReport{GeneratedAt: testStamp}), testWidth, testHeight)

	mustContain(t, view,
		"no dimension compared in this snapshot",
		"no suspect recorded in this snapshot",
		"no coverage gap recorded in this snapshot",
	)
}

func TestWhyRevisionFallbackShowsRevisions(t *testing.T) {
	r := whyFixture()
	r.Mode = result.ModeRevision
	r.Dimensions = nil
	r.Revisions = []result.Revision{{Name: "api-7f9", Number: 12, Active: "yes", Signals: "restarts 9", Created: testStamp, Replicas: 3}}

	view := render(newWhyModel(r), testWidth, testHeight)
	mustContain(t, view, "controller revisions", "api-7f9", "signals restarts 9")
}

func TestImpactGroupsFindingsBySeverityAndPinsVerdict(t *testing.T) {
	view := render(newImpactModel(impactFixture()), testWidth, testHeight)

	mustContain(t, view,
		"spanline impact",
		"change under review",
		"plan sha",
		"9f2c1ab7de40",
		"replace 2 node pools",
		"findings by severity",
		"OUTAGE",
		"DISRUPTION",
		"RISK",
		"every replica on the drained pool",
		"verdict OUTAGE",
		"exit code 3",
	)

	outage := strings.Index(view, "OUTAGE")
	disruption := strings.Index(view, "DISRUPTION")
	risk := strings.Index(view, "RISK")
	if !(outage < disruption && disruption < risk) {
		t.Errorf("findings are not grouped highest severity first: OUTAGE=%d DISRUPTION=%d RISK=%d\n%s",
			outage, disruption, risk, view)
	}
}

func TestImpactEmptyReportSaysSo(t *testing.T) {
	view := render(newImpactModel(&result.ImpactReport{Context: "prod-eu", Verdict: result.NotAssessed, GeneratedAt: testStamp}), testWidth, testHeight)

	mustContain(t, view,
		"no finding recorded in this report",
		"no coverage gap recorded in this report",
		"verdict NOT ASSESSED",
		"exit code 0",
	)
}

func TestImpactNarrativeIsShownAsAttachedText(t *testing.T) {
	r := impactFixture()
	r.Narrative = &result.Narrative{Text: "The drained pool holds every replica.", Model: "test-model", Unverified: true}

	view := render(newImpactModel(r), testWidth, testHeight)
	mustContain(t, view, "narrative (model test-model, unverified)", "The drained pool holds every replica.")
}

func TestViewsDegradeBelowEightyColumns(t *testing.T) {
	cases := map[string]tea.Model{
		"estate": newEstateModel(estateFixture()),
		"why":    newWhyModel(whyFixture()),
		"impact": newImpactModel(impactFixture()),
	}
	for _, name := range []string{"estate", "impact", "why"} {
		m := cases[name]
		view := render(m, 60, 30)
		for _, line := range strings.Split(view, "\n") {
			if len([]rune(line)) > 60 {
				t.Fatalf("%s: line wider than 60 columns: %q", name, line)
			}
		}
		mustContain(t, view, "spanline "+name)
	}
}

func TestViewsSurviveTinyTerminals(t *testing.T) {
	for _, m := range []tea.Model{
		newEstateModel(estateFixture()),
		newWhyModel(whyFixture()),
		newImpactModel(impactFixture()),
	} {
		if got := render(m, 20, 6); got == "" {
			t.Fatal("view is empty at 20 columns")
		}
	}
}

func TestAvailableDoesNotPanic(t *testing.T) {
	_ = Available()
}

func TestRunRejectsNilReports(t *testing.T) {
	if err := RunEstate(nil); err == nil {
		t.Error("RunEstate(nil) should return an error")
	}
	if err := RunWhy(nil); err == nil {
		t.Error("RunWhy(nil) should return an error")
	}
	if err := RunImpact(nil); err == nil {
		t.Error("RunImpact(nil) should return an error")
	}
}
