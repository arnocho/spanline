package collect

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixtureSnapshotRefusesAContextItDoesNotRecord(t *testing.T) {
	src, err := NewFixtureSource("estate")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"aks-prod-weu/../onprem-int", // cleans to a sibling context: a traversal
		"../estate/aks-prod-weu",
		"does-not-exist",
		"terraform", // a directory of the scenario that is not a context
		".",
	} {
		snap, err := src.Snapshot(context.Background(), name, "")
		if err == nil {
			t.Errorf("Snapshot(%q) = %d nodes, %d gaps, nil error; want a refusal", name, len(snap.Nodes), len(snap.Gaps))
		}
	}

	snap, err := src.Snapshot(context.Background(), "", "")
	if err != nil || snap.Context != "aks-prod-weu" {
		t.Fatalf("Snapshot(\"\") = %v, %v, want the first recorded context", snap, err)
	}
	snap, err = src.Snapshot(context.Background(), "onprem-int", "")
	if err != nil || len(snap.Nodes) == 0 {
		t.Fatalf("Snapshot(onprem-int) = %v, want the recorded nodes", err)
	}
}

func writeScenario(t *testing.T, meta string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestFixtureDirRecordsAMissingResourceAsAGap(t *testing.T) {
	dir := writeScenario(t, `{"name":"hand","contexts":["c1"],"now":"2026-09-16T03:20:00Z"}`, map[string]string{
		"c1/pods.json":  `{"items":[{"metadata":{"name":"a-1","namespace":"a"}},{"metadata":{"name":"b-1","namespace":"b"}}]}`,
		"c1/nodes.json": `{"items":[{"metadata":{"name":"node-1"}}]}`,
	})
	src, err := NewFixtureDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := src.Snapshot(context.Background(), "c1", "")
	if err != nil {
		t.Fatalf("Snapshot returned %v", err)
	}
	if len(snap.Pods) != 2 || len(snap.Nodes) != 1 {
		t.Errorf("pods %d nodes %d, want 2 and 1", len(snap.Pods), len(snap.Nodes))
	}
	if len(snap.Gaps) != len(resources)-2 {
		t.Errorf("gaps = %d, want one per missing resource file (%d)", len(snap.Gaps), len(resources)-2)
	}
	for _, g := range snap.Gaps {
		if g.Resource == "pods" || g.Resource == "nodes" {
			t.Errorf("a recorded resource was reported as a gap: %+v", g)
		}
		if g.Reason != "not recorded in this scenario" {
			t.Errorf("gap %+v, want the not recorded reason", g)
		}
	}

	// A namespace filter keeps cluster scoped objects.
	snap, err = src.Snapshot(context.Background(), "c1", "b")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Pods) != 1 || snap.Pods[0].Metadata.Namespace != "b" {
		t.Errorf("filtered pods = %+v, want only namespace b", snap.Pods)
	}
	if len(snap.Nodes) != 1 {
		t.Errorf("the namespace filter dropped the nodes, which are cluster scoped")
	}
}

func TestFixtureDirWithoutAUsableContextIsRefused(t *testing.T) {
	for _, meta := range []string{
		`{"name":"empty","contexts":[],"now":"2026-09-16T03:20:00Z"}`,
		`{"name":"none","now":"2026-09-16T03:20:00Z"}`,
		`{"name":"escape","contexts":["../other"],"now":"2026-09-16T03:20:00Z"}`,
		`{"name":"absolute","contexts":["/etc"],"now":"2026-09-16T03:20:00Z"}`,
	} {
		dir := writeScenario(t, meta, nil)
		if _, err := NewFixtureDir(dir); err == nil {
			t.Errorf("NewFixtureDir accepted meta %s", meta)
		} else if !strings.Contains(err.Error(), "context") {
			t.Errorf("NewFixtureDir(%s) = %v, want an error about the contexts", meta, err)
		}
	}
}

func TestFixtureNamespaceFilterMatchesTheLiveSource(t *testing.T) {
	src, err := NewFixtureSource("estate")
	if err != nil {
		t.Fatal(err)
	}
	all, err := src.Snapshot(context.Background(), "aks-prod-weu", "")
	if err != nil {
		t.Fatal(err)
	}
	ns, err := src.Snapshot(context.Background(), "aks-prod-weu", "payments")
	if err != nil {
		t.Fatal(err)
	}
	if len(ns.Nodes) != len(all.Nodes) || len(ns.PVs) != len(all.PVs) {
		t.Errorf("cluster scoped objects changed under a namespace filter: nodes %d/%d, pvs %d/%d",
			len(ns.Nodes), len(all.Nodes), len(ns.PVs), len(all.PVs))
	}
	if len(all.ArgoApps) == 0 {
		t.Fatal("the estate scenario records no Argo application, so this test proves nothing")
	}
	// Argo applications live in their own namespace and point at the workload's: the live source
	// reads them across namespaces, and the filter must keep them too.
	if len(ns.ArgoApps) != len(all.ArgoApps) {
		t.Errorf("argo apps = %d under the filter, %d without: attribution would silently vanish", len(ns.ArgoApps), len(all.ArgoApps))
	}
	if len(ns.Pods) == 0 || len(ns.Pods) >= len(all.Pods) {
		t.Errorf("pods = %d under the filter, %d without, want a strict subset", len(ns.Pods), len(all.Pods))
	}
	for _, p := range ns.Pods {
		if p.Metadata.Namespace != "payments" {
			t.Errorf("pod %s/%s survived the payments filter", p.Metadata.Namespace, p.Metadata.Name)
		}
	}
}
