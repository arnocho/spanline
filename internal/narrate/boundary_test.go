package narrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arnocho/spanline/internal/redact"
	"github.com/arnocho/spanline/internal/result"
)

// secrets hands out one distinctive marker per field, and remembers them all so a test can
// prove that not one of them reached the payload.
type secrets struct{ all []string }

func (s *secrets) mark(field string) string {
	m := "SECRET-" + field + "-hunter2"
	s.all = append(s.all, m)
	return m
}

func (s *secrets) assertAbsent(t *testing.T, label, text string) {
	t.Helper()
	if !strings.Contains(text, "SECRET-") {
		return
	}
	for _, m := range s.all {
		if strings.Contains(text, m) {
			t.Errorf("%s carries %q", label, m)
		}
	}
	// Even a marker the table above did not list must not slip through in any form.
	if i := strings.Index(text, "SECRET-"); i >= 0 {
		end := i + 60
		if end > len(text) {
			end = len(text)
		}
		t.Errorf("%s carries a secret shaped string: %q", label, text[i:end])
	}
}

func TestPayloadBuiltFromAReportFullOfSecretsCarriesNoneOfThem(t *testing.T) {
	var s secrets
	at := time.Date(2026, 9, 14, 8, 30, 0, 0, time.UTC)

	why := &result.WhyReport{
		Context:     s.mark("context"),
		Namespace:   s.mark("namespace"),
		Workload:    s.mark("workload"),
		Mode:        result.ModeCohort,
		CohortKey:   s.mark("cohortKey"),
		OnsetAt:     at,
		OnsetSignal: s.mark("onset"),
		Failing:     result.Cohort{Pods: []string{s.mark("failing-pod")}, Count: 4, Sample: s.mark("failing-sample")},
		Healthy:     result.Cohort{Pods: []string{s.mark("healthy-pod")}, Count: 7, Sample: s.mark("healthy-sample")},
		Dimensions: []result.Dimension{
			{Name: "node name", FailingValues: s.mark("node-a"), HealthyValues: s.mark("node-b"), Separation: result.Total, Purity: 1},
			{Name: "zone", FailingValues: "1", HealthyValues: "2", Separation: result.Partial, Purity: 0.5},
			{Name: "env.ZONE_API_KEY", FailingValues: s.mark("zone-key-f"), HealthyValues: s.mark("zone-key-h"), Separation: result.Total, Purity: 1},
		},
		Revisions: []result.Revision{{Name: s.mark("revision"), Number: 3, Active: s.mark("active"), Signals: s.mark("signals"), Created: at, Replicas: 4}},
		Suspects: []result.Suspect{{
			ID:        "replicaset/" + s.mark("replicaset-name"),
			Verdict:   result.Splits,
			At:        at,
			Title:     s.mark("title"),
			Dimension: "node.image",
			Diff: []string{
				"memory.limit: 512Mi -> 256Mi",
				"image: " + s.mark("registry") + "/team/api:v1.2.3 -> " + s.mark("registry") + "/team/api:v1.3.0",
				"env.ZONE_API_KEY: " + s.mark("zone-key-before") + " -> " + s.mark("zone-key-after"),
				"annotations.kernel-token: " + s.mark("kernel-token-before") + " -> " + s.mark("kernel-token-after"),
				"connectionString: " + s.mark("connection-before") + " -> " + s.mark("connection-after"),
				"annotations.checksum/config: " + s.mark("checksum-before") + " -> " + s.mark("checksum-after"),
			},
			Attribution: s.mark("attribution"),
			Actor:       result.ActorPipeline,
			Evidence:    []string{s.mark("evidence")},
		}},
		Gaps:        []string{s.mark("gap")},
		GeneratedAt: at,
	}
	impact := &result.ImpactReport{
		Context:     s.mark("impact-context"),
		Source:      s.mark("source"),
		SnapshotAt:  at,
		Nodes:       []string{s.mark("node-1"), s.mark("node-2")},
		PlanSHA:     s.mark("plan-sha"),
		PlanSummary: s.mark("plan-summary"),
		Mapping:     []string{s.mark("mapping")},
		Findings: []result.Finding{{
			Severity: result.Outage, Kind: "pdb",
			Object:   s.mark("finding-ns") + "/" + s.mark("finding-wl"),
			Reason:   s.mark("finding-reason"),
			Evidence: []string{s.mark("finding-evidence")},
			Context:  s.mark("finding-context"),
		}},
		Ignored:  []string{s.mark("ignored")},
		Gaps:     []string{s.mark("impact-gap")},
		Verdict:  result.Outage,
		ExitCode: 3,
	}
	estate := &result.EstateReport{
		Clusters: []result.ClusterSummary{{Context: s.mark("cluster-context"), Nodes: 12, Pods: 240, Workloads: 64, Risks: 3}},
		Pools: []result.PoolSummary{{
			Context: s.mark("pool-context"), Pool: s.mark("pool"), Nodes: 9,
			TerraformAddress: s.mark("terraform-address"), StateFile: s.mark("state-file"),
			CPUPercent: 71.5, MemPercent: 64.2, Workloads: 40, AtRisk: 2, Zones: "1,2",
		}},
		Risks: []result.Finding{{
			Severity: result.Risk, Kind: "Node", Object: s.mark("risk-object"),
			Reason: s.mark("risk-reason"), Evidence: []string{s.mark("risk-evidence")}, Context: s.mark("risk-context"),
		}},
		States: []result.StateSummary{{Path: s.mark("state-path"), Resources: 120, Matched: 1, Unmatched: 1}},
		Gaps:   []string{s.mark("estate-gap")},
	}

	p := redact.NewPseudonymizer("run-salt")
	for _, pl := range []Payload{PayloadForWhy(why, p), PayloadForImpact(impact, p), PayloadForEstate(estate, p)} {
		raw, err := json.Marshal(pl)
		if err != nil {
			t.Fatal(err)
		}
		s.assertAbsent(t, pl.Kind+" payload JSON", string(raw))
		s.assertAbsent(t, pl.Kind+" prompt", Prompt(pl))
		for _, f := range pl.Facts {
			for _, part := range []string{f.ID, f.Field, f.Before, f.After, f.Note} {
				s.assertAbsent(t, pl.Kind+" fact "+f.ID, part)
			}
		}
	}

	// The allowlisted half must still be there, under positional citation ids.
	pl := PayloadForWhy(why, p)
	if _, ok := factByID(pl, "chg-0001.verdict"); !ok {
		t.Errorf("the suspect verdict is missing or not under a positional id: %v", ids(pl))
	}
	if mem, ok := factByID(pl, "chg-0001.diff.memory.limit"); !ok || mem.Before != "512Mi" || mem.After != "256Mi" {
		t.Errorf("memory limit fact = %+v, want 512Mi -> 256Mi", mem)
	}
	if img, ok := factByID(pl, "chg-0001.diff.image.tag"); !ok || !strings.HasSuffix(img.After, ":v1.3.0") {
		t.Errorf("image tag fact = %+v, want the tag kept and the repository replaced", img)
	}
	for _, f := range pl.Facts {
		if strings.HasPrefix(f.ID, "replicaset") || strings.Contains(f.ID, "hunter2") {
			t.Errorf("a suspect id built from an object name reached the payload: %q", f.ID)
		}
	}
	if len(p.Table()) == 0 {
		t.Error("no placeholder was handed out, so nothing was pseudonymized")
	}
}

