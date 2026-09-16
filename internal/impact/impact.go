// Package impact answers one question, before anything is applied: if these nodes go away,
// or if this Terraform plan runs, what breaks on the live cluster.
//
// Every function here is a pure function of a snapshot. Nothing is evicted, cordoned or drained,
// and the Kubernetes Eviction API is never called, because calling it would need a write verb.
// The simulation happens in memory only. Anything the snapshot cannot settle is reported as
// NOT ASSESSED rather than dropped, so a short report is never mistaken for a safe one.
package impact

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
)

// DefaultTTL is how long a snapshot is treated as still describing the live cluster.
const DefaultTTL = 2 * time.Hour

// Options carries the caller's clock and the snapshot validity window.
// Now is taken from the caller instead of time.Now so two runs on the same snapshot
// produce the same bytes.
type Options struct {
	Now time.Time
	TTL time.Duration
}

func (o Options) ttl() time.Duration {
	if o.TTL <= 0 {
		return DefaultTTL
	}
	return o.TTL
}

// now falls back to the snapshot's own collection time, which keeps a report reproducible
// even when the caller left Now at its zero value.
func (o Options) now(snap *model.Snapshot) time.Time {
	if o.Now.IsZero() {
		return snap.CollectedAt
	}
	return o.Now
}

// poolLabelKeys are the labels a managed Kubernetes provider puts on a node to name its pool.
// They are read in this order, and the first one present wins.
var poolLabelKeys = []string{
	"kubernetes.azure.com/agentpool",
	"agentpool",
	"cloud.google.com/gke-nodepool",
	"eks.amazonaws.com/nodegroup",
}

// zoneLabelKeys are the labels that carry a node's failure domain, current key first.
var zoneLabelKeys = []string{
	"topology.kubernetes.io/zone",
	"failure-domain.beta.kubernetes.io/zone",
}

// ignored says, in words, what this package deliberately does not model. It is printed with
// every report so nobody reads a clean verdict as a promise about these.
var ignored = []string{
	"autoscaler scale up",
	"surge nodes created by the replacement",
	"topology spread constraints",
	"pod priority and preemption",
	"DaemonSet overhead",
	"HPA reactions",
}

// readResources are the snapshot resources these checks depend on. A gap in one of them
// becomes a NOT ASSESSED finding rather than silence.
var readResources = map[string]bool{
	"nodes": true, "pods": true, "replicasets": true, "deployments": true,
	"statefulsets": true, "daemonsets": true, "pdbs": true, "pvcs": true, "pvs": true,
}

// listCap is how many names an evidence line prints before it says how many more there are.
const listCap = 6

// analysis is one simulation: a snapshot, the nodes assumed gone, and the findings so far.
type analysis struct {
	snap     *model.Snapshot
	nodes    map[string]model.Node
	doomed   map[string]bool
	order    []string
	note     string
	findings []result.Finding
}

func newAnalysis(snap *model.Snapshot) *analysis {
	return &analysis{snap: snap, nodes: snap.NodeByName(), doomed: map[string]bool{}}
}

// doom marks nodes as going away. Calling it twice with the same node is harmless.
func (a *analysis) doom(nodes []model.Node) {
	for _, n := range nodes {
		if a.doomed[n.Metadata.Name] {
			continue
		}
		a.doomed[n.Metadata.Name] = true
		a.order = append(a.order, n.Metadata.Name)
	}
	sort.Strings(a.order)
}

func (a *analysis) add(sev result.Severity, kind, object, reason string, evidence []string) {
	a.addCtx(sev, kind, object, reason, evidence, "")
}

func (a *analysis) addCtx(sev result.Severity, kind, object, reason string, evidence []string, ctx string) {
	full := ctx
	switch {
	case a.note != "" && ctx != "":
		full = a.note + "; " + ctx
	case a.note != "":
		full = a.note
	}
	a.findings = append(a.findings, result.Finding{
		Severity: sev, Kind: kind, Object: object, Reason: reason, Evidence: evidence, Context: full,
	})
}

// survivors lists the nodes that stay, sorted by name.
func (a *analysis) survivors() []model.Node {
	out := make([]model.Node, 0, len(a.snap.Nodes))
	for _, n := range a.snap.Nodes {
		if !a.doomed[n.Metadata.Name] {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Name < out[j].Metadata.Name })
	return out
}

