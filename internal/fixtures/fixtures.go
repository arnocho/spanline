// Package fixtures embeds the mock cluster and Terraform data spanline ships with,
// so `spanline demo` works with no cluster, no network and no files on disk.
package fixtures

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed all:data
var FS embed.FS

// Meta describes one scenario, read from its meta.json.
type Meta struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Contexts    []string `json:"contexts"`
	Namespace   string   `json:"namespace,omitempty"`
	Workload    string   `json:"workload,omitempty"`
	Now         string   `json:"now"`
	TFState     string   `json:"tfstate,omitempty"`
	TFPlan      string   `json:"tfplan,omitempty"`
}

// Scenarios lists every embedded scenario, sorted by name. One malformed scenario fails the
// whole listing, because a scenario the binary cannot describe is a packaging defect.
func Scenarios() ([]Meta, error) { return scenariosIn(FS) }

func scenariosIn(fsys fs.FS) ([]Meta, error) {
	entries, err := fs.ReadDir(fsys, "data")
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := scenarioMetaIn(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ScenarioMeta reads one scenario's meta.json.
func ScenarioMeta(scenario string) (Meta, error) { return scenarioMetaIn(FS, scenario) }

func scenarioMetaIn(fsys fs.FS, scenario string) (Meta, error) {
	if err := checkScenario(scenario); err != nil {
		return Meta{}, err
	}
	b, err := fs.ReadFile(fsys, path.Join("data", scenario, "meta.json"))
	if err != nil {
		return Meta{}, fmt.Errorf("unknown scenario %q: %w", scenario, err)
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return Meta{}, fmt.Errorf("scenario %q: %w", scenario, err)
	}
	return m, nil
}

// ReadFile returns one file inside a scenario, for example "terraform/plan.json". The path is
// relative to the scenario and may not leave it.
func ReadFile(scenario, rel string) ([]byte, error) {
	if err := checkScenario(scenario); err != nil {
		return nil, err
	}
	if rel == "" || rel == "." || !fs.ValidPath(rel) {
		return nil, fmt.Errorf("fixtures: %q is not a path inside scenario %q", rel, scenario)
	}
	return FS.ReadFile(path.Join("data", scenario, rel))
}

// Sub returns the embedded scenario directory as a filesystem, so callers can treat
// embedded scenarios and on-disk directories the same way.
func Sub(scenario string) (fs.FS, error) {
	if err := checkScenario(scenario); err != nil {
		return nil, err
	}
	return fs.Sub(FS, path.Join("data", scenario))
}

// checkScenario accepts a single directory name and nothing else: no path separator, no dot
// element, so a scenario argument can never address another scenario or leave data/.
func checkScenario(scenario string) error {
	if scenario == "" || scenario == "." || scenario == ".." ||
		strings.ContainsAny(scenario, `/\`) || !fs.ValidPath(scenario) {
		return fmt.Errorf("fixtures: %q is not a scenario name", scenario)
	}
	return nil
}