func ids(pl Payload) []string {
	out := make([]string, 0, len(pl.Facts))
	for _, f := range pl.Facts {
		out = append(out, f.ID)
	}
	return out
}

func okAnswer(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"The mode is cohort [scope.mode]."}}]}`))
}

func TestNarratorRefusesToFollowARedirect(t *testing.T) {
	var elsewhere atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		okAnswer(w)
	}))
	defer other.Close()
	// The allowlisted endpoint answers with a redirect to a host that was never allowed. A 307
	// would make a client re-send the body, and the key, to that host.
	target := strings.Replace(other.URL, "127.0.0.1", "localhost", 1) + "/chat/completions"
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	t.Setenv("SPANLINE_TEST_KEY", "not-a-real-key")

	n, err := New(Options{
		Backend: "openai-compat", BaseURL: first.URL, Model: "m", APIKeyEnv: "SPANLINE_TEST_KEY",
		AllowHosts: []string{"127.0.0.1"}, Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := n.Narrate(context.Background(), PayloadForWhy(sampleWhy(), redact.NewPseudonymizer("s")))
	if err == nil || got != nil {
		t.Fatalf("Narrate followed a redirect: %v, %v", got, err)
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("error = %q, want it to name the redirect", err)
	}
	if elsewhere.Load() != 0 {
		t.Fatalf("the redirect target was contacted %d times, want 0 bytes sent", elsewhere.Load())
	}
}

func TestNarratorHandlesTheKeyWithCare(t *testing.T) {
	var hits atomic.Int64
	var seenAuth atomic.Value
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		seenAuth.Store(r.Header.Get("Authorization"))
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"BODY-SECRET-do-not-echo"}`))
			return
		}
		okAnswer(w)
	}))
	defer srv.Close()
	pl := PayloadForWhy(sampleWhy(), redact.NewPseudonymizer("s"))
	build := func(allow []string) Narrator {
		t.Helper()
		n, err := New(Options{
			Backend: "openai-compat", BaseURL: srv.URL, Model: "m", APIKeyEnv: "SPANLINE_TEST_KEY",
			AllowHosts: allow, Timeout: 3 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 1. With no allowlist the key is never even read: the error is about the allowlist, and
	//    an unset variable does not change it.
	os.Unsetenv("SPANLINE_TEST_KEY")
	_, err := build(nil).Narrate(context.Background(), pl)
	if err == nil || !strings.Contains(err.Error(), "allowlist") || strings.Contains(err.Error(), "SPANLINE_TEST_KEY") {
		t.Errorf("empty allowlist and no key: error = %v, want the allowlist refusal alone", err)
	}
	if hits.Load() != 0 {
		t.Fatal("something was sent without an allowlist")
	}

	// 2. A key pasted with surrounding whitespace is sent trimmed.
	t.Setenv("SPANLINE_TEST_KEY", "  KEY-SECRET-abc\n")
	if _, err := build([]string{"127.0.0.1"}).Narrate(context.Background(), pl); err != nil {
		t.Fatalf("Narrate returned %v", err)
	}
	if auth, _ := seenAuth.Load().(string); auth != "Bearer KEY-SECRET-abc" {
		t.Errorf("Authorization = %q, want the trimmed key", auth)
	}

	// 3. A failing endpoint never puts the key or the response body in the error.
	status = http.StatusInternalServerError
	_, err = build([]string{"127.0.0.1"}).Narrate(context.Background(), pl)
	if err == nil {
		t.Fatal("a 500 answer was accepted")
	}
	for _, leak := range []string{"KEY-SECRET", "BODY-SECRET"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q carries %s", err, leak)
		}
	}
	status = http.StatusOK

	// 4. A key with a control character is refused before any request, and not echoed.
	before := hits.Load()
	t.Setenv("SPANLINE_TEST_KEY", "KEY-SECRET-ab\ncd")
	_, err = build([]string{"127.0.0.1"}).Narrate(context.Background(), pl)
	if err == nil {
		t.Fatal("a key with a control character was sent")
	}
	if strings.Contains(err.Error(), "KEY-SECRET") {
		t.Errorf("error %q echoes the key", err)
	}
	if hits.Load() != before {
		t.Error("a request was sent with an unusable key")
	}
}

func TestAllowHostResolvesTheHostExactly(t *testing.T) {
	cases := []struct {
		base  string
		allow []string
		ok    bool
	}{
		{"https://gateway.internal/v1", []string{"gateway.internal"}, true},
		{"https://Gateway.Internal:8443/v1/", []string{"gateway.internal"}, true},
		{"https://gateway.internal/v1", []string{"GATEWAY.INTERNAL"}, true},
		{"https://gateway.internal:8443/v1", []string{"gateway.internal:8443"}, true},
		{"https://gateway.internal:8443/v1", []string{"gateway.internal:9000"}, false},
		{"https://gateway.internal.evil.example/v1", []string{"gateway.internal"}, false},
		{"https://evilgateway.internal/v1", []string{"gateway.internal"}, false},
		{"https://gateway.internal/v1", []string{"internal"}, false},
		{"https://gateway.internal/v1", []string{"https://gateway.internal"}, false},
		{"https://gateway.internal/v1", []string{"gateway.internal/v1"}, false},
		{"https://user:pw@gateway.internal/v1", []string{"gateway.internal"}, true},
		{"http://10.0.0.5:8000/v1", []string{"10.0.0.5"}, true},
		{"http://10.0.0.5:8000/v1", []string{"10.0.0.50"}, false},
		{"http://10.0.0.50:8000/v1", []string{"10.0.0.5"}, false},
		{"http://[::1]:8000/v1", []string{"::1"}, true},
		{"http://[::1]:8000/v1", []string{"[::1]"}, true},
		{"http://[::1]:8000/v1", []string{"::2"}, false},
		{"https://gateway.internal/v1", nil, false},
		{"https://gateway.internal/v1", []string{""}, false},
		{"https://gateway.internal/v1", []string{" ", ""}, false},
		{"https://gateway.internal/v1", []string{"other.internal", "gateway.internal"}, true},
		{"https:///v1", []string{""}, false},
	}
	for _, c := range cases {
		u, err := url.Parse(c.base)
		if err != nil {
			t.Fatal(err)
		}
		err = allowHost(u, c.allow)
		if (err == nil) != c.ok {
			t.Errorf("allowHost(%q, %q) = %v, want ok=%v", c.base, c.allow, err, c.ok)
		}
	}
}

func TestNewRefusesABaseURLWithoutAHost(t *testing.T) {
	for _, base := range []string{"https:///v1", "https://", "http:///chat", "file:///etc/passwd", "gateway.internal/v1"} {
		if _, err := New(Options{Backend: "openai-compat", BaseURL: base, Model: "m", APIKeyEnv: "K"}); err == nil {
			t.Errorf("New accepted base URL %q", base)
		}
	}
}

func TestValidateNumbersMustComeFromTheCitedFact(t *testing.T) {
	pl := Payload{Kind: "why", Counts: map[string]int{"failing": 4}}
	pl.add(Fact{ID: "a.b", Field: "nodes", After: "5"})
	pl.add(Fact{ID: "a.b.c", Field: "pods", After: "7"})
	pl.add(Fact{ID: "imp-0001.severity", Field: "finding.pdb", After: "OUTAGE", Note: "object ns-1/wl-2"})
	pl.add(Fact{ID: "dim-1.values", Field: "node.image", Before: "AKSUbuntu-2204gen2containerd-202508.12.0", After: "AKSUbuntu-2204gen2containerd-202506.02.0"})

	cases := []struct {
		sentence string
		kept     bool
	}{
		{"There are 7 pods [a.b.c].", true},
		{"There are 7 pods [a.b].", false}, // a.b.c is not cited by citing its prefix
		{"There are 5 nodes [a.b].", true},
		{"There are 5 nodes [a.b.c].", false}, // nor the other way round
		{"There are 5 nodes [a.b.cd].", false},
		{"wl-2 loses every replica [imp-0001.severity].", true},
		{"2 workloads lose every replica [imp-0001.severity].", false}, // the 2 belongs to a placeholder
		{"1 namespace is hit [imp-0001.severity].", false},
		{"4 pods fail [count.failing].", true},
		{"40 pods fail [count.failing].", false},
		{"The count is 4 [count.failingX].", false},
		{"The failing image is AKSUbuntu-2204gen2containerd-202508.12.0 [dim-1.values].", true},
		{"The failing image is 202508.12.0 [dim-1.values].", true},
		{"The failing image is 202508.13.0 [dim-1.values].", false},
	}
	for _, c := range cases {
		kept, dropped := Validate(c.sentence, pl)
		if c.kept && (dropped != 0 || kept != c.sentence) {
			t.Errorf("Validate(%q) dropped a supported sentence", c.sentence)
		}
		if !c.kept && (dropped != 1 || kept != "") {
			t.Errorf("Validate(%q) kept an unsupported sentence: %q", c.sentence, kept)
		}
	}
}