// doomedPods lists the live pods standing on the nodes that go away, sorted by name.
func (a *analysis) doomedPods() []model.Pod {
	var out []model.Pod
	for _, p := range a.snap.Pods {
		if podLive(p) && a.doomed[p.Spec.NodeName] {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return podName(out[i]) < podName(out[j]) })
	return out
}

// run executes every check in a fixed order.
func (a *analysis) run() {
	doomed := a.doomedPods()
	a.checkWorkloads(a.groupWorkloads())
	a.checkPDBs(doomed)
	a.checkVolumes(doomed)
	a.checkCapacity(doomed)
	a.checkGaps()
}

// report freezes the findings into the shape every renderer consumes.
func (a *analysis) report(source string, o Options) *result.ImpactReport {
	sortFindings(a.findings)
	verdict, code := result.ExitCodeFor(a.findings)
	findings := a.findings
	if findings == nil {
		findings = []result.Finding{}
	}
	return &result.ImpactReport{
		Context:     a.snap.Context,
		Source:      source,
		SnapshotAt:  a.snap.CollectedAt,
		ExpiresAt:   a.snap.CollectedAt.Add(o.ttl()),
		Nodes:       append([]string(nil), a.order...),
		Findings:    findings,
		Ignored:     append([]string(nil), ignored...),
		Gaps:        gapLines(a.snap),
		Verdict:     verdict,
		ExitCode:    code,
		GeneratedAt: o.now(a.snap),
	}
}

// summarise records what the simulation covered, so a report with no damage still says what it did.
func (a *analysis) summarise(object string) {
	surv := a.survivors()
	open := 0
	for _, n := range surv {
		if ok, _ := schedulable(n); ok {
			open++
		}
	}
	a.add(result.Info, "Simulation", object,
		fmt.Sprintf("%d of %d nodes go away, %d stay, and %d of those still accept new pods",
			len(a.order), len(a.snap.Nodes), len(surv), open),
		[]string{
			"nodes that go away: " + joinCapped(append([]string(nil), a.order...), listCap),
			fmt.Sprintf("spec.nodeName: %d live pods run on them", len(a.doomedPods())),
			"spec.unschedulable and spec.taints with effect NoSchedule decide which survivors accept pods",
		})
}

// checkGaps turns anything the collector could not read into a NOT ASSESSED finding.
func (a *analysis) checkGaps() {
	var missing []string
	for _, g := range a.snap.Gaps {
		if readResources[g.Resource] {
			missing = append(missing, g.Resource+" ("+g.Reason+")")
		}
	}
	if len(missing) == 0 {
		return
	}
	sort.Strings(missing)
	a.add(result.NotAssessed, "Snapshot", a.snap.Context,
		"part of this cluster was not read, so the checks below ran on less than the whole picture and their silence proves nothing",
		[]string{"resources missing from the snapshot: " + strings.Join(missing, ", ")})
}

// workloadKey identifies the thing that owns a set of pods.
type workloadKey struct {
	Kind      string
	Namespace string
	Name      string
}

func (w workloadKey) object() string {
	if w.Namespace == "" {
		return w.Name
	}
	return w.Namespace + "/" + w.Name
}

// workloadGroup is one workload with its live pods and the ones that go away.
type workloadGroup struct {
	key      workloadKey
	replicas int
	live     []model.Pod
	doomed   []model.Pod
	how      string
}

// groupWorkloads maps every live pod to the workload that owns it: through
// metadata.ownerReferences when the chain is in the snapshot, by selector match otherwise.
func (a *analysis) groupWorkloads() []workloadGroup {
	rs := make(map[string]model.ReplicaSet, len(a.snap.ReplicaSets))
	for _, r := range a.snap.ReplicaSets {
		rs[r.Metadata.Namespace+"/"+r.Metadata.Name] = r
	}
	workloads := a.snap.Workloads()
	sort.Slice(workloads, func(i, j int) bool {
		if workloads[i].Kind != workloads[j].Kind {
			return workloads[i].Kind < workloads[j].Kind
		}
		if workloads[i].Metadata.Namespace != workloads[j].Metadata.Namespace {
			return workloads[i].Metadata.Namespace < workloads[j].Metadata.Namespace
		}
		return workloads[i].Metadata.Name < workloads[j].Metadata.Name
	})

	index := map[workloadKey]*workloadGroup{}
	var keys []workloadKey
	for _, p := range a.snap.Pods {
		if !podLive(p) {
			continue
		}
		key, how := ownerOf(p, rs, workloads)
		g := index[key]
		if g == nil {
			g = &workloadGroup{key: key, replicas: -1, how: how}
			index[key] = g
			keys = append(keys, key)
		}
		g.live = append(g.live, p)
		if a.doomed[p.Spec.NodeName] {
			g.doomed = append(g.doomed, p)
		}
	}
	for _, w := range workloads {
		k := workloadKey{Kind: w.Kind, Namespace: w.Metadata.Namespace, Name: w.Metadata.Name}
		if g, ok := index[k]; ok && w.Spec.Replicas != nil {
			g.replicas = *w.Spec.Replicas
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].object() != keys[j].object() {
			return keys[i].object() < keys[j].object()
		}
		return keys[i].Kind < keys[j].Kind
	})
	out := make([]workloadGroup, 0, len(keys))
	for _, k := range keys {
		out = append(out, *index[k])
	}
	return out
}

