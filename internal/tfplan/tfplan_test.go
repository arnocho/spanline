package tfplan

import (
	"strings"
	"testing"
)

func parsePlanT(t *testing.T, src string) *Plan {
	t.Helper()
	p, err := ParsePlan([]byte(src))
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	return p
}

func onePool(t *testing.T, p *Plan) PoolChange {
	t.Helper()
	pcs := p.PoolChanges()
	if len(pcs) != 1 {
		t.Fatalf("PoolChanges = %d entries, want 1: %+v", len(pcs), pcs)
	}
	return pcs[0]
}

func TestParsePlanAcceptsAPlanWithNoResourceChangesKey(t *testing.T) {
	// terraform show -json omits resource_changes for an empty configuration (omitempty),
	// while planned_values is always written. That is a plan with nothing in it, not a bad file.
	p := parsePlanT(t, `{"format_version":"1.2","terraform_version":"1.9.8","planned_values":{}}`)
	if p.ResourceChanges == nil || len(p.ResourceChanges) != 0 {
		t.Errorf("ResourceChanges = %v, want an empty slice", p.ResourceChanges)
	}
	add, change, destroy := p.Summary()
	if add != 0 || change != 0 || destroy != 0 {
		t.Errorf("Summary = %d %d %d, want zeros", add, change, destroy)
	}
}

func TestParsePlanStillRejectsJSONThatIsNotAPlan(t *testing.T) {
	for _, src := range []string{
		`{"format_version":"1.0","values":{"root_module":{}}}`, // terraform show -json of a state
		`{"version":4,"resources":[]}`,                         // a raw state file
		`{}`,
		`null`,
		`[]`,
	} {
		if _, err := ParsePlan([]byte(src)); err == nil {
			t.Errorf("ParsePlan(%s) returned no error", src)
		}
	}
}

func TestReplaceIsReadInEitherActionOrder(t *testing.T) {
	for _, actions := range []string{`["delete","create"]`, `["create","delete"]`} {
		p := parsePlanT(t, `{"format_version":"1.2","resource_changes":[
			{"address":"azurerm_kubernetes_cluster_node_pool.apps","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"apps",
			 "change":{"actions":`+actions+`,"before":{"name":"apps","tags":{"a":"b"}},"after":{"name":"apps","tags":{"a":"b"}},
			 "replace_paths":[["upgrade_settings",0,"max_surge"]]}}]}`)
		pc := onePool(t, p)
		if pc.Effect != EffectReplaces {
			t.Errorf("actions %s: effect = %s, want REPLACES even with only harmless attributes changed", actions, pc.Effect)
		}
		add, change, destroy := p.Summary()
		if add != 1 || destroy != 1 || change != 0 {
			t.Errorf("actions %s: Summary = %d to add, %d to change, %d to destroy, want 1, 0, 1", actions, add, change, destroy)
		}
	}
}

func TestNoopAndEmptyActionsAreLeftOut(t *testing.T) {
	p := parsePlanT(t, `{"format_version":"1.2","resource_changes":[
		{"address":"azurerm_kubernetes_cluster_node_pool.apps","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"apps",
		 "change":{"actions":["no-op"],"before":{"name":"apps"},"after":{"name":"apps"}}},
		{"address":"azurerm_kubernetes_cluster.main","mode":"managed","type":"azurerm_kubernetes_cluster","name":"main",
		 "change":{"actions":[],"before":{"name":"main"},"after":{"name":"main"}}},
		{"address":"azurerm_resource_group.rg","mode":"managed","type":"azurerm_resource_group","name":"rg",
		 "change":{"actions":["no-op"],"before":{"name":"rg"},"after":{"name":"rg"}}}]}`)
	if n := len(p.PoolChanges()); n != 0 {
		t.Errorf("PoolChanges = %d, want 0 for a no-op", n)
	}
	if n := len(p.ClusterChanges()); n != 0 {
		t.Errorf("ClusterChanges = %d, want 0 for empty actions", n)
	}
	if n := p.OtherChanges(); n != 0 {
		t.Errorf("OtherChanges = %d, want 0", n)
	}
}

