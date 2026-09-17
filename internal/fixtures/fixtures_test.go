package fixtures

import (
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

func TestScenariosAreSortedAndNamedAfterTheirDirectory(t *testing.T) {
	ms, err := Scenarios()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 {
		t.Fatal("no embedded scenario")
	}
	if !sort.SliceIsSorted(ms, func(i, j int) bool { return ms[i].Name < ms[j].Name }) {
		t.Errorf("Scenarios() is not sorted by name: %v", names(ms))
	}
	entries, err := FS.ReadDir("data")
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			dirs[e.Name()] = true
		}
	}
	if len(dirs) != len(ms) {
		t.Errorf("%d scenario directories but %d listed: %v", len(dirs), len(ms), names(ms))
	}
	for _, m := range ms {
		if !dirs[m.Name] {
			t.Errorf("scenario %q is listed under a name that is not its directory, so it cannot be opened by that name", m.Name)
		}
		if got, err := ScenarioMeta(m.Name); err != nil || got.Name != m.Name {
			t.Errorf("ScenarioMeta(%q) = %+v, %v", m.Name, got, err)
		}
		if len(m.Contexts) == 0 || m.Now == "" {
			t.Errorf("scenario %q has no context or no recorded time", m.Name)
		}
	}
}

func names(ms []Meta) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return out
}

func TestScenarioNamesAndPathsCannotEscapeTheirScenario(t *testing.T) {
	if _, err := ReadFile("estate", "terraform/plan.json"); err != nil {
		t.Fatalf("a file inside the scenario is unreadable: %v", err)
	}
	for _, rel := range []string{
		"../node-image-drift/meta.json",
		"terraform/../../node-image-drift/meta.json",
		"/terraform/plan.json",
		"",
		".",
		"..",
	} {
		if b, err := ReadFile("estate", rel); err == nil {
			t.Errorf("ReadFile(estate, %q) read %d bytes, want a refusal", rel, len(b))
		}
	}
	for _, scenario := range []string{
		"estate/../node-image-drift",
		"../data/estate",
		"estate/aks-prod-weu",
		"",
		".",
		"..",
		"/estate",
		`estate\..\x`,
	} {
		if m, err := ScenarioMeta(scenario); err == nil {
			t.Errorf("ScenarioMeta(%q) = %+v, want a refusal", scenario, m)
		}
		if _, err := Sub(scenario); err == nil {
			t.Errorf("Sub(%q) succeeded, want a refusal", scenario)
		}
		if b, err := ReadFile(scenario, "meta.json"); err == nil {
			t.Errorf("ReadFile(%q, meta.json) read %d bytes, want a refusal", scenario, len(b))
		}
	}
}

func TestAMalformedOrMissingMetaIsAnError(t *testing.T) {
	good := &fstest.MapFile{Data: []byte(`{"name":"good","contexts":["c1"],"now":"2026-09-16T03:20:00Z"}`)}
	fsys := fstest.MapFS{
		"data/good/meta.json":   good,
		"data/broken/meta.json": {Data: []byte(`{"name": "broken", "contexts": [`)},
		"data/nometa/c1.json":   {Data: []byte(`{"items":[]}`)},
		"data/stray.txt":        {Data: []byte("not a scenario")},
	}
	if _, err := scenariosIn(fsys); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("scenariosIn with a malformed meta = %v, want an error naming the broken scenario", err)
	}
	if _, err := scenarioMetaIn(fsys, "nometa"); err == nil {
		t.Error("a scenario directory without meta.json was accepted")
	}
	delete(fsys, "data/broken/meta.json")
	delete(fsys, "data/nometa/c1.json")
	ms, err := scenariosIn(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Name != "good" {
		t.Errorf("scenariosIn = %v, want only the good scenario, and never the stray file", names(ms))
	}
}
