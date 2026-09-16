package render

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arnocho/spanline/internal/brief"
	"github.com/arnocho/spanline/internal/result"
)

// TestMain asks the terminal library to colour even though the test binary is not a tty,
// so the colour path is exercised rather than silently skipped on every machine.
func TestMain(m *testing.M) {
	os.Unsetenv("NO_COLOR")
	os.Setenv("CLICOLOR_FORCE", "1")
	os.Exit(m.Run())
}

const esc = "\x1b"

// maxShortLines is the contract of the default screen: it fits on one, with room to spare.
const maxShortLines = 25

var fixedTime = time.Date(2026, 3, 4, 9, 15, 0, 0, time.UTC)

// shortOptions is the default a terminal gets: the answer, and nothing else.
func shortOptions() Options { return Options{Color: false, Width: 100} }

// detailOptions is what --details asks for: the same screen, then everything under it.
func detailOptions() Options { return Options{Color: false, Width: 100, Details: true} }

func lineCount(s string) int {
	return len(strings.Split(strings.TrimRight(s, "\n"), "\n"))
}

func whyFixture() *result.WhyReport {
	return &result.WhyReport{
		Context:     "prod-eu",
		Namespace:   "payments",
		Workload:    "checkout",
		Mode:        result.ModeCohort,
		CohortKey:   "pod-template-hash",
		OnsetAt:     fixedTime,
		OnsetSignal: "readiness flipped on 4 of 9 pods",
		Failing:     result.Cohort{Pods: []string{"checkout-7d9-aaa", "checkout-7d9-bbb"}, Count: 2, Sample: "checkout-7d9-aaa"},
		Healthy:     result.Cohort{Pods: []string{"checkout-6c1-zzz"}, Count: 1, Sample: "checkout-6c1-zzz"},
		Dimensions: []result.Dimension{
			{Name: "pod-template-hash", FailingValues: "7d9", HealthyValues: "6c1", Separation: result.Total, Purity: 1},
			{Name: "node", FailingValues: "node-a, node-b", HealthyValues: "node-a", Separation: result.Partial, Purity: 0.5},
			{Name: "zone", FailingValues: "unresolved", HealthyValues: "unresolved", Separation: result.Unresolved, Purity: 0},
		},
		Suspects: []result.Suspect{
			{
				ID: "rs/checkout-7d9", Verdict: result.Splits, At: fixedTime, Title: "image bumped to v2.3.1",
				Dimension: "pod-template-hash", Actor: result.ActorPipeline, Attribution: "argocd",
				Diff:     []string{"- image: registry/checkout:v2.3.0", "+ image: registry/checkout:v2.3.1"},
				Evidence: []string{"replicaset.spec.template.spec.containers[0].image"},
			},
			{
				ID: "cm/checkout-flags", Verdict: result.NoSplit, At: fixedTime, Title: "flag flipped",
				Actor: result.ActorHuman, Evidence: []string{"configmap.metadata.managedFields[0].time"},
			},
		},
		Gaps:        []string{"metrics-server not reachable, node pressure not assessed"},
		GeneratedAt: fixedTime,
	}
}

func impactFixture() *result.ImpactReport {
	return &result.ImpactReport{
		Context:    "prod-eu",
		Source:     "tfplan (sha 9f2c)",
		SnapshotAt: fixedTime,
		ExpiresAt:  fixedTime.Add(10 * time.Minute),
		Nodes:      []string{"node-a", "node-b"},
		PlanSHA:    "9f2c11",
		Mapping:    []string{"module.gke.node_pool[\"batch\"] -> node-a, node-b"},
		Findings: []result.Finding{
			{Severity: result.Outage, Kind: "pdb", Object: "payments/checkout", Reason: "every replica sits on the drained pool", Evidence: []string{"pdb.status.currentHealthy"}},
			{Severity: result.NotAssessed, Kind: "pvc", Object: "payments/ledger", Reason: "volume topology not readable", Context: "storage class not listed"},
		},
		Ignored:     []string{"kube-system, excluded by default"},
		Gaps:        []string{"no metrics for the batch pool"},
		Verdict:     result.Outage,
		ExitCode:    3,
		GeneratedAt: fixedTime,
	}
}

func estateFixture() *result.EstateReport {
	return &result.EstateReport{
		Clusters: []result.ClusterSummary{
			{Context: "prod-eu", Nodes: 12, NodesNotReady: 1, Pods: 240, Namespaces: 9, Workloads: 40, Risks: 3, Outages: 1, CollectedAt: fixedTime},
		},
		Pools: []result.PoolSummary{
			{Context: "prod-eu", Pool: "batch", Nodes: 4, TerraformAddress: "module.gke.node_pool[\"batch\"]", StateFile: "env/prod/terraform.tfstate", CPUPercent: 61.5, MemPercent: 72, Workloads: 8, AtRisk: 2, Zones: "eu-west-1a"},
			{Context: "prod-eu", Pool: "orphan", Nodes: 2, CPUPercent: 10, MemPercent: 12, Workloads: 1},
		},
		Risks: []result.Finding{
			{Severity: result.Risk, Kind: "pdb", Object: "payments/ledger", Reason: "single replica"},
			{Severity: result.Outage, Kind: "node", Object: "node-c", Reason: "not ready for 3 collections", Evidence: []string{"node.status.conditions[Ready]"}},
			{Severity: result.Risk, Kind: "pool", Object: "orphan", Reason: "no terraform address matched"},
		},
		States:      []result.StateSummary{{Path: "env/prod/terraform.tfstate", Resources: 120, NodePools: 3, Clusters: 1, Matched: 2, Unmatched: 1}},
		Gaps:        []string{"one pool has no terraform owner"},
		GeneratedAt: fixedTime,
	}
}

