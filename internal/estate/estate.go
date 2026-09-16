// Package estate builds the cockpit: every cluster spanline could read, every node pool
// under it, the Terraform address that owns each pool, and what is fragile right now.
// It is deterministic, reads only what it is handed, and never calls a model.
package estate

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
	"github.com/arnocho/spanline/internal/tfplan"
)

// Options carries the one thing Build refuses to read from the world: the clock.
type Options struct {
	// Now is the reference time of the report. Build never calls time.Now, so the same
	// inputs always produce the same report, in a test and on a laptop.
	Now time.Time
}

const (
	zoneLabel        = "topology.kubernetes.io/zone"
	legacyZoneLabel  = "failure-domain.beta.kubernetes.io/zone"
	rolePrefix       = "node-role.kubernetes.io/"
	helmNameAnn      = "meta.helm.sh/release-name"
	helmNamespaceAnn = "meta.helm.sh/release-namespace"
	// unlabelledPool is what a node is grouped under when nothing names its pool.
	unlabelledPool = "unlabelled"
	// pressurePercent is the share of allocatable above which a drain has nowhere to go.
	pressurePercent = 85.0
)

// poolLabels are the labels that name a node pool, in the order spanline trusts them.
var poolLabels = []string{
	"kubernetes.azure.com/agentpool",
	"agentpool",
	"cloud.google.com/gke-nodepool",
	"eks.amazonaws.com/nodegroup",
}

// workloadKey identifies one workload across the whole estate.
type workloadKey struct {
	context   string
	kind      string
	namespace string
	name      string
}

func (k workloadKey) object() string { return nsName(k.namespace, k.name) }

// poolKey identifies one node pool inside one cluster.
type poolKey struct {
	context string
	name    string
}

// pool is everything known about one node pool before it becomes a report row.
type pool struct {
	key       poolKey
	label     string // the exact label that named this pool, kept for evidence
	nodes     []string
	zones     map[string]bool
	cpuReq    int64
	cpuAlloc  int64
	memReq    int64
	memAlloc  int64
	workloads map[workloadKey]bool
	owner     *tfplan.Owner
}

// index is the per snapshot lookup table every rule shares, so the pod to workload,
// pod to claim and claim to volume joins are computed once and stay consistent.
type index struct {
	snap        *model.Snapshot
	workloads   []model.Workload
	podWorkload map[string]workloadKey
	pvcPods     map[string][]model.Pod
	pvByName    map[string]model.PersistentVolume
	nodeByName  map[string]model.Node
}

// ownerCandidate is one node pool declared by one state, and whether a live pool claimed it.
type ownerCandidate struct {
	owner    tfplan.Owner
	stateIdx int
	clusters map[string]bool
	matched  bool
}

// Build turns snapshots and Terraform states into the estate report. It never mutates its
// inputs, never calls the clock, and never reports a coverage hole as a pass.
func Build(snaps []*model.Snapshot, states []*tfplan.State, o Options) (*result.EstateReport, error) {
	live := make([]*model.Snapshot, 0, len(snaps))
	for _, s := range snaps {
		if s != nil {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return nil, errors.New("estate: no snapshot was passed, so there is no estate to report on")
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].Context < live[j].Context })

	read := make([]*tfplan.State, 0, len(states))
	for _, st := range states {
		if st != nil {
			read = append(read, st)
		}
	}

	indexes := make([]*index, 0, len(live))
	for _, s := range live {
		indexes = append(indexes, newIndex(s))
	}

	pools, orphanPods := buildPools(indexes)
	candidates := ownPools(pools, read)

	risks, atRisk := collectRisks(indexes, pools, read)
	sortFindings(risks)

	return &result.EstateReport{
		Clusters:    clusterSummaries(indexes, risks),
		Pools:       poolSummaries(pools, atRisk),
		Risks:       risks,
		States:      stateSummaries(read, candidates),
		Gaps:        gapLines(indexes, read, orphanPods),
		GeneratedAt: o.Now,
	}, nil
}

