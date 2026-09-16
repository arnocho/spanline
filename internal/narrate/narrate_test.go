package narrate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arnocho/spanline/internal/redact"
	"github.com/arnocho/spanline/internal/result"
)

// The real names a payload must never carry.
const (
	realContext   = "aks-prod-weu"
	realNamespace = "prod-payments"
	realWorkload  = "checkout-api"
	realPod       = "checkout-api-7d9f4c8b6-x2m4q"
	realNode      = "aks-userpool-31415926-vmss000003"
	realRegistry  = "registry.payments.internal/team-checkout/api"
	realPool      = "userpool"
	realState     = "/Users/someone/terraform/prod.tfstate"
	realAddress   = "module.aks.azurerm_kubernetes_cluster_node_pool.user"
)

func sampleWhy() *result.WhyReport {
	at := time.Date(2026, 9, 14, 8, 30, 0, 0, time.UTC)
	return &result.WhyReport{
		Context:     realContext,
		Namespace:   realNamespace,
		Workload:    realWorkload,
		Mode:        result.ModeCohort,
		CohortKey:   "node.image",
		OnsetAt:     at,
		OnsetSignal: "OOMKilled on " + realPod,
		Failing:     result.Cohort{Pods: []string{realPod}, Count: 4, Sample: realPod},
		Healthy:     result.Cohort{Pods: []string{"checkout-api-6c1a-b7"}, Count: 7},
		Dimensions: []result.Dimension{
			{
				Name:          "node.image",
				FailingValues: "AKSUbuntu-2204gen2containerd-202508.12.0",
				HealthyValues: "AKSUbuntu-2204gen2containerd-202506.02.0",
				Separation:    result.Total,
				Purity:        1,
			},
			{
				Name:          "node",
				FailingValues: realNode,
				HealthyValues: "aks-userpool-31415926-vmss000001",
				Separation:    result.Partial,
				Purity:        0.5,
			},
		},
		Suspects: []result.Suspect{{
			ID:        "chg-0007",
			Verdict:   result.Splits,
			At:        at,
			Title:     "memory limit lowered on " + realWorkload + " in " + realNamespace,
			Dimension: "memory.limit",
			Diff: []string{
				"memory.limit: 512Mi -> 256Mi",
				"spec.replicas: 3 -> 2",
				"image: " + realRegistry + ":v1.4.2 -> " + realRegistry + ":v1.5.0",
				"annotations.connectionString: " + realState + " -> /elsewhere",
			},
			Actor:    result.ActorPipeline,
			Evidence: []string{"controllerrevision " + realWorkload + "-7d9f"},
		}},
		Gaps:        []string{"no metrics server in " + realNamespace},
		GeneratedAt: at,
	}
}

func sampleImpact() *result.ImpactReport {
	return &result.ImpactReport{
		Context: realContext,
		Source:  "kubectl",
		Nodes:   []string{realNode},
		Mapping: []string{realAddress + " -> " + realPool},
		Findings: []result.Finding{{
			Severity: result.Outage,
			Kind:     "pdb",
			Object:   realNamespace + "/" + realWorkload,
			Reason:   realWorkload + " would drop below minAvailable in " + realNamespace,
			Evidence: []string{"pdb.spec.minAvailable"},
		}},
		Verdict:  result.Outage,
		ExitCode: 3,
		Gaps:     []string{"no PDB read for " + realNamespace},
		PlanSHA:  "abc123",
		Ignored:  []string{realPod},
	}
}

func sampleEstate() *result.EstateReport {
	return &result.EstateReport{
		Clusters: []result.ClusterSummary{{
			Context: realContext, Nodes: 12, NodesNotReady: 1, Pods: 240,
			Namespaces: 18, Workloads: 64, Risks: 3, Outages: 0,
		}},
		Pools: []result.PoolSummary{{
			Context: realContext, Pool: realPool, Nodes: 9,
			TerraformAddress: realAddress, StateFile: realState,
			CPUPercent: 71.5, MemPercent: 64.2, Workloads: 40, AtRisk: 2, Zones: "1,2",
		}},
		Risks: []result.Finding{{
			Severity: result.Risk, Kind: "node-image-drift", Object: realNode,
			Reason: "node image behind the pool on " + realNode,
		}},
		States: []result.StateSummary{{
			Path: realState, Resources: 120, NodePools: 2, Clusters: 1, Matched: 1, Unmatched: 1,
		}},
	}
}

