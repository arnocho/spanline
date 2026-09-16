package cohort

import (
	"fmt"
	"sort"
	"strings"

	"github.com/arnocho/spanline/internal/result"
)

// dimension is one attribute compared across the two cohorts. A dimension either reads
// the pod, or the node the pod runs on: node dimensions are unresolved when the node
// itself was not collected.
type dimension struct {
	name  string
	node  bool
	value func(p podFacts) (string, bool)
}

// dimResult is one report row plus what the change ranking needs from it.
type dimResult struct {
	result.Dimension
	order         int
	node          bool
	discriminator string
	missingSide   string
}

// node pool labels, in the order they are trusted.
var poolLabels = []string{
	"kubernetes.azure.com/agentpool",
	"agentpool",
	"cloud.google.com/gke-nodepool",
	"eks.amazonaws.com/nodegroup",
}

const (
	nodeImageLabel = "kubernetes.azure.com/node-image-version"
	zoneLabel      = "topology.kubernetes.io/zone"
	rolePrefix     = "node-role.kubernetes.io/"
)

// dimensionSet is the fixed comparison order. The order is the tie break when two
// dimensions separate the cohorts equally well.
func dimensionSet(hashKey string) []dimension {
	return []dimension{
		{name: hashKey, value: func(p podFacts) (string, bool) {
			v := p.pod.Metadata.Labels[hashKey]
			return v, v != ""
		}},
		{name: "imageID", value: imageIDValue},
		{name: "node name", node: true, value: func(p podFacts) (string, bool) {
			v := p.pod.Spec.NodeName
			return v, v != ""
		}},
		{name: "node pool", node: true, value: nodePoolValue},
		{name: "node image version", node: true, value: nodeLabel(nodeImageLabel)},
		{name: "osImage", node: true, value: func(p podFacts) (string, bool) {
			v := p.node.Status.NodeInfo.OSImage
			return v, p.hasNode && v != ""
		}},
		{name: "kernelVersion", node: true, value: func(p podFacts) (string, bool) {
			v := p.node.Status.NodeInfo.KernelVersion
			return v, p.hasNode && v != ""
		}},
		{name: "kubeletVersion", node: true, value: func(p podFacts) (string, bool) {
			v := p.node.Status.NodeInfo.KubeletVersion
			return v, p.hasNode && v != ""
		}},
		{name: "containerRuntimeVersion", node: true, value: func(p podFacts) (string, bool) {
			v := p.node.Status.NodeInfo.ContainerRuntimeVersion
			return v, p.hasNode && v != ""
		}},
		{name: "zone", node: true, value: nodeLabel(zoneLabel)},
		{name: annotationChecksum, value: func(p podFacts) (string, bool) {
			v := p.pod.Metadata.Annotations[annotationChecksum]
			return v, v != ""
		}},
	}
}

func nodeLabel(key string) func(podFacts) (string, bool) {
	return func(p podFacts) (string, bool) {
		if !p.hasNode {
			return "", false
		}
		v := p.node.Metadata.Labels[key]
		return v, v != ""
	}
}

func imageIDValue(p podFacts) (string, bool) {
	var ids []string
	for _, cs := range p.pod.Status.ContainerStatuses {
		if cs.ImageID != "" {
			ids = append(ids, cs.ImageID)
		}
	}
	if len(ids) == 0 {
		return "", false
	}
	sort.Strings(ids)
	return strings.Join(ids, ","), true
}

func nodePoolValue(p podFacts) (string, bool) {
	if !p.hasNode {
		return "", false
	}
	labels := p.node.Metadata.Labels
	for _, k := range poolLabels {
		if v := labels[k]; v != "" {
			return v, true
		}
	}
	var roles []string
	for k := range labels {
		if strings.HasPrefix(k, rolePrefix) {
			roles = append(roles, strings.TrimPrefix(k, rolePrefix))
		}
	}
	if len(roles) == 0 {
		return "", false
	}
	sort.Strings(roles)
	return strings.Join(roles, ","), true
}

// computeDimensions fills the report rows, sorted by separation, then purity, then the
// fixed dimension order.
func (a *analysis) computeDimensions() {
	set := dimensionSet(a.hashKey)
	out := make([]dimResult, 0, len(set))
	for i, d := range set {
		failing, missingFailing := tally(a.failing, d)
		healthy, missingHealthy := tally(a.healthy, d)

		row := dimResult{order: i, node: d.node}
		row.Name = d.name
		row.FailingValues = renderValues(failing, missingFailing)
		row.HealthyValues = renderValues(healthy, missingHealthy)

		switch {
		case missingFailing > 0 || missingHealthy > 0:
			row.Separation = result.Unresolved
			row.missingSide = missingSide(missingFailing, missingHealthy)
		case sameKeys(failing, healthy):
			row.Separation = result.None
		case disjoint(failing, healthy):
			row.Separation = result.Total
		default:
			row.Separation = result.Partial
		}

		if row.Separation == result.Total || row.Separation == result.Partial {
			value, count := discriminating(failing, healthy)
			row.discriminator = value
			if len(a.failing) > 0 {
				row.Purity = float64(count) / float64(len(a.failing))
			}
		}
		out = append(out, row)
	}

	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := separationRank(out[i].Separation), separationRank(out[j].Separation)
		if ri != rj {
			return ri > rj
		}
		if out[i].Purity != out[j].Purity {
			return out[i].Purity > out[j].Purity
		}
		return out[i].order < out[j].order
	})
	a.dims = out
}

func separationRank(s result.Separation) int {
	switch s {
	case result.Total:
		return 3
	case result.Partial:
		return 2
	case result.Unresolved:
		return 1
	default:
		return 0
	}
}

func tally(pods []podFacts, d dimension) (map[string]int, int) {
	counts := map[string]int{}
	missing := 0
	for _, p := range pods {
		v, ok := d.value(p)
		if !ok {
			missing++
			continue
		}
		counts[v]++
	}
	return counts, missing
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sameKeys(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for _, k := range sortedKeys(a) {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func disjoint(a, b map[string]int) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	for _, k := range sortedKeys(a) {
		if _, ok := b[k]; ok {
			return false
		}
	}
	return true
}

// discriminating returns the failing value that never appears on the healthy side and
// covers the most failing pods. Ties go to the lexicographically smaller value.
func discriminating(failing, healthy map[string]int) (string, int) {
	best, bestCount := "", 0
	for _, k := range sortedKeys(failing) {
		if _, shared := healthy[k]; shared {
			continue
		}
		if failing[k] > bestCount {
			best, bestCount = k, failing[k]
		}
	}
	return best, bestCount
}

func missingSide(failing, healthy int) string {
	switch {
	case failing > 0 && healthy > 0:
		return "both sides"
	case failing > 0:
		return "the failing side"
	default:
		return "the healthy side"
	}
}

// renderValues prints a cohort's value set compactly, and never hides an absent value.
func renderValues(counts map[string]int, missing int) string {
	keys := sortedKeys(counts)
	var parts []string
	const shown = 3
	if len(keys) > shown {
		parts = append(parts, keys[:shown]...)
		parts = append(parts, fmt.Sprintf("+%d more", len(keys)-shown))
	} else {
		parts = append(parts, keys...)
	}
	if missing > 0 {
		parts = append(parts, fmt.Sprintf("absent on %d pod(s)", missing))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}