func ownerOf(p model.Pod, rs map[string]model.ReplicaSet, workloads []model.Workload) (workloadKey, string) {
	ns := p.Metadata.Namespace
	for _, o := range p.Metadata.OwnerReferences {
		switch o.Kind {
		case "ReplicaSet":
			r, ok := rs[ns+"/"+o.Name]
			if !ok {
				continue
			}
			for _, ro := range r.Metadata.OwnerReferences {
				if ro.Kind == "Deployment" {
					return workloadKey{Kind: "Deployment", Namespace: ns, Name: ro.Name},
						"metadata.ownerReferences: pod to ReplicaSet " + o.Name + " to Deployment " + ro.Name
				}
			}
			return workloadKey{Kind: "ReplicaSet", Namespace: ns, Name: o.Name},
				"metadata.ownerReferences: pod to ReplicaSet " + o.Name + ", which has no Deployment owner"
		case "StatefulSet", "DaemonSet", "Job":
			return workloadKey{Kind: o.Kind, Namespace: ns, Name: o.Name},
				"metadata.ownerReferences: pod to " + o.Kind + " " + o.Name
		}
	}
	for _, w := range workloads {
		if w.Metadata.Namespace != ns {
			continue
		}
		if model.SelectorMatches(w.Spec.Selector, p.Metadata.Labels) {
			return workloadKey{Kind: w.Kind, Namespace: ns, Name: w.Metadata.Name},
				"spec.selector.matchLabels of " + w.Kind + " " + w.Metadata.Name +
					" matches metadata.labels of the pod, and the owning ReplicaSet was not in the snapshot"
		}
	}
	return workloadKey{Kind: "Pod", Namespace: ns, Name: p.Metadata.Name},
		"metadata.ownerReferences names no controller in the snapshot and no workload selector matches metadata.labels"
}

// checkWorkloads reports a workload that loses every replica, and one left thin enough to matter.
func (a *analysis) checkWorkloads(groups []workloadGroup) {
	for _, g := range groups {
		if len(g.doomed) == 0 {
			continue
		}
		surviving := len(g.live) - len(g.doomed)
		ev := []string{
			fmt.Sprintf("spec.nodeName: %d of %d live pods stand on nodes that go away", len(g.doomed), len(g.live)),
			"pods that go away: " + joinCapped(podNames(g.doomed), listCap),
			g.how,
			"status.phase: a live pod is one that is neither Succeeded nor Failed",
		}
		if g.replicas >= 0 {
			ev = append(ev, fmt.Sprintf("spec.replicas: %d", g.replicas))
		}
		ev = append(ev, "nodes that go away and carry those pods: "+joinCapped(nodeNamesOf(g.doomed), listCap))

		switch {
		case surviving == 0 && g.key.Kind == "Pod":
			a.addCtx(result.Outage, g.key.Kind, g.key.object(),
				"this pod has no controller in the snapshot, so once its node goes away nothing recreates it",
				ev, "a bare pod is not rescheduled by Kubernetes")
		case surviving == 0:
			a.add(result.Outage, g.key.Kind, g.key.object(),
				fmt.Sprintf("every live pod of this workload stands on a node that goes away, so it drops from %d to 0", len(g.live)),
				ev)
		case surviving == 1:
			a.addCtx(result.Risk, g.key.Kind, g.key.object(),
				fmt.Sprintf("this workload drops from %d live pods to 1, so one more failure takes it out entirely", len(g.live)),
				ev, "whether the lost pods come back depends on capacity and scheduling, which this report only judges in aggregate")
		case len(g.doomed)*2 > len(g.live):
			a.addCtx(result.Risk, g.key.Kind, g.key.object(),
				fmt.Sprintf("this workload loses more than half its live pods, %d of %d", len(g.doomed), len(g.live)),
				ev, "whether the lost pods come back depends on capacity and scheduling, which this report only judges in aggregate")
		}
	}
}