func factByID(pl Payload, id string) (Fact, bool) {
	for _, f := range pl.Facts {
		if f.ID == id {
			return f, true
		}
	}
	return Fact{}, false
}

func assertNoRealNames(t *testing.T, label, text string) {
	t.Helper()
	for _, secret := range []string{
		realContext, realNamespace, realWorkload, realPod, realNode,
		realRegistry, realPool, realState, realAddress,
		"OOMKilled", "metrics server", "minAvailable", "controllerrevision",
	} {
		if strings.Contains(text, secret) {
			t.Errorf("%s leaked %q", label, secret)
		}
	}
}

func TestPayloadForWhyCarriesNoRealName(t *testing.T) {
	p := redact.NewPseudonymizer("run-salt")
	pl := PayloadForWhy(sampleWhy(), p)

	if pl.Kind != "why" {
		t.Fatalf("Kind = %q, want why", pl.Kind)
	}
	assertNoRealNames(t, "the why payload", Prompt(pl))

	ns, ok := factByID(pl, "scope.namespace")
	if !ok || !strings.HasPrefix(ns.After, "ns-") {
		t.Fatalf("scope.namespace = %#v, want an ns- placeholder", ns)
	}
	wl, ok := factByID(pl, "scope.workload")
	if !ok || !strings.HasPrefix(wl.After, "wl-") {
		t.Fatalf("scope.workload = %#v, want a wl- placeholder", wl)
	}
	if p.Rehydrate(ns.After) != realNamespace || p.Rehydrate(wl.After) != realWorkload {
		t.Fatal("the placeholders do not rehydrate to the real names")
	}
	if pl.Counts["failing"] != 4 || pl.Counts["healthy"] != 7 {
		t.Fatalf("counts = %#v, want failing 4 and healthy 7", pl.Counts)
	}
}

func TestPayloadForWhyKeepsOnlyAllowlistedFields(t *testing.T) {
	p := redact.NewPseudonymizer("run-salt")
	pl := PayloadForWhy(sampleWhy(), p)

	mem, ok := factByID(pl, "chg-0007.diff.memory.limit")
	if !ok {
		t.Fatalf("no memory limit fact, got %#v", pl.Facts)
	}
	if mem.Before != "512Mi" || mem.After != "256Mi" {
		t.Errorf("memory limit fact = %q -> %q, want 512Mi -> 256Mi", mem.Before, mem.After)
	}
	if _, ok := factByID(pl, "chg-0007.diff.replicas"); !ok {
		t.Error("the replica count did not survive the allowlist")
	}
	if _, ok := factByID(pl, "chg-0007.verdict"); !ok {
		t.Error("the suspect verdict did not survive the allowlist")
	}

	img, ok := factByID(pl, "chg-0007.diff.image.tag")
	if !ok {
		t.Fatal("the image tag did not survive the allowlist")
	}
	if !strings.HasSuffix(img.Before, ":v1.4.2") || !strings.HasSuffix(img.After, ":v1.5.0") {
		t.Errorf("image fact = %q -> %q, want the tags kept", img.Before, img.After)
	}
	if strings.Contains(img.Before+img.After, "registry") {
		t.Errorf("image fact kept the repository: %q -> %q", img.Before, img.After)
	}

	// A field outside the allowlist is refused whole, values included.
	for _, f := range pl.Facts {
		if strings.Contains(f.ID, "connectionString") || strings.Contains(f.Field, "connectionString") {
			t.Errorf("a field outside the allowlist was copied: %#v", f)
		}
	}
	// The node dimension keeps its separation, never its node names.
	prompt := Prompt(pl)
	if !strings.Contains(prompt, "PARTIAL") || !strings.Contains(prompt, "TOTAL") {
		t.Error("the separations did not reach the prompt")
	}
	if !strings.Contains(prompt, "AKSUbuntu-2204gen2containerd-202508.12.0") {
		t.Error("the node image, which is allowlisted, did not reach the prompt")
	}
}

