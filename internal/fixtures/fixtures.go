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

// Scenarios lists every embedded scenario, sorted by name.
func Scenarios() ([]Meta, error) {
	entries, err := FS.ReadDir("data")
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := ScenarioMeta(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ScenarioMeta reads one scenario's meta.json.
func ScenarioMeta(scenario string) (Meta, error) {
	b, err := FS.ReadFile(path.Join("data", scenario, "meta.json"))
	if err != nil {
		return Meta{}, fmt.Errorf("unknown scenario %q: %w", scenario, err)
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return Meta{}, fmt.Errorf("scenario %q: %w", scenario, err)
	}
	return m, nil
}

// ReadFile returns one file inside a scenario, for example "terraform/plan.json".
func ReadFile(scenario, rel string) ([]byte, error) {
	return FS.ReadFile(path.Join("data", scenario, rel))
}

// Sub returns the embedded scenario directory as a filesystem, so callers can treat
// embedded scenarios and on-disk directories the same way.
func Sub(scenario string) (fs.FS, error) {
	return fs.Sub(FS, path.Join("data", scenario))
}