func doctorFixture() *result.DoctorReport {
	return &result.DoctorReport{
		Context:  "prod-eu",
		Identity: "system:serviceaccount:ops:spanline",
		Profile:  "read-only",
		Level:    2,
		Checks: []result.Check{
			{Name: "kubectl", Status: "ok", Detail: "v1.31.2"},
			{Name: "secrets", Status: "denied", Detail: "get secrets refused, as expected"},
			{Name: "metrics", Status: "skipped", Detail: "metrics-server absent"},
		},
		Egress:      []string{"none, every call stays on the configured api server"},
		AuditFoot:   []string{"get and list on pods, nodes, replicasets"},
		Gaps:        []string{"no permission to list pdbs in kube-system"},
		GeneratedAt: fixedTime,
	}
}

// The realistic fixtures are the size of a real incident, which is the size that made the
// old output unreadable: eleven dimensions, six ranked changes, a dozen findings, three
// clusters. The short screen must survive them, not just the small fixtures above.

func realisticWhy() *result.WhyReport {
	dims := []result.Dimension{
		{Name: "pod-template-hash", FailingValues: "7d9", HealthyValues: "6c1", Separation: result.Total, Purity: 1},
		{Name: "node", FailingValues: "node-a, node-b, node-c", HealthyValues: "node-d, node-e", Separation: result.Partial, Purity: 0.62},
		{Name: "zone", FailingValues: "eu-west-1a, eu-west-1b", HealthyValues: "eu-west-1a", Separation: result.Partial, Purity: 0.5},
		{Name: "image", FailingValues: "registry/checkout:v2.3.1", HealthyValues: "registry/checkout:v2.3.0", Separation: result.Total, Purity: 1},
		{Name: "node pool", FailingValues: "batch", HealthyValues: "batch, general", Separation: result.Partial, Purity: 0.4},
		{Name: "configmap version", FailingValues: "8841", HealthyValues: "8841", Separation: result.None, Purity: 0},
		{Name: "secret version", FailingValues: "unresolved", HealthyValues: "unresolved", Separation: result.Unresolved, Purity: 0},
		{Name: "service account", FailingValues: "checkout", HealthyValues: "checkout", Separation: result.None, Purity: 0},
		{Name: "resource limits", FailingValues: "512Mi", HealthyValues: "512Mi", Separation: result.None, Purity: 0},
		{Name: "kernel", FailingValues: "5.15.0-1052", HealthyValues: "5.15.0-1052", Separation: result.None, Purity: 0},
		{Name: "runtime class", FailingValues: "unresolved", HealthyValues: "unresolved", Separation: result.Unresolved, Purity: 0},
	}
	suspects := []result.Suspect{
		{
			ID: "rs/checkout-7d9", Verdict: result.Splits, At: fixedTime, Title: "image bumped to v2.3.1",
			Dimension: "pod-template-hash", Actor: result.ActorPipeline, Attribution: "argocd",
			Diff: []string{
				"- image: registry/checkout:v2.3.0",
				"+ image: registry/checkout:v2.3.1",
				"- memory: 512Mi",
				"+ memory: 384Mi",
			},
			Evidence: []string{"replicaset.spec.template.spec.containers[0].image", "replicaset.metadata.creationTimestamp"},
		},
		{
			ID: "cm/checkout-flags", Verdict: result.Temporal, At: fixedTime.Add(-4 * time.Minute), Title: "feature flag rollout widened",
			Actor: result.ActorHuman, Evidence: []string{"configmap.metadata.managedFields[0].time"},
		},
		{
			ID: "hpa/checkout", Verdict: result.Temporal, At: fixedTime.Add(-9 * time.Minute), Title: "max replicas raised to 30",
			Actor: result.ActorController, Evidence: []string{"horizontalpodautoscaler.spec.maxReplicas"},
		},
		{
			ID: "np/batch", Verdict: result.NoSplit, At: fixedTime.Add(-40 * time.Minute), Title: "node pool scaled to 6",
			Actor: result.ActorPipeline, Attribution: "terraform apply", Evidence: []string{"nodepool.spec.replicas"},
		},
		{
			ID: "secret/checkout-db", Verdict: result.NoSplit, At: fixedTime.Add(-90 * time.Minute), Title: "database password rotated",
			Actor: result.ActorHuman, Evidence: []string{"secret.metadata.resourceVersion"},
		},
		{
			ID: "svc/checkout", Verdict: result.Unknown, At: time.Time{}, Title: "service annotations edited",
			Actor: result.ActorUnknown, Evidence: []string{"service.metadata.annotations"},
		},
	}
	return &result.WhyReport{
		Context: "prod-eu", Namespace: "payments", Workload: "deployment/checkout",
		Mode: result.ModeCohort, CohortKey: "pod-template-hash",
		OnsetAt: fixedTime, OnsetSignal: "Unhealthy on 7 of 12 pods",
		Failing: result.Cohort{
			Pods:  []string{"checkout-7d9-aaa", "checkout-7d9-bbb", "checkout-7d9-ccc", "checkout-7d9-ddd", "checkout-7d9-eee", "checkout-7d9-fff", "checkout-7d9-ggg"},
			Count: 7, Sample: "checkout-7d9-aaa",
		},
		Healthy: result.Cohort{
			Pods:  []string{"checkout-6c1-www", "checkout-6c1-xxx", "checkout-6c1-yyy", "checkout-6c1-zzz", "checkout-6c1-vvv"},
			Count: 5, Sample: "checkout-6c1-zzz",
		},
		Dimensions: dims,
		Suspects:   suspects,
		Gaps: []string{
			"metrics-server not reachable, node pressure not assessed",
			"no permission to read secrets, secret drift not compared",
			"audit log retention is 24h, changes older than that are invisible",
		},
		GeneratedAt: fixedTime,
	}
}

