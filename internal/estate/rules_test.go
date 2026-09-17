package estate

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
	"github.com/arnocho/spanline/internal/tfplan"
)

// The tests below build every snapshot by hand, one defect each, so a regression is named by
// the test that catches it rather than by a drift in the recorded scenario.

var testNow = time.Date(2026, 9, 16, 3, 20, 0, 0, time.UTC)

func intp(n int) *int { return &n }

func nodeWith(name string, labels map[string]string, cpu, mem string, ready bool) model.Node {
	status := "True"
	if !ready {
		status = "False"
	}
	n := model.Node{
		Metadata: model.ObjectMeta{Name: name, Labels: labels},
		Status: model.NodeStatus{
			Conditions: []model.NodeCondition{{Type: "Ready", Status: status}},
		},
	}
	if cpu != "" || mem != "" {
		n.Status.Allocatable = map[string]string{}
		if cpu != "" {
			n.Status.Allocatable["cpu"] = cpu
		}
		if mem != "" {
			n.Status.Allocatable["memory"] = mem
		}
	}
	return n
}

func podOn(ns, name, node, phase string, labels map[string]string, cpu, mem string) model.Pod {
	req := map[string]string{}
	if cpu != "" {
		req["cpu"] = cpu
	}
	if mem != "" {
		req["memory"] = mem
	}
	return model.Pod{
		Metadata: model.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: model.PodSpec{
			NodeName:   node,
			Containers: []model.Container{{Name: "main", Resources: model.ResourceRequirements{Requests: req}}},
		},
		Status: model.PodStatus{Phase: phase},
	}
}

func mounting(p model.Pod, claim string) model.Pod {
	p.Spec.Volumes = append(p.Spec.Volumes, model.Volume{
		Name: "data", PersistentVolumeClaim: &model.PVCVolumeSource{ClaimName: claim},
	})
	return p
}

func workloadWith(ns, name string, replicas *int, ready int, labels map[string]string) model.Workload {
	return model.Workload{
		Metadata: model.ObjectMeta{Name: name, Namespace: ns},
		Spec: model.WorkloadSpec{
			Replicas: replicas,
			Selector: &model.LabelSelector{MatchLabels: labels},
			Template: model.PodTemplateSpec{Metadata: model.ObjectMeta{Labels: labels}},
		},
		Status: model.WorkloadStatus{Replicas: ready, ReadyReplicas: ready, AvailableReplicas: ready},
	}
}

func snapshotWith(ctx string) *model.Snapshot {
	return &model.Snapshot{Context: ctx, CollectedAt: testNow}
}

func stateWith(path string, resources ...tfplan.StateResource) *tfplan.State {
	return &tfplan.State{Version: 4, Path: path, Resources: resources}
}

func poolResource(name string) tfplan.StateResource {
	return tfplan.StateResource{
		Mode: "managed", Type: "azurerm_kubernetes_cluster_node_pool", Name: name,
		Instances: []tfplan.StateInstance{{Attributes: map[string]any{"name": name}}},
	}
}

func clusterResource(name string) tfplan.StateResource {
	return tfplan.StateResource{
		Mode: "managed", Type: "azurerm_kubernetes_cluster", Name: "main",
		Instances: []tfplan.StateInstance{{Attributes: map[string]any{"name": name}}},
	}
}

func helmResource(ns, name string) tfplan.StateResource {
	return tfplan.StateResource{
		Mode: "managed", Type: "helm_release", Name: name,
		Instances: []tfplan.StateInstance{{Attributes: map[string]any{"name": name, "namespace": ns}}},
	}
}

func build(t *testing.T, snaps []*model.Snapshot, states []*tfplan.State) *result.EstateReport {
	t.Helper()
	rep, err := Build(snaps, states, Options{Now: testNow})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return rep
}

func poolRow(t *testing.T, rep *result.EstateReport, ctx, pool string) result.PoolSummary {
	t.Helper()
	for _, p := range rep.Pools {
		if p.Context == ctx && p.Pool == pool {
			return p
		}
	}
	t.Fatalf("no pool %s/%s in %+v", ctx, pool, rep.Pools)
	return result.PoolSummary{}
}