func newIndex(s *model.Snapshot) *index {
	ix := &index{
		snap:        s,
		workloads:   s.Workloads(),
		podWorkload: map[string]workloadKey{},
		pvcPods:     map[string][]model.Pod{},
		pvByName:    map[string]model.PersistentVolume{},
		nodeByName:  s.NodeByName(),
	}
	sort.SliceStable(ix.workloads, func(i, j int) bool {
		a, b := ix.workloads[i], ix.workloads[j]
		if a.Metadata.Namespace != b.Metadata.Namespace {
			return a.Metadata.Namespace < b.Metadata.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Metadata.Name < b.Metadata.Name
	})
	for _, pod := range s.Pods {
		for _, w := range ix.workloads {
			if w.Metadata.Namespace != pod.Metadata.Namespace {
				continue
			}
			if model.SelectorMatches(w.Spec.Selector, pod.Metadata.Labels) {
				ix.podWorkload[nsName(pod.Metadata.Namespace, pod.Metadata.Name)] = keyOf(s.Context, w)
				break
			}
		}
		for _, v := range pod.Spec.Volumes {
			if v.PersistentVolumeClaim == nil {
				continue
			}
			claim := nsName(pod.Metadata.Namespace, v.PersistentVolumeClaim.ClaimName)
			ix.pvcPods[claim] = append(ix.pvcPods[claim], pod)
		}
	}
	for _, pv := range s.PVs {
		ix.pvByName[pv.Metadata.Name] = pv
	}
	return ix
}

// workloadsSelectedBy resolves a namespaced selector to the workloads it covers, first by
// pod template labels, then through the live pods, so a selector still resolves when a
// template carries labels the controller adds later.
func (ix *index) workloadsSelectedBy(namespace string, sel *model.LabelSelector) []workloadKey {
	seen := map[workloadKey]bool{}
	var out []workloadKey
	add := func(k workloadKey) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, w := range ix.workloads {
		if w.Metadata.Namespace != namespace {
			continue
		}
		if model.SelectorMatches(sel, w.Spec.Template.Metadata.Labels) {
			add(keyOf(ix.snap.Context, w))
		}
	}
	for _, pod := range ix.snap.Pods {
		if pod.Metadata.Namespace != namespace {
			continue
		}
		if !model.SelectorMatches(sel, pod.Metadata.Labels) {
			continue
		}
		if k, ok := ix.podWorkload[nsName(pod.Metadata.Namespace, pod.Metadata.Name)]; ok {
			add(k)
		}
	}
	return out
}

// poolOf names the pool a node belongs to, and returns the exact label it read.
func poolOf(n model.Node) (string, string) {
	for _, key := range poolLabels {
		if v := n.Metadata.Labels[key]; v != "" {
			return v, "node label " + key + "=" + v
		}
	}
	var roles []string
	for key := range n.Metadata.Labels {
		if !strings.HasPrefix(key, rolePrefix) {
			continue
		}
		if role := strings.TrimPrefix(key, rolePrefix); role != "" {
			roles = append(roles, role)
		}
	}
	if len(roles) > 0 {
		sort.Strings(roles)
		return roles[0], "node label " + rolePrefix + roles[0]
	}
	return unlabelledPool, "no node pool label on this node"
}

func zoneOf(n model.Node) string {
	if z := n.Metadata.Labels[zoneLabel]; z != "" {
		return z
	}
	return n.Metadata.Labels[legacyZoneLabel]
}

func allocatable(n model.Node) (int64, int64) {
	var cpu, mem int64
	if v, ok := model.ParseCPU(n.Status.Allocatable["cpu"]); ok {
		cpu = v
	}
	if v, ok := model.ParseMemory(n.Status.Allocatable["memory"]); ok {
		mem = v
	}
	return cpu, mem
}

func requests(p model.Pod) (int64, int64) {
	var cpu, mem int64
	for _, c := range p.Spec.Containers {
		if v, ok := model.ParseCPU(c.Resources.Requests["cpu"]); ok {
			cpu += v
		}
		if v, ok := model.ParseMemory(c.Resources.Requests["memory"]); ok {
			mem += v
		}
	}
	return cpu, mem
}

// buildPools groups nodes into pools, then folds the pods scheduled on those nodes into each
// pool's request totals and workload set. Pods whose node was not read are counted and
// returned, so the report admits the undercount instead of hiding it.
func buildPools(indexes []*index) (map[poolKey]*pool, map[string]int) {
	pools := map[poolKey]*pool{}
	orphans := map[string]int{}
	for _, ix := range indexes {
		for _, n := range ix.snap.Nodes {
			name, label := poolOf(n)
			p := poolFor(pools, poolKey{context: ix.snap.Context, name: name}, label)
			p.nodes = append(p.nodes, n.Metadata.Name)
			if z := zoneOf(n); z != "" {
				p.zones[z] = true
			}
			cpu, mem := allocatable(n)
			p.cpuAlloc += cpu
			p.memAlloc += mem
		}
		for _, pod := range ix.snap.Pods {
			if pod.Spec.NodeName == "" {
				continue
			}
			n, ok := ix.nodeByName[pod.Spec.NodeName]
			if !ok {
				orphans[ix.snap.Context]++
				continue
			}
			name, label := poolOf(n)
			p := poolFor(pools, poolKey{context: ix.snap.Context, name: name}, label)
			cpu, mem := requests(pod)
			p.cpuReq += cpu
			p.memReq += mem
			if k, ok := ix.podWorkload[nsName(pod.Metadata.Namespace, pod.Metadata.Name)]; ok {
				p.workloads[k] = true
			}
		}
	}
	return pools, orphans
}

func poolFor(pools map[poolKey]*pool, k poolKey, label string) *pool {
	p, ok := pools[k]
	if !ok {
		p = &pool{
			key:       k,
			label:     label,
			zones:     map[string]bool{},
			workloads: map[workloadKey]bool{},
		}
		pools[k] = p
	}
	return p
}

// ownPools matches each live pool to the Terraform address that declares it, by pool name.
// When several states declare the same name, the one whose state also declares a cluster
// named like the kube context wins, so two clouds sharing a pool name do not swap owners.
func ownPools(pools map[poolKey]*pool, states []*tfplan.State) []*ownerCandidate {
	var candidates []*ownerCandidate
	for i, st := range states {
		clusters := map[string]bool{}
		for _, c := range st.Clusters() {
			clusters[c.Name] = true
		}
		for _, o := range st.NodePools() {
			candidates = append(candidates, &ownerCandidate{owner: o, stateIdx: i, clusters: clusters})
		}
	}
	if len(candidates) == 0 {
		return candidates
	}
	for _, k := range sortedPoolKeys(pools) {
		p := pools[k]
		var fallback *ownerCandidate
		var chosen *ownerCandidate
		for _, c := range candidates {
			if c.owner.Name != p.key.name {
				continue
			}
			if c.clusters[p.key.context] {
				chosen = c
				break
			}
			if fallback == nil {
				fallback = c
			}
		}
		if chosen == nil {
			chosen = fallback
		}
		if chosen == nil {
			continue
		}
		chosen.matched = true
		owner := chosen.owner
		p.owner = &owner
	}
	return candidates
}

func clusterSummaries(indexes []*index, risks []result.Finding) []result.ClusterSummary {
	riskCount := map[string]int{}
	outageCount := map[string]int{}
	for _, f := range risks {
		if f.Severity.Rank() >= result.Risk.Rank() {
			riskCount[f.Context]++
		}
		if f.Severity == result.Outage {
			outageCount[f.Context]++
		}
	}
	out := make([]result.ClusterSummary, 0, len(indexes))
	for _, ix := range indexes {
		notReady := 0
		for _, n := range ix.snap.Nodes {
			if ready, _ := nodeReady(n); !ready {
				notReady++
			}
		}
		namespaces := map[string]bool{}
		for _, p := range ix.snap.Pods {
			if p.Metadata.Namespace != "" {
				namespaces[p.Metadata.Namespace] = true
			}
		}
		for _, w := range ix.workloads {
			if w.Metadata.Namespace != "" {
				namespaces[w.Metadata.Namespace] = true
			}
		}
		out = append(out, result.ClusterSummary{
			Context:       ix.snap.Context,
			Nodes:         len(ix.snap.Nodes),
			NodesNotReady: notReady,
			Pods:          len(ix.snap.Pods),
			Namespaces:    len(namespaces),
			Workloads:     len(ix.workloads),
			Risks:         riskCount[ix.snap.Context],
			Outages:       outageCount[ix.snap.Context],
			CollectedAt:   ix.snap.CollectedAt,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Context < out[j].Context })
	return out
}

func poolSummaries(pools map[poolKey]*pool, atRisk map[workloadKey]bool) []result.PoolSummary {
	out := make([]result.PoolSummary, 0, len(pools))
	for _, k := range sortedPoolKeys(pools) {
		p := pools[k]
		row := result.PoolSummary{
			Context:    p.key.context,
			Pool:       p.key.name,
			Nodes:      len(p.nodes),
			CPUPercent: percent(p.cpuReq, p.cpuAlloc),
			MemPercent: percent(p.memReq, p.memAlloc),
			Workloads:  len(p.workloads),
			Zones:      strings.Join(sortedKeys(p.zones), ","),
		}
		if p.owner != nil {
			row.TerraformAddress = p.owner.Address
			row.StateFile = p.owner.StateFile
		}
		for w := range p.workloads {
			if atRisk[w] {
				row.AtRisk++
			}
		}
		out = append(out, row)
	}
	return out
}

func stateSummaries(states []*tfplan.State, candidates []*ownerCandidate) []result.StateSummary {
	if len(states) == 0 {
		return nil
	}
	out := make([]result.StateSummary, 0, len(states))
	for i, st := range states {
		row := result.StateSummary{
			Path:      st.Path,
			Resources: st.CountResources(),
			NodePools: len(st.NodePools()),
			Clusters:  len(st.Clusters()),
		}
		for _, c := range candidates {
			if c.stateIdx != i {
				continue
			}
			if c.matched {
				row.Matched++
			} else {
				row.Unmatched++
			}
		}
		out = append(out, row)
	}
	return out
}

// gapLines says out loud what was not read. An empty risk list only means something when
// this list is empty too.
func gapLines(indexes []*index, states []*tfplan.State, orphanPods map[string]int) []string {
	var out []string
	for _, ix := range indexes {
		ctx := ix.snap.Context
		for _, g := range ix.snap.Gaps {
			out = append(out, fmt.Sprintf("%s: %s was not read (%s), so nothing about it was assessed", ctx, g.Resource, g.Reason))
		}
		if len(ix.snap.Nodes) == 0 {
			out = append(out, fmt.Sprintf("%s: returned no nodes, so no pool, no capacity and no node risk was assessed there", ctx))
		}
		if n := orphanPods[ctx]; n > 0 {
			out = append(out, fmt.Sprintf("%s: %d pod(s) run on a node that was not read, so their requests are missing from the pool totals", ctx, n))
		}
	}
	if len(states) == 0 {
		out = append(out, "no Terraform or OpenTofu state was read, so no pool ownership was assessed anywhere in this report")
	}
	return out
}

func keyOf(context string, w model.Workload) workloadKey {
	return workloadKey{
		context:   context,
		kind:      w.Kind,
		namespace: w.Metadata.Namespace,
		name:      w.Metadata.Name,
	}
}

func nsName(namespace, name string) string { return namespace + "/" + name }

func percent(used, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedPoolKeys(pools map[poolKey]*pool) []poolKey {
	out := make([]poolKey, 0, len(pools))
	for k := range pools {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].context != out[j].context {
			return out[i].context < out[j].context
		}
		return out[i].name < out[j].name
	})
	return out
}

// sortFindings orders the risk list the way an operator reads it: worst first, then by
// cluster, then by object, with kind and reason as the last tie breaks so the order is total.
func sortFindings(f []result.Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		a, b := f[i], f[j]
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() > b.Severity.Rank()
		}
		if a.Context != b.Context {
			return a.Context < b.Context
		}
		if a.Object != b.Object {
			return a.Object < b.Object
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Reason < b.Reason
	})
}
