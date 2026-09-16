package impact

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arnocho/spanline/internal/collect"
	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
	"github.com/arnocho/spanline/internal/tfplan"
)

// fixedNow is the caller's clock. Nothing in this package reads the wall clock.
var fixedNow = time.Date(2026, 9, 16, 4, 0, 0, 0, time.UTC)

func estate(t *testing.T) (*model.Snapshot, *collect.FixtureSource) {
	t.Helper()
	src, err := collect.NewFixtureSource("estate")
	if err != nil {
		t.Fatalf("open estate fixture: %v", err)
	}
	snap, err := src.Snapshot(context.Background(), "aks-prod-weu", "")
	if err != nil {
		t.Fatalf("read aks-prod-weu snapshot: %v", err)
	}
	if len(snap.Nodes) == 0 || len(snap.Pods) == 0 {
		t.Fatalf("estate fixture is empty: %d nodes, %d pods", len(snap.Nodes), len(snap.Pods))
	}
	return snap, src
}

func find(rep *result.ImpactReport, sev result.Severity, kind, object string) *result.Finding {
	for i := range rep.Findings {
		f := &rep.Findings[i]
		if f.Severity == sev && f.Kind == kind && f.Object == object {
			return f
		}
	}
	return nil
}

func evidenceHas(f *result.Finding, want string) bool {
	for _, e := range f.Evidence {
		if strings.Contains(e, want) {
			return true
		}
	}
	return false
}

func names(rep *result.ImpactReport) string { return strings.Join(rep.Nodes, ",") }

func dump(t *testing.T, rep *result.ImpactReport) {
	t.Helper()
	for _, f := range rep.Findings {
		t.Logf("%-12s %-22s %s | %s", f.Severity, f.Kind, f.Object, f.Reason)
	}
}

func TestNodesPoolAppsFindsOutageBlockedDrainAndStrandedVolume(t *testing.T) {
	snap, _ := estate(t)

	rep, err := Nodes(snap, "pool=apps", Options{Now: fixedNow})
	if err != nil {
		t.Fatalf("Nodes(pool=apps): %v", err)
	}
	dump(t, rep)

	want := "aks-apps-10000,aks-apps-10001,aks-apps-10002,aks-apps-10003,aks-apps-10004,aks-apps-10005"
	if got := names(rep); got != want {
		t.Errorf("nodes that go away:\n got %s\nwant %s", got, want)
	}

	// The single replica workload loses everything it has.
	outage := find(rep, result.Outage, "Deployment", "orders/orders")
	if outage == nil {
		t.Fatal("no OUTAGE for Deployment orders/orders, the single replica workload")
	}
	if !evidenceHas(outage, "spec.nodeName") {
		t.Errorf("outage evidence does not name spec.nodeName: %v", outage.Evidence)
	}
	if !evidenceHas(outage, "spec.replicas: 1") {
		t.Errorf("outage evidence does not name spec.replicas: %v", outage.Evidence)
	}

	// The budget that allows nothing blocks the drain.
	pdb := find(rep, result.Disruption, "PodDisruptionBudget", "payments/payments-api")
	if pdb == nil {
		t.Fatal("no DISRUPTION for the payments-api PodDisruptionBudget")
	}
	if !evidenceHas(pdb, "status.disruptionsAllowed: 0") {
		t.Errorf("pdb evidence does not name status.disruptionsAllowed: %v", pdb.Evidence)
	}
	if !evidenceHas(pdb, "spec.selector.matchLabels: app=payments-api") {
		t.Errorf("pdb evidence does not name the selector it matched on: %v", pdb.Evidence)
	}

	// The redis volume is pinned to zone 2 and its own pool leaves nothing there.
	vol := find(rep, result.Disruption, "PersistentVolumeClaim", "orders/redis-data-redis-0")
	if vol == nil {
		t.Fatal("no DISRUPTION for the redis claim bound to the zone 2 volume")
	}
	if !strings.Contains(vol.Reason, "zone 2") {
		t.Errorf("stranded volume reason does not name zone 2: %s", vol.Reason)
	}
	if !evidenceHas(vol, "pv-redis-z2") || !evidenceHas(vol, "topology.kubernetes.io/zone") {
		t.Errorf("stranded volume evidence does not name the volume and the zone key: %v", vol.Evidence)
	}
	if !evidenceHas(vol, "spec.nodeAffinity.required.nodeSelectorTerms[].matchExpressions") {
		t.Errorf("stranded volume evidence does not name the affinity field: %v", vol.Evidence)
	}

	// Every replica of payments-api and of the redis StatefulSet is on the doomed pool too.
	if find(rep, result.Outage, "Deployment", "payments/payments-api") == nil {
		t.Error("no OUTAGE for payments/payments-api, whose four pods are all on the pool")
	}
	if find(rep, result.Outage, "StatefulSet", "orders/redis") == nil {
		t.Error("no OUTAGE for the redis StatefulSet")
	}
	// The workload that lives on another pool is untouched.
	for _, f := range rep.Findings {
		if f.Object == "analytics/warehouse" {
			t.Errorf("warehouse runs on the data pool and should not be reported: %+v", f)
		}
	}

	if rep.Verdict != result.Outage {
		t.Errorf("verdict = %s, want OUTAGE", rep.Verdict)
	}
	if rep.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3 for an outage", rep.ExitCode)
	}
	if rep.Context != "aks-prod-weu" {
		t.Errorf("context = %q", rep.Context)
	}
	if !rep.SnapshotAt.Equal(snap.CollectedAt) {
		t.Errorf("snapshotAt = %s, want %s", rep.SnapshotAt, snap.CollectedAt)
	}
	if want := snap.CollectedAt.Add(DefaultTTL); !rep.ExpiresAt.Equal(want) {
		t.Errorf("expiresAt = %s, want %s with the default 2h TTL", rep.ExpiresAt, want)
	}
	if !rep.GeneratedAt.Equal(fixedNow) {
		t.Errorf("generatedAt = %s, want the caller's clock %s", rep.GeneratedAt, fixedNow)
	}

	for _, want := range []string{
		"autoscaler scale up",
		"surge nodes created by the replacement",
		"topology spread constraints",
		"pod priority and preemption",
		"DaemonSet overhead",
		"HPA reactions",
	} {
		found := false
		for _, ig := range rep.Ignored {
			if ig == want {
				found = true
			}
		}
		if !found {
			t.Errorf("ignored list does not say %q: %v", want, rep.Ignored)
		}
	}

	for i := 1; i < len(rep.Findings); i++ {
		a, b := rep.Findings[i-1], rep.Findings[i]
		if a.Severity.Rank() < b.Severity.Rank() {
			t.Errorf("findings are not sorted by severity: %s before %s", a.Severity, b.Severity)
		}
		if a.Severity.Rank() == b.Severity.Rank() && a.Object > b.Object {
			t.Errorf("findings of equal severity are not sorted by object: %q before %q", a.Object, b.Object)
		}
	}
}

