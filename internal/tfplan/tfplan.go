// Package tfplan reads Terraform and OpenTofu JSON: the plan a CI job exported with
// `terraform show -json`, and the state file. It never runs terraform, never needs a
// provider, never touches a backend, and never reads the binary plan.
package tfplan

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// Change is the before and after of one resource in a plan.
type Change struct {
	Actions      []string       `json:"actions"`
	Before       map[string]any `json:"before"`
	After        map[string]any `json:"after"`
	ReplacePaths [][]any        `json:"replace_paths,omitempty"`
}

// ResourceChange is one entry of a plan's resource_changes.
type ResourceChange struct {
	Address      string `json:"address"`
	Type         string `json:"type"`
	Name         string `json:"name"`
	ProviderName string `json:"provider_name,omitempty"`
	ActionReason string `json:"action_reason,omitempty"`
	Change       Change `json:"change"`
}

// Plan is the subset of a Terraform or OpenTofu plan JSON that spanline reads.
type Plan struct {
	FormatVersion    string           `json:"format_version"`
	TerraformVersion string           `json:"terraform_version"`
	ResourceChanges  []ResourceChange `json:"resource_changes"`
}

func has(actions []string, a string) bool {
	for _, v := range actions {
		if v == a {
			return true
		}
	}
	return false
}

// IsReplace reports a destroy and recreate, whatever the order the plan lists them in.
func (c Change) IsReplace() bool {
	return has(c.Actions, "delete") && has(c.Actions, "create")
}

// IsDelete reports a pure destroy.
func (c Change) IsDelete() bool { return has(c.Actions, "delete") && !has(c.Actions, "create") }

// IsCreate reports a pure create.
func (c Change) IsCreate() bool { return has(c.Actions, "create") && !has(c.Actions, "delete") }

// IsUpdate reports an in-place update.
func (c Change) IsUpdate() bool { return has(c.Actions, "update") }

// IsNoop reports a resource the plan leaves alone.
func (c Change) IsNoop() bool { return has(c.Actions, "no-op") || len(c.Actions) == 0 }

// ChangedAttributes lists the top level attributes whose value differs, sorted.
func (c Change) ChangedAttributes() []string {
	var out []string
	seen := map[string]bool{}
	for k, av := range c.After {
		if bv, ok := c.Before[k]; !ok || !sameJSON(av, bv) {
			out = append(out, k)
			seen[k] = true
		}
	}
	for k := range c.Before {
		if _, ok := c.After[k]; !ok && !seen[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sameJSON(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ab) == string(bb)
}

// ParsePlan decodes plan JSON.
func ParsePlan(b []byte) (*Plan, error) {
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("this file is not a Terraform or OpenTofu plan in JSON form: %w", err)
	}
	if p.ResourceChanges == nil {
		return nil, fmt.Errorf("this JSON has no resource_changes: export it with `terraform show -json plan.tfplan`")
	}
	return &p, nil
}

// LoadPlanFile reads and decodes a plan JSON file.
func LoadPlanFile(path string) (*Plan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParsePlan(b)
}

// Summary counts the plan the way the terraform CLI prints it.
func (p *Plan) Summary() (add, change, destroy int) {
	for _, rc := range p.ResourceChanges {
		switch {
		case rc.Change.IsReplace():
			add++
			destroy++
		case rc.Change.IsCreate():
			add++
		case rc.Change.IsDelete():
			destroy++
		case rc.Change.IsUpdate():
			change++
		}
	}
	return add, change, destroy
}

// Effect says what a resource change does to the nodes underneath a cluster.
type Effect string

const (
	// EffectReplaces means every node of the pool is destroyed and recreated.
	EffectReplaces Effect = "REPLACES"
	// EffectRotates means nodes are surged and drained one wave at a time.
	EffectRotates Effect = "ROTATES"
	// EffectNone means the change does not move a node.
	EffectNone Effect = "NONE"
	// EffectUnknown means spanline has no rule for this attribute, so nothing is assumed.
	EffectUnknown Effect = "UNKNOWN"
)

// nodePoolTypes are the resource types that own Kubernetes nodes, per provider.
var nodePoolTypes = map[string]string{
	"azurerm_kubernetes_cluster_node_pool": "name",
	"google_container_node_pool":           "name",
	"aws_eks_node_group":                   "node_group_name",
	"scaleway_k8s_pool":                    "name",
	"proxmox_vm_qemu":                      "name",
}

// clusterTypes are the resource types that own a cluster itself.
var clusterTypes = map[string]string{
	"azurerm_kubernetes_cluster": "name",
	"google_container_cluster":   "name",
	"aws_eks_cluster":            "name",
	"scaleway_k8s_cluster":       "name",
}

// attributeEffects is the curated table of attribute changes whose node effect is documented.
// Anything absent stays EffectUnknown, which becomes NOT ASSESSED, never a silent pass.
var attributeEffects = map[string]map[string]Effect{
	"azurerm_kubernetes_cluster_node_pool": {
		"vm_size":                     EffectReplaces,
		"os_disk_size_gb":             EffectReplaces,
		"os_disk_type":                EffectReplaces,
		"os_sku":                      EffectReplaces,
		"zones":                       EffectReplaces,
		"vnet_subnet_id":              EffectReplaces,
		"pod_subnet_id":               EffectReplaces,
		"max_pods":                    EffectReplaces,
		"orchestrator_version":        EffectRotates,
		"node_labels":                 EffectRotates,
		"node_taints":                 EffectRotates,
		"temporary_name_for_rotation": EffectRotates,
		"node_count":                  EffectNone,
		"min_count":                   EffectNone,
		"max_count":                   EffectNone,
		"tags":                        EffectNone,
	},
	"google_container_node_pool": {
		"machine_type": EffectReplaces,
		"disk_size_gb": EffectReplaces,
		"disk_type":    EffectReplaces,
		"version":      EffectRotates,
		"node_count":   EffectNone,
	},
	"aws_eks_node_group": {
		"instance_types": EffectReplaces,
		"disk_size":      EffectReplaces,
		"version":        EffectRotates,
		"scaling_config": EffectNone,
		"tags":           EffectNone,
	},
}

// PoolChange is one node pool the plan touches.
type PoolChange struct {
	Address      string
	Pool         string
	Type         string
	Actions      []string
	ActionReason string
	Effect       Effect
	Attributes   []string
	Unmodelled   []string
}

// ClusterChange is one cluster resource the plan touches.
type ClusterChange struct {
	Address    string
	Cluster    string
	Type       string
	Actions    []string
	Effect     Effect
	Attributes []string
	Unmodelled []string
}

func attrString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
	return ""
}

