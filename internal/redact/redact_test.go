package redact

import (
	"strings"
	"testing"
)

func TestIsSecretish(t *testing.T) {
	secret := []string{
		"password", "PASSWORD", "DB_PASSWORD", "passwd", "user.passwd",
		"token", "BearerToken", "secret", "clientSecret",
		"key", "apiKey", "APIKEY", "ssh-key", "AZURE_CLIENT_SECRET",
		"connection", "ConnectionString", "credential", "credentials",
	}
	for _, k := range secret {
		if !IsSecretish(k) {
			t.Errorf("IsSecretish(%q) = false, want true", k)
		}
	}

	plain := []string{
		"replicas", "cpu.limit", "memory.request", "zone", "image.tag",
		"kernelVersion", "nodeImage", "", "namespace",
	}
	for _, k := range plain {
		if IsSecretish(k) {
			t.Errorf("IsSecretish(%q) = true, want false", k)
		}
	}
}

func TestValueIsStableAndOpaque(t *testing.T) {
	const salt = "run-salt"
	const in = "postgres://admin:hunter2@db.internal:5432/app"

	first := Value(salt, in)
	second := Value(salt, in)
	if first != second {
		t.Fatalf("Value not stable: %q then %q", first, second)
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("Value(%q) = %q, want a sha256: prefix", in, first)
	}
	if got := len(strings.TrimPrefix(first, "sha256:")); got != 12 {
		t.Fatalf("fingerprint length = %d, want 12", got)
	}
	if strings.Contains(first, in) || strings.Contains(first, "hunter2") || strings.Contains(first, "db.internal") {
		t.Fatalf("Value leaked its input: %q", first)
	}
	if Value("other-salt", in) == first {
		t.Fatal("Value ignored the salt")
	}
	if Value(salt, in+"!") == first {
		t.Fatal("Value collided on a changed input")
	}
}

func TestMapRedactsOnlySecretishKeys(t *testing.T) {
	const salt = "run-salt"
	in := map[string]string{
		"DB_PASSWORD":    "hunter2",
		"api_key":        "sk-live-123",
		"replicas":       "3",
		"memory.limit":   "512Mi",
		"connectionInfo": "postgres://x",
	}
	out := Map(salt, in)

	if out["replicas"] != "3" || out["memory.limit"] != "512Mi" {
		t.Fatalf("Map altered a plain value: %#v", out)
	}
	for _, k := range []string{"DB_PASSWORD", "api_key", "connectionInfo"} {
		if out[k] == in[k] {
			t.Errorf("Map left %s in clear", k)
		}
		if out[k] != Value(salt, in[k]) {
			t.Errorf("Map(%s) = %q, want the salted fingerprint", k, out[k])
		}
	}
	if in["DB_PASSWORD"] != "hunter2" {
		t.Fatal("Map mutated its input")
	}
}

func TestNameIsStablePerRun(t *testing.T) {
	p := NewPseudonymizer("run-salt")

	first := p.Name("namespace", "prod-payments")
	again := p.Name("namespace", "prod-payments")
	if first != again {
		t.Fatalf("Name not stable: %q then %q", first, again)
	}
	if !strings.HasPrefix(first, "ns-") {
		t.Fatalf("namespace placeholder = %q, want an ns- prefix", first)
	}

	other := p.Name("namespace", "prod-billing")
	if other == first {
		t.Fatalf("two namespaces share the placeholder %q", first)
	}

	workload := p.Name("workload", "checkout-api")
	node := p.Name("node", "aks-userpool-31415926-vmss000003")
	image := p.Name("image", "registry.internal/team/checkout")
	if !strings.HasPrefix(workload, "wl-") {
		t.Errorf("workload placeholder = %q, want a wl- prefix", workload)
	}
	if !strings.HasPrefix(node, "node-") {
		t.Errorf("node placeholder = %q, want a node- prefix", node)
	}
	if !strings.HasPrefix(image, "img-") {
		t.Errorf("image placeholder = %q, want an img- prefix", image)
	}

	// The same string under two kinds is two different objects.
	if p.Name("workload", "prod-payments") == first {
		t.Fatal("a workload and a namespace shared one placeholder")
	}

	// A second run is free to number differently, but must stay internally stable.
	q := NewPseudonymizer("run-salt")
	if q.Name("namespace", "prod-payments") != q.Name("namespace", "prod-payments") {
		t.Fatal("a fresh pseudonymizer is not stable within its own run")
	}
}

func TestNameNeverLeaksAndSkipsEmpty(t *testing.T) {
	p := NewPseudonymizer("run-salt")
	if got := p.Name("namespace", ""); got != "" {
		t.Fatalf("Name(kind, \"\") = %q, want an empty string", got)
	}
	got := p.Name("namespace", "prod-payments")
	if strings.Contains(got, "prod") || strings.Contains(got, "payments") {
		t.Fatalf("placeholder %q carries the real name", got)
	}
}

func TestRehydrateRoundTrips(t *testing.T) {
	p := NewPseudonymizer("run-salt")
	ns := p.Name("namespace", "prod-payments")
	wl := p.Name("workload", "checkout-api")

	text := "The memory limit on " + wl + " in " + ns + " fell to 256Mi."
	back := p.Rehydrate(text)
	want := "The memory limit on checkout-api in prod-payments fell to 256Mi."
	if back != want {
		t.Fatalf("Rehydrate = %q, want %q", back, want)
	}
	if p.Rehydrate("nothing to map here") != "nothing to map here" {
		t.Fatal("Rehydrate altered text with no placeholder")
	}
}

func TestRehydrateHandlesLongerPlaceholdersFirst(t *testing.T) {
	p := NewPseudonymizer("run-salt")
	names := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		names = append(names, p.Name("namespace", "team-"+string(rune('a'+i))))
	}
	// ns-1 is a prefix of ns-10: the tenth placeholder must still come back whole.
	tenth := names[9]
	got := p.Rehydrate("scoped to " + tenth + ".")
	want := "scoped to " + p.Table()[tenth] + "."
	if got != want {
		t.Fatalf("Rehydrate = %q, want %q", got, want)
	}
}

func TestTableIsACopy(t *testing.T) {
	p := NewPseudonymizer("run-salt")
	ns := p.Name("namespace", "prod-payments")

	table := p.Table()
	if table[ns] != "prod-payments" {
		t.Fatalf("Table()[%q] = %q, want prod-payments", ns, table[ns])
	}
	table[ns] = "tampered"
	delete(table, ns)
	if p.Table()[ns] != "prod-payments" {
		t.Fatal("Table returned the live mapping")
	}
	if p.Rehydrate(ns) != "prod-payments" {
		t.Fatal("editing the returned table changed the pseudonymizer")
	}
}