func TestNodesTTLOverrideFillsExpiresAt(t *testing.T) {
	snap, _ := estate(t)
	rep, err := Nodes(snap, "pool=apps", Options{Now: fixedNow, TTL: 30 * time.Minute})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if want := snap.CollectedAt.Add(30 * time.Minute); !rep.ExpiresAt.Equal(want) {
		t.Errorf("expiresAt = %s, want %s", rep.ExpiresAt, want)
	}
}

func TestNodesSelectorThatMatchesNothingIsNeverAPass(t *testing.T) {
	snap, _ := estate(t)

	rep, err := Nodes(snap, "pool=doesnotexist", Options{Now: fixedNow})
	if err != nil {
		t.Fatalf("Nodes(pool=doesnotexist): %v", err)
	}
	dump(t, rep)

	for _, f := range rep.Findings {
		if f.Severity != result.NotAssessed {
			t.Errorf("a selector matching no node produced a %s finding: %+v", f.Severity, f)
		}
	}
	if rep.ExitCode != 1 && rep.ExitCode != 0 {
		t.Errorf("exit code = %d, want 1 for NOT ASSESSED or 0 for an empty report", rep.ExitCode)
	}
	if len(rep.Findings) > 0 && rep.ExitCode != 1 {
		t.Errorf("exit code = %d with NOT ASSESSED findings present, want 1", rep.ExitCode)
	}
	if len(rep.Nodes) != 0 {
		t.Errorf("nodes = %v, want none", rep.Nodes)
	}
	if f := find(rep, result.NotAssessed, "Selector", "pool=doesnotexist"); f == nil {
		t.Error("no NOT ASSESSED finding saying the selector matched nothing")
	} else if !evidenceHas(f, "accepted forms:") {
		t.Errorf("the NOT ASSESSED finding does not list the accepted forms: %v", f.Evidence)
	}
}

