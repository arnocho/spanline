package impact

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
	"github.com/arnocho/spanline/internal/tfplan"
)

// The tests in this file build every snapshot by hand, so each one pins one rule to one field.

var collected = time.Date(2026, 9, 17, 6, 0, 0, 0, time.UTC)

func mkNode(name, pool, zone string) model.Node {
	n := model.Node{}
	n.Metadata.Name = name
	n.Metadata.Labels = map[string]string{"kubernetes.io/hostname": name}
	if pool != "" {
		n.Metadata.Labels["agentpool"] = pool
	}
	if zone != "" {
		n.Metadata.Labels["topology.kubernetes.io/zone"] = zone
	}
	n.Status.Allocatable = map[string]string{"cpu": "4", "memory": "16Gi"}
	return n
}

func owner(kind, name string) model.OwnerReference {
	return model.OwnerReference{Kind: kind, Name: name}
}

func mkPod(ns, name, node string, labels map[string]string, owners ...model.OwnerReference) model.Pod {
	p := model.Pod{}
	p.Metadata.Namespace = ns
	p.Metadata.Name = name
	p.Metadata.Labels = labels
	p.Metadata.OwnerReferences = owners
	p.Spec.NodeName = node
	p.Spec.Containers = []model.Container{{Name: "c", Resources: model.ResourceRequirements{
		Requests: map[string]string{"cpu": "100m", "memory": "128Mi"},
	}}}
	p.Status.Phase = "Running"
	return p
}

func mkDeploy(ns, name string, replicas int, sel map[string]string) model.Workload {
	w := model.Workload{Kind: "Deployment"}
	w.Metadata.Namespace = ns
	w.Metadata.Name = name
	w.Spec.Replicas = &replicas
	w.Spec.Selector = &model.LabelSelector{MatchLabels: sel}
	return w
}

func mkSnap(nodes []model.Node, pods []model.Pod) *model.Snapshot {
	return &model.Snapshot{Context: "hand-built", CollectedAt: collected, Nodes: nodes, Pods: pods}
}

func mkPDB(ns, name string, sel map[string]string, allowed int) model.PodDisruptionBudget {
	pdb := model.PodDisruptionBudget{}
	pdb.Metadata.Namespace = ns
	pdb.Metadata.Name = name
	if sel != nil {
		pdb.Spec.Selector = &model.LabelSelector{MatchLabels: sel}
	}
	pdb.Status.DisruptionsAllowed = allowed
	return pdb
}

func mkPVC(ns, name, volume string) model.PersistentVolumeClaim {
	c := model.PersistentVolumeClaim{}
	c.Metadata.Namespace = ns
	c.Metadata.Name = name
	c.Spec.VolumeName = volume
	c.Status.Phase = "Bound"
	return c
}

func mkPV(name string, reqs ...model.NodeSelectorRequirement) model.PersistentVolume {
	v := model.PersistentVolume{}
	v.Metadata.Name = name
	if len(reqs) > 0 {
		v.Spec.NodeAffinity = &model.VolumeNodeAffinity{}
		v.Spec.NodeAffinity.Required = &struct {
			NodeSelectorTerms []model.NodeSelectorTerm `json:"nodeSelectorTerms,omitempty"`
		}{NodeSelectorTerms: []model.NodeSelectorTerm{{MatchExpressions: reqs}}}
	}
	return v
}

func in(key string, values ...string) model.NodeSelectorRequirement {
	return model.NodeSelectorRequirement{Key: key, Operator: "In", Values: values}
}

func withClaim(p model.Pod, claim string) model.Pod {
	p.Spec.Volumes = append(p.Spec.Volumes, model.Volume{Name: "data", PersistentVolumeClaim: &model.PVCVolumeSource{ClaimName: claim}})
	return p
}

func withPhase(p model.Pod, phase string) model.Pod {
	p.Status.Phase = phase
	return p
}

func run(t *testing.T, snap *model.Snapshot, selector string) *result.ImpactReport {
	t.Helper()
	rep, err := Nodes(snap, selector, Options{Now: collected})
	if err != nil {
		t.Fatalf("Nodes(%q): %v", selector, err)
	}
	return rep
}

func countKind(rep *result.ImpactReport, sev result.Severity, kind string) int {
	n := 0
	for _, f := range rep.Findings {
		if f.Severity == sev && f.Kind == kind {
			n++
		}
	}
	return n
}

func TestSelectorEdgesParseAsDocumented(t *testing.T) {
	a := mkNode("a", "apps", "1")
	a.Metadata.Labels["team"] = "x=y"
	apps := mkNode("apps", "apps", "1") // a node whose name is also a pool name
	snap := mkSnap([]model.Node{a, apps, mkNode("b", "data", "2")}, nil)

	cases := []struct {
		selector string
		want     string
	}{
		{"label=team=x=y", "a"},
		{" node= a , b , ", "a,b"},
		{"a,b,", "a,b"},
		{"apps", "apps"},
		{"pool=apps", "a,apps"},
		{"zone=2", "b"},
		{"node=a,a,a", "a"},
	}
	for _, c := range cases {
		rep := run(t, snap, c.selector)
		if got := names(rep); got != c.want {
			t.Errorf("Nodes(%q) selected %q, want %q", c.selector, got, c.want)
		}
	}
	for _, bad := range []string{"", " ", "pool=", "pool= ", "label=k=", "label==v", "node=,", ","} {
		if _, err := Nodes(snap, bad, Options{Now: collected}); err == nil {
			t.Errorf("Nodes(%q) returned no error", bad)
		}
	}
}