// checkPDBs reports the two documented ways a PodDisruptionBudget stops a drain from finishing:
// a budget that allows no disruption at all, and a pod covered by more than one budget.
func (a *analysis) checkPDBs(doomed []model.Pod) {
	pdbs := append([]model.PodDisruptionBudget(nil), a.snap.PDBs...)
	sort.Slice(pdbs, func(i, j int) bool {
		return pdbs[i].Metadata.Namespace+"/"+pdbs[i].Metadata.Name <
			pdbs[j].Metadata.Namespace+"/"+pdbs[j].Metadata.Name
	})

	covers := map[string][]string{}
	var covered []string
	for _, pdb := range pdbs {
		obj := pdb.Metadata.Namespace + "/" + pdb.Metadata.Name
		if pdb.Spec.Selector == nil || len(pdb.Spec.Selector.MatchLabels) == 0 {
			a.add(result.NotAssessed, "PodDisruptionBudget", obj,
				"this budget has no spec.selector.matchLabels, so spanline cannot tell which pods it covers and cannot say whether it would block a drain",
				[]string{
					"spec.selector.matchLabels: empty or expression based, which this check does not read",
					fmt.Sprintf("status.disruptionsAllowed: %d", pdb.Status.DisruptionsAllowed),
				})
			continue
		}
		var matched []model.Pod
		for _, p := range doomed {
			if p.Metadata.Namespace != pdb.Metadata.Namespace {
				continue
			}
			if model.SelectorMatches(pdb.Spec.Selector, p.Metadata.Labels) {
				matched = append(matched, p)
				name := podName(p)
				if len(covers[name]) == 0 {
					covered = append(covered, name)
				}
				covers[name] = append(covers[name], obj)
			}
		}
		if len(matched) == 0 {
			continue
		}
		if pdb.Status.DisruptionsAllowed != 0 {
			continue
		}
		a.addCtx(result.Disruption, "PodDisruptionBudget", obj,
			"this budget allows 0 disruptions while it covers pods on nodes that go away, so every eviction of those pods is refused and the drain does not finish",
			[]string{
				fmt.Sprintf("status.disruptionsAllowed: %d", pdb.Status.DisruptionsAllowed),
				fmt.Sprintf("status.currentHealthy: %d, status.desiredHealthy: %d, status.expectedPods: %d",
					pdb.Status.CurrentHealthy, pdb.Status.DesiredHealthy, pdb.Status.ExpectedPods),
				"spec.selector.matchLabels: " + labelString(pdb.Spec.Selector.MatchLabels),
				budgetLine(pdb),
				fmt.Sprintf("pods it covers that stand on nodes going away: %s", joinCapped(podNames(matched), listCap)),
			},
			"spanline never calls the Eviction API, which is a write verb, so this is read from status, not from a trial eviction")
	}

	sort.Strings(covered)
	for _, name := range covered {
		owners := covers[name]
		if len(owners) < 2 {
			continue
		}
		sort.Strings(owners)
		a.add(result.Disruption, "Pod", name,
			fmt.Sprintf("this pod is covered by %d PodDisruptionBudgets at once, which is a documented reason for an eviction to be refused whatever each budget allows", len(owners)),
			[]string{
				"budgets whose spec.selector.matchLabels match metadata.labels of this pod: " + strings.Join(owners, ", "),
				"spec.nodeName: " + nodeNameOfPod(a, name),
			})
	}
}

