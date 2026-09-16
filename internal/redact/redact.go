// Package redact keeps sensitive material on the laptop. A client security team asks two
// things of it: a value must never leave in clear, and a value that changed must still be
// provable. Both are answered by a per-run salted fingerprint, never by an encoding, and by
// placeholders that a prompt can carry without carrying a single real name.
package redact

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// secretish lists the substrings that make a key sensitive. The list is deliberately broad:
// a false positive costs one unreadable value, a false negative costs a leak.
var secretish = []string{
	"password",
	"token",
	"secret",
	"key",
	"connection",
	"credential",
	"passwd",
	"apikey",
}

// IsSecretish reports whether a key name says its value must never be shown.
// The match is case insensitive and on substrings, so DB_PASSWORD and apiKeyRef both match.
func IsSecretish(key string) bool {
	lower := strings.ToLower(key)
	for _, needle := range secretish {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// Value returns a short salted fingerprint of v, in the form "sha256:" plus twelve hex
// characters of HMAC-SHA256(salt, v). Two runs with the same salt agree on whether a value
// changed, and neither run discloses the value itself. The fingerprint is one way: nothing
// here can turn it back into v.
func Value(salt, v string) string {
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(v))
	return "sha256:" + hex.EncodeToString(mac.Sum(nil))[:12]
}

// Map copies m and replaces the value of every secretish key with its fingerprint.
// Keys are never altered: an operator still sees which setting changed.
func Map(salt string, m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if IsSecretish(k) {
			out[k] = Value(salt, v)
			continue
		}
		out[k] = v
	}
	return out
}

// prefixes maps a kind of object to the placeholder prefix used in prompts and in reports.
var prefixes = map[string]string{
	"namespace":   "ns",
	"ns":          "ns",
	"workload":    "wl",
	"wl":          "wl",
	"deployment":  "wl",
	"statefulset": "wl",
	"daemonset":   "wl",
	"pod":         "wl",
	"node":        "node",
	"image":       "img",
	"img":         "img",
	"context":     "ctx",
	"cluster":     "ctx",
	"pool":        "pool",
	"nodepool":    "pool",
}

// prefixFor picks the placeholder prefix for a kind. An unknown kind keeps its own letters,
// so a new caller never silently collides with an existing family of placeholders.
func prefixFor(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	if p, ok := prefixes[k]; ok {
		return p
	}
	var b strings.Builder
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "obj"
	}
	return b.String()
}

// Pseudonymizer hands out stable placeholders for the names that must not leave the machine:
// namespaces, workloads, nodes, images. The mapping lives for one run only, in memory, and is
// keyed by the salted fingerprint of the real name, so the index itself holds no plain lookup
// key. It is safe for concurrent use.
type Pseudonymizer struct {
	salt  string
	mu    sync.Mutex
	next  int
	byKey map[string]string // kind plus fingerprint -> placeholder
	table map[string]string // placeholder -> real name
}

// NewPseudonymizer starts an empty mapping bound to one run salt.
func NewPseudonymizer(salt string) *Pseudonymizer {
	return &Pseudonymizer{
		salt:  salt,
		byKey: make(map[string]string),
		table: make(map[string]string),
	}
}

// Name returns the placeholder for a real name, for example ns-1, wl-2, node-3, img-4.
// The same kind and name always give the same placeholder within a run, and an empty name
// stays empty so a caller never invents an object that does not exist.
func (p *Pseudonymizer) Name(kind, real string) string {
	if real == "" {
		return ""
	}
	prefix := prefixFor(kind)
	key := prefix + "\x00" + Value(p.salt, real)

	p.mu.Lock()
	defer p.mu.Unlock()
	if placeholder, ok := p.byKey[key]; ok {
		return placeholder
	}
	p.next++
	placeholder := prefix + "-" + strconv.Itoa(p.next)
	p.byKey[key] = placeholder
	p.table[placeholder] = real
	return placeholder
}

// Rehydrate maps every placeholder in text back to its real name, so an operator reads the
// answer in the terms of their own estate. Longer placeholders are replaced first, so ns-10
// is never mistaken for ns-1.
func (p *Pseudonymizer) Rehydrate(text string) string {
	p.mu.Lock()
	placeholders := make([]string, 0, len(p.table))
	for ph := range p.table {
		placeholders = append(placeholders, ph)
	}
	sort.Slice(placeholders, func(i, j int) bool {
		if len(placeholders[i]) != len(placeholders[j]) {
			return len(placeholders[i]) > len(placeholders[j])
		}
		return placeholders[i] < placeholders[j]
	})
	pairs := make([]string, 0, len(placeholders)*2)
	for _, ph := range placeholders {
		pairs = append(pairs, ph, p.table[ph])
	}
	p.mu.Unlock()

	if len(pairs) == 0 {
		return text
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// Table returns a copy of the placeholder to real name mapping, for --show-prompt and for an
// audit of exactly what a placeholder stood for. The copy keeps the caller from editing the
// live mapping mid run.
func (p *Pseudonymizer) Table() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]string, len(p.table))
	for k, v := range p.table {
		out[k] = v
	}
	return out
}