func TestDuplicateMissingNamesAreReportedOnce(t *testing.T) {
	snap := mkSnap([]model.Node{mkNode("a", "apps", "1")}, nil)
	rep := run(t, snap, "node=ghost,ghost,a")
	if n := countKind(rep, result.NotAssessed, "Node"); n != 1 {
		t.Errorf("%d NOT ASSESSED Node findings for one unknown name, want 1", n)
	}
	if names(rep) != "a" {
		t.Errorf("nodes = %q, want a", names(rep))
	}
}

func TestExitCodesFromHandBuiltSnapshots(t *testing.T) {
	snap := mkSnap([]model.Node{mkNode("a", "apps", "1"), mkNode("b", "apps", "1")}, nil)

	clean := run(t, snap, "node=a")
	for _, f := range clean.Findings {
		if f.Severity != result.Info {
			t.Errorf("an empty node produced a %s finding: %+v", f.Severity, f)
		}
	}
	if clean.ExitCode != 0 || clean.Verdict != result.Info {
		t.Errorf("INFO only: exit %d verdict %s, want 0 and INFO", clean.ExitCode, clean.Verdict)
	}

	nothing := run(t, snap, "pool=ghost")
	if nothing.ExitCode != 1 || nothing.Verdict != result.NotAssessed {
		t.Errorf("empty match: exit %d verdict %s, want 1 and NOT ASSESSED", nothing.ExitCode, nothing.Verdict)
	}
	if len(nothing.Findings) != 1 || nothing.Findings[0].Kind != "Selector" {
		t.Errorf("empty match findings = %+v, want the one Selector finding", nothing.Findings)
	}

	unknown := run(t, snap, "node=a,ghost")
	if unknown.ExitCode != 1 {
		t.Errorf("NOT ASSESSED next to INFO exits %d, want 1", unknown.ExitCode)
	}
}

func TestOwnerKindsSpanlineDoesNotReadAreNotBarePods(t *testing.T) {
	nodes := []model.Node{mkNode("a", "apps", "1"), mkNode("b", "apps", "1")}
	pods := []model.Pod{
		mkPod("ns", "web-1", "a", map[string]string{"app": "web"}, owner("Rollout", "web")),
		mkPod("ns", "bare", "a", map[string]string{"app": "bare"}),
		mkPod("ns", "orphan-1", "a", map[string]string{"app": "orphan"}, owner("ReplicaSet", "orphan-abc")),
		mkPod("ns", "adopt-1", "a", map[string]string{"app": "adopt"}),
		mkPod("ns", "nightly-x", "a", map[string]string{"job": "nightly"}, owner("Job", "nightly")),
	}
	snap := mkSnap(nodes, pods)
	snap.Deployments = []model.Workload{mkDeploy("ns", "adopt", 1, map[string]string{"app": "adopt"})}
	rep := run(t, snap, "node=a")
	dump(t, rep)

	if f := find(rep, result.Outage, "Rollout", "ns/web"); f == nil {
		t.Error("a pod owned by a Rollout is not grouped under Rollout ns/web")
	} else {
		if strings.Contains(f.Reason, "no controller") || strings.Contains(f.Context, "bare pod") {
			t.Errorf("a Rollout owned pod is described as a bare pod: %s | %s", f.Reason, f.Context)
		}
		if !evidenceHas(f, "Rollout web") {
			t.Errorf("evidence does not name the owner read from metadata.ownerReferences: %v", f.Evidence)
		}
	}
	if f := find(rep, result.Outage, "Pod", "ns/bare"); f == nil {
		t.Error("a pod with no ownerReferences is not reported as a bare pod")
	} else if !strings.Contains(f.Reason, "metadata.ownerReferences") {
		t.Errorf("the bare pod reason does not name the field it comes from: %s", f.Reason)
	}
	if f := find(rep, result.Outage, "ReplicaSet", "ns/orphan-abc"); f == nil {
		t.Error("a pod whose ReplicaSet is missing and matches no selector is not grouped under that ReplicaSet")
	} else if !evidenceHas(f, "not in the snapshot") {
		t.Errorf("evidence does not say the ReplicaSet is missing: %v", f.Evidence)
	}
	if f := find(rep, result.Outage, "Deployment", "ns/adopt"); f == nil {
		t.Error("a pod with no owner whose labels match a Deployment selector is not attributed to it")
	} else if evidenceHas(f, "owning ReplicaSet was not in the snapshot") || !evidenceHas(f, "no metadata.ownerReferences") {
		t.Errorf("the selector match line claims a ReplicaSet the pod never named: %v", f.Evidence)
	}
	if find(rep, result.Outage, "Job", "ns/nightly") == nil {
		t.Error("a Job pod is not grouped under its Job")
	}
	if n := countKind(rep, result.Outage, "Pod"); n != 1 {
		t.Errorf("%d bare Pod findings, want exactly 1", n)
	}
}

