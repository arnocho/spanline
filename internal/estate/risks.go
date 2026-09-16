package estate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
	"github.com/arnocho/spanline/internal/tfplan"
)

// collectRisks applies every estate rule in a fixed order. Each finding names the exact
// fields it was read from, so an operator can check a verdict without trusting spanline.
// It also returns the workloads a finding touches, which is what a pool's AtRisk counts.
func collectRisks(indexes []*index, pools map[poolKey]*pool, states []*tfplan.State) ([]result.Finding, map[workloadKey]bool) {
	out := make([]result.Finding, 0)
	atRisk := map[workloadKey]bool{}
	for _, ix := range indexes {
		out = append(out, workloadRisks(ix, atRisk)...)
		out = append(out, pdbRisks(ix, atRisk)...)
		out = append(out, volumeRisks(ix, atRisk)...)
		out = append(out, nodeRisks(ix)...)
	}
	out = append(out, poolRisks(pools, states)...)
	out = append(out, helmFindings(indexes, states)...)
	return out, atRisk
}

// desiredCount reads how many instances are wanted, and names the field it read.
// A DaemonSet counts nodes, not replicas, so it is read from its own status fields.
func desiredCount(w model.Workload) (int, string) {
	if w.Kind == "DaemonSet" {
		return w.Status.DesiredNumberScheduled, "status.desiredNumberScheduled"
	}
	if w.Spec.Replicas != nil {
		return *w.Spec.Replicas, "spec.replicas"
	}
	return w.Status.Replicas, "status.replicas"
}

// readyCount reads how many instances are ready, and names the field it read.
func readyCount(w model.Workload) (int, string) {
	if w.Kind == "DaemonSet" {
		return w.Status.NumberReady, "status.numberReady"
	}
	return w.Status.ReadyReplicas, "status.readyReplicas"
}

// workloadRisks reports a workload that serves nothing, then a workload one node loss ends.
// An outage supersedes the single replica risk on the same object: it is already down.
func workloadRisks(ix *index, atRisk map[workloadKey]bool) []result.Finding {
	var out []result.Finding
	for _, w := range ix.workloads {
		key := keyOf(ix.snap.Context, w)
		desired, desiredField := desiredCount(w)
		ready, readyField := readyCount(w)
		switch {
		case desired > 0 && ready == 0:
			atRisk[key] = true
			evidence := []string{
				fmt.Sprintf("%s=%d", desiredField, desired),
				fmt.Sprintf("%s=%d", readyField, ready),
			}
			if w.Kind != "DaemonSet" {
				evidence = append(evidence, fmt.Sprintf("status.availableReplicas=%d", w.Status.AvailableReplicas))
			}
			out = append(out, result.Finding{
				Severity: result.Outage,
				Kind:     w.Kind,
				Object:   key.object(),
				Context:  ix.snap.Context,
				Reason:   fmt.Sprintf("nothing is ready while %d is wanted, so this workload serves no traffic at all", desired),
				Evidence: evidence,
			})
		case desired == 1 && w.Kind != "DaemonSet":
			atRisk[key] = true
			out = append(out, result.Finding{
				Severity: result.Risk,
				Kind:     w.Kind,
				Object:   key.object(),
				Context:  ix.snap.Context,
				Reason:   "one replica only, so the loss of its node takes this workload down",
				Evidence: []string{
					fmt.Sprintf("%s=%d", desiredField, desired),
					fmt.Sprintf("%s=%d", readyField, ready),
				},
			})
		}
	}
	return out
}

// pdbRisks reports a budget that allows no disruption at all, which blocks every drain of
// a node running the pods it selects.
func pdbRisks(ix *index, atRisk map[workloadKey]bool) []result.Finding {
	var out []result.Finding
	pdbs := append([]model.PodDisruptionBudget(nil), ix.snap.PDBs...)
	sort.SliceStable(pdbs, func(i, j int) bool {
		if pdbs[i].Metadata.Namespace != pdbs[j].Metadata.Namespace {
			return pdbs[i].Metadata.Namespace < pdbs[j].Metadata.Namespace
		}
		return pdbs[i].Metadata.Name < pdbs[j].Metadata.Name
	})
	for _, pdb := range pdbs {
		if pdb.Status.DisruptionsAllowed != 0 {
			continue
		}
		evidence := []string{
			"status.disruptionsAllowed=0",
			fmt.Sprintf("status.currentHealthy=%d", pdb.Status.CurrentHealthy),
			fmt.Sprintf("status.desiredHealthy=%d", pdb.Status.DesiredHealthy),
			fmt.Sprintf("status.expectedPods=%d", pdb.Status.ExpectedPods),
		}
		if pdb.Spec.MinAvailable != "" {
			evidence = append(evidence, "spec.minAvailable="+pdb.Spec.MinAvailable)
		}
		if pdb.Spec.MaxUnavailable != "" {
			evidence = append(evidence, "spec.maxUnavailable="+pdb.Spec.MaxUnavailable)
		}
		selected := ix.workloadsSelectedBy(pdb.Metadata.Namespace, pdb.Spec.Selector)
		for _, k := range selected {
			atRisk[k] = true
			evidence = append(evidence, "spec.selector covers "+k.kind+" "+k.object())
		}
		if len(selected) == 0 {
			evidence = append(evidence, "spec.selector matched no workload that was read")
		}
		out = append(out, result.Finding{
			Severity: result.Risk,
			Kind:     "PodDisruptionBudget",
			Object:   nsName(pdb.Metadata.Namespace, pdb.Metadata.Name),
			Context:  ix.snap.Context,
			Reason:   "this budget allows zero disruptions, so draining any node running these pods blocks",
			Evidence: evidence,
		})
	}
	return out
}

