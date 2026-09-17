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

// desiredCount reads how many instances are wanted, and returns the evidence line naming
// the field it read. A DaemonSet counts nodes, not replicas, so it is read from its own
// status field. A Deployment or StatefulSet with spec.replicas unset wants 1, the default
// the API server applies; status.replicas is what exists, not what is wanted.
func desiredCount(w model.Workload) (int, string) {
	if w.Kind == "DaemonSet" {
		return w.Status.DesiredNumberScheduled, fmt.Sprintf("status.desiredNumberScheduled=%d", w.Status.DesiredNumberScheduled)
	}
	if w.Spec.Replicas != nil {
		return *w.Spec.Replicas, fmt.Sprintf("spec.replicas=%d", *w.Spec.Replicas)
	}
	return 1, "spec.replicas=1 (unset, which Kubernetes defaults to 1)"
}

// readyCount reads how many instances are ready, and returns the evidence line for it.
func readyCount(w model.Workload) (int, string) {
	if w.Kind == "DaemonSet" {
		return w.Status.NumberReady, fmt.Sprintf("status.numberReady=%d", w.Status.NumberReady)
	}
	return w.Status.ReadyReplicas, fmt.Sprintf("status.readyReplicas=%d", w.Status.ReadyReplicas)
}

// workloadRisks reports a workload that serves nothing, then a workload one node loss ends.
// An outage supersedes the single replica risk on the same object: it is already down.
func workloadRisks(ix *index, atRisk map[workloadKey]bool) []result.Finding {
	var out []result.Finding
	for _, w := range ix.workloads {
		key := keyOf(ix.snap.Context, w)
		desired, desiredLine := desiredCount(w)
		ready, readyLine := readyCount(w)
		switch {
		case desired > 0 && ready == 0:
			atRisk[key] = true
			evidence := []string{desiredLine, readyLine}
			if w.Kind != "DaemonSet" {
				evidence = append(evidence, fmt.Sprintf("status.availableReplicas=%d", w.Status.AvailableReplicas))
			}
			out = append(out, result.Finding{
				Severity: result.Outage,
				Kind:     w.Kind,
				Object:   key.object(),
				Context:  ix.snap.Context,
				Reason: fmt.Sprintf("nothing is ready while %d %s wanted, so this workload serves no traffic at all",
					desired, plural(desired, "is", "are")),
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
				Evidence: []string{desiredLine, readyLine},
			})
		}
	}
	return out
}