func realisticImpact() *result.ImpactReport {
	findings := []result.Finding{
		{Severity: result.Outage, Kind: "pdb", Object: "payments/checkout", Reason: "every replica sits on the drained pool", Evidence: []string{"pdb.status.currentHealthy", "pod.spec.nodeName"}},
		{Severity: result.Outage, Kind: "deployment", Object: "payments/ledger-api", Reason: "both replicas share one node", Evidence: []string{"pod.spec.nodeName"}},
		{Severity: result.Disruption, Kind: "pdb", Object: "search/indexer", Reason: "budget allows one disruption, two are needed", Evidence: []string{"pdb.spec.maxUnavailable"}},
		{Severity: result.Disruption, Kind: "statefulset", Object: "data/kafka", Reason: "two of five brokers move at once", Evidence: []string{"statefulset.status.readyReplicas"}},
		{Severity: result.Disruption, Kind: "deployment", Object: "web/storefront", Reason: "half the fleet is on the drained pool"},
		{Severity: result.Disruption, Kind: "job", Object: "batch/nightly-reconcile", Reason: "running job would be evicted"},
		{Severity: result.Risk, Kind: "hpa", Object: "payments/checkout", Reason: "remaining nodes cannot hold the scaled target"},
		{Severity: result.Risk, Kind: "pvc", Object: "data/kafka-2", Reason: "volume is zone bound to eu-west-1b"},
		{Severity: result.Risk, Kind: "affinity", Object: "search/indexer", Reason: "anti affinity leaves one schedulable node"},
		{Severity: result.NotAssessed, Kind: "pvc", Object: "payments/ledger", Reason: "volume topology not readable", Context: "storage class not listed"},
		{Severity: result.NotAssessed, Kind: "workload", Object: "kube-system/coredns", Reason: "not readable with this profile"},
		{Severity: result.Info, Kind: "deployment", Object: "web/docs", Reason: "spread across three pools, unaffected"},
	}
	return &result.ImpactReport{
		Context:     "prod-eu",
		Source:      "tfplan (sha 9f2c11)",
		SnapshotAt:  fixedTime,
		ExpiresAt:   fixedTime.Add(10 * time.Minute),
		Nodes:       []string{"node-a", "node-b", "node-c", "node-d", "node-e", "node-f"},
		PlanSHA:     "9f2c11",
		PlanSummary: "replace 6 nodes in module.gke.node_pool[\"batch\"]",
		Mapping: []string{
			"module.gke.node_pool[\"batch\"] -> node-a, node-b, node-c",
			"module.gke.node_pool[\"batch\"] -> node-d, node-e, node-f",
			"module.gke.cluster -> prod-eu",
		},
		Findings:    findings,
		Ignored:     []string{"kube-system, excluded by default", "monitoring, excluded by flag"},
		Gaps:        []string{"no metrics for the batch pool", "two namespaces not readable", "pdbs not listed in kube-system"},
		Verdict:     result.Outage,
		ExitCode:    3,
		GeneratedAt: fixedTime,
	}
}

func realisticEstate() *result.EstateReport {
	risks := []result.Finding{
		{Severity: result.Outage, Kind: "deployment", Object: "payments/checkout", Reason: "no ready replica for 3 collections", Evidence: []string{"deployment.status.readyReplicas"}},
		{Severity: result.Outage, Kind: "node", Object: "node-c", Reason: "not ready for 3 collections", Evidence: []string{"node.status.conditions[Ready]"}},
		{Severity: result.Disruption, Kind: "pdb", Object: "search/indexer", Reason: "budget already exhausted"},
		{Severity: result.Disruption, Kind: "statefulset", Object: "data/kafka", Reason: "one broker unscheduled for 2 collections"},
		{Severity: result.Risk, Kind: "pdb", Object: "payments/ledger", Reason: "single replica"},
		{Severity: result.Risk, Kind: "pool", Object: "orphan", Reason: "no terraform address matched"},
		{Severity: result.Risk, Kind: "pool", Object: "legacy", Reason: "no terraform address matched"},
		{Severity: result.Risk, Kind: "deployment", Object: "web/storefront", Reason: "all replicas in one zone"},
		{Severity: result.Risk, Kind: "hpa", Object: "search/indexer", Reason: "max replicas exceed pool capacity"},
		{Severity: result.Risk, Kind: "node", Object: "node-k", Reason: "memory pressure for 2 collections"},
		{Severity: result.NotAssessed, Kind: "namespace", Object: "kube-system", Reason: "not readable with this profile"},
		{Severity: result.NotAssessed, Kind: "namespace", Object: "monitoring", Reason: "not readable with this profile"},
		{Severity: result.Info, Kind: "deployment", Object: "web/docs", Reason: "spread across three pools"},
		{Severity: result.Info, Kind: "pool", Object: "general", Reason: "matched to terraform"},
	}
	pools := []result.PoolSummary{
		{Context: "prod-eu", Pool: "batch", Nodes: 6, TerraformAddress: "module.gke.node_pool[\"batch\"]", StateFile: "env/prod/terraform.tfstate", CPUPercent: 61.5, MemPercent: 72, Workloads: 8, AtRisk: 2, Zones: "eu-west-1a, eu-west-1b"},
		{Context: "prod-eu", Pool: "general", Nodes: 9, TerraformAddress: "module.gke.node_pool[\"general\"]", StateFile: "env/prod/terraform.tfstate", CPUPercent: 44, MemPercent: 51, Workloads: 22, AtRisk: 1, Zones: "eu-west-1a"},
		{Context: "prod-eu", Pool: "orphan", Nodes: 2, CPUPercent: 10, MemPercent: 12, Workloads: 1},
		{Context: "staging-eu", Pool: "general", Nodes: 3, TerraformAddress: "module.gke_staging.node_pool[\"general\"]", StateFile: "env/staging/terraform.tfstate", CPUPercent: 22, MemPercent: 30, Workloads: 11},
		{Context: "staging-eu", Pool: "spot", Nodes: 4, TerraformAddress: "module.gke_staging.node_pool[\"spot\"]", StateFile: "env/staging/terraform.tfstate", CPUPercent: 70, MemPercent: 65, Workloads: 6, AtRisk: 1},
		{Context: "dev-eu", Pool: "general", Nodes: 2, CPUPercent: 15, MemPercent: 19, Workloads: 5},
		{Context: "dev-eu", Pool: "legacy", Nodes: 1, CPUPercent: 5, MemPercent: 9, Workloads: 1},
	}
	return &result.EstateReport{
		Clusters: []result.ClusterSummary{
			{Context: "prod-eu", Nodes: 17, NodesNotReady: 1, Pods: 412, Namespaces: 14, Workloads: 96, Risks: 6, Outages: 2, CollectedAt: fixedTime},
			{Context: "staging-eu", Nodes: 7, Pods: 150, Namespaces: 9, Workloads: 41, Risks: 2, CollectedAt: fixedTime},
			{Context: "dev-eu", Nodes: 3, Pods: 48, Namespaces: 6, Workloads: 18, CollectedAt: fixedTime},
		},
		Pools: pools,
		Risks: risks,
		States: []result.StateSummary{
			{Path: "env/prod/terraform.tfstate", Resources: 320, NodePools: 3, Clusters: 1, Matched: 2, Unmatched: 1},
			{Path: "env/staging/terraform.tfstate", Resources: 140, NodePools: 2, Clusters: 1, Matched: 2, Unmatched: 0},
		},
		Gaps: []string{
			"two pools have no terraform owner",
			"kube-system not readable with this profile",
			"metrics-server absent on dev-eu",
		},
		GeneratedAt: fixedTime,
	}
}