// checkVolumes reports a pod whose bound volume is pinned to a zone it cannot reach after the change.
func (a *analysis) checkVolumes(doomed []model.Pod) {
	pvcs := make(map[string]model.PersistentVolumeClaim, len(a.snap.PVCs))
	for _, c := range a.snap.PVCs {
		pvcs[c.Metadata.Namespace+"/"+c.Metadata.Name] = c
	}
	pvs := make(map[string]model.PersistentVolume, len(a.snap.PVs))
	for _, v := range a.snap.PVs {
		pvs[v.Metadata.Name] = v
	}
	survivors := a.survivors()

	for _, p := range doomed {
		podPoolKey, podPool, hasPool := "", "", false
		if n, ok := a.nodes[p.Spec.NodeName]; ok {
			podPoolKey, podPool, hasPool = nodePool(n)
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim == nil {
				continue
			}
			claim := p.Metadata.Namespace + "/" + v.PersistentVolumeClaim.ClaimName
			pvc, ok := pvcs[claim]
			if !ok {
				a.add(result.NotAssessed, "PersistentVolumeClaim", claim,
					"a pod on a node that goes away mounts this claim, but the claim is not in the snapshot, so spanline cannot tell whether its volume is pinned to a zone",
					[]string{
						"spec.volumes[].persistentVolumeClaim.claimName: " + v.PersistentVolumeClaim.ClaimName,
						"mounted by pod " + podName(p) + " on spec.nodeName " + p.Spec.NodeName,
					})
				continue
			}
			if pvc.Spec.VolumeName == "" {
				a.add(result.NotAssessed, "PersistentVolumeClaim", claim,
					"this claim has no spec.volumeName, so spanline cannot follow it to a PersistentVolume and cannot say whether it is pinned to a zone",
					[]string{
						"spec.volumeName: empty",
						"status.phase: " + pvc.Status.Phase,
						"mounted by pod " + podName(p) + " on spec.nodeName " + p.Spec.NodeName,
					})
				continue
			}
			pv, ok := pvs[pvc.Spec.VolumeName]
			if !ok {
				a.add(result.NotAssessed, "PersistentVolume", pvc.Spec.VolumeName,
					"a claim mounted by a pod on a node that goes away is bound to this volume, which is not in the snapshot, so its zone pinning is unknown",
					[]string{
						"PersistentVolumeClaim " + claim + " spec.volumeName: " + pvc.Spec.VolumeName,
						"mounted by pod " + podName(p) + " on spec.nodeName " + p.Spec.NodeName,
					})
				continue
			}
			zoneKey, zones := pinnedZones(pv)
			if len(zones) == 0 {
				continue
			}

			var inZone, samePool []string
			for _, n := range survivors {
				if ok, _ := schedulable(n); !ok {
					continue
				}
				_, z, has := nodeZone(n)
				if !has || !contains(zones, z) {
					continue
				}
				inZone = append(inZone, describeNode(n))
				if hasPool {
					if _, pool, _ := nodePool(n); pool == podPool {
						samePool = append(samePool, n.Metadata.Name)
					}
				}
			}

			ev := []string{
				"spec.volumes[].persistentVolumeClaim.claimName: " + v.PersistentVolumeClaim.ClaimName,
				"PersistentVolumeClaim " + claim + " spec.volumeName: " + pvc.Spec.VolumeName + ", status.phase: " + pvc.Status.Phase,
				"PersistentVolume " + pv.Metadata.Name + " spec.nodeAffinity.required.nodeSelectorTerms[].matchExpressions: " +
					zoneKey + " In [" + strings.Join(zones, ", ") + "]",
				"pod " + podName(p) + " spec.nodeName: " + p.Spec.NodeName + ", which goes away",
				"surviving schedulable nodes in that zone: " + emptyOr(joinCapped(inZone, listCap), "none"),
				"a node is counted out when spec.unschedulable is true or it carries a taint with effect NoSchedule",
			}
			if hasPool {
				ev = append(ev, "pool of the pod's node, from "+podPoolKey+": "+podPool)
			}

			switch {
			case len(inZone) == 0:
				a.addCtx(result.Disruption, "PersistentVolumeClaim", claim,
					"this volume is pinned to zone "+strings.Join(zones, " or ")+" and no surviving schedulable node is in that zone, so the pod cannot be rescheduled anywhere it can reach its data",
					ev, "a zone pinned volume never follows a pod to another zone")
			case hasPool && len(samePool) == 0:
				a.addCtx(result.Disruption, "PersistentVolumeClaim", claim,
					"this volume is pinned to zone "+strings.Join(zones, " or ")+" and no surviving schedulable node of pool "+podPool+
						" is left in that zone, so the pod can only come back on a node outside its own pool",
					ev, "replacement and surge nodes the change creates are not modelled, so this describes the window, not the end state")
			}
		}
	}
}