// volumeRisks reports a bound claim whose volume is pinned to a single zone, so the pod
// using it cannot be rescheduled anywhere else. A claim whose volume was not read is not
// a pass: it is reported as not assessed.
func volumeRisks(ix *index, atRisk map[workloadKey]bool) []result.Finding {
	var out []result.Finding
	pvcs := append([]model.PersistentVolumeClaim(nil), ix.snap.PVCs...)
	sort.SliceStable(pvcs, func(i, j int) bool {
		if pvcs[i].Metadata.Namespace != pvcs[j].Metadata.Namespace {
			return pvcs[i].Metadata.Namespace < pvcs[j].Metadata.Namespace
		}
		return pvcs[i].Metadata.Name < pvcs[j].Metadata.Name
	})
	for _, pvc := range pvcs {
		if pvc.Status.Phase != "Bound" || pvc.Spec.VolumeName == "" {
			continue
		}
		object := nsName(pvc.Metadata.Namespace, pvc.Metadata.Name)
		pv, ok := ix.pvByName[pvc.Spec.VolumeName]
		if !ok {
			out = append(out, result.Finding{
				Severity: result.NotAssessed,
				Kind:     "PersistentVolumeClaim",
				Object:   object,
				Context:  ix.snap.Context,
				Reason:   "the volume this claim is bound to was not read, so whether it pins the pod to one zone is unknown",
				Evidence: []string{
					"spec.volumeName=" + pvc.Spec.VolumeName,
					"status.phase=" + pvc.Status.Phase,
					"no persistentvolume of that name in this snapshot",
				},
			})
			continue
		}
		zones := pinnedZones(pv)
		if len(zones) != 1 {
			continue
		}
		evidence := []string{
			"spec.volumeName=" + pvc.Spec.VolumeName,
			"status.phase=" + pvc.Status.Phase,
			fmt.Sprintf("persistentvolume %s spec.nodeAffinity requires %s in [%s]", pv.Metadata.Name, zoneLabel, strings.Join(zones, " ")),
		}
		for _, pod := range ix.pvcPods[object] {
			if k, ok := ix.podWorkload[nsName(pod.Metadata.Namespace, pod.Metadata.Name)]; ok {
				atRisk[k] = true
				evidence = append(evidence, "mounted by pod "+nsName(pod.Metadata.Namespace, pod.Metadata.Name)+" of "+k.kind+" "+k.object())
			}
		}
		out = append(out, result.Finding{
			Severity: result.Risk,
			Kind:     "PersistentVolumeClaim",
			Object:   object,
			Context:  ix.snap.Context,
			Reason:   "its volume lives in one zone only, so the pod using it cannot move out of zone " + zones[0],
			Evidence: evidence,
		})
	}
	return out
}

// pinnedZones lists the zones a volume's node affinity allows, sorted.
func pinnedZones(pv model.PersistentVolume) []string {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return nil
	}
	set := map[string]bool{}
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Key != zoneLabel && expr.Key != legacyZoneLabel {
				continue
			}
			if expr.Operator != "" && expr.Operator != "In" {
				continue
			}
			for _, v := range expr.Values {
				set[v] = true
			}
		}
	}
	return sortedKeys(set)
}

// nodeReady reports the Ready condition, and the exact field it read. A node with no Ready
// condition at all is not ready: an absent condition never reads as a pass.
func nodeReady(n model.Node) (bool, string) {
	for _, c := range n.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True", "status.conditions[type=Ready].status=" + c.Status
		}
	}
	return false, "status.conditions carries no entry of type Ready"
}