// textRenderers is the table every text level assertion runs over.
func textRenderers() []struct {
	name   string
	render func(Options) string
} {
	return []struct {
		name   string
		render func(Options) string
	}{
		{"why", func(o Options) string { return WhyText(whyFixture(), o) }},
		{"impact", func(o Options) string { return ImpactText(impactFixture(), o) }},
		{"estate", func(o Options) string { return EstateText(estateFixture(), o) }},
		{"doctor", func(o Options) string { return DoctorText(doctorFixture(), o) }},
	}
}

func TestDefaultOptions(t *testing.T) {
	o := DefaultOptions()
	if o.Color {
		t.Errorf("Color = true, want false")
	}
	if o.Width != 100 {
		t.Errorf("Width = %d, want 100", o.Width)
	}
	if o.Compact {
		t.Errorf("Compact = true, want false")
	}
	if o.Details {
		t.Errorf("Details = true, want false: the short screen is the default")
	}
	if (Options{}).width() != 100 {
		t.Errorf("zero Width resolves to %d, want 100", (Options{}).width())
	}
}

// TestShortFormFitsOneScreen is the whole point of the short form: a realistic report, the
// size that made the old output unreadable, still has to fit on a screen.
func TestShortFormFitsOneScreen(t *testing.T) {
	cases := []struct {
		name  string
		short string
		full  string
	}{
		{"why", WhyText(realisticWhy(), shortOptions()), WhyText(realisticWhy(), detailOptions())},
		{"impact", ImpactText(realisticImpact(), shortOptions()), ImpactText(realisticImpact(), detailOptions())},
		{"estate", EstateText(realisticEstate(), shortOptions()), EstateText(realisticEstate(), detailOptions())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			short, full := lineCount(tc.short), lineCount(tc.full)
			t.Logf("%s: short %d lines, details %d lines", tc.name, short, full)
			if short >= maxShortLines {
				t.Errorf("short form is %d lines, want under %d:\n%s", short, maxShortLines, tc.short)
			}
			if full <= short {
				t.Errorf("details is %d lines and short is %d: details must add, never remove", full, short)
			}
			if !strings.HasPrefix(tc.full, tc.short[:strings.Index(tc.short, "\n")]) {
				t.Errorf("details must open with the same context line as the short form")
			}
		})
	}
}

// TestShortFormLeadsWithTheHeadline pins the hierarchy: context, then the answer, before
// any supporting fact. The sentence itself is the brief's, never rewritten here.
func TestShortFormLeadsWithTheHeadline(t *testing.T) {
	cases := []struct {
		name     string
		got      string
		headline string
	}{
		{"why", WhyText(realisticWhy(), shortOptions()), brief.Why(realisticWhy()).Headline},
		{"impact", ImpactText(realisticImpact(), shortOptions()), brief.Impact(realisticImpact()).Headline},
		{"estate", EstateText(realisticEstate(), shortOptions()), brief.Estate(realisticEstate()).Headline},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.TrimSpace(tc.headline) == "" {
				t.Fatalf("the brief produced no headline to lead with")
			}
			lines := strings.Split(tc.got, "\n")
			if len(lines) < 3 {
				t.Fatalf("short form is too short to carry a headline:\n%s", tc.got)
			}
			if lines[2] != tc.headline {
				t.Errorf("line 3 = %q, want the headline %q", lines[2], tc.headline)
			}
			if !strings.HasPrefix(lines[0], "spanline ") {
				t.Errorf("line 1 = %q, want the context header", lines[0])
			}
			if lines[1] != "" {
				t.Errorf("line 2 = %q, want a blank line under the context", lines[1])
			}
			// The same sentence leads the markdown, so both surfaces answer alike.
			md := map[string]string{
				"why":    WhyMarkdown(realisticWhy()),
				"impact": ImpactMarkdown(realisticImpact()),
				"estate": EstateMarkdown(realisticEstate()),
			}[tc.name]
			if !strings.Contains(md, "**"+tc.headline+"**") {
				t.Errorf("markdown does not lead with the headline:\n%s", md)
			}
		})
	}
}

// TestHeldBackLineNamesWhatIsHidden is the honesty contract of the short form: it may defer
// anything, as long as it says so. It is never blank.
func TestHeldBackLineNamesWhatIsHidden(t *testing.T) {
	t.Run("with something held back", func(t *testing.T) {
		cases := map[string]string{
			"why":    WhyText(realisticWhy(), shortOptions()),
			"impact": ImpactText(realisticImpact(), shortOptions()),
			"estate": EstateText(realisticEstate(), shortOptions()),
		}
		for _, name := range sortedKeys(cases) {
			got := cases[name]
			if !strings.Contains(got, detailsHint) {
				t.Errorf("%s: no line tells the reader how to see the rest:\n%s", name, got)
			}
			if !strings.Contains(got, "coverage gap") {
				t.Errorf("%s: coverage gaps were hidden without a word:\n%s", name, got)
			}
		}
		// why and estate count the dimensions and findings they defer.
		if !strings.Contains(cases["why"], "ranked lower") {
			t.Errorf("why must say how many changes it ranked lower:\n%s", cases["why"])
		}
		if !strings.Contains(cases["estate"], "more finding") {
			t.Errorf("estate must say how many findings it held back:\n%s", cases["estate"])
		}
	})

	t.Run("with nothing held back", func(t *testing.T) {
		// One dimension, one suspect, no gap: there is genuinely nothing more to show.
		r := realisticWhy()
		r.Dimensions = r.Dimensions[:1]
		r.Suspects = r.Suspects[:1]
		r.Gaps = nil

		got := WhyText(r, shortOptions())
		if !strings.Contains(got, nothingHeldMsg) {
			t.Errorf("want %q when nothing is deferred, got:\n%s", nothingHeldMsg, got)
		}
		if strings.Contains(got, detailsHint) {
			t.Errorf("nothing is held back, so nothing should point at --details:\n%s", got)
		}
		for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
			_ = line
		}
	})

	t.Run("details says the rest is below", func(t *testing.T) {
		got := WhyText(realisticWhy(), detailOptions())
		if !strings.Contains(got, detailsBelow) {
			t.Errorf("with --details the held back line must point below, got:\n%s", got)
		}
		if strings.Contains(got, detailsHint) {
			t.Errorf("--details is already on, it must not ask for it again")
		}
	})
}