// pdbRisks reports a budget that allows no disruption at all, which blocks every drain of
// a node running the pods it selects. A budget that selects no pod blocks nothing: its zero
// is reported as information, never as a risk.
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
		if pdb.Spec.Selector.Empty() {
			evidence = append(evidence, "spec.selector is empty, which policy/v1 reads as every pod in the namespace")
		}
		if pdb.Status.ExpectedPods == 0 && ix.livePodsSelectedBy(pdb.Metadata.Namespace, pdb.Spec.Selector) == 0 {
			out = append(out, result.Finding{
				Severity: result.Info,
				Kind:     "PodDisruptionBudget",
				Object:   nsName(pdb.Metadata.Namespace, pdb.Metadata.Name),
				Context:  ix.snap.Context,
				Reason:   "this budget covers no pod right now, so it blocks no drain",
				Evidence: append(evidence, "no live pod in namespace "+pdb.Metadata.Namespace+" matches spec.selector"),
			})
			continue
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

// volumeRisks reports a bound claim whose volume is pinned to a single node or a single
// zone, so the pod using it cannot be rescheduled anywhere else, and a claim that is not
// bound while a live pod mounts it, since that pod has no volume to start with. A claim
// whose volume was not read is not a pass: it is reported as not assessed. A claim that is
// not bound and that nothing mounts is left alone: with WaitForFirstConsumer that is normal.
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
		object := nsName(pvc.Metadata.Namespace, pvc.Metadata.Name)
		mounts := ix.pvcPods[object]
		if pvc.Status.Phase != "Bound" {
			if len(mounts) == 0 {
				continue
			}
			phase := pvc.Status.Phase
			if phase == "" {
				phase = "unset"
			}
			evidence := []string{"status.phase=" + phase}
			if pvc.Spec.VolumeName != "" {
				evidence = append(evidence, "spec.volumeName="+pvc.Spec.VolumeName)
			} else {
				evidence = append(evidence, "spec.volumeName is empty")
			}
			out = append(out, result.Finding{
				Severity: result.Risk,
				Kind:     "PersistentVolumeClaim",
				Object:   object,
				Context:  ix.snap.Context,
				Reason:   fmt.Sprintf("this claim is %s, not Bound, so the pod mounting it has no volume to start with", phase),
				Evidence: append(evidence, ix.mountLines(mounts, atRisk)...),
			})
			continue
		}
		if pvc.Spec.VolumeName == "" {
			continue
		}
		pv, ok := ix.pvByName[pvc.Spec.VolumeName]
		if !ok {
			out = append(out, result.Finding{
				Severity: result.NotAssessed,
				Kind:     "PersistentVolumeClaim",
				Object:   object,
				Context:  ix.snap.Context,
				Reason:   "the volume this claim is bound to was not read, so whether it pins the pod to one node or one zone is unknown",
				Evidence: []string{
					"spec.volumeName=" + pvc.Spec.VolumeName,
					"status.phase=" + pvc.Status.Phase,
					"no persistentvolume of that name in this snapshot",
				},
			})
			continue
		}
		pin := pinnedTo(pv)
		if pin.key == "" {
			continue
		}
		evidence := []string{
			"spec.volumeName=" + pvc.Spec.VolumeName,
			"status.phase=" + pvc.Status.Phase,
			fmt.Sprintf("persistentvolume %s spec.nodeAffinity requires %s in [%s]", pv.Metadata.Name, pin.key, pin.value),
		}
		reason := "its volume lives in one zone only, so the pod using it cannot move out of zone " + pin.value
		if pin.key == hostnameLabel {
			reason = "its volume lives on one node only, so the pod using it cannot move off node " + pin.value
		}
		out = append(out, result.Finding{
			Severity: result.Risk,
			Kind:     "PersistentVolumeClaim",
			Object:   object,
			Context:  ix.snap.Context,
			Reason:   reason,
			Evidence: append(evidence, ix.mountLines(mounts, atRisk)...),
		})
	}
	return out
}

// mountLines names the live pods mounting a claim, marks their workloads at risk, and says
// when a pod has no workload read behind it.
func (ix *index) mountLines(mounts []model.Pod, atRisk map[workloadKey]bool) []string {
	var lines []string
	for _, pod := range mounts {
		name := nsName(pod.Metadata.Namespace, pod.Metadata.Name)
		if k, ok := ix.podWorkload[name]; ok {
			atRisk[k] = true
			lines = append(lines, "mounted by pod "+name+" of "+k.kind+" "+k.object())
		} else {
			lines = append(lines, "mounted by pod "+name+", which no workload read owns")
		}
	}
	return lines
}

// volumePin is the one node or one zone a volume's required node affinity allows.
type volumePin struct {
	key   string
	value string
}

// pinKeys are the affinity keys spanline reads as a pin, tightest first: a hostname pin
// beats a zone pin, and the current zone key is preferred to the legacy one.
var pinKeys = []string{hostnameLabel, zoneLabel, legacyZoneLabel}