func TestPayloadForImpactAndEstateCarryNoRealName(t *testing.T) {
	p := redact.NewPseudonymizer("run-salt")

	impact := PayloadForImpact(sampleImpact(), p)
	assertNoRealNames(t, "the impact payload", Prompt(impact))
	if impact.Counts["outage"] != 1 || impact.Counts["exitCode"] != 3 {
		t.Errorf("impact counts = %#v, want one outage and exit code 3", impact.Counts)
	}
	verdict, ok := factByID(impact, "scope.verdict")
	if !ok || verdict.After != string(result.Outage) {
		t.Errorf("scope.verdict = %#v, want OUTAGE", verdict)
	}

	estate := PayloadForEstate(sampleEstate(), p)
	assertNoRealNames(t, "the estate payload", Prompt(estate))
	if estate.Counts["clusters"] != 1 || estate.Counts["pools"] != 1 {
		t.Errorf("estate counts = %#v, want one cluster and one pool", estate.Counts)
	}
	if nodes, ok := factByID(estate, "cluster-1.nodes"); !ok || nodes.After != "12" {
		t.Errorf("cluster-1.nodes = %#v, want 12", nodes)
	}
	if zone, ok := factByID(estate, "pool-1.zone"); !ok || zone.After != "1,2" {
		t.Errorf("pool-1.zone = %#v, want 1,2", zone)
	}
}

func TestPromptIsDeterministic(t *testing.T) {
	a := Prompt(PayloadForWhy(sampleWhy(), redact.NewPseudonymizer("run-salt")))
	b := Prompt(PayloadForWhy(sampleWhy(), redact.NewPseudonymizer("run-salt")))
	if a != b {
		t.Fatal("Prompt is not deterministic, so PromptSHA could not be trusted")
	}
	if !strings.Contains(a, "[chg-0007.diff.memory.limit]") {
		t.Fatalf("the prompt does not offer the citation id, got:\n%s", a)
	}
}

func TestValidateDropsUncitedAndInventedNumbers(t *testing.T) {
	pl := PayloadForWhy(sampleWhy(), redact.NewPseudonymizer("run-salt"))

	good := "The memory limit fell from 512Mi to 256Mi [chg-0007.diff.memory.limit]."
	uncited := "The root cause was a bad deploy on Tuesday evening."
	invented := "The workload now runs 9 replicas [chg-0007.diff.replicas]."

	kept, dropped := Validate(good+" "+uncited+" "+invented, pl)
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2, kept %q", dropped, kept)
	}
	if kept != good {
		t.Fatalf("kept = %q, want %q", kept, good)
	}

	// A sentence may reuse a placeholder that was sent, because a name is not a number.
	withName, dropped := Validate("The limit on "+mustFact(t, pl, "scope.workload").After+" fell to 256Mi [chg-0007.diff.memory.limit].", pl)
	if dropped != 0 || withName == "" {
		t.Fatalf("a sentence citing a real fact was dropped: kept %q, dropped %d", withName, dropped)
	}

	// An id that is not in this payload is not a citation.
	_, dropped = Validate("Everything is fine [chg-9999.diff.memory.limit].", pl)
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1 for an invented citation", dropped)
	}

	// Nothing in, nothing out.
	if kept, dropped := Validate("", pl); kept != "" || dropped != 0 {
		t.Fatalf("Validate(\"\") = %q, %d, want an empty answer", kept, dropped)
	}
}

func mustFact(t *testing.T, pl Payload, id string) Fact {
	t.Helper()
	f, ok := factByID(pl, id)
	if !ok {
		t.Fatalf("no fact %q in the payload", id)
	}
	return f
}

func TestNewNoneNarratesNothing(t *testing.T) {
	for _, backend := range []string{"none", "", "NONE"} {
		n, err := New(Options{Backend: backend})
		if err != nil {
			t.Fatalf("New(%q) returned %v", backend, err)
		}
		got, err := n.Narrate(context.Background(), PayloadForWhy(sampleWhy(), redact.NewPseudonymizer("s")))
		if err != nil || got != nil {
			t.Fatalf("Narrate on backend %q = %v, %v, want nil, nil", backend, got, err)
		}
	}
}

