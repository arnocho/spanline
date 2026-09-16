package impact

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
	"github.com/arnocho/spanline/internal/tfplan"
)

// planSHALength is how much of the plan digest is printed. It is long enough to tie a report
// to one file, short enough to read out loud.
const planSHALength = 12

// PlanSHA returns the short sha256 of the exact plan bytes the caller read, so a report can be
// tied back to the file it came from. Plan itself never sees the bytes, so the caller that read
// them sets ImpactReport.PlanSHA with this.
func PlanSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:planSHALength]
}

// Plan answers: if this Terraform or OpenTofu plan is applied, what breaks on the live cluster.
// The plan is read as JSON that was already exported: terraform is never run, no provider is
// downloaded, no backend is contacted, and nothing is applied to find out.
//
// states are the state files that were read, used to name the Terraform address that owns each
// live pool. Pass nil when none were read.
func Plan(snap *model.Snapshot, plan *tfplan.Plan, states []*tfplan.State, source string, o Options) (*result.ImpactReport, error) {
	if snap == nil {
		return nil, errors.New("impact: there is no snapshot to analyse")
	}
	if plan == nil {
		return nil, errors.New("impact: there is no plan to analyse")
	}

	a := newAnalysis(snap)
	owners := poolOwners(states)
	poolChanges := plan.PoolChanges()
	clusterChanges := plan.ClusterChanges()

	var mapping []string
	var rotating []string
	planned := map[string]bool{}

	for _, pc := range poolChanges {
		planned[pc.Pool] = true
		nodes, key := nodesForPool(snap, pc.Pool)
		action := actionWord(pc.Actions)

		line := pc.Address + " (" + action + ") -> "
		if len(nodes) == 0 {
			line += "no live node carries this pool name"
		} else {
			line += fmt.Sprintf("nodes %s=%s (%d)", key, pc.Pool, len(nodes))
		}
		mapping = append(mapping, line+stateNote(owners, pc.Pool))

		ev := []string{
			"resource_changes[].change.actions: " + emptyOr(strings.Join(pc.Actions, ", "), "none"),
			"changed attributes: " + emptyOr(strings.Join(pc.Attributes, ", "), "none"),
			"pool name read from the plan: " + emptyOr(pc.Pool, "empty"),
			"node label keys read to find it: " + strings.Join(poolLabelKeys, ", "),
		}
		if pc.ActionReason != "" {
			ev = append(ev, "resource_changes[].action_reason: "+pc.ActionReason)
		}

		if len(nodes) == 0 {
			a.add(result.NotAssessed, "NodePool", pc.Address,
				"this plan changes a pool that no live node claims, so spanline cannot say what the change would move, and silence here is not a pass",
				ev)
			continue
		}
		ev = append(ev, fmt.Sprintf("live nodes matched on %s=%s: %d", key, pc.Pool, len(nodes)))

		switch pc.Effect {
		case tfplan.EffectReplaces:
			a.doom(nodes)
		case tfplan.EffectRotates:
			a.doom(nodes)
			rotating = append(rotating, pc.Address)
		case tfplan.EffectNone:
			a.add(result.Info, "NodePool", pc.Address,
				fmt.Sprintf("this change moves no node, so the %d nodes of pool %s stay where they are", len(nodes), pc.Pool),
				ev)
		default:
			attrs := pc.Unmodelled
			if len(attrs) == 0 {
				attrs = pc.Attributes
			}
			a.add(result.NotAssessed, "NodePool", pc.Address,
				"spanline has no rule that says whether "+emptyOr(strings.Join(attrs, ", "), "this change")+
					" moves a node on "+pc.Type+", so this change is not judged either way",
				append(ev, "attributes with no node effect rule: "+emptyOr(strings.Join(attrs, ", "), "none")))
		}
	}

	for _, cc := range clusterChanges {
		action := actionWord(cc.Actions)
		attrs := cc.Unmodelled
		if len(attrs) == 0 {
			attrs = cc.Attributes
		}
		ev := []string{
			"resource_changes[].change.actions: " + emptyOr(strings.Join(cc.Actions, ", "), "none"),
			"changed attributes: " + emptyOr(strings.Join(cc.Attributes, ", "), "none"),
			"cluster name read from the plan: " + emptyOr(cc.Cluster, "empty"),
		}

		if cc.Effect == tfplan.EffectReplaces {
			mapping = append(mapping, fmt.Sprintf("%s (%s) -> every live node (%d)", cc.Address, action, len(snap.Nodes)))
			a.doom(snap.Nodes)
			a.add(result.Disruption, "Cluster", cc.Address,
				"this plan destroys and recreates the cluster resource itself, so every node in it goes away at once",
				ev)
			continue
		}

		mapping = append(mapping, cc.Address+" ("+action+") -> node effect not readable from this plan")
		a.add(result.NotAssessed, "Cluster", cc.Address,
			"this cluster change touches "+emptyOr(strings.Join(attrs, ", "), "attributes spanline does not model")+
				", and spanline has no rule that says whether that rolls the nodes, so it is not judged either way",
			append(ev, "attributes with no node effect rule: "+emptyOr(strings.Join(attrs, ", "), "none")))
	}

	mapping = append(mapping, untouchedPools(snap, owners, planned)...)

	if len(rotating) > 0 {
		sort.Strings(rotating)
		a.note = "rotation on " + strings.Join(rotating, ", ") +
			" is surge based, so the checks below describe one wave of nodes at a time, not the whole rotation"
	}

	a.run()
	if len(a.order) > 0 {
		a.summarise(source)
	}
	a.note = ""
	addOtherChanges(a, plan, poolChanges, clusterChanges)

	rep := a.report(source, o)
	add, change, destroy := plan.Summary()
	rep.PlanSummary = fmt.Sprintf("%d to add, %d to destroy, %d to change", add, destroy, change)
	rep.Mapping = mapping
	return rep, nil
}