// TestDetailsRestoresEverything checks that nothing was deleted, only deferred. Sections are
// matched on their own line, because the short screen legitimately says words like details,
// dimensions and pools while naming what it is holding back.
func TestDetailsRestoresEverything(t *testing.T) {
	cases := []struct {
		name string
		// restored must come back with --details.
		restored []string
		// deferred must be absent from the short screen, and present with --details.
		deferred []string
		short    string
		full     string
	}{
		{
			name: "why",
			restored: []string{
				"\ndetails\n", "\nonset\n", "\ncohorts\n", "\ndimensions\n",
				"\nsuspects (ranked)\n", "\ncoverage and gaps\n",
			},
			deferred: []string{
				"SEPARATION", "PURITY", "runtime class", "UNRESOLVED",
				"replicaset.spec.template.spec.containers[0].image",
				"max replicas raised to 30", "database password rotated",
				"audit log retention is 24h, changes older than that are invisible",
			},
			short: WhyText(realisticWhy(), shortOptions()),
			full:  WhyText(realisticWhy(), detailOptions()),
		},
		{
			name: "impact",
			restored: []string{
				"\ndetails\n", "\nscope\n", "\nmapping\n", "\nfindings\n",
				"\nignored\n", "\ncoverage and gaps\n",
			},
			deferred: []string{
				"SEVERITY", "pdb.status.currentHealthy", "spread across three pools, unaffected",
				"kube-system, excluded by default", "pdbs not listed in kube-system",
				"module.gke.cluster -> prod-eu", "volume is zone bound to eu-west-1b",
			},
			short: ImpactText(realisticImpact(), shortOptions()),
			full:  ImpactText(realisticImpact(), detailOptions()),
		},
		{
			name: "estate",
			restored: []string{
				"\ndetails\n", "\nclusters\n", "\npools\n", "\nstate files\n",
				"\nrisks\n", "\ncoverage and gaps\n",
			},
			deferred: []string{
				"TERRAFORM ADDRESS", "module.gke.node_pool", "not matched to terraform",
				"node.status.conditions[Ready]", "env/prod/terraform.tfstate",
				"kube-system not readable with this profile", "memory pressure for 2 collections",
			},
			short: EstateText(realisticEstate(), shortOptions()),
			full:  EstateText(realisticEstate(), detailOptions()),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range append(append([]string{}, tc.restored...), tc.deferred...) {
				if !strings.Contains(tc.full, want) {
					t.Errorf("--details lost %q:\n%s", want, tc.full)
				}
			}
			for _, hidden := range tc.deferred {
				if strings.Contains(tc.short, hidden) {
					t.Errorf("the short form still carries %q, which belongs under --details:\n%s", hidden, tc.short)
				}
			}
		})
	}
}

// TestEstateShortFormSummarisesClustersInWords keeps the per cluster line a sentence, not
// a table, and keeps it honest when a cluster has nothing recorded against it.
func TestEstateShortFormSummarisesClustersInWords(t *testing.T) {
	got := EstateText(realisticEstate(), shortOptions())
	for _, want := range []string{
		"prod-eu",
		"17 nodes, 1 not ready, 412 pods, 2 outages, 6 risks",
		"staging-eu",
		"7 nodes, 150 pods, 2 risks",
		"3 nodes, 48 pods, no outage or risk recorded",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing cluster summary %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "CONTEXT") || strings.Contains(got, "NODES") {
		t.Errorf("the short form must summarise clusters in words, not in a table:\n%s", got)
	}

	t.Run("no cluster read", func(t *testing.T) {
		r := realisticEstate()
		r.Clusters = nil
		if got := EstateText(r, shortOptions()); !strings.Contains(got, "no cluster read") {
			t.Errorf("an empty cluster list must say so, never read as nothing to worry about:\n%s", got)
		}
	})
}

func TestDoctorHoldsBackTheChecksTable(t *testing.T) {
	short := DoctorText(doctorFixture(), shortOptions())
	full := DoctorText(doctorFixture(), detailOptions())

	for _, want := range []string{"identity=system:serviceaccount:ops:spanline", "profile=read-only", "can write", "can read secrets"} {
		if !strings.Contains(short, want) {
			t.Errorf("the short form dropped %q:\n%s", want, short)
		}
	}
	if !strings.Contains(short, "refused reads") || !strings.Contains(short, "get secrets refused, as expected") {
		t.Errorf("the short form must name every refused read:\n%s", short)
	}
	if strings.Contains(short, "CHECK") || strings.Contains(short, "metrics-server absent") {
		t.Errorf("the checks table belongs under --details:\n%s", short)
	}
	for _, want := range []string{"CHECK", "STATUS", "DETAIL", "metrics-server absent"} {
		if !strings.Contains(full, want) {
			t.Errorf("--details lost %q from the checks table:\n%s", want, full)
		}
	}
	for _, want := range []string{"egress", "audit footprint", "coverage and gaps"} {
		if !strings.Contains(short, want) || !strings.Contains(full, want) {
			t.Errorf("%q must survive in both forms", want)
		}
	}

	t.Run("no refusal recorded", func(t *testing.T) {
		r := doctorFixture()
		r.Checks = []result.Check{{Name: "kubectl", Status: "ok"}}
		if got := DoctorText(r, shortOptions()); !strings.Contains(got, "none recorded") {
			t.Errorf("an empty refusal list must be stated, not left blank:\n%s", got)
		}
	})
}