func TestUnknownAttributeIsNeverNone(t *testing.T) {
	// upgrade_settings is not in the curated table: with node_count it must yield UNKNOWN, not NONE.
	p := parsePlanT(t, `{"format_version":"1.2","resource_changes":[
		{"address":"azurerm_kubernetes_cluster_node_pool.apps","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"apps",
		 "change":{"actions":["update"],
		  "before":{"name":"apps","node_count":3,"upgrade_settings":[{"max_surge":"10%"}]},
		  "after":{"name":"apps","node_count":4,"upgrade_settings":[{"max_surge":"33%"}]}}}]}`)
	pc := onePool(t, p)
	if pc.Effect != EffectUnknown {
		t.Errorf("effect = %s, want UNKNOWN for an attribute the table does not know", pc.Effect)
	}
	if len(pc.Unmodelled) != 1 || pc.Unmodelled[0] != "upgrade_settings" {
		t.Errorf("unmodelled = %v, want [upgrade_settings]", pc.Unmodelled)
	}
	if strings.Join(pc.Attributes, ",") != "node_count,upgrade_settings" {
		t.Errorf("attributes = %v, want the sorted changed attributes", pc.Attributes)
	}
}

func TestOnlyHarmlessAttributesGiveNone(t *testing.T) {
	p := parsePlanT(t, `{"format_version":"1.2","resource_changes":[
		{"address":"azurerm_kubernetes_cluster_node_pool.apps","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"apps",
		 "change":{"actions":["update"],
		  "before":{"name":"apps","node_count":3,"tags":{"a":"1"}},
		  "after":{"name":"apps","node_count":6,"tags":{"a":"2"}}}}]}`)
	pc := onePool(t, p)
	if pc.Effect != EffectNone {
		t.Errorf("effect = %s, want NONE when every changed attribute is known harmless", pc.Effect)
	}
	if len(pc.Lowered) != 0 {
		t.Errorf("a scale up lowered %v, want nothing", pc.Lowered)
	}
}

func TestUnknownResourceTypeIsUnknown(t *testing.T) {
	p := parsePlanT(t, `{"format_version":"1.2","resource_changes":[
		{"address":"scaleway_k8s_pool.apps","mode":"managed","type":"scaleway_k8s_pool","name":"apps",
		 "change":{"actions":["update"],"before":{"name":"apps","size":2},"after":{"name":"apps","size":3}}}]}`)
	pc := onePool(t, p)
	if pc.Effect != EffectUnknown {
		t.Errorf("effect = %s, want UNKNOWN for a type with no table", pc.Effect)
	}
}

func TestLoweredCountsAreReported(t *testing.T) {
	cases := []struct {
		name, src, attr string
		before, after   int64
	}{
		{"azure node_count", `{"address":"azurerm_kubernetes_cluster_node_pool.apps","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"apps",
			"change":{"actions":["update"],"before":{"name":"apps","node_count":6},"after":{"name":"apps","node_count":3}}}`,
			"node_count", 6, 3},
		{"azure max_count", `{"address":"azurerm_kubernetes_cluster_node_pool.apps","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"apps",
			"change":{"actions":["update"],"before":{"name":"apps","max_count":10},"after":{"name":"apps","max_count":2}}}`,
			"max_count", 10, 2},
		{"aws desired_size", `{"address":"aws_eks_node_group.apps","mode":"managed","type":"aws_eks_node_group","name":"apps",
			"change":{"actions":["update"],
			 "before":{"node_group_name":"apps","scaling_config":[{"desired_size":5,"max_size":8,"min_size":1}]},
			 "after":{"node_group_name":"apps","scaling_config":[{"desired_size":2,"max_size":8,"min_size":1}]}}}`,
			"scaling_config.desired_size", 5, 2},
		{"gke node_count", `{"address":"google_container_node_pool.apps","mode":"managed","type":"google_container_node_pool","name":"apps",
			"change":{"actions":["update"],"before":{"name":"apps","node_count":2},"after":{"name":"apps","node_count":1}}}`,
			"node_count", 2, 1},
	}
	for _, c := range cases {
		p := parsePlanT(t, `{"format_version":"1.2","resource_changes":[`+c.src+`]}`)
		pc := onePool(t, p)
		if len(pc.Lowered) != 1 {
			t.Errorf("%s: lowered = %+v, want one entry", c.name, pc.Lowered)
			continue
		}
		got := pc.Lowered[0]
		if got.Attribute != c.attr || got.Before != c.before || got.After != c.after {
			t.Errorf("%s: lowered = %+v, want %s %d to %d", c.name, got, c.attr, c.before, c.after)
		}
		if pc.Effect != EffectNone {
			t.Errorf("%s: effect = %s, want NONE (the count change itself is not a rotation)", c.name, pc.Effect)
		}
	}
}