func nodeRisks(ix *index) []result.Finding {
	var out []result.Finding
	nodes := append([]model.Node(nil), ix.snap.Nodes...)
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Metadata.Name < nodes[j].Metadata.Name })
	for _, n := range nodes {
		ready, evidence := nodeReady(n)
		if ready {
			continue
		}
		poolName, label := poolOf(n)
		lines := []string{evidence, label, "pool " + poolName}
		if n.Spec.Unschedulable {
			lines = append(lines, "spec.unschedulable=true")
		}
		lines = append(lines, fmt.Sprintf("%d pod(s) scheduled on it", len(ix.snap.PodsOnNode(n.Metadata.Name))))
		out = append(out, result.Finding{
			Severity: result.Risk,
			Kind:     "Node",
			Object:   n.Metadata.Name,
			Context:  ix.snap.Context,
			Reason:   "this node is not Ready, so its capacity is gone and the pods on it are not protected",
			Evidence: lines,
		})
	}
	return out
}

// poolRisks reports a pool with no headroom for a drain, and a pool no state claims.
// Ownership is only assessed when at least one state was read: with none, the report says
// so once in its gaps instead of flagging every pool.
func poolRisks(pools map[poolKey]*pool, states []*tfplan.State) []result.Finding {
	var out []result.Finding
	declared := 0
	for _, st := range states {
		declared += len(st.NodePools())
	}
	for _, k := range sortedPoolKeys(pools) {
		p := pools[k]
		var tight []string
		evidence := []string{fmt.Sprintf("%d node(s) in this pool", len(p.nodes))}
		if p.cpuAlloc > 0 {
			cpu := percent(p.cpuReq, p.cpuAlloc)
			evidence = append(evidence, fmt.Sprintf(
				"sum of spec.containers[].resources.requests.cpu %s against sum of status.allocatable.cpu %s (%.1f percent)",
				model.HumanCPU(p.cpuReq), model.HumanCPU(p.cpuAlloc), cpu))
			if cpu > pressurePercent {
				tight = append(tight, "cpu")
			}
		}
		if p.memAlloc > 0 {
			mem := percent(p.memReq, p.memAlloc)
			evidence = append(evidence, fmt.Sprintf(
				"sum of spec.containers[].resources.requests.memory %s against sum of status.allocatable.memory %s (%.1f percent)",
				model.HumanMemory(p.memReq), model.HumanMemory(p.memAlloc), mem))
			if mem > pressurePercent {
				tight = append(tight, "memory")
			}
		}
		if len(tight) > 0 {
			out = append(out, result.Finding{
				Severity: result.Risk,
				Kind:     "NodePool",
				Object:   p.key.name,
				Context:  p.key.context,
				Reason: fmt.Sprintf("%s requests pass %.0f percent of this pool's allocatable, so a drain has nowhere to put the pods",
					strings.Join(tight, " and "), pressurePercent),
				Evidence: evidence,
			})
		}
		if len(states) > 0 && p.owner == nil {
			out = append(out, result.Finding{
				Severity: result.NotAssessed,
				Kind:     "NodePool",
				Object:   p.key.name,
				Context:  p.key.context,
				Reason:   "no state read declares a node pool of this name, so who owns these nodes was not assessed",
				Evidence: []string{
					p.label,
					fmt.Sprintf("%d node pool(s) declared across %d state file(s) read", declared, len(states)),
				},
			})
		}
	}
	return out
}

// helmFindings reports the Helm releases Terraform owns, mapped to the live workloads that
// carry the release annotation. It is information, never a verdict.
func helmFindings(indexes []*index, states []*tfplan.State) []result.Finding {
	var out []result.Finding
	for _, st := range states {
		for _, rel := range st.HelmReleases() {
			namespace, name := splitRelease(rel.Name)
			context := ""
			var live []string
			for _, ix := range indexes {
				for _, w := range ix.workloads {
					if w.Metadata.Annotations[helmNameAnn] != name {
						continue
					}
					if ns := w.Metadata.Annotations[helmNamespaceAnn]; ns != "" && ns != namespace {
						continue
					}
					if context == "" {
						context = ix.snap.Context
					}
					live = append(live, w.Kind+" "+nsName(w.Metadata.Namespace, w.Metadata.Name))
				}
			}
			evidence := []string{
				"state resource " + rel.Address,
				fmt.Sprintf("attributes name=%s namespace=%s", name, namespace),
			}
			reason := "Terraform owns this Helm release, and no workload read carries the matching release annotation"
			if len(live) > 0 {
				reason = "Terraform owns this Helm release, live as " + strings.Join(live, ", ")
				evidence = append(evidence, "matched on metadata.annotations "+helmNameAnn+"="+name+": "+strings.Join(live, ", "))
			} else {
				evidence = append(evidence, "no workload carries metadata.annotations "+helmNameAnn+"="+name)
			}
			out = append(out, result.Finding{
				Severity: result.Info,
				Kind:     "HelmRelease",
				Object:   rel.Name,
				Context:  context,
				Reason:   reason,
				Evidence: evidence,
			})
		}
	}
	return out
}

// splitRelease splits the namespace/name key tfplan uses for a Helm release.
func splitRelease(key string) (string, string) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", key
}