func TestNoEscapeSequencesWhenColorIsOff(t *testing.T) {
	for _, tc := range textRenderers() {
		t.Run(tc.name, func(t *testing.T) {
			for _, compact := range []bool{false, true} {
				for _, details := range []bool{false, true} {
					got := tc.render(Options{Color: false, Width: 100, Compact: compact, Details: details})
					if strings.Contains(got, esc) {
						t.Errorf("compact=%v details=%v: output contains an escape sequence with Color off:\n%q", compact, details, got)
					}
				}
			}
		})
	}
}

func TestColorOnEmitsEscapeSequences(t *testing.T) {
	if !strings.Contains(styleOutage.Render("probe"), esc) {
		t.Skip("this terminal profile cannot colour, nothing to assert")
	}
	for _, tc := range textRenderers() {
		t.Run(tc.name, func(t *testing.T) {
			for _, details := range []bool{false, true} {
				got := tc.render(Options{Color: true, Width: 100, Details: details})
				if !strings.Contains(got, esc) {
					t.Errorf("details=%v: no escape sequence with Color on", details)
				}
			}
		})
	}
}

func TestImpactTextEndsWithVerdictAndExitCode(t *testing.T) {
	cases := []struct {
		name     string
		verdict  result.Severity
		exitCode int
		want     string
	}{
		{"outage", result.Outage, 3, "exit code 3"},
		{"not assessed", result.NotAssessed, 1, "exit code 1"},
		{"info", result.Info, 0, "exit code 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, o := range []Options{shortOptions(), detailOptions()} {
				r := impactFixture()
				r.Verdict = tc.verdict
				r.ExitCode = tc.exitCode

				got := ImpactText(r, o)
				if !strings.Contains(got, tc.want) {
					t.Errorf("details=%v: missing %q in:\n%s", o.Details, tc.want, got)
				}
				lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
				last := lines[len(lines)-1]
				if !strings.HasPrefix(last, "verdict") {
					t.Errorf("details=%v: last line = %q, want it to start with verdict", o.Details, last)
				}
				if !strings.Contains(last, string(tc.verdict)) || !strings.Contains(last, tc.want) {
					t.Errorf("details=%v: last line = %q, want verdict %q and %q", o.Details, last, tc.verdict, tc.want)
				}
			}
		})
	}
}

func TestGapsSectionIsAlwaysPresent(t *testing.T) {
	const heading = "coverage and gaps"

	t.Run("with gaps", func(t *testing.T) {
		cases := []struct {
			name string
			got  string
			want string
		}{
			{"why", WhyText(whyFixture(), detailOptions()), "metrics-server not reachable"},
			{"impact", ImpactText(impactFixture(), detailOptions()), "no metrics for the batch pool"},
			{"estate", EstateText(estateFixture(), detailOptions()), "one pool has no terraform owner"},
			{"doctor", DoctorText(doctorFixture(), detailOptions()), "no permission to list pdbs"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if !strings.Contains(tc.got, heading) {
					t.Errorf("missing %q section", heading)
				}
				if !strings.Contains(tc.got, tc.want) {
					t.Errorf("missing gap %q in:\n%s", tc.want, tc.got)
				}
				if strings.Contains(tc.got, "gaps: none recorded") {
					t.Errorf("reported no gap while the report has one")
				}
			})
		}
	})

	t.Run("without gaps", func(t *testing.T) {
		why, impact := whyFixture(), impactFixture()
		estate, doctor := estateFixture(), doctorFixture()
		why.Gaps, impact.Gaps, estate.Gaps, doctor.Gaps = nil, nil, nil, nil

		cases := []struct {
			name string
			got  string
		}{
			{"why", WhyText(why, detailOptions())},
			{"impact", ImpactText(impact, detailOptions())},
			{"estate", EstateText(estate, detailOptions())},
			{"doctor", DoctorText(doctor, detailOptions())},
			{"doctor short", DoctorText(doctor, shortOptions())},
			{"why markdown", WhyMarkdown(why)},
			{"impact markdown", ImpactMarkdown(impact)},
			{"estate markdown", EstateMarkdown(estate)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if !strings.Contains(strings.ToLower(tc.got), heading) {
					t.Errorf("missing coverage section")
				}
				if !strings.Contains(tc.got, "gaps: none recorded") {
					t.Errorf("an empty gap list must read as none recorded, got:\n%s", tc.got)
				}
			})
		}
	})
}

func TestMarkdownCarriesEverything(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want []string
	}{
		{
			"why",
			WhyMarkdown(whyFixture()),
			[]string{
				"| field | value |",
				"| --- | --- |",
				"| dimension | failing values | healthy values | separation | purity |",
				"| rank | verdict | at | title | dimension | actor | attribution |",
				"| cohort | pods | sample |",
			},
		},
		{
			"impact",
			ImpactMarkdown(impactFixture()),
			[]string{
				"| severity | kind | object | reason | context |",
				"| --- | --- | --- | --- | --- |",
				"exit code 3",
			},
		},
		{
			"estate",
			EstateMarkdown(estateFixture()),
			[]string{
				"| context | pool | nodes | zones | cpu | mem | workloads | at risk | terraform address | state file |",
				"| kind | object | reason | evidence |",
				"### OUTAGE (1)",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range tc.want {
				if !strings.Contains(tc.got, want) {
					t.Errorf("missing %q in:\n%s", want, tc.got)
				}
			}
			if strings.Contains(tc.got, esc) {
				t.Errorf("markdown contains an escape sequence")
			}
			// The headline leads, on its own line, under the title.
			lines := strings.Split(tc.got, "\n")
			if !strings.HasPrefix(lines[0], "# spanline ") {
				t.Errorf("line 1 = %q, want the title", lines[0])
			}
			if !strings.HasPrefix(lines[2], "**") || !strings.HasSuffix(lines[2], "**") {
				t.Errorf("line 3 = %q, want the bold headline", lines[2])
			}
		})
	}
}

