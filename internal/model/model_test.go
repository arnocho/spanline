package model

import (
	"encoding/json"
	"testing"
)

// decodeSelector builds a selector the way every snapshot does: from the JSON kubectl printed.
func decodeSelector(t *testing.T, raw string) *LabelSelector {
	t.Helper()
	var sel *LabelSelector
	if err := json.Unmarshal([]byte(raw), &sel); err != nil {
		t.Fatalf("decode selector %s: %v", raw, err)
	}
	return sel
}

func TestSelectorMatches(t *testing.T) {
	web := map[string]string{"app": "web", "tier": "front"}
	cases := []struct {
		name   string
		sel    *LabelSelector
		labels map[string]string
		want   bool
	}{
		{"nil selector matches nothing", nil, web, false},
		{"empty selector matches everything, as Kubernetes documents", &LabelSelector{}, web, true},
		{"empty selector against no labels", decodeSelector(t, `{}`), nil, true},
		{"matchLabels hit", &LabelSelector{MatchLabels: map[string]string{"app": "web"}}, web, true},
		{"matchLabels miss", &LabelSelector{MatchLabels: map[string]string{"app": "api"}}, web, false},
		{"matchLabels against nil labels", &LabelSelector{MatchLabels: map[string]string{"app": "web"}}, nil, false},
		{"matchLabels with an empty value needs the key present",
			&LabelSelector{MatchLabels: map[string]string{"canary": ""}}, web, false},
		{"matchLabels with an empty value and the key present",
			&LabelSelector{MatchLabels: map[string]string{"canary": ""}}, map[string]string{"canary": ""}, true},
		{"In hit", decodeSelector(t, `{"matchExpressions":[{"key":"app","operator":"In","values":["web","api"]}]}`), web, true},
		{"In miss", decodeSelector(t, `{"matchExpressions":[{"key":"app","operator":"In","values":["api"]}]}`), web, false},
		{"In on an absent key", decodeSelector(t, `{"matchExpressions":[{"key":"zone","operator":"In","values":["a"]}]}`), web, false},
		{"NotIn hit", decodeSelector(t, `{"matchExpressions":[{"key":"app","operator":"NotIn","values":["api"]}]}`), web, true},
		{"NotIn miss", decodeSelector(t, `{"matchExpressions":[{"key":"app","operator":"NotIn","values":["web"]}]}`), web, false},
		{"NotIn on an absent key matches", decodeSelector(t, `{"matchExpressions":[{"key":"zone","operator":"NotIn","values":["a"]}]}`), web, true},
		{"Exists hit", decodeSelector(t, `{"matchExpressions":[{"key":"tier","operator":"Exists"}]}`), web, true},
		{"Exists miss", decodeSelector(t, `{"matchExpressions":[{"key":"zone","operator":"Exists"}]}`), web, false},
		{"DoesNotExist hit", decodeSelector(t, `{"matchExpressions":[{"key":"zone","operator":"DoesNotExist"}]}`), web, true},
		{"DoesNotExist miss", decodeSelector(t, `{"matchExpressions":[{"key":"app","operator":"DoesNotExist"}]}`), web, false},
		{"matchLabels and matchExpressions are ANDed",
			decodeSelector(t, `{"matchLabels":{"app":"web"},"matchExpressions":[{"key":"tier","operator":"In","values":["back"]}]}`), web, false},
		{"matchLabels and matchExpressions both satisfied",
			decodeSelector(t, `{"matchLabels":{"app":"web"},"matchExpressions":[{"key":"tier","operator":"In","values":["front"]}]}`), web, true},
		{"an operator spanline does not know is never a match",
			decodeSelector(t, `{"matchLabels":{"app":"web"},"matchExpressions":[{"key":"tier","operator":"Gt","values":["1"]}]}`), web, false},
	}
	for _, c := range cases {
		if got := SelectorMatches(c.sel, c.labels); got != c.want {
			t.Errorf("%s: SelectorMatches = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestSelectorDecodeKeepsExpressions checks that matchExpressions survive the decode of a whole
// object, so a workload or budget selected by expression is evaluated, not silently assumed.
func TestSelectorDecodeKeepsExpressions(t *testing.T) {
	raw := `{"kind":"Deployment","metadata":{"name":"web","namespace":"shop"},
	  "spec":{"replicas":2,"selector":{"matchExpressions":[{"key":"app","operator":"In","values":["web"]}]},
	  "template":{"metadata":{"labels":{"app":"web"}}}}}`
	var w Workload
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatalf("decode workload: %v", err)
	}
	if w.Spec.Selector == nil {
		t.Fatal("selector decoded as nil")
	}
	if !SelectorMatches(w.Spec.Selector, map[string]string{"app": "web"}) {
		t.Error("an expression-only selector must match the pods it selects")
	}
	if SelectorMatches(w.Spec.Selector, map[string]string{"app": "api"}) {
		t.Error("an expression-only selector must not match pods outside its values")
	}
	if w.Spec.Replicas == nil || *w.Spec.Replicas != 2 {
		t.Errorf("replicas = %v, want 2: the selector decode must not disturb its siblings", w.Spec.Replicas)
	}

	var pdb PodDisruptionBudget
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"p","namespace":"shop"},"spec":{"selector":null,"minAvailable":"1"}}`), &pdb); err != nil {
		t.Fatalf("decode pdb: %v", err)
	}
	if pdb.Spec.Selector != nil {
		t.Errorf("a null selector must decode as nil, got %+v", pdb.Spec.Selector)
	}
	if pdb.Spec.MinAvailable != "1" {
		t.Errorf("minAvailable = %q, want 1", pdb.Spec.MinAvailable)
	}
}
