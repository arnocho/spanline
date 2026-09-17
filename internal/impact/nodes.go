package impact

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
)

// selectorForms is the one place the accepted selector shapes are written, so every error
// message offers the same list.
const selectorForms = "pool=<name>, zone=<zone>, node=<name1,name2>, label=<key>=<value>, " +
	"or a comma separated list of node names"

// selection is a parsed node selector.
type selection struct {
	form  string
	key   string
	value string
	names []string
	text  string
}

// parseSelector reads one of the five accepted forms, and refuses anything else by name.
func parseSelector(s string) (selection, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return selection{}, fmt.Errorf("impact: an empty selector selects no node: use %s", selectorForms)
	}
	switch {
	case strings.HasPrefix(t, "pool="):
		v := strings.TrimSpace(strings.TrimPrefix(t, "pool="))
		if v == "" {
			return selection{}, fmt.Errorf("impact: selector %q names no pool: write pool=<name>, for example pool=apps", s)
		}
		return selection{form: "pool", value: v, text: t}, nil

	case strings.HasPrefix(t, "zone="):
		v := strings.TrimSpace(strings.TrimPrefix(t, "zone="))
		if v == "" {
			return selection{}, fmt.Errorf("impact: selector %q names no zone: write zone=<zone>, for example zone=2", s)
		}
		return selection{form: "zone", value: v, text: t}, nil

	case strings.HasPrefix(t, "node="):
		names := splitNames(strings.TrimPrefix(t, "node="))
		if len(names) == 0 {
			return selection{}, fmt.Errorf("impact: selector %q names no node: write node=<name1,name2>", s)
		}
		return selection{form: "node", names: names, text: t}, nil

	case strings.HasPrefix(t, "label="):
		k, v, ok := strings.Cut(strings.TrimPrefix(t, "label="), "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return selection{}, fmt.Errorf("impact: selector %q is not a label match: write label=<key>=<value>, "+
				"for example label=topology.kubernetes.io/zone=2", s)
		}
		return selection{form: "label", key: k, value: v, text: t}, nil

	default:
		if strings.Contains(t, "=") {
			return selection{}, fmt.Errorf("impact: selector %q is not a form spanline understands: use %s", s, selectorForms)
		}
		names := splitNames(t)
		if len(names) == 0 {
			return selection{}, fmt.Errorf("impact: selector %q names no node: use %s", s, selectorForms)
		}
		return selection{form: "names", names: names, text: t}, nil
	}
}

// splitNames reads a comma separated list, dropping blanks and repeats, so a name written twice
// is looked up once and reported once.
func splitNames(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// match returns the nodes this selector names, and the names it found nothing for.
func (s selection) match(snap *model.Snapshot) ([]model.Node, []string) {
	var matched []model.Node
	var missing []string

	switch s.form {
	case "pool":
		for _, n := range snap.Nodes {
			if _, v, ok := nodePool(n); ok && v == s.value {
				matched = append(matched, n)
			}
		}
	case "zone":
		for _, n := range snap.Nodes {
			if _, v, ok := nodeZone(n); ok && v == s.value {
				matched = append(matched, n)
			}
		}
	case "label":
		for _, n := range snap.Nodes {
			if v, ok := n.Metadata.Labels[s.key]; ok && v == s.value {
				matched = append(matched, n)
			}
		}
	case "node", "names":
		byName := snap.NodeByName()
		for _, want := range s.names {
			if n, ok := byName[want]; ok {
				matched = append(matched, n)
			} else {
				missing = append(missing, want)
			}
		}
	}

	sort.Slice(matched, func(i, j int) bool { return matched[i].Metadata.Name < matched[j].Metadata.Name })
	sort.Strings(missing)
	return matched, missing
}

// describe says which node fields the selector was compared against, for the evidence lines.
func (s selection) describe() string {
	switch s.form {
	case "pool":
		return "metadata.labels, first of " + strings.Join(poolLabelKeys, ", ") + ", equal to " + s.value
	case "zone":
		return "metadata.labels, first of " + strings.Join(zoneLabelKeys, ", ") + ", equal to " + s.value
	case "label":
		return "metadata.labels[" + s.key + "] equal to " + s.value
	default:
		return "metadata.name in " + strings.Join(s.names, ", ")
	}
}

// Nodes answers: if the nodes this selector names go away, what breaks.
// Nothing is cordoned, drained or evicted to find out.
func Nodes(snap *model.Snapshot, selector string, o Options) (*result.ImpactReport, error) {
	if snap == nil {
		return nil, errors.New("impact: there is no snapshot to analyse")
	}
	sel, err := parseSelector(selector)
	if err != nil {
		return nil, err
	}

	a := newAnalysis(snap)
	matched, missing := sel.match(snap)

	for _, name := range missing {
		a.add(result.NotAssessed, "Node", name,
			"this node name is not in the snapshot, so spanline cannot say what runs on it or what losing it would break",
			[]string{
				"metadata.name: no node with this name was collected in context " + snap.Context,
				fmt.Sprintf("nodes in the snapshot: %d", len(snap.Nodes)),
			})
	}

	if len(matched) == 0 {
		a.add(result.NotAssessed, "Selector", sel.text,
			"this selector matches no node in the snapshot, so nothing was simulated, and that is not a pass",
			[]string{
				fmt.Sprintf("matched 0 of %d nodes in context %s", len(snap.Nodes), snap.Context),
				"compared against " + sel.describe(),
				"accepted forms: " + selectorForms,
			})
		return a.report("nodes "+sel.text, o), nil
	}

	a.doom(matched)
	a.run()
	a.summarise(sel.text)
	return a.report("nodes "+sel.text, o), nil
}