func TestOutputNeverUsesDashesOrTheWordItMustNotPrint(t *testing.T) {
	outputs := map[string]string{
		"why":             WhyText(realisticWhy(), shortOptions()),
		"why details":     WhyText(realisticWhy(), detailOptions()),
		"impact":          ImpactText(realisticImpact(), shortOptions()),
		"impact details":  ImpactText(realisticImpact(), detailOptions()),
		"estate":          EstateText(realisticEstate(), shortOptions()),
		"estate details":  EstateText(realisticEstate(), detailOptions()),
		"doctor":          DoctorText(doctorFixture(), shortOptions()),
		"doctor details":  DoctorText(doctorFixture(), detailOptions()),
		"why markdown":    WhyMarkdown(realisticWhy()),
		"impact markdown": ImpactMarkdown(realisticImpact()),
		"estate markdown": EstateMarkdown(realisticEstate()),
	}
	for _, name := range sortedKeys(outputs) {
		got := outputs[name]
		if strings.ContainsAny(got, "—–") {
			t.Errorf("%s: output contains a long dash", name)
		}
		if strings.Contains(strings.ToLower(got), "cause") {
			t.Errorf("%s: output must never print that word", name)
		}
	}
}

func TestCompactDropsEvidenceLines(t *testing.T) {
	const evidence = "replicaset.spec.template.spec.containers[0].image"

	full := WhyText(whyFixture(), Options{Width: 100, Details: true})
	if !strings.Contains(full, evidence) {
		t.Fatalf("evidence missing from the full render")
	}
	compact := WhyText(whyFixture(), Options{Width: 100, Details: true, Compact: true})
	if strings.Contains(compact, evidence) {
		t.Errorf("Compact kept an evidence line")
	}
	if !strings.Contains(compact, "image bumped to v2.3.1") {
		t.Errorf("Compact dropped the suspect itself")
	}
}

