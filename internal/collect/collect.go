// Package collect reads a cluster. It has exactly two sources: recorded fixtures,
// and kubectl restricted to read-only subcommands. Nothing here ever mutates anything.
package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/arnocho/spanline/internal/fixtures"
	"github.com/arnocho/spanline/internal/model"
)

// Source is where a snapshot comes from.
type Source interface {
	// Contexts lists the kube contexts this source can read.
	Contexts() ([]string, error)
	// Snapshot reads one context. An empty namespace means every readable namespace.
	Snapshot(ctx context.Context, kubeContext, namespace string) (*model.Snapshot, error)
	// Describe says, in one line, exactly what the source is, for the run banner.
	Describe() string
	// Now is the reference time: the fixture's recorded time, or the wall clock.
	Now() time.Time
	// Extra reads a non-Kubernetes file that travels with the source, such as a Terraform plan.
	Extra(rel string) ([]byte, error)
}

// resources are the object kinds a snapshot is made of, and the file names fixtures use.
var resources = []string{
	"nodes", "pods", "replicasets", "controllerrevisions", "deployments",
	"statefulsets", "daemonsets", "events", "pdbs", "pvcs", "pvs", "argoapps",
}

type list struct {
	Items []json.RawMessage `json:"items"`
}

func decodeInto(raw []byte, target any) error {
	var l list
	if err := json.Unmarshal(raw, &l); err != nil {
		return err
	}
	items, err := json.Marshal(l.Items)
	if err != nil {
		return err
	}
	return json.Unmarshal(items, target)
}

func assign(snap *model.Snapshot, resource string, raw []byte) error {
	switch resource {
	case "nodes":
		return decodeInto(raw, &snap.Nodes)
	case "pods":
		return decodeInto(raw, &snap.Pods)
	case "replicasets":
		return decodeInto(raw, &snap.ReplicaSets)
	case "controllerrevisions":
		return decodeInto(raw, &snap.ControllerRevisions)
	case "deployments":
		return decodeInto(raw, &snap.Deployments)
	case "statefulsets":
		return decodeInto(raw, &snap.StatefulSets)
	case "daemonsets":
		return decodeInto(raw, &snap.DaemonSets)
	case "events":
		return decodeInto(raw, &snap.Events)
	case "pdbs":
		return decodeInto(raw, &snap.PDBs)
	case "pvcs":
		return decodeInto(raw, &snap.PVCs)
	case "pvs":
		return decodeInto(raw, &snap.PVs)
	case "argoapps":
		return decodeInto(raw, &snap.ArgoApps)
	}
	return fmt.Errorf("unknown resource %q", resource)
}

func setKinds(snap *model.Snapshot) {
	for i := range snap.Deployments {
		snap.Deployments[i].Kind = "Deployment"
	}
	for i := range snap.StatefulSets {
		snap.StatefulSets[i].Kind = "StatefulSet"
	}
	for i := range snap.DaemonSets {
		snap.DaemonSets[i].Kind = "DaemonSet"
	}
}

// FixtureSource replays recorded JSON: embedded scenarios, or a directory on disk.
type FixtureSource struct {
	root     fs.FS
	scenario string
	meta     fixtures.Meta
	now      time.Time
}

// NewFixtureSource opens one of the embedded scenarios by name.
func NewFixtureSource(scenario string) (*FixtureSource, error) {
	meta, err := fixtures.ScenarioMeta(scenario)
	if err != nil {
		return nil, err
	}
	sub, err := fixtures.Sub(scenario)
	if err != nil {
		return nil, err
	}
	return newFixtureSource(sub, scenario, meta)
}

// NewFixtureDir opens a scenario directory on disk, for fixtures recorded by hand.
func NewFixtureDir(dir string) (*FixtureSource, error) {
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, fmt.Errorf("fixture directory %q has no meta.json: %w", dir, err)
	}
	var meta fixtures.Meta
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, err
	}
	return newFixtureSource(os.DirFS(dir), filepath.Base(dir), meta)
}

func newFixtureSource(root fs.FS, scenario string, meta fixtures.Meta) (*FixtureSource, error) {
	now, err := time.Parse(time.RFC3339, meta.Now)
	if err != nil {
		return nil, fmt.Errorf("scenario %q has an unreadable now field: %w", scenario, err)
	}
	return &FixtureSource{root: root, scenario: scenario, meta: meta, now: now}, nil
}

// Meta exposes the scenario description, so commands can print what they are replaying.
func (f *FixtureSource) Meta() fixtures.Meta { return f.meta }

func (f *FixtureSource) Contexts() ([]string, error) { return f.meta.Contexts, nil }

func (f *FixtureSource) Describe() string {
	return fmt.Sprintf("fixtures: %s (recorded, no cluster contacted)", f.scenario)
}

func (f *FixtureSource) Now() time.Time { return f.now }

func (f *FixtureSource) Extra(rel string) ([]byte, error) {
	return fs.ReadFile(f.root, rel)
}

func (f *FixtureSource) Snapshot(_ context.Context, kubeContext, namespace string) (*model.Snapshot, error) {
	if kubeContext == "" && len(f.meta.Contexts) > 0 {
		kubeContext = f.meta.Contexts[0]
	}
	snap := &model.Snapshot{Context: kubeContext, CollectedAt: f.now, Namespace: namespace}
	for _, r := range resources {
		raw, err := fs.ReadFile(f.root, filepath.Join(kubeContext, r+".json"))
		if err != nil {
			snap.Gaps = append(snap.Gaps, model.CoverageGap{Resource: r, Reason: "not recorded in this scenario"})
			continue
		}
		if err := assign(snap, r, raw); err != nil {
			return nil, fmt.Errorf("scenario %s, %s: %w", f.scenario, r, err)
		}
	}
	setKinds(snap)
	if namespace != "" {
		filterNamespace(snap, namespace)
	}
	return snap, nil
}

func filterNamespace(snap *model.Snapshot, ns string) {
	snap.Pods = filter(snap.Pods, func(p model.Pod) bool { return p.Metadata.Namespace == ns })
	snap.ReplicaSets = filter(snap.ReplicaSets, func(r model.ReplicaSet) bool { return r.Metadata.Namespace == ns })
	snap.ControllerRevisions = filter(snap.ControllerRevisions, func(c model.ControllerRevision) bool {
		return c.Metadata.Namespace == ns
	})
	snap.Deployments = filter(snap.Deployments, func(w model.Workload) bool { return w.Metadata.Namespace == ns })
	snap.StatefulSets = filter(snap.StatefulSets, func(w model.Workload) bool { return w.Metadata.Namespace == ns })
	snap.DaemonSets = filter(snap.DaemonSets, func(w model.Workload) bool { return w.Metadata.Namespace == ns })
	snap.Events = filter(snap.Events, func(e model.Event) bool { return e.Metadata.Namespace == ns })
	snap.PDBs = filter(snap.PDBs, func(p model.PodDisruptionBudget) bool { return p.Metadata.Namespace == ns })
	snap.PVCs = filter(snap.PVCs, func(p model.PersistentVolumeClaim) bool { return p.Metadata.Namespace == ns })
}

func filter[T any](in []T, keep func(T) bool) []T {
	out := in[:0]
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}