func TestNewRejectsAnIncompleteConfiguration(t *testing.T) {
	if _, err := New(Options{Backend: "openai-compat", Model: "m", APIKeyEnv: "K"}); err == nil {
		t.Error("New accepted a backend with no base URL")
	}
	if _, err := New(Options{Backend: "openai-compat", BaseURL: "https://x/v1", APIKeyEnv: "K"}); err == nil {
		t.Error("New accepted a backend with no model")
	}
	if _, err := New(Options{Backend: "telepathy", BaseURL: "https://x", Model: "m", APIKeyEnv: "K"}); err == nil {
		t.Error("New accepted an unknown backend")
	}
}

func TestHTTPNarratorRefusesAHostOutsideTheAllowlist(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"anything at all"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("SPANLINE_TEST_KEY", "not-a-real-key")

	n, err := New(Options{
		Backend:   "openai-compat",
		BaseURL:   srv.URL,
		Model:     "local-model",
		APIKeyEnv: "SPANLINE_TEST_KEY",
		Timeout:   2 * time.Second,
		// AllowHosts is empty on purpose: nothing may leave.
	})
	if err != nil {
		t.Fatalf("New returned %v", err)
	}

	got, err := n.Narrate(context.Background(), PayloadForWhy(sampleWhy(), redact.NewPseudonymizer("s")))
	if err == nil {
		t.Fatal("the narrator sent to a host that was never allowed")
	}
	if got != nil {
		t.Fatalf("a refused narrator still returned %#v", got)
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("error = %q, want it to name the allowlist", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("the server was called %d times, want 0 bytes sent", hits.Load())
	}
}

func TestHTTPNarratorValidatesWhatComesBack(t *testing.T) {
	const answer = "The memory limit fell from 512Mi to 256Mi [chg-0007.diff.memory.limit]. " +
		"You should roll back immediately, the cause is obvious."

	var seenAuth, seenBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth.Store(r.Header.Get("Authorization"))
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		seenBody.Store(string(buf[:n]))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + answer + `"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("SPANLINE_TEST_KEY", "not-a-real-key")

	n, err := New(Options{
		Backend:    "openai-compat",
		BaseURL:    srv.URL,
		Model:      "local-model",
		APIKeyEnv:  "SPANLINE_TEST_KEY",
		AllowHosts: []string{"127.0.0.1"},
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New returned %v", err)
	}

	p := redact.NewPseudonymizer("run-salt")
	pl := PayloadForWhy(sampleWhy(), p)
	got, err := n.Narrate(context.Background(), pl)
	if err != nil {
		t.Fatalf("Narrate returned %v", err)
	}
	if got == nil {
		t.Fatal("Narrate returned no narrative")
	}
	if !got.Unverified {
		t.Error("the narrative is not marked unverified")
	}
	if got.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1 uncited recommendation dropped", got.Dropped)
	}
	if strings.Contains(got.Text, "roll back") {
		t.Errorf("an uncited recommendation survived: %q", got.Text)
	}
	if !strings.Contains(got.Text, "512Mi") {
		t.Errorf("the cited sentence did not survive: %q", got.Text)
	}
	if len(got.Citations) != 1 || got.Citations[0] != "chg-0007.diff.memory.limit" {
		t.Errorf("Citations = %v, want the one cited fact", got.Citations)
	}
	if len(got.PromptSHA) != 64 || len(got.AnswerSHA) != 64 {
		t.Errorf("PromptSHA %q and AnswerSHA %q, want two sha256 hex digests", got.PromptSHA, got.AnswerSHA)
	}
	if got.Model != "local-model" {
		t.Errorf("Model = %q, want local-model", got.Model)
	}

	if auth, _ := seenAuth.Load().(string); auth != "Bearer not-a-real-key" {
		t.Errorf("Authorization header = %q, want the key read from the environment", auth)
	}
	body, _ := seenBody.Load().(string)
	assertNoRealNames(t, "the request body", body)
}