func TestLoweredCountIgnoresUnknownAndNullValues(t *testing.T) {
	p := parsePlanT(t, `{"format_version":"1.2","resource_changes":[
		{"address":"azurerm_kubernetes_cluster_node_pool.apps","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"apps",
		 "change":{"actions":["update"],"before":{"name":"apps","node_count":6,"max_count":null},"after":{"name":"apps","max_count":null},
		 "after_unknown":{"node_count":true}}}]}`)
	pc := onePool(t, p)
	if len(pc.Lowered) != 0 {
		t.Errorf("lowered = %+v, want nothing when the after value is unknown", pc.Lowered)
	}
}

func TestPureCreateMovesNoNode(t *testing.T) {
	// Every after attribute of a create counts as changed; vm_size would read as REPLACES.
	p := parsePlanT(t, `{"format_version":"1.2","resource_changes":[
		{"address":"azurerm_kubernetes_cluster_node_pool.batch","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"batch",
		 "change":{"actions":["create"],"before":null,"after":{"name":"batch","vm_size":"Standard_D4s_v5","node_count":2}}},
		{"address":"azurerm_kubernetes_cluster.new","mode":"managed","type":"azurerm_kubernetes_cluster","name":"new",
		 "change":{"actions":["create"],"before":null,"after":{"name":"aks-new","kubernetes_version":"1.30"}}}]}`)
	pc := onePool(t, p)
	if pc.Effect != EffectNone {
		t.Errorf("pool create effect = %s, want NONE: a create destroys nothing", pc.Effect)
	}
	if pc.Pool != "batch" {
		t.Errorf("pool = %q, want batch read from after", pc.Pool)
	}
	ccs := p.ClusterChanges()
	if len(ccs) != 1 || ccs[0].Effect != EffectNone || ccs[0].Cluster != "aks-new" {
		t.Errorf("cluster create = %+v, want NONE on aks-new", ccs)
	}
	add, change, destroy := p.Summary()
	if add != 2 || change != 0 || destroy != 0 {
		t.Errorf("Summary = %d %d %d, want 2 to add", add, change, destroy)
	}
}

func TestDataSourcesAreNotChanges(t *testing.T) {
	// A deferred data read of a node pool type carries every attribute in after and no before.
	p := parsePlanT(t, `{"format_version":"1.2","resource_changes":[
		{"address":"data.azurerm_kubernetes_cluster_node_pool.apps","mode":"data","type":"azurerm_kubernetes_cluster_node_pool","name":"apps",
		 "change":{"actions":["read"],"before":null,"after":{"name":"apps","vm_size":"Standard_D8s_v5"}}},
		{"address":"data.azurerm_kubernetes_cluster.main","mode":"data","type":"azurerm_kubernetes_cluster","name":"main",
		 "change":{"actions":["read"],"before":null,"after":{"name":"aks-prod-weu"}}},
		{"address":"data.azurerm_client_config.current","mode":"data","type":"azurerm_client_config","name":"current",
		 "change":{"actions":["read"],"before":null,"after":{}}},
		{"address":"azurerm_public_ip.egress","mode":"managed","type":"azurerm_public_ip","name":"egress",
		 "change":{"actions":["create"],"before":null,"after":{"name":"pip"}}}]}`)
	if n := len(p.PoolChanges()); n != 0 {
		t.Errorf("PoolChanges = %d, want 0: a data read moves no node", n)
	}
	if n := len(p.ClusterChanges()); n != 0 {
		t.Errorf("ClusterChanges = %d, want 0 for a data read", n)
	}
	if n := p.OtherChanges(); n != 1 {
		t.Errorf("OtherChanges = %d, want 1: only the public ip changes", n)
	}
	add, change, destroy := p.Summary()
	if add != 1 || change != 0 || destroy != 0 {
		t.Errorf("Summary = %d %d %d, want 1 to add, as terraform prints it", add, change, destroy)
	}
}