func gapSaying(rep *result.EstateReport, parts ...string) int {
	n := 0
	for _, g := range rep.Gaps {
		all := true
		for _, p := range parts {
			if !strings.Contains(g, p) {
				all = false
			}
		}
		if all {
			n++
		}
	}
	return n
}

// A pod that has finished holds no cpu and no memory, so it must not push a pool toward the
// drain pressure threshold, and a node's pod count must not present it as something to protect.
func TestTerminalPodsDoNotCountAsRequests(t *testing.T) {
	s := snapshotWith("c")
	s.Nodes = []model.Node{
		nodeWith("node-a", map[string]string{"agentpool": "apps"}, "1000m", "1Gi", true),
		nodeWith("node-b", map[string]string{"agentpool": "apps"}, "1000m", "1Gi", false),
	}
	web := map[string]string{"app": "web"}
	s.Deployments = []model.Workload{workloadWith("shop", "web", intp(2), 2, web)}
	s.Pods = []model.Pod{
		podOn("shop", "web-1", "node-a", "Running", web, "100m", "100Mi"),
		podOn("shop", "web-2", "node-b", "Running", web, "100m", "100Mi"),
		podOn("batch", "job-done", "node-a", "Succeeded", map[string]string{"job": "x"}, "900m", "900Mi"),
		podOn("batch", "job-failed", "node-b", "Failed", map[string]string{"job": "y"}, "900m", "900Mi"),
	}
	rep := build(t, []*model.Snapshot{s}, nil)
	apps := poolRow(t, rep, "c", "apps")
	if !near(apps.CPUPercent, 10) {
		t.Errorf("apps cpu = %.2f percent, want 10: finished pods hold no cpu", apps.CPUPercent)
	}
	if !near(apps.MemPercent, 9.77) {
		t.Errorf("apps memory = %.2f percent, want about 9.8: finished pods hold no memory", apps.MemPercent)
	}
	if has(t, rep, "c", "apps", "NodePool") {
		t.Error("the apps pool is flagged for pressure although only finished pods push it there")
	}
	node := find(t, rep, "c", "node-b", "Node")
	if !evidenceHas(node, "1 live pod(s)") {
		t.Errorf("node-b evidence = %v, want it to count 1 live pod, not the failed one", node.Evidence)
	}
}

// A budget that selects no pod blocks no drain, so a status of zero allowed disruptions is
// information, not a risk. The same status with a live pod under it stays a risk.
func TestBudgetCoveringNoPodIsNotARisk(t *testing.T) {
	s := snapshotWith("c")
	s.Nodes = []model.Node{nodeWith("node-a", map[string]string{"agentpool": "apps"}, "4", "8Gi", true)}
	batch := map[string]string{"app": "batch"}
	web := map[string]string{"app": "web"}
	s.Deployments = []model.Workload{
		workloadWith("shop", "batch", intp(0), 0, batch),
		workloadWith("shop", "web", intp(2), 2, web),
	}
	s.Pods = []model.Pod{
		podOn("shop", "web-1", "node-a", "Running", web, "100m", "100Mi"),
		podOn("shop", "web-2", "node-a", "Running", web, "100m", "100Mi"),
	}
	s.PDBs = []model.PodDisruptionBudget{
		{
			Metadata: model.ObjectMeta{Name: "batch", Namespace: "shop"},
			Spec:     model.PDBSpec{Selector: &model.LabelSelector{MatchLabels: batch}, MinAvailable: "1"},
			Status:   model.PDBStatus{DisruptionsAllowed: 0, CurrentHealthy: 0, DesiredHealthy: 0, ExpectedPods: 0},
		},
		{
			Metadata: model.ObjectMeta{Name: "web-stale", Namespace: "shop"},
			Spec:     model.PDBSpec{Selector: &model.LabelSelector{MatchLabels: web}, MinAvailable: "2"},
			Status:   model.PDBStatus{DisruptionsAllowed: 0, CurrentHealthy: 0, DesiredHealthy: 0, ExpectedPods: 0},
		},
	}
	rep := build(t, []*model.Snapshot{s}, nil)

	empty := find(t, rep, "c", "shop/batch", "PodDisruptionBudget")
	if empty.Severity != result.Info {
		t.Errorf("budget over no pod: severity = %s, want %s", empty.Severity, result.Info)
	}
	if !strings.Contains(empty.Reason, "no pod") {
		t.Errorf("budget over no pod: reason = %q, want it to say it covers no pod", empty.Reason)
	}
	if !evidenceHas(empty, "status.expectedPods=0") {
		t.Errorf("budget over no pod: evidence = %v, want status.expectedPods", empty.Evidence)
	}
	if got := poolRow(t, rep, "c", "apps").AtRisk; got != 1 {
		t.Errorf("apps at risk = %d, want 1: web under its stale budget, never the scaled down batch deployment", got)
	}

	stale := find(t, rep, "c", "shop/web-stale", "PodDisruptionBudget")
	if stale.Severity != result.Risk {
		t.Errorf("budget with live pods under it: severity = %s, want %s", stale.Severity, result.Risk)
	}
	if !evidenceHas(stale, "spec.selector covers Deployment shop/web") {
		t.Errorf("budget with live pods: evidence = %v, want the web deployment named", stale.Evidence)
	}
}

