package model

import (
	"encoding/json"
	"testing"
)

// TestBudgetDecodesIntOrString guards the live collector: Kubernetes serialises minAvailable
// and maxUnavailable as a number or a string, and a real cluster almost always has at least one
// budget written as a bare integer.
func TestBudgetDecodesIntOrString(t *testing.T) {
	cases := []struct {
		name string
		in   string
		min  string
		max  string
	}{
		{"integer minAvailable", `{"minAvailable": 1}`, "1", ""},
		{"percentage minAvailable", `{"minAvailable": "100%"}`, "100%", ""},
		{"integer maxUnavailable", `{"maxUnavailable": 2}`, "", "2"},
		{"percentage maxUnavailable", `{"maxUnavailable": "25%"}`, "", "25%"},
		{"both absent", `{"selector": {"matchLabels": {"app": "x"}}}`, "", ""},
		{"null", `{"minAvailable": null}`, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var spec PDBSpec
			if err := json.Unmarshal([]byte(c.in), &spec); err != nil {
				t.Fatalf("decode failed: %v", err)
			}
			if spec.MinAvailable != c.min || spec.MaxUnavailable != c.max {
				t.Errorf("got min %q max %q, want min %q max %q", spec.MinAvailable, spec.MaxUnavailable, c.min, c.max)
			}
		})
	}

	// the selector must survive the custom decode, expressions included
	var spec PDBSpec
	in := `{"minAvailable": 1, "selector": {"matchLabels": {"app": "web"}, "matchExpressions": [{"key": "tier", "operator": "In", "values": ["front"]}]}}`
	if err := json.Unmarshal([]byte(in), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Selector == nil || spec.Selector.MatchLabels["app"] != "web" {
		t.Fatal("the selector was lost by the budget decode")
	}
	if !SelectorMatches(spec.Selector, map[string]string{"app": "web", "tier": "front"}) {
		t.Error("a pod matching both the labels and the expression was not matched")
	}
	if SelectorMatches(spec.Selector, map[string]string{"app": "web", "tier": "back"}) {
		t.Error("a pod failing the expression was matched")
	}

	// a whole list, the way kubectl returns it, with mixed forms
	list := `{"items": [{"spec": {"minAvailable": 1}}, {"spec": {"maxUnavailable": "10%"}}]}`
	var out struct {
		Items []PodDisruptionBudget `json:"items"`
	}
	if err := json.Unmarshal([]byte(list), &out); err != nil {
		t.Fatalf("a mixed list failed to decode: %v", err)
	}
	if len(out.Items) != 2 || out.Items[0].Spec.MinAvailable != "1" || out.Items[1].Spec.MaxUnavailable != "10%" {
		t.Errorf("mixed list decoded wrong: %+v", out.Items)
	}
}