func TestModuleAddressesPassThroughAndClusterIsRead(t *testing.T) {
	p := parsePlanT(t, `{"format_version":"1.2","resource_changes":[
		{"address":"module.aks.azurerm_kubernetes_cluster_node_pool.apps","module_address":"module.aks","mode":"managed",
		 "type":"azurerm_kubernetes_cluster_node_pool","name":"apps",
		 "change":{"actions":["update"],
		  "before":{"name":"apps","kubernetes_cluster_id":"/subscriptions/s/resourceGroups/rg/providers/Microsoft.ContainerService/managedClusters/aks-prod-weu","node_count":3},
		  "after":{"name":"apps","kubernetes_cluster_id":"/subscriptions/s/resourceGroups/rg/providers/Microsoft.ContainerService/managedClusters/aks-prod-weu","node_count":4}}}]}`)
	pc := onePool(t, p)
	if pc.Address != "module.aks.azurerm_kubernetes_cluster_node_pool.apps" {
		t.Errorf("address = %q, want the module address as the plan writes it", pc.Address)
	}
	if pc.Cluster != "aks-prod-weu" || pc.ClusterAttribute != "kubernetes_cluster_id" {
		t.Errorf("cluster = %q from %q, want aks-prod-weu from kubernetes_cluster_id", pc.Cluster, pc.ClusterAttribute)
	}
}

func TestChangedAttributesReadNestedDiffsAtTheTopLevel(t *testing.T) {
	c := Change{
		Actions: []string{"update"},
		Before:  map[string]any{"a": 1.0, "nested": []any{map[string]any{"x": "1"}}, "gone": "v", "same": "s"},
		After:   map[string]any{"a": 1.0, "nested": []any{map[string]any{"x": "2"}}, "added": "w", "same": "s"},
	}
	if got := strings.Join(c.ChangedAttributes(), ","); got != "added,gone,nested" {
		t.Errorf("ChangedAttributes = %q, want added,gone,nested", got)
	}
}

func TestParseStateSkipsDataAndNamesInstances(t *testing.T) {
	s, err := ParseState([]byte(`{"version":4,"terraform_version":"1.9.8","serial":3,"lineage":"l","resources":[
		{"mode":"data","type":"azurerm_kubernetes_cluster_node_pool","name":"seen","provider":"p",
		 "instances":[{"attributes":{"name":"seen-only"}}]},
		{"module":"module.aks","mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"pools","provider":"p",
		 "instances":[{"index_key":"apps","attributes":{"name":"apps"}},{"index_key":"data","attributes":{"name":"data"}}]},
		{"mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"counted","provider":"p",
		 "instances":[{"index_key":0,"attributes":{"name":"batch"}},{"index_key":1,"attributes":{}},{"index_key":2}]},
		{"mode":"managed","type":"azurerm_kubernetes_cluster_node_pool","name":"empty","provider":"p","instances":[]},
		{"module":"module.aks","mode":"managed","type":"azurerm_kubernetes_cluster","name":"main","provider":"p",
		 "instances":[{"attributes":{"name":"aks-prod-weu"}}]},
		{"mode":"managed","type":"helm_release","name":"payments","provider":"p",
		 "instances":[{"attributes":{"name":"payments","namespace":"payments"}}]}]}`))
	if err != nil {
		t.Fatalf("ParseState: %v", err)
	}
	pools := s.NodePools()
	got := make([]string, 0, len(pools))
	for _, o := range pools {
		got = append(got, o.Name+"="+o.Address)
	}
	want := []string{
		"apps=module.aks.azurerm_kubernetes_cluster_node_pool.pools[\"apps\"]",
		"batch=azurerm_kubernetes_cluster_node_pool.counted[0]",
		"data=module.aks.azurerm_kubernetes_cluster_node_pool.pools[\"data\"]",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("NodePools:\n got %v\nwant %v", got, want)
	}
	clusters := s.Clusters()
	if len(clusters) != 1 || clusters[0].Address != "module.aks.azurerm_kubernetes_cluster.main" {
		t.Errorf("Clusters = %+v, want the module qualified address", clusters)
	}
	if n := s.CountResources(); n != 7 {
		t.Errorf("CountResources = %d, want 7 managed instances", n)
	}
	if n := len(s.HelmReleases()); n != 1 || s.HelmReleases()[0].Name != "payments/payments" {
		t.Errorf("HelmReleases = %+v", s.HelmReleases())
	}
}

func TestParseStateRejectsNonState(t *testing.T) {
	if _, err := ParseState([]byte(`{"format_version":"1.2","resource_changes":[]}`)); err == nil {
		t.Error("a plan parsed as a state")
	}
	if _, err := ParseState([]byte(`not json`)); err == nil {
		t.Error("garbage parsed as a state")
	}
	s, err := ParseState([]byte(`{"version":4,"resources":[]}`))
	if err != nil || len(s.NodePools()) != 0 {
		t.Errorf("an empty state should parse: %v %+v", err, s)
	}
}