// Selectors written with matchExpressions, or left empty on purpose, select pods too.
func TestBudgetSelectorsBeyondMatchLabels(t *testing.T) {
	s := snapshotWith("c")
	s.Nodes = []model.Node{nodeWith("node-a", map[string]string{"agentpool": "apps"}, "4", "8Gi", true)}
	web := map[string]string{"app": "web"}
	api := map[string]string{"app": "api"}
	s.Deployments = []model.Workload{
		workloadWith("shop", "web", intp(2), 2, web),
		workloadWith("shop", "api", intp(2), 2, api),
	}
	s.Pods = []model.Pod{
		podOn("shop", "web-1", "node-a", "Running", web, "100m", "100Mi"),
		podOn("shop", "api-1", "node-a", "Running", api, "100m", "100Mi"),
	}
	var byExpression, everything model.PodDisruptionBudget
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"by-expression","namespace":"shop"},
	  "spec":{"selector":{"matchExpressions":[{"key":"app","operator":"In","values":["web"]}]},"minAvailable":"2"},
	  "status":{"disruptionsAllowed":0,"currentHealthy":2,"desiredHealthy":2,"expectedPods":2}}`), &byExpression); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"everything","namespace":"shop"},
	  "spec":{"selector":{},"minAvailable":"100%"},
	  "status":{"disruptionsAllowed":0,"currentHealthy":2,"desiredHealthy":2,"expectedPods":2}}`), &everything); err != nil {
		t.Fatal(err)
	}
	s.PDBs = []model.PodDisruptionBudget{byExpression, everything}
	rep := build(t, []*model.Snapshot{s}, nil)

	expr := find(t, rep, "c", "shop/by-expression", "PodDisruptionBudget")
	if !evidenceHas(expr, "spec.selector covers Deployment shop/web") || evidenceHas(expr, "shop/api") {
		t.Errorf("expression selector: evidence = %v, want web covered and api not", expr.Evidence)
	}
	all := find(t, rep, "c", "shop/everything", "PodDisruptionBudget")
	if !evidenceHas(all, "spec.selector covers Deployment shop/web") || !evidenceHas(all, "spec.selector covers Deployment shop/api") {
		t.Errorf("empty selector: evidence = %v, want both deployments covered, as policy/v1 documents", all.Evidence)
	}
}