// checkCapacity compares the load that has to move with the room left to take it.
func (a *analysis) checkCapacity(doomed []model.Pod) {
	if len(a.order) == 0 {
		return
	}
	var wantCPU, wantMem int64
	var partial []string
	for _, p := range doomed {
		cpu, mem, complete := podRequests(p)
		wantCPU += cpu
		wantMem += mem
		if !complete {
			partial = append(partial, podName(p))
		}
	}

	used := map[string][2]int64{}
	for _, p := range a.snap.Pods {
		if !podLive(p) || p.Spec.NodeName == "" {
			continue
		}
		cpu, mem, _ := podRequests(p)
		cur := used[p.Spec.NodeName]
		used[p.Spec.NodeName] = [2]int64{cur[0] + cpu, cur[1] + mem}
	}

	var freeCPU, freeMem int64
	var counted, excluded, unreadable []string
	for _, n := range a.survivors() {
		ok, why := schedulable(n)
		if !ok {
			excluded = append(excluded, n.Metadata.Name+" ("+why+")")
			continue
		}
		alloc := n.Status.Allocatable
		cpu, okCPU := model.ParseCPU(alloc["cpu"])
		mem, okMem := model.ParseMemory(alloc["memory"])
		if !okCPU || !okMem {
			unreadable = append(unreadable, n.Metadata.Name)
			continue
		}
		u := used[n.Metadata.Name]
		freeCPU += max0(cpu - u[0])
		freeMem += max0(mem - u[1])
		counted = append(counted, n.Metadata.Name)
	}

	ev := []string{
		fmt.Sprintf("sum of spec.containers[].resources.requests over the %d live pods that go away: cpu %s, memory %s",
			len(doomed), model.HumanCPU(wantCPU), model.HumanMemory(wantMem)),
		fmt.Sprintf("sum of status.allocatable minus the requests already on them, over the %d surviving schedulable nodes: cpu %s, memory %s",
			len(counted), model.HumanCPU(freeCPU), model.HumanMemory(freeMem)),
	}
	if len(counted) > 0 {
		ev = append(ev, "surviving schedulable nodes counted: "+joinCapped(counted, listCap))
	}
	if len(excluded) > 0 {
		ev = append(ev, "surviving nodes left out because they take no new pod: "+joinCapped(excluded, listCap))
	}

	if wantCPU > freeCPU || wantMem > freeMem {
		a.addCtx(result.Disruption, "Capacity", a.snap.Context,
			fmt.Sprintf("the load that has to move does not fit on what stays: it needs cpu %s and memory %s, and the surviving schedulable nodes offer cpu %s and memory %s",
				model.HumanCPU(wantCPU), model.HumanMemory(wantMem), model.HumanCPU(freeCPU), model.HumanMemory(freeMem)),
			ev, "this is a sum, not a scheduling simulation: a load that fits in total can still fail to place")
	} else {
		a.addCtx(result.Info, "Capacity", a.snap.Context,
			fmt.Sprintf("the load that has to move fits on what stays: it needs cpu %s and memory %s, and the surviving schedulable nodes offer cpu %s and memory %s",
				model.HumanCPU(wantCPU), model.HumanMemory(wantMem), model.HumanCPU(freeCPU), model.HumanMemory(freeMem)),
			ev, "this is a sum, not a scheduling simulation: a load that fits in total can still fail to place")
	}

	if len(partial) > 0 || len(unreadable) > 0 {
		gap := []string{}
		if len(partial) > 0 {
			gap = append(gap, "pods with no readable spec.containers[].resources.requests: "+joinCapped(partial, listCap))
		}
		if len(unreadable) > 0 {
			gap = append(gap, "nodes with no readable status.allocatable: "+joinCapped(unreadable, listCap))
		}
		a.add(result.NotAssessed, "Capacity", a.snap.Context,
			"part of the capacity arithmetic had no numbers to work from, so the fit above covers less than the whole load",
			gap)
	}
}

func sortFindings(f []result.Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].Severity.Rank() != f[j].Severity.Rank() {
			return f[i].Severity.Rank() > f[j].Severity.Rank()
		}
		if f[i].Object != f[j].Object {
			return f[i].Object < f[j].Object
		}
		if f[i].Kind != f[j].Kind {
			return f[i].Kind < f[j].Kind
		}
		return f[i].Reason < f[j].Reason
	})
}

