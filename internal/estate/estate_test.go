package estate

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arnocho/spanline/internal/collect"
	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
	"github.com/arnocho/spanline/internal/tfplan"
)

// loadEstate replays the recorded estate scenario: two clusters and the Terraform state
// that travels with them. No cluster is contacted, so the expectations below are exact.
func loadEstate(t *testing.T) ([]*model.Snapshot, []*tfplan.State, time.Time) {
	t.Helper()
	src, err := collect.NewFixtureSource("estate")
	if err != nil {
		t.Fatalf("open the estate fixture: %v", err)
	}
	contexts, err := src.Contexts()
	if err != nil {
		t.Fatalf("list contexts: %v", err)
	}
	want := []string{"aks-prod-weu", "onprem-int"}
	if !reflect.DeepEqual(contexts, want) {
		t.Fatalf("estate fixture contexts = %v, want %v", contexts, want)
	}
	snaps := make([]*model.Snapshot, 0, len(contexts))
	for _, kubeContext := range contexts {
		snap, err := src.Snapshot(context.Background(), kubeContext, "")
		if err != nil {
			t.Fatalf("snapshot %s: %v", kubeContext, err)
		}
		snaps = append(snaps, snap)
	}
	raw, err := src.Extra("terraform/state.json")
	if err != nil {
		t.Fatalf("read the terraform state: %v", err)
	}
	state, err := tfplan.ParseState(raw)
	if err != nil {
		t.Fatalf("parse the terraform state: %v", err)
	}
	state.Path = "terraform/state.json"
	return snaps, []*tfplan.State{state}, src.Now()
}

func buildEstate(t *testing.T) *result.EstateReport {
	t.Helper()
	snaps, states, now := loadEstate(t)
	rep, err := Build(snaps, states, Options{Now: now})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return rep
}