func effectFor(resourceType string, attrs []string) (Effect, []string) {
	table, known := attributeEffects[resourceType]
	if !known {
		return EffectUnknown, attrs
	}
	worst := EffectNone
	var unmodelled []string
	for _, a := range attrs {
		e, ok := table[a]
		if !ok {
			unmodelled = append(unmodelled, a)
			continue
		}
		switch e {
		case EffectReplaces:
			worst = EffectReplaces
		case EffectRotates:
			if worst != EffectReplaces {
				worst = EffectRotates
			}
		}
	}
	if len(unmodelled) > 0 && worst == EffectNone {
		return EffectUnknown, unmodelled
	}
	return worst, unmodelled
}

// PoolChanges lists every node pool the plan creates, destroys, replaces or updates.
func (p *Plan) PoolChanges() []PoolChange {
	var out []PoolChange
	for _, rc := range p.ResourceChanges {
		nameAttr, ok := nodePoolTypes[rc.Type]
		if !ok || rc.Change.IsNoop() {
			continue
		}
		pool := attrString(rc.Change.After, nameAttr)
		if pool == "" {
			pool = attrString(rc.Change.Before, nameAttr)
		}
		attrs := rc.Change.ChangedAttributes()
		effect, unmodelled := effectFor(rc.Type, attrs)
		if rc.Change.IsReplace() || rc.Change.IsDelete() {
			effect = EffectReplaces
		}
		out = append(out, PoolChange{
			Address: rc.Address, Pool: pool, Type: rc.Type, Actions: rc.Change.Actions,
			ActionReason: rc.ActionReason, Effect: effect, Attributes: attrs, Unmodelled: unmodelled,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

// ClusterChanges lists cluster level changes, whose node effect is often not visible in the plan.
func (p *Plan) ClusterChanges() []ClusterChange {
	var out []ClusterChange
	for _, rc := range p.ResourceChanges {
		nameAttr, ok := clusterTypes[rc.Type]
		if !ok || rc.Change.IsNoop() {
			continue
		}
		attrs := rc.Change.ChangedAttributes()
		effect := EffectUnknown
		var unmodelled = attrs
		if rc.Change.IsReplace() || rc.Change.IsDelete() {
			effect = EffectReplaces
			unmodelled = nil
		}
		name := attrString(rc.Change.After, nameAttr)
		if name == "" {
			name = attrString(rc.Change.Before, nameAttr)
		}
		out = append(out, ClusterChange{
			Address: rc.Address, Cluster: name, Type: rc.Type,
			Actions: rc.Change.Actions, Effect: effect, Attributes: attrs, Unmodelled: unmodelled,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

// OtherChanges counts the changes spanline does not map to nodes, so the report can say so.
func (p *Plan) OtherChanges() int {
	n := 0
	for _, rc := range p.ResourceChanges {
		if rc.Change.IsNoop() {
			continue
		}
		if _, ok := nodePoolTypes[rc.Type]; ok {
			continue
		}
		if _, ok := clusterTypes[rc.Type]; ok {
			continue
		}
		n++
	}
	return n
}
