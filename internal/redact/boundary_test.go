package redact

import "testing"

func TestIsSecretishCoversTheCredentialWords(t *testing.T) {
	secret := []string{
		"authorization", "Authorization", "http.authorization.header",
		"bearer", "BearerToken", "bearer_value",
		"cert", "tls.cert", "clientCertificate", "ca-cert",
		"private", "privateKey", "private-key", "PRIVATE_PEM",
		"pwd", "DB_PWD", "db.pwd",
		"passphrase", "PASSPHRASE", "cookie", "SessionCookie",
	}
	for _, k := range secret {
		if !IsSecretish(k) {
			t.Errorf("IsSecretish(%q) = false, want true", k)
		}
	}
	plain := []string{"replicas", "zone", "image.tag", "kernelVersion", "memory.limit", "cpu.request", "node.image", "kubeletVersion"}
	for _, k := range plain {
		if IsSecretish(k) {
			t.Errorf("IsSecretish(%q) = true, want false", k)
		}
	}
}

// ns-dy6sd and ns-ejoid were found by brute force: under the salt run-salt their twelve
// character fingerprints are equal. Two objects must never share one placeholder.
func TestNameNeverMergesTwoNamesWhoseFingerprintsCollide(t *testing.T) {
	const salt = "run-salt"
	const a, b = "ns-dy6sd", "ns-ejoid"
	if Value(salt, a) != Value(salt, b) {
		t.Fatalf("the recorded pair no longer collides under Value (%s vs %s): find a new pair, or this test proves nothing",
			Value(salt, a), Value(salt, b))
	}

	p := NewPseudonymizer(salt)
	pa := p.Name("namespace", a)
	pb := p.Name("namespace", b)
	if pa == pb {
		t.Fatalf("two different namespaces got the same placeholder %q", pa)
	}
	if p.Name("namespace", a) != pa || p.Name("namespace", b) != pb {
		t.Fatal("the placeholders are not stable once both names are known")
	}
	table := p.Table()
	if table[pa] != a || table[pb] != b {
		t.Errorf("table = %v, want %s->%s and %s->%s", table, pa, a, pb, b)
	}
	if got := p.Rehydrate(pa + " and " + pb); got != a+" and "+b {
		t.Errorf("Rehydrate = %q, want both real names back", got)
	}
}

func TestRehydrateReplacesWholeTokensOnly(t *testing.T) {
	p := NewPseudonymizer("s")
	ns := p.Name("namespace", "prod-payments") // ns-1
	wl := p.Name("workload", "checkout-api")   // wl-2
	if ns != "ns-1" || wl != "wl-2" {
		t.Fatalf("placeholders = %q %q, the cases below assume ns-1 and wl-2", ns, wl)
	}
	cases := map[string]string{
		"ns-1":             "prod-payments",
		"ns-1.":            "prod-payments.",
		"(ns-1)":           "(prod-payments)",
		"[ns-1]":           "[prod-payments]",
		"ns-1/wl-2":        "prod-payments/checkout-api",
		"ns-1,wl-2":        "prod-payments,checkout-api",
		"in ns-1 the wl-2": "in prod-payments the checkout-api",
		"dns-1":            "dns-1",
		"ns-10":            "ns-10",
		"ns-1a":            "ns-1a",
		"ns-1_x":           "ns-1_x",
		"ns-1-old":         "ns-1-old",
		"owl-2":            "owl-2",
		"Ns-1":             "Ns-1",
	}
	for in, want := range cases {
		if got := p.Rehydrate(in); got != want {
			t.Errorf("Rehydrate(%q) = %q, want %q", in, got, want)
		}
	}
}