// find returns the one finding for this context, object and kind, or fails.
func find(t *testing.T, rep *result.EstateReport, kubeContext, object, kind string) result.Finding {
	t.Helper()
	var hits []result.Finding
	for _, f := range rep.Risks {
		if f.Context == kubeContext && f.Object == object && f.Kind == kind {
			hits = append(hits, f)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one finding for %s %s %s, got %d", kubeContext, kind, object, len(hits))
	}
	return hits[0]
}

func has(t *testing.T, rep *result.EstateReport, kubeContext, object, kind string) bool {
	t.Helper()
	for _, f := range rep.Risks {
		if f.Context == kubeContext && f.Object == object && f.Kind == kind {
			return true
		}
	}
	return false
}

func TestBuildClusters(t *testing.T) {
	rep := buildEstate(t)
	if len(rep.Clusters) != 2 {
		t.Fatalf("clusters = %d, want 2", len(rep.Clusters))
	}
	want := []result.ClusterSummary{
		{Context: "aks-prod-weu", Nodes: 9, NodesNotReady: 0, Pods: 8, Namespaces: 3, Workloads: 4, Risks: 4, Outages: 0},
		{Context: "onprem-int", Nodes: 4, NodesNotReady: 1, Pods: 4, Namespaces: 1, Workloads: 2, Risks: 2, Outages: 1},
	}
	for i, w := range want {
		got := rep.Clusters[i]
		if got.Context != w.Context {
			t.Fatalf("cluster %d context = %q, want %q", i, got.Context, w.Context)
		}
		if got.Nodes != w.Nodes || got.NodesNotReady != w.NodesNotReady {
			t.Errorf("%s nodes = %d (%d not ready), want %d (%d not ready)",
				w.Context, got.Nodes, got.NodesNotReady, w.Nodes, w.NodesNotReady)
		}
		if got.Pods != w.Pods || got.Namespaces != w.Namespaces || got.Workloads != w.Workloads {
			t.Errorf("%s pods/namespaces/workloads = %d/%d/%d, want %d/%d/%d",
				w.Context, got.Pods, got.Namespaces, got.Workloads, w.Pods, w.Namespaces, w.Workloads)
		}
		if got.Risks != w.Risks || got.Outages != w.Outages {
			t.Errorf("%s risks/outages = %d/%d, want %d/%d", w.Context, got.Risks, got.Outages, w.Risks, w.Outages)
		}
		if got.CollectedAt != rep.GeneratedAt {
			t.Errorf("%s collectedAt = %v, want the fixture time %v", w.Context, got.CollectedAt, rep.GeneratedAt)
		}
	}
}

func TestBuildPools(t *testing.T) {
	rep := buildEstate(t)
	type poolWant struct {
		kubeContext string
		pool        string
		nodes       int
		zones       string
		address     string
		stateFile   string
		workloads   int
		atRisk      int
	}
	want := []poolWant{
		{"aks-prod-weu", "apps", 6, "1,2,3", "azurerm_kubernetes_cluster_node_pool.apps", "terraform/state.json", 3, 3},
		{"aks-prod-weu", "data", 1, "2", "azurerm_kubernetes_cluster_node_pool.data", "terraform/state.json", 1, 0},
		{"aks-prod-weu", "system", 2, "1,2", "", "", 0, 0},
		{"onprem-int", "workers", 4, "", "", "", 2, 1},
	}
	if len(rep.Pools) != len(want) {
		t.Fatalf("pools = %d, want %d: %+v", len(rep.Pools), len(want), rep.Pools)
	}
	for i, w := range want {
		got := rep.Pools[i]
		if got.Context != w.kubeContext || got.Pool != w.pool {
			t.Fatalf("pool %d = %s/%s, want %s/%s", i, got.Context, got.Pool, w.kubeContext, w.pool)
		}
		if got.Nodes != w.nodes {
			t.Errorf("%s nodes = %d, want %d", w.pool, got.Nodes, w.nodes)
		}
		if got.Zones != w.zones {
			t.Errorf("%s zones = %q, want %q", w.pool, got.Zones, w.zones)
		}
		if got.TerraformAddress != w.address {
			t.Errorf("%s terraform address = %q, want %q", w.pool, got.TerraformAddress, w.address)
		}
		if got.StateFile != w.stateFile {
			t.Errorf("%s state file = %q, want %q", w.pool, got.StateFile, w.stateFile)
		}
		if got.Workloads != w.workloads || got.AtRisk != w.atRisk {
			t.Errorf("%s workloads/atRisk = %d/%d, want %d/%d", w.pool, got.Workloads, got.AtRisk, w.workloads, w.atRisk)
		}
	}
	// The data pool holds both warehouse pods, so its utilisation is the one worth pinning.
	data := rep.Pools[1]
	if !near(data.CPUPercent, 50.9) {
		t.Errorf("data pool cpu = %.2f, want about 50.9", data.CPUPercent)
	}
	if !near(data.MemPercent, 57.1) {
		t.Errorf("data pool memory = %.2f, want about 57.1", data.MemPercent)
	}
	// No pool in this fixture is under drain pressure, so none is flagged for it.
	for _, p := range rep.Pools {
		if p.CPUPercent > pressurePercent || p.MemPercent > pressurePercent {
			t.Errorf("pool %s/%s reads as tight (%.1f cpu, %.1f memory), the fixture has no such pool",
				p.Context, p.Pool, p.CPUPercent, p.MemPercent)
		}
	}
}

func TestBuildRisks(t *testing.T) {
	rep := buildEstate(t)

	orders := find(t, rep, "aks-prod-weu", "orders/orders", "Deployment")
	if orders.Severity != result.Risk {
		t.Errorf("orders severity = %s, want %s", orders.Severity, result.Risk)
	}
	if !evidenceHas(orders, "spec.replicas=1") {
		t.Errorf("orders evidence = %v, want it to name spec.replicas", orders.Evidence)
	}

	pdb := find(t, rep, "aks-prod-weu", "payments/payments-api", "PodDisruptionBudget")
	if pdb.Severity != result.Risk {
		t.Errorf("payments-api pdb severity = %s, want %s", pdb.Severity, result.Risk)
	}
	if !evidenceHas(pdb, "status.disruptionsAllowed=0") {
		t.Errorf("pdb evidence = %v, want it to name status.disruptionsAllowed", pdb.Evidence)
	}

	pvc := find(t, rep, "aks-prod-weu", "orders/redis-data-redis-0", "PersistentVolumeClaim")
	if pvc.Severity != result.Risk {
		t.Errorf("redis volume severity = %s, want %s", pvc.Severity, result.Risk)
	}
	if !evidenceHas(pvc, "pv-redis-z2") || !evidenceHas(pvc, zoneLabel) {
		t.Errorf("redis volume evidence = %v, want the volume name and the zone key", pvc.Evidence)
	}

	node := find(t, rep, "onprem-int", "onprem-worker-3", "Node")
	if node.Severity != result.Risk {
		t.Errorf("onprem-worker-3 severity = %s, want %s", node.Severity, result.Risk)
	}
	if !evidenceHas(node, "status.conditions[type=Ready].status=False") {
		t.Errorf("node evidence = %v, want the Ready condition", node.Evidence)
	}

	scheduler := find(t, rep, "onprem-int", "pipeline/scheduler", "Deployment")
	if scheduler.Severity != result.Outage {
		t.Errorf("scheduler severity = %s, want %s", scheduler.Severity, result.Outage)
	}
	if !evidenceHas(scheduler, "status.readyReplicas=0") {
		t.Errorf("scheduler evidence = %v, want status.readyReplicas", scheduler.Evidence)
	}

	// The single replica rule also catches the redis statefulset next to it.
	redis := find(t, rep, "aks-prod-weu", "orders/redis", "StatefulSet")
	if redis.Severity != result.Risk {
		t.Errorf("redis severity = %s, want %s", redis.Severity, result.Risk)
	}

	// A pool nothing in the state claims is not assessed, never a silent pass.
	workers := find(t, rep, "onprem-int", "workers", "NodePool")
	if workers.Severity != result.NotAssessed {
		t.Errorf("workers pool severity = %s, want %s", workers.Severity, result.NotAssessed)
	}
	if has(t, rep, "aks-prod-weu", "apps", "NodePool") || has(t, rep, "aks-prod-weu", "data", "NodePool") {
		t.Error("apps and data are owned by the state, so neither should carry a NodePool finding")
	}
	if !has(t, rep, "aks-prod-weu", "system", "NodePool") {
		t.Error("the system pool has no Terraform owner either, so it must be reported as not assessed")
	}

	helm := find(t, rep, "aks-prod-weu", "payments/payments", "HelmRelease")
	if helm.Severity != result.Info {
		t.Errorf("helm release severity = %s, want %s", helm.Severity, result.Info)
	}
	if !evidenceHas(helm, "payments-api") || !evidenceHas(helm, helmNameAnn) {
		t.Errorf("helm evidence = %v, want the live workload and the release annotation", helm.Evidence)
	}

	// Every finding names its cluster and shows its work.
	for _, f := range rep.Risks {
		if f.Context == "" {
			t.Errorf("finding %s %s has no context", f.Kind, f.Object)
		}
		if len(f.Evidence) == 0 {
			t.Errorf("finding %s %s has no evidence", f.Kind, f.Object)
		}
	}

	// Worst first, then cluster, then object.
	if rep.Risks[0].Severity != result.Outage {
		t.Errorf("first finding = %s, want the outage first", rep.Risks[0].Severity)
	}
	for i := 1; i < len(rep.Risks); i++ {
		a, b := rep.Risks[i-1], rep.Risks[i]
		if a.Severity.Rank() < b.Severity.Rank() {
			t.Fatalf("findings are not sorted by severity: %s before %s", a.Severity, b.Severity)
		}
		if a.Severity.Rank() == b.Severity.Rank() && a.Context > b.Context {
			t.Fatalf("findings are not sorted by context: %s before %s", a.Context, b.Context)
		}
		if a.Severity.Rank() == b.Severity.Rank() && a.Context == b.Context && a.Object > b.Object {
			t.Fatalf("findings are not sorted by object: %s before %s", a.Object, b.Object)
		}
	}
}

func TestStateSummary(t *testing.T) {
	rep := buildEstate(t)
	if len(rep.States) != 1 {
		t.Fatalf("states = %d, want 1", len(rep.States))
	}
	got := rep.States[0]
	want := result.StateSummary{
		Path:      "terraform/state.json",
		Resources: 5,
		NodePools: 2,
		Clusters:  1,
		Matched:   2,
		Unmatched: 0,
	}
	if got != want {
		t.Errorf("state summary = %+v, want %+v", got, want)
	}
	if len(rep.Gaps) != 0 {
		t.Errorf("gaps = %v, want none: the fixture records every resource and a state", rep.Gaps)
	}
}

func TestBuildWithoutState(t *testing.T) {
	snaps, _, now := loadEstate(t)
	rep, err := Build(snaps, nil, Options{Now: now})
	if err != nil {
		t.Fatalf("Build without state: %v", err)
	}
	if len(rep.States) != 0 {
		t.Errorf("states = %+v, want none", rep.States)
	}
	said := false
	for _, g := range rep.Gaps {
		if strings.Contains(g, "Terraform") && strings.Contains(g, "ownership") {
			said = true
		}
	}
	if !said {
		t.Errorf("gaps = %v, want one line saying no state was read so ownership was not assessed", rep.Gaps)
	}
	for _, f := range rep.Risks {
		if f.Kind == "NodePool" && f.Severity == result.NotAssessed {
			t.Errorf("with no state read, no pool should be flagged for ownership, got %s/%s", f.Context, f.Object)
		}
	}
	for _, p := range rep.Pools {
		if p.TerraformAddress != "" || p.StateFile != "" {
			t.Errorf("pool %s/%s claims an owner with no state read", p.Context, p.Pool)
		}
	}
	// The cluster risks survive the missing state: an empty ownership list hides nothing else.
	if !has(t, rep, "onprem-int", "pipeline/scheduler", "Deployment") {
		t.Error("the scheduler outage must still be reported when no state is read")
	}
	if len(rep.Pools) != 4 {
		t.Errorf("pools = %d, want the same 4 pools", len(rep.Pools))
	}
}

func TestPoolPressureAndEmptyContext(t *testing.T) {
	now := time.Date(2026, 9, 16, 3, 20, 0, 0, time.UTC)
	tight := &model.Snapshot{
		Context:     "synthetic",
		CollectedAt: now,
		Nodes: []model.Node{{
			Metadata: model.ObjectMeta{
				Name:   "node-a",
				Labels: map[string]string{"agentpool": "tight", zoneLabel: "1"},
			},
			Status: model.NodeStatus{
				Allocatable: map[string]string{"cpu": "1000m", "memory": "1Gi"},
				Conditions:  []model.NodeCondition{{Type: "Ready", Status: "True"}},
			},
		}},
		Pods: []model.Pod{{
			Metadata: model.ObjectMeta{Name: "hog-0", Namespace: "ns", Labels: map[string]string{"app": "hog"}},
			Spec: model.PodSpec{
				NodeName: "node-a",
				Containers: []model.Container{{
					Name:      "hog",
					Resources: model.ResourceRequirements{Requests: map[string]string{"cpu": "950m", "memory": "900Mi"}},
				}},
			},
		}},
	}
	blind := &model.Snapshot{
		Context:     "no-nodes",
		CollectedAt: now,
		Gaps:        []model.CoverageGap{{Resource: "nodes", Reason: "forbidden"}},
	}
	rep, err := Build([]*model.Snapshot{tight, blind}, nil, Options{Now: now})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	pressure := find(t, rep, "synthetic", "tight", "NodePool")
	if pressure.Severity != result.Risk {
		t.Errorf("pressure severity = %s, want %s", pressure.Severity, result.Risk)
	}
	if !strings.Contains(pressure.Reason, "cpu") || !strings.Contains(pressure.Reason, "memory") {
		t.Errorf("pressure reason = %q, want it to name cpu and memory", pressure.Reason)
	}
	sawGap, sawEmpty := false, false
	for _, g := range rep.Gaps {
		if strings.Contains(g, "no-nodes") && strings.Contains(g, "nodes") {
			sawGap = true
		}
		if strings.Contains(g, "no-nodes") && strings.Contains(g, "returned no nodes") {
			sawEmpty = true
		}
	}
	if !sawGap || !sawEmpty {
		t.Errorf("gaps = %v, want the unread nodes and the empty context both named", rep.Gaps)
	}
}

func TestBuildIsDeterministicAndNeedsSnapshots(t *testing.T) {
	first := buildEstate(t)
	second := buildEstate(t)
	if !reflect.DeepEqual(first, second) {
		t.Error("two runs over the same fixture produced different reports")
	}
	if _, err := Build(nil, nil, Options{}); err == nil {
		t.Error("Build with no snapshot should fail rather than report an empty estate")
	}
}

func near(got, want float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d < 0.1
}

func evidenceHas(f result.Finding, want string) bool {
	for _, e := range f.Evidence {
		if strings.Contains(e, want) {
			return true
		}
	}
	return false
}