// addOtherChanges counts what the plan does outside the clusters, so the report says plainly
// that those changes were seen and have no node effect.
func addOtherChanges(a *analysis, plan *tfplan.Plan, pools []tfplan.PoolChange, clusters []tfplan.ClusterChange) {
	total := plan.OtherChanges()
	if total == 0 {
		return
	}
	touched := map[string]bool{}
	for _, pc := range pools {
		touched[pc.Address] = true
	}
	for _, cc := range clusters {
		touched[cc.Address] = true
	}
	byType := map[string]int{}
	for _, rc := range plan.ResourceChanges {
		if rc.Change.IsNoop() || touched[rc.Address] {
			continue
		}
		byType[rc.Type]++
	}
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)
	ev := make([]string, 0, len(types)+1)
	for _, t := range types {
		ev = append(ev, fmt.Sprintf("%s: %d", t, byType[t]))
	}
	ev = append(ev, "these resource types own no Kubernetes node, so no workload check was run against them")

	a.add(result.Info, "Plan", "changes outside the node pools",
		fmt.Sprintf("%d changes in this plan touch no Kubernetes node", total), ev)
}

// poolOwners indexes the node pools the state files declare, by pool name.
func poolOwners(states []*tfplan.State) map[string][]tfplan.Owner {
	out := map[string][]tfplan.Owner{}
	for _, st := range states {
		if st == nil {
			continue
		}
		for _, o := range st.NodePools() {
			out[o.Name] = append(out[o.Name], o)
		}
	}
	for name := range out {
		list := out[name]
		sort.Slice(list, func(i, j int) bool {
			if list[i].Address != list[j].Address {
				return list[i].Address < list[j].Address
			}
			return list[i].StateFile < list[j].StateFile
		})
		out[name] = list
	}
	return out
}

// stateNote names the state file entry that declares a pool, or says no state file does.
func stateNote(owners map[string][]tfplan.Owner, pool string) string {
	list := owners[pool]
	if len(list) == 0 {
		return " [declared in no state file that was read]"
	}
	o := list[0]
	if o.StateFile != "" {
		return " [state " + o.StateFile + ": " + o.Address + "]"
	}
	return " [state: " + o.Address + "]"
}

// untouchedPools records the live pools a state declares and this plan leaves alone.
func untouchedPools(snap *model.Snapshot, owners map[string][]tfplan.Owner, planned map[string]bool) []string {
	names := make([]string, 0, len(owners))
	for name := range owners {
		if !planned[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var out []string
	for _, name := range names {
		nodes, key := nodesForPool(snap, name)
		if len(nodes) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("%s (no change in this plan) -> nodes %s=%s (%d)",
			owners[name][0].Address, key, name, len(nodes)))
	}
	return out
}

// nodesForPool finds the live nodes of a pool, and says which label key matched.
func nodesForPool(snap *model.Snapshot, pool string) ([]model.Node, string) {
	if pool == "" {
		return nil, ""
	}
	for _, key := range poolLabelKeys {
		var out []model.Node
		for _, n := range snap.Nodes {
			if n.Metadata.Labels[key] == pool {
				out = append(out, n)
			}
		}
		if len(out) > 0 {
			sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Name < out[j].Metadata.Name })
			return out, key
		}
	}
	return nil, ""
}

// actionWord turns a plan's action list into the word an operator reads in terraform output.
func actionWord(actions []string) string {
	has := func(want string) bool {
		for _, a := range actions {
			if a == want {
				return true
			}
		}
		return false
	}
	switch {
	case has("delete") && has("create"):
		return "replace"
	case has("delete"):
		return "destroy"
	case has("create"):
		return "create"
	case has("update"):
		return "update"
	case len(actions) == 0:
		return "no action"
	default:
		return strings.Join(actions, ",")
	}
}