func TestNodesUnknownNodeNameIsNotAssessed(t *testing.T) {
	snap, _ := estate(t)
	rep, err := Nodes(snap, "node=aks-apps-10000,ghost-node", Options{Now: fixedNow})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if f := find(rep, result.NotAssessed, "Node", "ghost-node"); f == nil {
		t.Error("an unknown node name should be NOT ASSESSED, not ignored")
	}
	if len(rep.Nodes) != 1 || rep.Nodes[0] != "aks-apps-10000" {
		t.Errorf("nodes = %v, want only the one that exists", rep.Nodes)
	}
}

func TestSelectorFormsAllResolve(t *testing.T) {
	snap, _ := estate(t)
	cases := []struct {
		selector string
		want     int
	}{
		{"pool=apps", 6},
		{"pool=data", 1},
		{"zone=2", 4},
		{"node=aks-apps-10000,aks-apps-10001", 2},
		{"label=agentpool=data", 1},
		{"label=topology.kubernetes.io/zone=3", 2},
		{"aks-apps-10000", 1},
		{"aks-apps-10000,aks-data-30000", 2},
	}
	for _, c := range cases {
		rep, err := Nodes(snap, c.selector, Options{Now: fixedNow})
		if err != nil {
			t.Errorf("Nodes(%q): %v", c.selector, err)
			continue
		}
		if len(rep.Nodes) != c.want {
			t.Errorf("Nodes(%q) selected %v, want %d nodes", c.selector, rep.Nodes, c.want)
		}
	}
}

func TestSelectorErrorsListTheAcceptedForms(t *testing.T) {
	snap, _ := estate(t)
	for _, bad := range []string{"", "   ", "kind=node", "pool=", "zone=", "label=oops", "node="} {
		_, err := Nodes(snap, bad, Options{Now: fixedNow})
		if err == nil {
			t.Errorf("Nodes(%q) returned no error", bad)
			continue
		}
		msg := err.Error()
		ok := strings.Contains(msg, "pool=<name>") ||
			strings.Contains(msg, "write pool=") ||
			strings.Contains(msg, "write zone=") ||
			strings.Contains(msg, "write node=") ||
			strings.Contains(msg, "label=<key>=<value>")
		if !ok {
			t.Errorf("Nodes(%q) error does not say what a selector may look like: %s", bad, msg)
		}
	}
}

func TestNodesRejectsANilSnapshot(t *testing.T) {
	if _, err := Nodes(nil, "pool=apps", Options{}); err == nil {
		t.Error("Nodes(nil) returned no error")
	}
}

func planFixture(t *testing.T, src *collect.FixtureSource) ([]byte, *tfplan.Plan, []*tfplan.State) {
	t.Helper()
	b, err := src.Extra("terraform/plan.json")
	if err != nil {
		t.Fatalf("read terraform/plan.json: %v", err)
	}
	p, err := tfplan.ParsePlan(b)
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	sb, err := src.Extra("terraform/state.json")
	if err != nil {
		t.Fatalf("read terraform/state.json: %v", err)
	}
	st, err := tfplan.ParseState(sb)
	if err != nil {
		t.Fatalf("parse state: %v", err)
	}
	return b, p, []*tfplan.State{st}
}