func TestSameSelectorInAnotherNamespaceIsNotMatched(t *testing.T) {
	nodes := []model.Node{mkNode("a", "apps", "1"), mkNode("b", "apps", "1")}
	pods := []model.Pod{
		mkPod("one", "web-1", "a", map[string]string{"app": "web"}, owner("ReplicaSet", "web-1a")),
		mkPod("two", "web-1", "b", map[string]string{"app": "web"}, owner("ReplicaSet", "web-1b")),
	}
	snap := mkSnap(nodes, pods)
	snap.Deployments = []model.Workload{
		mkDeploy("one", "web", 1, map[string]string{"app": "web"}),
		mkDeploy("two", "web", 1, map[string]string{"app": "web"}),
	}
	rep := run(t, snap, "node=a")
	if find(rep, result.Outage, "Deployment", "one/web") == nil {
		t.Error("no OUTAGE for one/web")
	}
	for _, f := range rep.Findings {
		if f.Object == "two/web" {
			t.Errorf("two/web runs on the surviving node and was reported: %+v", f)
		}
	}
}

func TestSucceededAndFailedPodsAreNotLive(t *testing.T) {
	nodes := []model.Node{mkNode("a", "apps", "1"), mkNode("b", "apps", "1")}
	pods := []model.Pod{
		mkPod("ns", "web-1", "b", map[string]string{"app": "web"}, owner("ReplicaSet", "web-x")),
		withPhase(mkPod("ns", "web-2", "a", map[string]string{"app": "web"}, owner("ReplicaSet", "web-x")), "Succeeded"),
		withPhase(mkPod("ns", "web-3", "a", map[string]string{"app": "web"}, owner("ReplicaSet", "web-x")), "Failed"),
	}
	snap := mkSnap(nodes, pods)
	snap.Deployments = []model.Workload{mkDeploy("ns", "web", 1, map[string]string{"app": "web"})}
	rep := run(t, snap, "node=a")
	for _, f := range rep.Findings {
		if f.Object == "ns/web" {
			t.Errorf("finished pods on the node counted as a loss: %+v", f)
		}
	}
	if f := find(rep, result.Info, "Simulation", "node=a"); f == nil || !evidenceHas(f, "spec.nodeName: 0 live pods") {
		t.Errorf("the simulation summary counts finished pods as live: %+v", f)
	}
}

func TestUnscheduledPodIsNotASurvivor(t *testing.T) {
	nodes := []model.Node{mkNode("a", "apps", "1"), mkNode("b", "apps", "1")}
	pods := []model.Pod{
		mkPod("ns", "web-1", "a", map[string]string{"app": "web"}, owner("ReplicaSet", "web-x")),
		withPhase(mkPod("ns", "web-2", "", map[string]string{"app": "web"}, owner("ReplicaSet", "web-x")), "Pending"),
	}
	snap := mkSnap(nodes, pods)
	snap.Deployments = []model.Workload{mkDeploy("ns", "web", 2, map[string]string{"app": "web"})}
	rep := run(t, snap, "node=a")
	dump(t, rep)
	f := find(rep, result.Outage, "Deployment", "ns/web")
	if f == nil {
		t.Fatal("one running pod on the node plus one pod with no node is a RISK, want OUTAGE: nothing keeps running")
	}
	if !evidenceHas(f, "no spec.nodeName") {
		t.Errorf("evidence does not say a pod has no node: %v", f.Evidence)
	}
}

