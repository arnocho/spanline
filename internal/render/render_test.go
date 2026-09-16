package render

import (
	"os"
	"strings"
	"testing"
	"time"

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

var fixedTime = time.Date(2026, 3, 4, 9, 15, 0, 0, time.UTC)

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
	if (Options{}).width() != 100 {
		t.Errorf("zero Width resolves to %d, want 100", (Options{}).width())
	}
}

func TestNoEscapeSequencesWhenColorIsOff(t *testing.T) {
	for _, tc := range textRenderers() {
		t.Run(tc.name, func(t *testing.T) {
			for _, compact := range []bool{false, true} {
				got := tc.render(Options{Color: false, Width: 100, Compact: compact})
				if strings.Contains(got, esc) {
					t.Errorf("compact=%v: output contains an escape sequence with Color off:\n%q", compact, got)
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
			got := tc.render(Options{Color: true, Width: 100})
			if !strings.Contains(got, esc) {
				t.Errorf("no escape sequence with Color on")
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
			r := impactFixture()
			r.Verdict = tc.verdict
			r.ExitCode = tc.exitCode

			got := ImpactText(r, DefaultOptions())
			if !strings.Contains(got, tc.want) {
				t.Errorf("missing %q in:\n%s", tc.want, got)
			}
			lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
			last := lines[len(lines)-1]
			if !strings.HasPrefix(last, "verdict") {
				t.Errorf("last line = %q, want it to start with verdict", last)
			}
			if !strings.Contains(last, string(tc.verdict)) || !strings.Contains(last, tc.want) {
				t.Errorf("last line = %q, want verdict %q and %q", last, tc.verdict, tc.want)
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
			{"why", WhyText(whyFixture(), DefaultOptions()), "metrics-server not reachable"},
			{"impact", ImpactText(impactFixture(), DefaultOptions()), "no metrics for the batch pool"},
			{"estate", EstateText(estateFixture(), DefaultOptions()), "one pool has no terraform owner"},
			{"doctor", DoctorText(doctorFixture(), DefaultOptions()), "no permission to list pdbs"},
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
			{"why", WhyText(why, DefaultOptions())},
			{"impact", ImpactText(impact, DefaultOptions())},
			{"estate", EstateText(estate, DefaultOptions())},
			{"doctor", DoctorText(doctor, DefaultOptions())},
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

func TestMarkdownTableHeaders(t *testing.T) {
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
		})
	}
}

func TestOutputNeverUsesDashesOrTheWordItMustNotPrint(t *testing.T) {
	outputs := map[string]string{
		"why":             WhyText(whyFixture(), Options{Color: false, Width: 100}),
		"impact":          ImpactText(impactFixture(), Options{Color: false, Width: 100}),
		"estate":          EstateText(estateFixture(), Options{Color: false, Width: 100}),
		"doctor":          DoctorText(doctorFixture(), Options{Color: false, Width: 100}),
		"why markdown":    WhyMarkdown(whyFixture()),
		"impact markdown": ImpactMarkdown(impactFixture()),
		"estate markdown": EstateMarkdown(estateFixture()),
	}
	names := make([]string, 0, len(outputs))
	for name := range outputs {
		names = append(names, name)
	}
	sortStrings(names)

	for _, name := range names {
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

	full := WhyText(whyFixture(), Options{Width: 100})
	if !strings.Contains(full, evidence) {
		t.Fatalf("evidence missing from the full render")
	}
	compact := WhyText(whyFixture(), Options{Width: 100, Compact: true})
	if strings.Contains(compact, evidence) {
		t.Errorf("Compact kept an evidence line")
	}
	if !strings.Contains(compact, "image bumped to v2.3.1") {
		t.Errorf("Compact dropped the suspect itself")
	}
}

func TestWhyTextSectionsAndSeverityWording(t *testing.T) {
	got := WhyText(whyFixture(), DefaultOptions())
	for _, want := range []string{
		"spanline why", "context=prod-eu", "namespace=payments", "workload=checkout", "source=cohort",
		"onset", "cohorts", "dimensions", "SEPARATION", "suspects (ranked)",
		"1.", "SPLITS", "  - image: registry/checkout:v2.3.0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	r := impactFixture()
	if !strings.Contains(ImpactText(r, DefaultOptions()), "NOT ASSESSED") {
		t.Errorf("NOT ASSESSED must be spelled out")
	}
}

func TestEstateTextGroupsRisksBySeverity(t *testing.T) {
	got := EstateText(estateFixture(), DefaultOptions())

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

func TestNilReportsDoNotPanic(t *testing.T) {
	cases := map[string]string{
		"why":             WhyText(nil, DefaultOptions()),
		"impact":          ImpactText(nil, DefaultOptions()),
		"estate":          EstateText(nil, DefaultOptions()),
		"doctor":          DoctorText(nil, DefaultOptions()),
		"why markdown":    WhyMarkdown(nil),
		"impact markdown": ImpactMarkdown(nil),
		"estate markdown": EstateMarkdown(nil),
	}
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		if !strings.Contains(strings.ToLower(cases[name]), "no report to render") {
			t.Errorf("%s: want an explicit empty message, got %q", name, cases[name])
		}
	}
}

func TestEmptyReportsStillRenderEverySection(t *testing.T) {
	why := WhyText(&result.WhyReport{}, DefaultOptions())
	for _, want := range []string{"onset", "cohorts", "no dimension compared", "no suspect recorded", "gaps: none recorded"} {
		if !strings.Contains(why, want) {
			t.Errorf("empty why report missing %q in:\n%s", want, why)
		}
	}
	if !strings.Contains(why, "unknown") {
		t.Errorf("a zero timestamp must read as unknown")
	}

	impact := ImpactText(&result.ImpactReport{}, DefaultOptions())
	if !strings.Contains(impact, "no finding recorded") || !strings.Contains(impact, "exit code 0") {
		t.Errorf("empty impact report is incomplete:\n%s", impact)
	}
	if !strings.Contains(impact, "NOT ASSESSED") {
		t.Errorf("an absent verdict must read as NOT ASSESSED, not blank:\n%s", impact)
	}

	estate := EstateText(&result.EstateReport{}, DefaultOptions())
	for _, want := range []string{"no cluster read", "no pool read", "no risk recorded"} {
		if !strings.Contains(estate, want) {
			t.Errorf("empty estate report missing %q", want)
		}
	}

	doctor := DoctorText(&result.DoctorReport{}, DefaultOptions())
	if !strings.Contains(doctor, "no check run") {
		t.Errorf("empty doctor report missing the checks section")
	}
}

func TestTableColumnsAreAligned(t *testing.T) {
	got := DoctorText(doctorFixture(), DefaultOptions())
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

func TestWrapRespectsWidth(t *testing.T) {
	long := strings.Repeat("token ", 60)
	got := WhyText(&result.WhyReport{Gaps: []string{long}}, Options{Width: 60})
	for _, line := range strings.Split(got, "\n") {
		if len(line) > 60 {
			t.Errorf("line longer than the requested width: %q", line)
		}
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

// sortStrings keeps the tests deterministic when they walk a map.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