func TestWhyDetailsSectionsAndSeverityWording(t *testing.T) {
	got := WhyText(whyFixture(), detailOptions())
	for _, want := range []string{
		"spanline why", "context=prod-eu", "namespace=payments", "workload=checkout", "source=cohort",
		"onset", "cohorts", "dimensions", "SEPARATION", "suspects (ranked)",
		"1.", "SPLITS", "  - image: registry/checkout:v2.3.0",
		"cohort key", "pod-template-hash", "generated",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// The context header survives in the short form too: it is the only line of scope.
	short := WhyText(whyFixture(), shortOptions())
	for _, want := range []string{"spanline why", "context=prod-eu", "namespace=payments", "workload=checkout"} {
		if !strings.Contains(short, want) {
			t.Errorf("the short form lost its scope: missing %q in:\n%s", want, short)
		}
	}

	r := impactFixture()
	if !strings.Contains(ImpactText(r, detailOptions()), "NOT ASSESSED") {
		t.Errorf("NOT ASSESSED must be spelled out")
	}
}

func TestEstateTextGroupsRisksBySeverity(t *testing.T) {
	got := EstateText(estateFixture(), detailOptions())

	// Scope the check to the risks section: the clusters table has RISKS and OUTAGES
	// columns, and the pools table an AT RISK column, all printed earlier.
	start := strings.Index(got, "\nrisks\n")
	if start < 0 {
		t.Fatalf("risks section not found in:\n%s", got)
	}
	risks := got[start:]
	if end := strings.Index(risks, "coverage and gaps"); end > 0 {
		risks = risks[:end]
	}
	outage := strings.Index(risks, "OUTAGE")
	risk := strings.Index(risks, "RISK")
	if outage < 0 || risk < 0 {
		t.Fatalf("both severity groups must appear in:\n%s", risks)
	}
	if outage > risk {
		t.Errorf("OUTAGE group must print before the RISK group:\n%s", risks)
	}
	if !strings.Contains(risks, "OUTAGE  (1)") || !strings.Contains(risks, "RISK  (2)") {
		t.Errorf("each group must carry its count:\n%s", risks)
	}
	if !strings.Contains(got, "module.gke.node_pool") {
		t.Errorf("a pool must carry its terraform address")
	}
	if !strings.Contains(got, "not matched to terraform") {
		t.Errorf("an unmatched pool must say so")
	}
}

func TestJSONIsIndentedAndDeterministic(t *testing.T) {
	v := map[string]any{"zeta": 1, "alpha": []string{"b", "a"}, "mid": map[string]int{"y": 2, "x": 1}}

	first, err := JSON(v)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	second, err := JSON(v)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if first != second {
		t.Errorf("two runs differ:\n%s\n%s", first, second)
	}
	if !strings.Contains(first, "\n  \"alpha\"") {
		t.Errorf("output is not indented:\n%s", first)
	}
	if strings.Index(first, "alpha") > strings.Index(first, "zeta") {
		t.Errorf("map keys are not sorted:\n%s", first)
	}
	if strings.HasSuffix(first, "\n") {
		t.Errorf("JSON must not end with a newline")
	}

	report, err := JSON(impactFixture())
	if err != nil {
		t.Fatalf("JSON report: %v", err)
	}
	if !strings.Contains(report, "\"exitCode\": 3") {
		t.Errorf("report JSON lost its exit code:\n%s", report)
	}
}

// TestRenderIsDeterministic guards the promise that two runs on one report are byte equal.
func TestRenderIsDeterministic(t *testing.T) {
	for _, o := range []Options{shortOptions(), detailOptions()} {
		if a, b := WhyText(realisticWhy(), o), WhyText(realisticWhy(), o); a != b {
			t.Errorf("why differs between two runs, details=%v", o.Details)
		}
		if a, b := ImpactText(realisticImpact(), o), ImpactText(realisticImpact(), o); a != b {
			t.Errorf("impact differs between two runs, details=%v", o.Details)
		}
		if a, b := EstateText(realisticEstate(), o), EstateText(realisticEstate(), o); a != b {
			t.Errorf("estate differs between two runs, details=%v", o.Details)
		}
	}
}

func TestNilReportsDoNotPanic(t *testing.T) {
	cases := map[string]string{
		"why":             WhyText(nil, DefaultOptions()),
		"impact":          ImpactText(nil, DefaultOptions()),
		"estate":          EstateText(nil, DefaultOptions()),
		"doctor":          DoctorText(nil, DefaultOptions()),
		"why details":     WhyText(nil, detailOptions()),
		"impact details":  ImpactText(nil, detailOptions()),
		"estate details":  EstateText(nil, detailOptions()),
		"doctor details":  DoctorText(nil, detailOptions()),
		"why markdown":    WhyMarkdown(nil),
		"impact markdown": ImpactMarkdown(nil),
		"estate markdown": EstateMarkdown(nil),
	}
	for _, name := range sortedKeys(cases) {
		if !strings.Contains(strings.ToLower(cases[name]), "no report to render") {
			t.Errorf("%s: want an explicit empty message, got %q", name, cases[name])
		}
	}
}

func TestEmptyReportsStillRenderEverySection(t *testing.T) {
	why := WhyText(&result.WhyReport{}, detailOptions())
	for _, want := range []string{"onset", "cohorts", "no dimension compared", "no suspect recorded", "gaps: none recorded"} {
		if !strings.Contains(why, want) {
			t.Errorf("empty why report missing %q in:\n%s", want, why)
		}
	}
	if !strings.Contains(why, "unknown") {
		t.Errorf("a zero timestamp must read as unknown")
	}

	impact := ImpactText(&result.ImpactReport{}, detailOptions())
	if !strings.Contains(impact, "no finding recorded") || !strings.Contains(impact, "exit code 0") {
		t.Errorf("empty impact report is incomplete:\n%s", impact)
	}
	if !strings.Contains(impact, "NOT ASSESSED") {
		t.Errorf("an absent verdict must read as NOT ASSESSED, not blank:\n%s", impact)
	}

	estate := EstateText(&result.EstateReport{}, detailOptions())
	for _, want := range []string{"no cluster read", "no pool read", "no risk recorded"} {
		if !strings.Contains(estate, want) {
			t.Errorf("empty estate report missing %q", want)
		}
	}

	doctor := DoctorText(&result.DoctorReport{}, detailOptions())
	if !strings.Contains(doctor, "no check run") {
		t.Errorf("empty doctor report missing the checks section")
	}

	// An empty report still answers, and still says it is holding nothing back.
	for name, got := range map[string]string{
		"why":    WhyText(&result.WhyReport{}, shortOptions()),
		"impact": ImpactText(&result.ImpactReport{}, shortOptions()),
		"estate": EstateText(&result.EstateReport{}, shortOptions()),
	} {
		if strings.TrimSpace(got) == "" {
			t.Errorf("%s: empty report rendered nothing at all", name)
		}
		if !strings.Contains(got, nothingHeldMsg) && !strings.Contains(got, detailsHint) {
			t.Errorf("%s: the short form must always close on what it is holding back:\n%s", name, got)
		}
	}
}

func TestTableColumnsAreAligned(t *testing.T) {
	got := DoctorText(doctorFixture(), detailOptions())
	var rows []string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "  ") && strings.Contains(line, "  ") {
			rows = append(rows, line)
		}
		if strings.HasSuffix(line, "\t") {
			t.Fatalf("a tab leaked into the output: %q", line)
		}
	}
	// Key the row lookup on a value that appears only in the checks table: the word
	// secrets also shows up in the access section just above it.
	check := indexOfLineWith(rows, "CHECK")
	denied := indexOfLineWith(rows, "denied")
	if check < 0 || denied < 0 {
		t.Fatalf("checks table not found in:\n%s", got)
	}
	if strings.Index(rows[check], "STATUS") != strings.Index(rows[denied], "denied") {
		t.Errorf("status column is not aligned:\n%q\n%q", rows[check], rows[denied])
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("line has trailing whitespace: %q", line)
		}
	}
}

// TestShortFormKeyLinesAreAligned checks the one piece of layout the short screen has: the
// labels form a column, so the values start at the same place.
func TestShortFormKeyLinesAreAligned(t *testing.T) {
	got := EstateText(realisticEstate(), shortOptions())
	var keys []string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, indent1+"outage") || strings.HasPrefix(line, indent1+"disruption") {
			keys = append(keys, line)
		}
	}
	if len(keys) < 2 {
		t.Fatalf("expected several key lines in:\n%s", got)
	}
	want := strings.Index(keys[0], "payments/checkout")
	for _, line := range keys[1:] {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if got := strings.Index(line, fields[1]); got != want {
			t.Errorf("value column starts at %d, want %d:\n%q\n%q", got, want, keys[0], line)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("line has trailing whitespace: %q", line)
		}
	}
}

func TestWrapRespectsWidth(t *testing.T) {
	long := strings.Repeat("token ", 60)
	for _, o := range []Options{{Width: 60}, {Width: 60, Details: true}} {
		got := WhyText(&result.WhyReport{Gaps: []string{long}}, o)
		for _, line := range strings.Split(got, "\n") {
			if len(line) > 60 {
				t.Errorf("details=%v: line longer than the requested width: %q", o.Details, line)
			}
		}
	}

	// A value far wider than the terminal is clipped, not wrapped into a paragraph.
	r := realisticWhy()
	r.Suspects[0].Title = strings.Repeat("verylongtitle", 20)
	got := WhyText(r, Options{Width: 60})
	for _, line := range strings.Split(got, "\n") {
		if len(line) > 60 {
			t.Errorf("a wide value was not clipped: %q", line)
		}
	}
	if lineCount(got) >= maxShortLines {
		t.Errorf("clipping must keep the screen short, got %d lines:\n%s", lineCount(got), got)
	}
}

func indexOfLineWith(lines []string, want string) int {
	for i, l := range lines {
		if strings.Contains(l, want) {
			return i
		}
	}
	return -1
}

// sortedKeys keeps the tests deterministic when they walk a map.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