func TestPDBWithoutMatchLabelsIsOnlyReportedWhereItCanMatter(t *testing.T) {
	nodes := []model.Node{mkNode("a", "apps", "1"), mkNode("b", "apps", "1")}
	pods := []model.Pod{mkPod("apps", "web-1", "a", map[string]string{"app": "web"}, owner("ReplicaSet", "web-x"))}
	snap := mkSnap(nodes, pods)
	snap.PDBs = []model.PodDisruptionBudget{
		mkPDB("elsewhere", "expr", nil, 0),
		mkPDB("apps", "expr", nil, 0),
	}
	rep := run(t, snap, "node=a")
	if find(rep, result.NotAssessed, "PodDisruptionBudget", "elsewhere/expr") != nil {
		t.Error("a budget in a namespace with no pod on the nodes that go away was reported as NOT ASSESSED")
	}
	if f := find(rep, result.NotAssessed, "PodDisruptionBudget", "apps/expr"); f == nil {
		t.Error("a selector spanline cannot read, next to pods that go away, must be NOT ASSESSED")
	} else if !evidenceHas(f, "spec.nodeName") {
		t.Errorf("evidence does not tie the budget to the pods that go away: %v", f.Evidence)
	}

	// Nothing doomed at all: the same budgets cannot block anything, so a plan that moves no node stays clean.
	plan, err := tfplan.ParsePlan([]byte(`{"format_version":"1.2","resource_changes":[
		{"address":"azurerm_kubernetes_cluster_node_pool.apps","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"apps",
		 "change":{"actions":["update"],"before":{"name":"apps","tags":{"a":"1"}},"after":{"name":"apps","tags":{"a":"2"}}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	prep, err := Plan(snap, plan, nil, "plan.json", Options{Now: collected})
	if err != nil {
		t.Fatal(err)
	}
	dump(t, prep)
	if prep.ExitCode != 0 {
		t.Errorf("a tag only change exits %d, want 0", prep.ExitCode)
	}
}

func TestPDBFormsAndNamespaceBoundary(t *testing.T) {
	nodes := []model.Node{mkNode("a", "apps", "1"), mkNode("b", "apps", "1")}
	pods := []model.Pod{
		mkPod("apps", "web-1", "a", map[string]string{"app": "web"}, owner("ReplicaSet", "web-x")),
		mkPod("apps", "web-2", "b", map[string]string{"app": "web"}, owner("ReplicaSet", "web-x")),
		mkPod("apps", "api-1", "a", map[string]string{"app": "api"}, owner("ReplicaSet", "api-x")),
		mkPod("apps", "api-2", "b", map[string]string{"app": "api"}, owner("ReplicaSet", "api-x")),
	}
	snap := mkSnap(nodes, pods)
	maxU := mkPDB("apps", "web-max", map[string]string{"app": "web"}, 0)
	maxU.Spec.MaxUnavailable = "0"
	pct := mkPDB("apps", "api-pct", map[string]string{"app": "api"}, 1)
	pct.Spec.MinAvailable = "50%"
	other := mkPDB("other", "web-other", map[string]string{"app": "web"}, 0)
	twice := mkPDB("apps", "web-twice", map[string]string{"app": "web"}, 1)
	snap.PDBs = []model.PodDisruptionBudget{maxU, pct, other, twice}

	rep := run(t, snap, "node=a")
	dump(t, rep)
	if f := find(rep, result.Disruption, "PodDisruptionBudget", "apps/web-max"); f == nil {
		t.Error("a maxUnavailable budget with 0 disruptions allowed does not block the drain")
	} else if !evidenceHas(f, "spec.maxUnavailable: 0") {
		t.Errorf("evidence does not name spec.maxUnavailable: %v", f.Evidence)
	}
	if find(rep, result.Disruption, "PodDisruptionBudget", "apps/api-pct") != nil {
		t.Error("a budget that allows one disruption was reported as blocking")
	}
	if find(rep, result.Disruption, "PodDisruptionBudget", "other/web-other") != nil {
		t.Error("a budget in another namespace matched pods by labels across the namespace boundary")
	}
	if f := find(rep, result.Disruption, "Pod", "apps/web-1"); f == nil {
		t.Error("a pod covered by two budgets is not reported")
	} else if !evidenceHas(f, "apps/web-max, apps/web-twice") {
		t.Errorf("the two budgets are not both named: %v", f.Evidence)
	}
}

func TestStrandedVolumeForms(t *testing.T) {
	doomed := mkNode("a", "apps", "1")
	surv := mkNode("b", "apps", "1")
	surv.Metadata.Labels["topology.disk.csi.azure.com/zone"] = "westeurope-1"
	nodes := []model.Node{doomed, surv}
	pods := []model.Pod{
		withClaim(mkPod("ns", "nopv", "a", map[string]string{"app": "nopv"}, owner("StatefulSet", "nopv")), "nopv-claim"),
		withClaim(mkPod("ns", "lostpv", "a", map[string]string{"app": "lostpv"}, owner("StatefulSet", "lostpv")), "lostpv-claim"),
		withClaim(mkPod("ns", "twozones", "a", map[string]string{"app": "twozones"}, owner("StatefulSet", "twozones")), "twozones-claim"),
		withClaim(mkPod("ns", "local", "a", map[string]string{"app": "local"}, owner("StatefulSet", "local")), "local-claim"),
		withClaim(mkPod("ns", "csi", "a", map[string]string{"app": "csi"}, owner("StatefulSet", "csi")), "csi-claim"),
		withClaim(mkPod("ns", "csigone", "a", map[string]string{"app": "csigone"}, owner("StatefulSet", "csigone")), "csigone-claim"),
		withClaim(mkPod("ns", "gt", "a", map[string]string{"app": "gt"}, owner("StatefulSet", "gt")), "gt-claim"),
	}
	snap := mkSnap(nodes, pods)
	snap.PVCs = []model.PersistentVolumeClaim{
		mkPVC("ns", "nopv-claim", ""),
		mkPVC("ns", "lostpv-claim", "pv-missing"),
		mkPVC("ns", "twozones-claim", "pv-twozones"),
		mkPVC("ns", "local-claim", "pv-local"),
		mkPVC("ns", "csi-claim", "pv-csi"),
		mkPVC("ns", "csigone-claim", "pv-csigone"),
		mkPVC("ns", "gt-claim", "pv-gt"),
	}
	snap.PVs = []model.PersistentVolume{
		mkPV("pv-twozones", in("topology.kubernetes.io/zone", "2", "1")),
		mkPV("pv-local", in("kubernetes.io/hostname", "a")),
		mkPV("pv-csi", in("topology.disk.csi.azure.com/zone", "westeurope-1")),
		mkPV("pv-csigone", in("topology.disk.csi.azure.com/zone", "westeurope-3")),
		mkPV("pv-gt", model.NodeSelectorRequirement{Key: "rank", Operator: "Gt", Values: []string{"3"}}),
	}
	rep := run(t, snap, "node=a")
	dump(t, rep)

	if find(rep, result.NotAssessed, "PersistentVolumeClaim", "ns/nopv-claim") == nil {
		t.Error("a claim with no spec.volumeName is not NOT ASSESSED")
	}
	if find(rep, result.NotAssessed, "PersistentVolume", "pv-missing") == nil {
		t.Error("a volume missing from the snapshot is not NOT ASSESSED")
	}
	for _, f := range rep.Findings {
		if f.Object == "ns/twozones-claim" && f.Severity == result.Disruption {
			t.Errorf("a volume allowed in zones 1 or 2 with a survivor in zone 1 was reported stranded: %+v", f)
		}
		if f.Object == "ns/csi-claim" && f.Severity == result.Disruption {
			t.Errorf("a CSI zone pin with a surviving node carrying that label was reported stranded: %+v", f)
		}
	}
	if f := find(rep, result.Disruption, "PersistentVolumeClaim", "ns/local-claim"); f == nil {
		t.Error("a volume pinned by kubernetes.io/hostname to the node that goes away is not reported at all")
	} else if !strings.Contains(f.Reason, "node a") || !evidenceHas(f, "kubernetes.io/hostname In [a]") {
		t.Errorf("the local volume finding does not name the node and the field: %s %v", f.Reason, f.Evidence)
	}
	if f := find(rep, result.Disruption, "PersistentVolumeClaim", "ns/csigone-claim"); f == nil {
		t.Error("a CSI zone pin no surviving node satisfies is not reported: the key is not one of the two legacy zone keys")
	} else if !evidenceHas(f, "topology.disk.csi.azure.com/zone In [westeurope-3]") {
		t.Errorf("the CSI finding does not print the requirement as written: %v", f.Evidence)
	}
	if f := find(rep, result.NotAssessed, "PersistentVolumeClaim", "ns/gt-claim"); f == nil {
		t.Error("an affinity operator spanline does not evaluate is silently passed")
	} else if !evidenceHas(f, "Gt") {
		t.Errorf("the NOT ASSESSED finding does not name the operator: %v", f.Evidence)
	}
}

func TestStrandedVolumeSurvivorsMustAcceptPods(t *testing.T) {
	mk := func(alter func(n *model.Node)) *model.Snapshot {
		doomed := mkNode("a", "apps", "1")
		surv := mkNode("b", "apps", "1")
		alter(&surv)
		snap := mkSnap([]model.Node{doomed, surv}, []model.Pod{
			withClaim(mkPod("ns", "db-0", "a", map[string]string{"app": "db"}, owner("StatefulSet", "db")), "db-claim"),
		})
		snap.PVCs = []model.PersistentVolumeClaim{mkPVC("ns", "db-claim", "pv-db")}
		snap.PVs = []model.PersistentVolume{mkPV("pv-db", in("topology.kubernetes.io/zone", "1"))}
		return snap
	}
	cases := map[string]func(n *model.Node){
		"cordoned": func(n *model.Node) { n.Spec.Unschedulable = true },
		"NoSchedule": func(n *model.Node) {
			n.Spec.Taints = []model.Taint{{Key: "dedicated", Value: "gpu", Effect: "NoSchedule"}}
		},
		"NoExecute": func(n *model.Node) {
			n.Spec.Taints = []model.Taint{{Key: "node.kubernetes.io/unreachable", Effect: "NoExecute"}}
		},
	}
	for name, alter := range cases {
		rep := run(t, mk(alter), "node=a")
		if f := find(rep, result.Disruption, "PersistentVolumeClaim", "ns/db-claim"); f == nil {
			t.Errorf("%s: the only node in the zone takes no new pod, yet the volume is not reported stranded", name)
		} else if name == "NoExecute" && !evidenceHas(f, "NoExecute") {
			t.Errorf("%s: evidence does not say a NoExecute taint counts a node out: %v", name, f.Evidence)
		}
	}
	// A healthy survivor in the zone and the pool is a clean run for the volume.
	rep := run(t, mk(func(*model.Node) {}), "node=a")
	if f := find(rep, result.Disruption, "PersistentVolumeClaim", "ns/db-claim"); f != nil {
		t.Errorf("a schedulable survivor in the zone was not enough: %+v", f)
	}
}

func TestCapacityArithmetic(t *testing.T) {
	a := mkNode("a", "apps", "1")
	b := mkNode("b", "apps", "1")
	b.Status.Allocatable = map[string]string{"cpu": "2", "memory": "3Gi"}
	blind := mkNode("blind", "apps", "1")
	blind.Status.Allocatable = nil
	half := mkPod("ns", "half", "a", map[string]string{"app": "half"}, owner("StatefulSet", "half"))
	half.Spec.Containers = []model.Container{
		{Name: "c1", Resources: model.ResourceRequirements{Requests: map[string]string{"cpu": "0.5", "memory": "2Gi"}}},
		{Name: "c2", Resources: model.ResourceRequirements{Requests: map[string]string{"cpu": "1500m", "memory": "512Mi"}}},
	}
	bare := mkPod("ns", "noreq", "a", map[string]string{"app": "noreq"}, owner("StatefulSet", "noreq"))
	bare.Spec.Containers = []model.Container{{Name: "c"}}
	snap := mkSnap([]model.Node{a, b, blind}, []model.Pod{half, bare})

	rep := run(t, snap, "node=a")
	dump(t, rep)
	f := find(rep, result.Info, "Capacity", "hand-built")
	if f == nil {
		t.Fatal("no INFO capacity line: 2 cores and 2.5Gi fit on the 2 core, 3Gi survivor")
	}
	if !strings.Contains(f.Reason, "needs cpu 2 and memory 2.5Gi") {
		t.Errorf("the sum of 0.5 + 1500m and 2Gi + 512Mi is wrong: %s", f.Reason)
	}
	if !strings.Contains(f.Reason, "offer cpu 2 and memory 3.0Gi") {
		t.Errorf("free capacity ignores the node with no allocatable and reads the other: %s", f.Reason)
	}
	na := find(rep, result.NotAssessed, "Capacity", "hand-built")
	if na == nil {
		t.Fatal("a node with no status.allocatable and a container with no requests must be NOT ASSESSED")
	}
	if !evidenceHas(na, "blind") || !evidenceHas(na, "ns/noreq") {
		t.Errorf("the gap does not name the blind node and the pod without requests: %v", na.Evidence)
	}
}

func TestCapacitySumsDoNotWrap(t *testing.T) {
	a := mkNode("a", "apps", "1")
	b := mkNode("b", "apps", "1")
	huge := func(name string) model.Pod {
		p := mkPod("ns", name, "a", map[string]string{"app": name}, owner("StatefulSet", name))
		p.Spec.Containers[0].Resources.Requests = map[string]string{"cpu": "1", "memory": "9000000T"}
		return p
	}
	neg := mkPod("ns", "neg", "a", map[string]string{"app": "neg"}, owner("StatefulSet", "neg"))
	neg.Spec.Containers[0].Resources.Requests = map[string]string{"cpu": "-4", "memory": "-1Gi"}
	snap := mkSnap([]model.Node{a, b}, []model.Pod{huge("h1"), huge("h2"), neg})

	rep := run(t, snap, "node=a")
	dump(t, rep)
	if f := find(rep, result.Info, "Capacity", "hand-built"); f != nil {
		t.Errorf("two requests of 9000000T each are claimed to fit: %s", f.Reason)
	}
	if find(rep, result.Disruption, "Capacity", "hand-built") == nil {
		t.Error("the wrapped sum did not become a DISRUPTION")
	}
	if na := find(rep, result.NotAssessed, "Capacity", "hand-built"); na == nil || !evidenceHas(na, "ns/neg") {
		t.Errorf("a negative request was read as a number: %+v", na)
	}
}

func planOf(t *testing.T, changes string) *tfplan.Plan {
	t.Helper()
	p, err := tfplan.ParsePlan([]byte(`{"format_version":"1.2","resource_changes":[` + changes + `]}`))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	return p
}

func runPlan(t *testing.T, snap *model.Snapshot, p *tfplan.Plan, states []*tfplan.State) *result.ImpactReport {
	t.Helper()
	rep, err := Plan(snap, p, states, "plan.json", Options{Now: collected})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	dump(t, rep)
	for _, m := range rep.Mapping {
		t.Logf("mapping: %s", m)
	}
	return rep
}

func appsSnap() *model.Snapshot {
	nodes := []model.Node{mkNode("apps-0", "apps", "1"), mkNode("apps-1", "apps", "2"), mkNode("data-0", "data", "1")}
	pods := []model.Pod{
		mkPod("ns", "web-1", "apps-0", map[string]string{"app": "web"}, owner("ReplicaSet", "web-x")),
		mkPod("ns", "web-2", "apps-1", map[string]string{"app": "web"}, owner("ReplicaSet", "web-x")),
	}
	snap := mkSnap(nodes, pods)
	snap.Context = "aks-prod"
	snap.Deployments = []model.Workload{mkDeploy("ns", "web", 2, map[string]string{"app": "web"})}
	return snap
}

const poolHead = `"address":"azurerm_kubernetes_cluster_node_pool.apps","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"apps"`

func TestPlanCreateMovesNothing(t *testing.T) {
	rep := runPlan(t, appsSnap(), planOf(t, `{"address":"azurerm_kubernetes_cluster_node_pool.batch","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"batch",
		"change":{"actions":["create"],"before":null,"after":{"name":"batch","vm_size":"Standard_D4s_v5","node_count":2}}},
		{"address":"azurerm_kubernetes_cluster.new","mode":"managed","type":"azurerm_kubernetes_cluster","name":"new",
		"change":{"actions":["create"],"before":null,"after":{"name":"aks-new"}}}`), nil)
	if len(rep.Nodes) != 0 {
		t.Errorf("a create doomed nodes: %v", rep.Nodes)
	}
	if rep.ExitCode != 0 {
		t.Errorf("creating a pool and a cluster exits %d, want 0", rep.ExitCode)
	}
	if f := find(rep, result.Info, "NodePool", "azurerm_kubernetes_cluster_node_pool.batch"); f == nil || !strings.Contains(f.Reason, "creates") {
		t.Errorf("no INFO saying the pool is created: %+v", f)
	}
	if f := find(rep, result.Info, "Cluster", "azurerm_kubernetes_cluster.new"); f == nil || !strings.Contains(f.Reason, "creates") {
		t.Errorf("no INFO saying the cluster is created: %+v", f)
	}

	// A create whose name collides with live nodes still destroys nothing.
	rep = runPlan(t, appsSnap(), planOf(t, `{`+poolHead+`,
		"change":{"actions":["create"],"before":null,"after":{"name":"apps","vm_size":"Standard_D8s_v5"}}}`), nil)
	if len(rep.Nodes) != 0 {
		t.Errorf("a create of a pool name live nodes carry doomed them: %v", rep.Nodes)
	}
}

func TestPlanScaleDownIsNotAPass(t *testing.T) {
	rep := runPlan(t, appsSnap(), planOf(t, `{`+poolHead+`,
		"change":{"actions":["update"],"before":{"name":"apps","node_count":2},"after":{"name":"apps","node_count":1}}}`), nil)
	if f := find(rep, result.Info, "NodePool", "azurerm_kubernetes_cluster_node_pool.apps"); f != nil {
		t.Errorf("a scale down from 2 to 1 is reported as moving no node: %s", f.Reason)
	}
	f := find(rep, result.NotAssessed, "NodePool", "azurerm_kubernetes_cluster_node_pool.apps")
	if f == nil {
		t.Fatal("a scale down that does not name the nodes it removes must be NOT ASSESSED")
	}
	if !strings.Contains(f.Reason, "node_count") || !strings.Contains(f.Reason, "2 to 1") {
		t.Errorf("the reason does not name the attribute and the numbers: %s", f.Reason)
	}
	if len(rep.Nodes) != 0 {
		t.Errorf("nodes were guessed for a scale down: %v", rep.Nodes)
	}
	if rep.ExitCode != 1 {
		t.Errorf("exit %d, want 1", rep.ExitCode)
	}

	low := runPlan(t, appsSnap(), planOf(t, `{`+poolHead+`,
		"change":{"actions":["update"],"before":{"name":"apps","max_count":10},"after":{"name":"apps","max_count":1}}}`), nil)
	if f := find(low, result.NotAssessed, "NodePool", "azurerm_kubernetes_cluster_node_pool.apps"); f == nil || !strings.Contains(f.Reason, "max_count") {
		t.Errorf("a ceiling under the 2 live nodes must be NOT ASSESSED: %+v", f)
	}
	high := runPlan(t, appsSnap(), planOf(t, `{`+poolHead+`,
		"change":{"actions":["update"],"before":{"name":"apps","max_count":10},"after":{"name":"apps","max_count":5}}}`), nil)
	if f := find(high, result.Info, "NodePool", "azurerm_kubernetes_cluster_node_pool.apps"); f == nil || !evidenceHas(f, "max_count") {
		t.Errorf("a ceiling above the live nodes moves nothing and must say so in evidence: %+v", f)
	}
	if high.ExitCode != 0 {
		t.Errorf("a harmless ceiling change exits %d, want 0", high.ExitCode)
	}
}

func TestPlanDataSourceReadMovesNothing(t *testing.T) {
	rep := runPlan(t, appsSnap(), planOf(t, `{"address":"data.azurerm_kubernetes_cluster_node_pool.apps","mode":"data","type":"azurerm_kubernetes_cluster_node_pool","name":"apps",
		"change":{"actions":["read"],"before":null,"after":{"name":"apps","vm_size":"Standard_D8s_v5"}}},
		{"address":"data.azurerm_client_config.current","mode":"data","type":"azurerm_client_config","name":"current",
		"change":{"actions":["read"],"before":null,"after":{}}},
		{"address":"azurerm_public_ip.pip","mode":"managed","type":"azurerm_public_ip","name":"pip",
		"change":{"actions":["create"],"before":null,"after":{"name":"pip"}}}`), nil)
	if len(rep.Nodes) != 0 {
		t.Errorf("a data source read doomed nodes: %v", rep.Nodes)
	}
	if countKind(rep, result.NotAssessed, "NodePool") != 0 || countKind(rep, result.Info, "NodePool") != 0 {
		t.Error("a data source produced a NodePool finding")
	}
	other := find(rep, result.Info, "Plan", "changes outside the node pools")
	if other == nil {
		t.Fatal("no INFO for the public ip change")
	}
	if !strings.HasPrefix(other.Reason, "1 changes") || evidenceHas(other, "azurerm_client_config") {
		t.Errorf("data reads are counted as changes: %s %v", other.Reason, other.Evidence)
	}
	if rep.PlanSummary != "1 to add, 0 to destroy, 0 to change" {
		t.Errorf("planSummary = %q", rep.PlanSummary)
	}
}

func TestPlanMappingNamesModuleAndInstanceAddresses(t *testing.T) {
	st, err := tfplan.ParseState([]byte(`{"version":4,"resources":[
		{"module":"module.aks","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"pools","provider":"p",
		 "instances":[{"index_key":"apps","attributes":{"name":"apps"}},{"index_key":"data","attributes":{"name":"data"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	st.Path = "aks.tfstate"
	rep := runPlan(t, appsSnap(), planOf(t, `{"address":"module.aks.azurerm_kubernetes_cluster_node_pool.pools[\"apps\"]","module_address":"module.aks","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"pools","index":"apps",
		"change":{"actions":["delete","create"],"before":{"name":"apps","vm_size":"a"},"after":{"name":"apps","vm_size":"b"},"replace_paths":[["vm_size"]]}}`), []*tfplan.State{st})
	var replaced, untouched bool
	for _, m := range rep.Mapping {
		if strings.HasPrefix(m, `module.aks.azurerm_kubernetes_cluster_node_pool.pools["apps"] (replace)`) &&
			strings.Contains(m, `[state aks.tfstate: module.aks.azurerm_kubernetes_cluster_node_pool.pools["apps"]]`) {
			replaced = true
		}
		if strings.HasPrefix(m, `module.aks.azurerm_kubernetes_cluster_node_pool.pools["data"] (no change in this plan)`) {
			untouched = true
		}
	}
	if !replaced {
		t.Errorf("the mapping does not name the state instance address of the replaced pool: %v", rep.Mapping)
	}
	if !untouched {
		t.Errorf("the mapping does not name the untouched pool by its instance address: %v", rep.Mapping)
	}
	if names(rep) != "apps-0,apps-1" {
		t.Errorf("nodes = %q", names(rep))
	}
}

func TestPlanNamesTheClusterThePoolBelongsTo(t *testing.T) {
	const id = `"kubernetes_cluster_id":"/subscriptions/s/resourceGroups/rg/providers/Microsoft.ContainerService/managedClusters/aks-other"`
	mappingLine := func(rep *result.ImpactReport) string {
		for _, m := range rep.Mapping {
			if strings.HasPrefix(m, "azurerm_kubernetes_cluster_node_pool.apps") {
				return m
			}
		}
		return ""
	}

	// A replacement has no finding of its own, so the mapping line carries the cluster and the mismatch.
	rep := runPlan(t, appsSnap(), planOf(t, `{`+poolHead+`,
		"change":{"actions":["delete","create"],
		 "before":{"name":"apps","vm_size":"a",`+id+`},"after":{"name":"apps","vm_size":"b",`+id+`}}}`), nil)
	if line := mappingLine(rep); !strings.Contains(line, "cluster aks-other in the plan (the snapshot context is aks-prod)") {
		t.Errorf("the mapping line does not name the cluster the plan targets next to the snapshot context: %s", line)
	}

	// An in-place update has a finding, whose evidence names the field the cluster was read from.
	upd := runPlan(t, appsSnap(), planOf(t, `{`+poolHead+`,
		"change":{"actions":["update"],
		 "before":{"name":"apps","tags":{"a":"1"},`+id+`},"after":{"name":"apps","tags":{"a":"2"},`+id+`}}}`), nil)
	f := find(upd, result.Info, "NodePool", "azurerm_kubernetes_cluster_node_pool.apps")
	if f == nil {
		t.Fatal("no INFO finding for the tag change")
	}
	if !evidenceHas(f, "kubernetes_cluster_id") || !evidenceHas(f, "aks-other") || !evidenceHas(f, "aks-prod") {
		t.Errorf("evidence does not name the cluster read from kubernetes_cluster_id next to the snapshot context: %v", f.Evidence)
	}

	// The same cluster name as the context: no mismatch note anywhere.
	same := appsSnap()
	same.Context = "aks-other"
	rep = runPlan(t, same, planOf(t, `{`+poolHead+`,
		"change":{"actions":["update"],
		 "before":{"name":"apps","tags":{"a":"1"},`+id+`},"after":{"name":"apps","tags":{"a":"2"},`+id+`}}}`), nil)
	if line := mappingLine(rep); strings.Contains(line, "snapshot context") {
		t.Errorf("a matching cluster name was flagged as a mismatch: %s", line)
	}
}

func TestHandBuiltReportsAreDeterministic(t *testing.T) {
	snap := appsSnap()
	snap.PDBs = []model.PodDisruptionBudget{
		mkPDB("ns", "z", map[string]string{"app": "web"}, 0),
		mkPDB("ns", "a", map[string]string{"app": "web"}, 0),
		mkPDB("ns", "m", map[string]string{"app": "web"}, 0),
	}
	p := planOf(t, `{`+poolHead+`,
		"change":{"actions":["delete","create"],"before":{"name":"apps","vm_size":"a"},"after":{"name":"apps","vm_size":"b"}}},
		{"address":"azurerm_public_ip.a","mode":"managed","type":"azurerm_public_ip","name":"a","change":{"actions":["create"],"before":null,"after":{}}},
		{"address":"azurerm_resource_group.b","mode":"managed","type":"azurerm_resource_group","name":"b","change":{"actions":["update"],"before":{},"after":{"tags":{}}}},
		{"address":"azurerm_storage_account.c","mode":"managed","type":"azurerm_storage_account","name":"c","change":{"actions":["delete"],"before":{},"after":null}}`)
	var last string
	for i := 0; i < 5; i++ {
		rep, err := Plan(snap, p, nil, "plan.json", Options{Now: collected})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(rep)
		if last != "" && string(b) != last {
			t.Fatal("two runs of Plan on the same hand built snapshot produced different bytes")
		}
		last = string(b)
	}
}