// A volume pinned to one node is more fragile than one pinned to a zone, and a legacy zone key
// must be named as read. A claim that is not bound while a live pod mounts it is a risk.
func TestVolumePinsAndUnboundClaims(t *testing.T) {
	s := snapshotWith("c")
	s.Nodes = []model.Node{nodeWith("node-a", map[string]string{"agentpool": "apps"}, "4", "8Gi", true)}
	db := map[string]string{"app": "db"}
	cache := map[string]string{"app": "cache"}
	queue := map[string]string{"app": "queue"}
	s.StatefulSets = []model.Workload{
		workloadWith("shop", "db", intp(2), 2, db),
		workloadWith("shop", "cache", intp(2), 2, cache),
		workloadWith("shop", "queue", intp(2), 2, queue),
	}
	s.Pods = []model.Pod{
		mounting(podOn("shop", "db-0", "node-a", "Running", db, "100m", "100Mi"), "data-db-0"),
		mounting(podOn("shop", "cache-0", "node-a", "Running", cache, "100m", "100Mi"), "data-cache-0"),
		mounting(podOn("shop", "queue-0", "", "Pending", queue, "100m", "100Mi"), "data-queue-0"),
	}
	bound := func(name, volume string) model.PersistentVolumeClaim {
		pvc := model.PersistentVolumeClaim{
			Metadata: model.ObjectMeta{Name: name, Namespace: "shop"},
			Spec:     model.PVCSpec{VolumeName: volume},
		}
		pvc.Status.Phase = "Bound"
		return pvc
	}
	pending := model.PersistentVolumeClaim{Metadata: model.ObjectMeta{Name: "data-queue-0", Namespace: "shop"}}
	pending.Status.Phase = "Pending"
	idle := model.PersistentVolumeClaim{Metadata: model.ObjectMeta{Name: "data-idle", Namespace: "shop"}}
	idle.Status.Phase = "Pending"
	s.PVCs = []model.PersistentVolumeClaim{bound("data-db-0", "pv-local-a"), bound("data-cache-0", "pv-legacy-z1"), pending, idle}

	var local, legacy model.PersistentVolume
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"pv-local-a"},"spec":{"nodeAffinity":{"required":{"nodeSelectorTerms":[
	  {"matchExpressions":[{"key":"kubernetes.io/hostname","operator":"In","values":["node-a"]}]}]}}},"status":{"phase":"Bound"}}`), &local); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"pv-legacy-z1"},"spec":{"nodeAffinity":{"required":{"nodeSelectorTerms":[
	  {"matchExpressions":[{"key":"failure-domain.beta.kubernetes.io/zone","operator":"In","values":["z1"]}]}]}}},"status":{"phase":"Bound"}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	s.PVs = []model.PersistentVolume{local, legacy}
	rep := build(t, []*model.Snapshot{s}, nil)

	node := find(t, rep, "c", "shop/data-db-0", "PersistentVolumeClaim")
	if node.Severity != result.Risk {
		t.Errorf("node pinned volume: severity = %s, want %s", node.Severity, result.Risk)
	}
	if !strings.Contains(node.Reason, "node-a") || !evidenceHas(node, "kubernetes.io/hostname") {
		t.Errorf("node pinned volume: reason %q evidence %v, want the node and the hostname key named", node.Reason, node.Evidence)
	}
	if !evidenceHas(node, "StatefulSet shop/db") {
		t.Errorf("node pinned volume: evidence = %v, want the statefulset that mounts it", node.Evidence)
	}

	zone := find(t, rep, "c", "shop/data-cache-0", "PersistentVolumeClaim")
	if !evidenceHas(zone, legacyZoneLabel) || evidenceHas(zone, zoneLabel) {
		t.Errorf("legacy zone key: evidence = %v, want the key actually read, %s", zone.Evidence, legacyZoneLabel)
	}

	unbound := find(t, rep, "c", "shop/data-queue-0", "PersistentVolumeClaim")
	if unbound.Severity != result.Risk {
		t.Errorf("pending claim with a pod on it: severity = %s, want %s", unbound.Severity, result.Risk)
	}
	if !evidenceHas(unbound, "status.phase=Pending") || !evidenceHas(unbound, "shop/queue-0") {
		t.Errorf("pending claim: evidence = %v, want the phase and the pod mounting it", unbound.Evidence)
	}
	if has(t, rep, "c", "shop/data-idle", "PersistentVolumeClaim") {
		t.Error("a pending claim nothing mounts is normal with WaitForFirstConsumer and must not be flagged")
	}
	if poolRow(t, rep, "c", "apps").AtRisk != 2 {
		t.Errorf("apps at risk = %d, want 2: db and cache are pinned, queue has no node yet", poolRow(t, rep, "c", "apps").AtRisk)
	}
}

// Kubernetes defaults spec.replicas to 1 when it is unset; status.replicas is not a substitute.
func TestReplicasUnsetDefaultsToOne(t *testing.T) {
	s := snapshotWith("c")
	s.Nodes = []model.Node{nodeWith("node-a", map[string]string{"agentpool": "apps"}, "4", "8Gi", true)}
	one := map[string]string{"app": "one"}
	down := map[string]string{"app": "down"}
	s.Deployments = []model.Workload{
		workloadWith("shop", "one", nil, 1, one),
		workloadWith("shop", "down", nil, 0, down),
	}
	s.Deployments[1].Status.Replicas = 0
	s.DaemonSets = []model.Workload{workloadWith("kube-system", "agent", nil, 0, map[string]string{"app": "agent"})}
	s.Pods = []model.Pod{podOn("shop", "one-1", "node-a", "Running", one, "100m", "100Mi")}
	rep := build(t, []*model.Snapshot{s}, nil)

	single := find(t, rep, "c", "shop/one", "Deployment")
	if single.Severity != result.Risk {
		t.Errorf("unset replicas with one ready: severity = %s, want %s", single.Severity, result.Risk)
	}
	if !evidenceHas(single, "spec.replicas=1") || !evidenceHas(single, "default") {
		t.Errorf("unset replicas: evidence = %v, want the default of 1 named as such", single.Evidence)
	}
	outage := find(t, rep, "c", "shop/down", "Deployment")
	if outage.Severity != result.Outage {
		t.Errorf("unset replicas with nothing ready: severity = %s, want %s", outage.Severity, result.Outage)
	}
	if !strings.Contains(outage.Reason, "1 is wanted") {
		t.Errorf("outage reason = %q, want it to count the defaulted replica", outage.Reason)
	}
	if has(t, rep, "c", "kube-system/agent", "DaemonSet") {
		t.Error("a daemonset has no spec.replicas and must not inherit the default")
	}
}

// The outage reason reads as a sentence whatever the count.
func TestOutageWordingAgreesInNumber(t *testing.T) {
	s := snapshotWith("c")
	s.Nodes = []model.Node{nodeWith("node-a", map[string]string{"agentpool": "apps"}, "4", "8Gi", true)}
	s.Deployments = []model.Workload{
		workloadWith("shop", "three", intp(3), 0, map[string]string{"app": "three"}),
		workloadWith("shop", "one", intp(1), 0, map[string]string{"app": "one"}),
	}
	rep := build(t, []*model.Snapshot{s}, nil)
	if r := find(t, rep, "c", "shop/three", "Deployment").Reason; !strings.Contains(r, "3 are wanted") {
		t.Errorf("reason = %q, want \"3 are wanted\"", r)
	}
	if r := find(t, rep, "c", "shop/one", "Deployment").Reason; !strings.Contains(r, "1 is wanted") {
		t.Errorf("reason = %q, want \"1 is wanted\"", r)
	}
}

// One Terraform resource owns one live pool. A name shared by two states, or by two clusters,
// is never handed to a pool the state does not name: it is reported as not assessed, with the
// candidates listed, and the name match stays available when it is the only candidate.
func TestPoolOwnershipAcrossStatesAndClusters(t *testing.T) {
	cluster := func(ctx string) *model.Snapshot {
		s := snapshotWith(ctx)
		s.Nodes = []model.Node{nodeWith(ctx+"-node", map[string]string{"agentpool": "apps"}, "4", "8Gi", true)}
		return s
	}
	weuState := stateWith("weu.tfstate", clusterResource("aks-prod-weu"), poolResource("apps"))
	neuState := stateWith("neu.tfstate", clusterResource("aks-prod-neu"), poolResource("apps"))

	t.Run("each cluster named by its state", func(t *testing.T) {
		rep := build(t, []*model.Snapshot{cluster("aks-prod-weu"), cluster("aks-prod-neu")}, []*tfplan.State{weuState, neuState})
		if got := poolRow(t, rep, "aks-prod-weu", "apps").StateFile; got != "weu.tfstate" {
			t.Errorf("weu apps owned by %q, want weu.tfstate", got)
		}
		if got := poolRow(t, rep, "aks-prod-neu", "apps").StateFile; got != "neu.tfstate" {
			t.Errorf("neu apps owned by %q, want neu.tfstate", got)
		}
		for _, st := range rep.States {
			if st.Matched != 1 || st.Unmatched != 0 {
				t.Errorf("%s matched/unmatched = %d/%d, want 1/0", st.Path, st.Matched, st.Unmatched)
			}
		}
	})

	t.Run("contexts named differently from both clusters", func(t *testing.T) {
		rep := build(t, []*model.Snapshot{cluster("weu-ctx"), cluster("neu-ctx")}, []*tfplan.State{weuState, neuState})
		for _, ctx := range []string{"weu-ctx", "neu-ctx"} {
			if got := poolRow(t, rep, ctx, "apps").TerraformAddress; got != "" {
				t.Errorf("%s apps claims %q although two states compete for the name", ctx, got)
			}
			f := find(t, rep, ctx, "apps", "NodePool")
			if f.Severity != result.NotAssessed {
				t.Errorf("%s apps severity = %s, want %s", ctx, f.Severity, result.NotAssessed)
			}
			if strings.Contains(f.Reason, "no state read declares") {
				t.Errorf("%s apps reason = %q, but two states do declare the name", ctx, f.Reason)
			}
			if !evidenceHas(f, "weu.tfstate") || !evidenceHas(f, "neu.tfstate") {
				t.Errorf("%s apps evidence = %v, want both candidate states named", ctx, f.Evidence)
			}
		}
		for _, st := range rep.States {
			if st.Matched != 0 || st.Unmatched != 1 {
				t.Errorf("%s matched/unmatched = %d/%d, want 0/1", st.Path, st.Matched, st.Unmatched)
			}
		}
	})

	t.Run("a resource claimed by its own cluster is not reused for another", func(t *testing.T) {
		rep := build(t, []*model.Snapshot{cluster("aks-prod-weu"), cluster("aks-prod-neu")}, []*tfplan.State{weuState})
		if got := poolRow(t, rep, "aks-prod-weu", "apps").StateFile; got != "weu.tfstate" {
			t.Errorf("weu apps owned by %q, want weu.tfstate", got)
		}
		if got := poolRow(t, rep, "aks-prod-neu", "apps").TerraformAddress; got != "" {
			t.Errorf("neu apps claims %q, which already owns the weu pool", got)
		}
		f := find(t, rep, "aks-prod-neu", "apps", "NodePool")
		if f.Severity != result.NotAssessed || !evidenceHas(f, "already matched") {
			t.Errorf("neu apps = %s %v, want not assessed with the prior match named", f.Severity, f.Evidence)
		}
		if rep.States[0].Matched != 1 || rep.States[0].Unmatched != 0 {
			t.Errorf("state matched/unmatched = %d/%d, want 1/0", rep.States[0].Matched, rep.States[0].Unmatched)
		}
	})

	t.Run("a state naming another cluster read is not a candidate for this one", func(t *testing.T) {
		weu := cluster("aks-prod-weu")
		weu.Nodes[0].Metadata.Labels["agentpool"] = "system"
		rep := build(t, []*model.Snapshot{weu, cluster("neu-ctx")}, []*tfplan.State{weuState})
		if got := poolRow(t, rep, "neu-ctx", "apps").TerraformAddress; got != "" {
			t.Errorf("neu-ctx apps claims %q, a resource whose state names cluster aks-prod-weu", got)
		}
		if rep.States[0].Matched != 0 || rep.States[0].Unmatched != 1 {
			t.Errorf("state matched/unmatched = %d/%d, want 0/1", rep.States[0].Matched, rep.States[0].Unmatched)
		}
	})

	t.Run("the name match still works when it is the only candidate", func(t *testing.T) {
		rep := build(t, []*model.Snapshot{cluster("prod-weu")}, []*tfplan.State{weuState})
		if got := poolRow(t, rep, "prod-weu", "apps").TerraformAddress; got != "azurerm_kubernetes_cluster_node_pool.apps" {
			t.Errorf("prod-weu apps owned by %q, want the single candidate", got)
		}
		if has(t, rep, "prod-weu", "apps", "NodePool") {
			t.Error("an owned pool carries no NodePool finding")
		}
	})

	t.Run("a pool no state declares keeps the plain reason", func(t *testing.T) {
		rep := build(t, []*model.Snapshot{cluster("prod-weu")}, []*tfplan.State{stateWith("x.tfstate", poolResource("gpu"))})
		f := find(t, rep, "prod-weu", "apps", "NodePool")
		if !strings.Contains(f.Reason, "no state read declares") {
			t.Errorf("reason = %q, want the plain no-declaration reason", f.Reason)
		}
		if rep.States[0].Matched != 0 || rep.States[0].Unmatched != 1 {
			t.Errorf("declared but absent live: matched/unmatched = %d/%d, want 0/1", rep.States[0].Matched, rep.States[0].Unmatched)
		}
	})
}

// A Helm release found in two clusters yields one finding per cluster, each listing its own
// workloads, never one finding that mixes clusters under the first context found.
func TestHelmReleaseFoundInSeveralContexts(t *testing.T) {
	annotated := func(ctx string) *model.Snapshot {
		s := snapshotWith(ctx)
		s.Nodes = []model.Node{nodeWith(ctx+"-node", map[string]string{"agentpool": "apps"}, "4", "8Gi", true)}
		w := workloadWith("pipeline", "ingest-"+ctx, intp(2), 2, map[string]string{"app": "ingest"})
		w.Metadata.Annotations = map[string]string{helmNameAnn: "pipeline", helmNamespaceAnn: "pipeline"}
		s.Deployments = []model.Workload{w}
		return s
	}
	state := stateWith("helm.tfstate", helmResource("pipeline", "pipeline"))
	rep := build(t, []*model.Snapshot{annotated("ctx-a"), annotated("ctx-b")}, []*tfplan.State{state})

	a := find(t, rep, "ctx-a", "pipeline/pipeline", "HelmRelease")
	b := find(t, rep, "ctx-b", "pipeline/pipeline", "HelmRelease")
	if !strings.Contains(a.Reason, "ingest-ctx-a") || strings.Contains(a.Reason, "ingest-ctx-b") {
		t.Errorf("ctx-a reason = %q, want only its own workload", a.Reason)
	}
	if !strings.Contains(b.Reason, "ingest-ctx-b") || strings.Contains(b.Reason, "ingest-ctx-a") {
		t.Errorf("ctx-b reason = %q, want only its own workload", b.Reason)
	}
	for _, f := range rep.Risks {
		if f.Kind == "HelmRelease" && f.Context == "" {
			t.Errorf("a release that was found live carries no context: %+v", f)
		}
	}
}

// Gaps are said once each, and a quantity that cannot be read is a gap, not a zero.
func TestGapLinesAreExactAndComplete(t *testing.T) {
	s := snapshotWith("c")
	s.Gaps = []model.CoverageGap{
		{Resource: "events", Reason: "forbidden for this identity"},
		{Resource: "events", Reason: "forbidden for this identity"},
	}
	s.Nodes = []model.Node{
		nodeWith("node-a", map[string]string{"agentpool": "apps"}, "4", "8Gi", true),
		nodeWith("node-b", map[string]string{"agentpool": "apps"}, "", "", true),
	}
	web := map[string]string{"app": "web"}
	s.Deployments = []model.Workload{workloadWith("shop", "web", intp(2), 2, web)}
	s.Pods = []model.Pod{
		podOn("shop", "web-1", "node-a", "Running", web, "100m", "100Mi"),
		podOn("shop", "web-2", "node-b", "Running", web, "garbage", "100Mi"),
	}
	rep := build(t, []*model.Snapshot{s}, nil)
	if n := gapSaying(rep, "c: events was not read"); n != 1 {
		t.Errorf("the events gap is said %d times, want once: %v", n, rep.Gaps)
	}
	if n := gapSaying(rep, "c:", "allocatable", "1 node(s)"); n != 1 {
		t.Errorf("gaps = %v, want one line saying node-b's allocatable could not be read", rep.Gaps)
	}
	if n := gapSaying(rep, "c:", "request", "1 "); n != 1 {
		t.Errorf("gaps = %v, want one line saying one request value could not be read", rep.Gaps)
	}
}