func gapLines(snap *model.Snapshot) []string {
	if len(snap.Gaps) == 0 {
		return nil
	}
	out := make([]string, 0, len(snap.Gaps))
	for _, g := range snap.Gaps {
		out = append(out, g.Resource+": "+g.Reason)
	}
	sort.Strings(out)
	return out
}

// nodePool returns the label key and value that name a node's pool.
func nodePool(n model.Node) (string, string, bool) {
	for _, k := range poolLabelKeys {
		if v, ok := n.Metadata.Labels[k]; ok && v != "" {
			return k, v, true
		}
	}
	return "", "", false
}

// nodeZone returns the label key and value that name a node's failure domain.
func nodeZone(n model.Node) (string, string, bool) {
	for _, k := range zoneLabelKeys {
		if v, ok := n.Metadata.Labels[k]; ok && v != "" {
			return k, v, true
		}
	}
	return "", "", false
}

// schedulable says whether a node still takes new pods, and names the field that says otherwise.
func schedulable(n model.Node) (bool, string) {
	if n.Spec.Unschedulable {
		return false, "spec.unschedulable: true"
	}
	for _, t := range n.Spec.Taints {
		if t.Effect == "NoSchedule" {
			if t.Value == "" {
				return false, "spec.taints: " + t.Key + ":NoSchedule"
			}
			return false, "spec.taints: " + t.Key + "=" + t.Value + ":NoSchedule"
		}
	}
	return true, ""
}

func describeNode(n model.Node) string {
	if k, v, ok := nodePool(n); ok {
		return n.Metadata.Name + " (" + k + "=" + v + ")"
	}
	return n.Metadata.Name
}

// pinnedZones reads the zone a PersistentVolume's node affinity pins it to.
func pinnedZones(pv model.PersistentVolume) (string, []string) {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return "", nil
	}
	key := ""
	seen := map[string]bool{}
	var zones []string
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, req := range term.MatchExpressions {
			if !isZoneKey(req.Key) || req.Operator != "In" {
				continue
			}
			if key == "" {
				key = req.Key
			}
			for _, v := range req.Values {
				if !seen[v] {
					seen[v] = true
					zones = append(zones, v)
				}
			}
		}
	}
	sort.Strings(zones)
	return key, zones
}

func isZoneKey(k string) bool {
	for _, z := range zoneLabelKeys {
		if z == k {
			return true
		}
	}
	return false
}

func podLive(p model.Pod) bool {
	switch p.Status.Phase {
	case "Succeeded", "Failed":
		return false
	}
	return true
}

func podName(p model.Pod) string { return p.Metadata.Namespace + "/" + p.Metadata.Name }

func podNames(pods []model.Pod) []string {
	out := make([]string, 0, len(pods))
	for _, p := range pods {
		out = append(out, podName(p))
	}
	return out
}

func nodeNamesOf(pods []model.Pod) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range pods {
		if p.Spec.NodeName != "" && !seen[p.Spec.NodeName] {
			seen[p.Spec.NodeName] = true
			out = append(out, p.Spec.NodeName)
		}
	}
	return out
}

func nodeNameOfPod(a *analysis, name string) string {
	for _, p := range a.snap.Pods {
		if podName(p) == name {
			return p.Spec.NodeName
		}
	}
	return "unknown"
}

// podRequests sums the container requests of a pod, and says whether every number was readable.
func podRequests(p model.Pod) (int64, int64, bool) {
	var cpu, mem int64
	complete := len(p.Spec.Containers) > 0
	for _, c := range p.Spec.Containers {
		if v, ok := model.ParseCPU(c.Resources.Requests["cpu"]); ok {
			cpu += v
		} else {
			complete = false
		}
		if v, ok := model.ParseMemory(c.Resources.Requests["memory"]); ok {
			mem += v
		} else {
			complete = false
		}
	}
	return cpu, mem, complete
}

func budgetLine(pdb model.PodDisruptionBudget) string {
	switch {
	case pdb.Spec.MinAvailable != "":
		return "spec.minAvailable: " + pdb.Spec.MinAvailable
	case pdb.Spec.MaxUnavailable != "":
		return "spec.maxUnavailable: " + pdb.Spec.MaxUnavailable
	default:
		return "spec.minAvailable and spec.maxUnavailable: neither is set"
	}
}

func labelString(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

func joinCapped(names []string, cap int) string {
	sort.Strings(names)
	if len(names) <= cap {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:cap], ", ") + fmt.Sprintf(", and %d more", len(names)-cap)
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func emptyOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func max0(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