func TestPlanEstateReplacesTheAppsPool(t *testing.T) {
	snap, src := estate(t)
	b, p, states := planFixture(t, src)

	rep, err := Plan(snap, p, states, "terraform/plan.json", Options{Now: fixedNow})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	rep.PlanSHA = PlanSHA(b)
	dump(t, rep)
	t.Logf("summary: %s", rep.PlanSummary)
	for _, m := range rep.Mapping {
		t.Logf("mapping: %s", m)
	}

	// The pool replacement takes the same nodes down as the selector run, so the same outage lands.
	if find(rep, result.Outage, "Deployment", "orders/orders") == nil {
		t.Error("no OUTAGE for Deployment orders/orders under the plan")
	}
	if find(rep, result.Disruption, "PodDisruptionBudget", "payments/payments-api") == nil {
		t.Error("no DISRUPTION for the payments-api budget under the plan")
	}
	want := "aks-apps-10000,aks-apps-10001,aks-apps-10002,aks-apps-10003,aks-apps-10004,aks-apps-10005"
	if got := names(rep); got != want {
		t.Errorf("nodes that go away:\n got %s\nwant %s", got, want)
	}

	// The cluster change spanline has no rule for is named, not swallowed.
	na := find(rep, result.NotAssessed, "Cluster", "azurerm_kubernetes_cluster.main")
	if na == nil {
		t.Fatal("no NOT ASSESSED finding for azurerm_kubernetes_cluster.main")
	}
	if !strings.Contains(na.Reason, "upgrade_settings") && !evidenceHas(na, "upgrade_settings") {
		t.Errorf("the cluster finding does not name upgrade_settings: %s %v", na.Reason, na.Evidence)
	}

	// The plan summary reads the way terraform prints it, over the whole 43 change plan.
	if rep.PlanSummary != "2 to add, 1 to destroy, 41 to change" {
		t.Errorf("planSummary = %q, want %q", rep.PlanSummary, "2 to add, 1 to destroy, 41 to change")
	}
	info := find(rep, result.Info, "Plan", "changes outside the node pools")
	if info == nil {
		t.Fatal("no INFO finding counting the changes with no node effect")
	}
	if !evidenceHas(info, "azurerm_resource_group: 40") {
		t.Errorf("the INFO finding does not count the 40 resource groups: %v", info.Evidence)
	}

	// Mapping ties the Terraform address to the live nodes, and names the pool the plan leaves alone.
	var replaced, untouched bool
	for _, m := range rep.Mapping {
		if strings.Contains(m, "azurerm_kubernetes_cluster_node_pool.apps (replace)") &&
			strings.Contains(m, "=apps (6)") {
			replaced = true
		}
		if strings.Contains(m, "azurerm_kubernetes_cluster_node_pool.data") &&
			strings.Contains(m, "no change in this plan") {
			untouched = true
		}
	}
	if !replaced {
		t.Errorf("mapping has no line tying the apps pool replacement to its 6 nodes: %v", rep.Mapping)
	}
	if !untouched {
		t.Errorf("mapping does not name the data pool the plan leaves alone: %v", rep.Mapping)
	}

	if rep.Verdict != result.Outage || rep.ExitCode != 3 {
		t.Errorf("verdict = %s, exit = %d, want OUTAGE and 3", rep.Verdict, rep.ExitCode)
	}
	if len(rep.PlanSHA) != 12 {
		t.Errorf("planSHA = %q, want a 12 character prefix", rep.PlanSHA)
	}
	if rep.Source != "terraform/plan.json" {
		t.Errorf("source = %q", rep.Source)
	}
}

func TestPlanRejectsMissingInput(t *testing.T) {
	snap, src := estate(t)
	_, p, _ := planFixture(t, src)
	if _, err := Plan(nil, p, nil, "plan.json", Options{}); err == nil {
		t.Error("Plan with no snapshot returned no error")
	}
	if _, err := Plan(snap, nil, nil, "plan.json", Options{}); err == nil {
		t.Error("Plan with no plan returned no error")
	}
}

func TestPlanSHAIsStableAndShort(t *testing.T) {
	a := PlanSHA([]byte("plan bytes"))
	b := PlanSHA([]byte("plan bytes"))
	c := PlanSHA([]byte("other bytes"))
	if a != b {
		t.Errorf("PlanSHA is not stable: %q then %q", a, b)
	}
	if a == c {
		t.Error("PlanSHA collides on different bytes")
	}
	if len(a) != 12 {
		t.Errorf("PlanSHA length = %d, want 12", len(a))
	}
}

func TestExitCodesFollowTheHighestSeverity(t *testing.T) {
	snap, _ := estate(t)

	outage, err := Nodes(snap, "pool=apps", Options{Now: fixedNow})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if outage.ExitCode != 3 {
		t.Errorf("an OUTAGE report exits %d, want 3", outage.ExitCode)
	}

	nothing, err := Nodes(snap, "pool=doesnotexist", Options{Now: fixedNow})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if nothing.ExitCode != 1 {
		t.Errorf("a NOT ASSESSED report exits %d, want 1", nothing.ExitCode)
	}

	// A pool whose loss breaks nothing must not climb above INFO.
	quiet, err := Nodes(snap, "pool=system", Options{Now: fixedNow})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	dump(t, quiet)
	if quiet.ExitCode > 1 {
		t.Errorf("the empty system pool exits %d, want 0 or 1", quiet.ExitCode)
	}
}

func TestReportsAreDeterministic(t *testing.T) {
	snap, src := estate(t)
	first, err := Nodes(snap, "pool=apps", Options{Now: fixedNow})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	second, err := Nodes(snap, "pool=apps", Options{Now: fixedNow})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Error("two runs of Nodes on the same snapshot produced different reports")
	}

	_, p, states := planFixture(t, src)
	pa, err := Plan(snap, p, states, "terraform/plan.json", Options{Now: fixedNow})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	pb, err := Plan(snap, p, states, "terraform/plan.json", Options{Now: fixedNow})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ja, _ := json.Marshal(pa)
	jb, _ := json.Marshal(pb)
	if string(ja) != string(jb) {
		t.Error("two runs of Plan on the same snapshot produced different reports")
	}
}