// pinnedTo reads whether a volume's required node affinity allows exactly one node, or
// failing that exactly one zone, and returns the key as it was read. Any other affinity is
// not a pin spanline understands, so nothing is reported for it.
func pinnedTo(pv model.PersistentVolume) volumePin {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return volumePin{}
	}
	byKey := map[string]map[string]bool{}
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Operator != "" && expr.Operator != "In" {
				continue
			}
			if byKey[expr.Key] == nil {
				byKey[expr.Key] = map[string]bool{}
			}
			for _, v := range expr.Values {
				byKey[expr.Key][v] = true
			}
		}
	}
	for _, key := range pinKeys {
		if values := sortedKeys(byKey[key]); len(values) == 1 {
			return volumePin{key: key, value: values[0]}
		}
	}
	return volumePin{}
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
		live := 0
		for _, p := range ix.snap.PodsOnNode(n.Metadata.Name) {
			if podLive(p) {
				live++
			}
		}
		lines = append(lines, fmt.Sprintf("%d live pod(s) scheduled on it", live))
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
			reason := "no state read declares a node pool of this name, so who owns these nodes was not assessed"
			evidence := []string{p.label}
			if len(p.rivals) > 0 {
				reason = "state resources declare a node pool of this name, but none can be tied to cluster " +
					p.key.context + " without guessing, so who owns these nodes was not assessed"
				for _, c := range p.rivals {
					evidence = append(evidence, rivalLine(c))
				}
			}
			evidence = append(evidence, fmt.Sprintf("%d node pool(s) declared across %d state file(s) read", declared, len(states)))
			out = append(out, result.Finding{
				Severity: result.NotAssessed,
				Kind:     "NodePool",
				Object:   p.key.name,
				Context:  p.key.context,
				Reason:   reason,
				Evidence: evidence,
			})
		}
	}
	return out
}

// rivalLine describes one state resource that declares a contested pool name, and why it
// was not handed to the pool: it already owns another pool, or its state names a cluster.
func rivalLine(c *ownerCandidate) string {
	line := c.owner.Address
	if c.owner.StateFile != "" {
		line += " in " + c.owner.StateFile
	}
	if c.matched {
		line += ", already matched to pool " + c.claimedBy.context + "/" + c.claimedBy.name
	}
	if len(c.clusters) == 0 {
		return line + ", its state names no cluster"
	}
	names := sortedKeys(c.clusters)
	return line + ", its state names " + plural(len(names), "cluster ", "clusters ") + strings.Join(names, ", ")
}

// helmFindings reports the Helm releases Terraform owns, mapped to the live workloads that
// carry the release annotation: one finding per cluster where the release is found, so a
// release installed in several clusters never lists them all under the first context. A
// release found nowhere is reported once, with no context. It is information, never a verdict.
func helmFindings(indexes []*index, states []*tfplan.State) []result.Finding {
	var out []result.Finding
	for _, st := range states {
		for _, rel := range st.HelmReleases() {
			namespace, name := splitRelease(rel.Name)
			base := []string{
				"state resource " + rel.Address,
				fmt.Sprintf("attributes name=%s namespace=%s", name, namespace),
			}
			found := false
			for _, ix := range indexes {
				var live []string
				for _, w := range ix.workloads {
					if w.Metadata.Annotations[helmNameAnn] != name {
						continue
					}
					if ns := w.Metadata.Annotations[helmNamespaceAnn]; ns != "" && ns != namespace {
						continue
					}
					live = append(live, w.Kind+" "+nsName(w.Metadata.Namespace, w.Metadata.Name))
				}
				if len(live) == 0 {
					continue
				}
				found = true
				evidence := append(append([]string(nil), base...),
					"matched on metadata.annotations "+helmNameAnn+"="+name+": "+strings.Join(live, ", "))
				out = append(out, result.Finding{
					Severity: result.Info,
					Kind:     "HelmRelease",
					Object:   rel.Name,
					Context:  ix.snap.Context,
					Reason:   "Terraform owns this Helm release, live as " + strings.Join(live, ", "),
					Evidence: evidence,
				})
			}
			if !found {
				out = append(out, result.Finding{
					Severity: result.Info,
					Kind:     "HelmRelease",
					Object:   rel.Name,
					Reason:   "Terraform owns this Helm release, and no workload read carries the matching release annotation",
					Evidence: append(base, "no workload carries metadata.annotations "+helmNameAnn+"="+name),
				})
			}
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
