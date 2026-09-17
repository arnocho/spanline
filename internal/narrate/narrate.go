// Package narrate turns a finished deterministic report into optional prose. It never computes
// anything: a verdict, an order and an exit code are decided before a narrator is even built.
//
// Two rules make the narrator acceptable to a client security team. First, only an allowlist of
// fields may leave the machine, and every name inside it is a placeholder handed out by the
// redact package. Second, every sentence that comes back must cite a fact that was sent, and
// every number in that sentence must appear in the cited fact, or the sentence is dropped.
// What remains is marked unverified, with the SHA of the prompt and of the raw answer, so a
// reader always knows a model touched it.
package narrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arnocho/spanline/internal/redact"
	"github.com/arnocho/spanline/internal/result"
)

const (
	defaultTimeout = 30 * time.Second
	maxAnswerBytes = 1 << 20
	maxValueRunes  = 80
)

// Options configures the optional narrator. It holds the name of the environment variable
// that carries the API key, never the key itself, so an Options value is safe to print.
type Options struct {
	Backend    string        // "none", "openai-compat", "anthropic"
	BaseURL    string        // endpoint root, for example https://gateway.internal/v1
	Model      string        // model identifier passed through to the backend
	APIKeyEnv  string        // name of the env var holding the key, never the key itself
	AllowHosts []string      // explicit endpoint allowlist; empty means refuse
	Timeout    time.Duration // per call deadline; zero means the package default
	ShowPrompt bool          // print the exact prompt to stderr before sending
}

// Fact is one piece of evidence a sentence may be built on. Nothing outside a Fact and the
// Counts map is ever sent, and a Fact only ever holds allowlisted fields and placeholders.
type Fact struct {
	ID     string // citation id, for example "chg-0007.diff.memory.limit"
	Field  string // the field path this fact is about
	Before string // value before the change, when there is one
	After  string // value after the change, or the single observed value
	Note   string // short deterministic qualifier, never free text from the estate
}

// Payload is the whole of what may leave the machine for one report.
type Payload struct {
	Kind   string         // "why", "impact", "estate"
	Facts  []Fact         // the only data that may leave the machine
	Counts map[string]int // deterministic counts, citable as count.<name>
}

// add appends a fact, keeping every id unique so a citation points at exactly one fact.
func (pl *Payload) add(f Fact) {
	if f.ID == "" {
		return
	}
	base := f.ID
	for n := 2; pl.hasID(f.ID); n++ {
		f.ID = base + "-" + strconv.Itoa(n)
	}
	pl.Facts = append(pl.Facts, f)
}

func (pl *Payload) hasID(id string) bool {
	for _, f := range pl.Facts {
		if f.ID == id {
			return true
		}
	}
	return false
}

// allowField is the allowlist. It maps a raw field path to its canonical name, and reports
// false for everything else. A field that is not recognised here never leaves the machine,
// which is the whole point: an unknown field is refused, not inspected.
func allowField(raw string) (string, bool) {
	f := strings.ToLower(strings.TrimSpace(raw))
	f = strings.NewReplacer(" ", ".", "/", ".", "_", ".").Replace(f)
	f = strings.Trim(f, ".")
	if f == "" {
		return "", false
	}
	// A path that names a credential is refused before any keyword below can match inside it:
	// env.ZONE_API_KEY contains "zone", and its values must never leave.
	if redact.IsSecretish(f) {
		return "", false
	}
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(f, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("cpu") && has("request"):
		return "cpu.request", true
	case has("cpu") && has("limit"):
		return "cpu.limit", true
	case has("memory", "mem.") && has("request"):
		return "memory.request", true
	case has("memory", "mem.") && has("limit"):
		return "memory.limit", true
	case has("replica"):
		return "replicas", true
	case has("nodeimage", "node.image", "osimage", "os.image"):
		return "node.image", true
	case has("kernel"):
		return "kernel.version", true
	case has("kubelet"):
		return "kubelet.version", true
	case has("zone"):
		return "zone", true
	case has("image"):
		return "image.tag", true
	}
	return "", false
}

// fieldSlug keeps a field path readable and free of anything that could be mistaken for text.
func fieldSlug(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "field"
	}
	return out
}

// safeValue trims an allowlisted value, and pseudonymizes the repository half of an image.
func safeValue(canonical, raw string, p *redact.Pseudonymizer) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	if canonical == "image.tag" {
		return imageValue(v, p)
	}
	r := []rune(v)
	if len(r) > maxValueRunes {
		v = string(r[:maxValueRunes])
	}
	return v
}

// imageValue keeps the tag, which is the fact that matters, and replaces the repository,
// which names the client's registry and its teams.
func imageValue(v string, p *redact.Pseudonymizer) string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, oneImage(part, p))
	}
	return strings.Join(out, ", ")
}

func oneImage(ref string, p *redact.Pseudonymizer) string {
	if repo, digest, ok := strings.Cut(ref, "@"); ok {
		short := digest
		if i := strings.LastIndex(digest, ":"); i >= 0 && len(digest) > i+13 {
			short = digest[:i+13]
		}
		return p.Name("image", repo) + "@" + short
	}
	repo, tag := ref, ""
	if i := strings.LastIndex(ref, ":"); i >= 0 && i > strings.LastIndex(ref, "/") {
		repo, tag = ref[:i], ref[i+1:]
	}
	name := p.Name("image", repo)
	if tag == "" {
		return name
	}
	return name + ":" + tag
}

// parseDiff reads one diff line of the form "field: before -> after". A line it cannot read
// is refused rather than guessed at.
func parseDiff(s string) (field, before, after string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", "", false
	}
	sep := ""
	for _, candidate := range []string{" -> ", " => ", "->", "=>"} {
		if strings.Contains(s, candidate) {
			sep = candidate
			break
		}
	}
	if sep == "" {
		return "", "", "", false
	}
	left, right, _ := strings.Cut(s, sep)
	right = strings.TrimSpace(right)
	if name, value, found := strings.Cut(left, ":"); found {
		return strings.TrimSpace(name), strings.TrimSpace(value), right, true
	}
	words := strings.Fields(left)
	if len(words) < 2 {
		return strings.TrimSpace(left), "", right, true
	}
	return words[0], strings.Join(words[1:], " "), right, true
}

// pseudoObject replaces every name in an object reference such as "prod-payments/checkout-api".
func pseudoObject(p *redact.Pseudonymizer, kindHint, obj string) string {
	obj = strings.TrimSpace(obj)
	if obj == "" {
		return ""
	}
	parts := strings.Split(obj, "/")
	if len(parts) == 1 {
		if strings.Contains(strings.ToLower(kindHint), "node") {
			return p.Name("node", parts[0])
		}
		return p.Name("workload", parts[0])
	}
	out := make([]string, 0, len(parts))
	out = append(out, p.Name("namespace", parts[0]))
	for _, rest := range parts[1:] {
		out = append(out, p.Name("workload", rest))
	}
	return strings.Join(out, "/")
}

func severitySlug(s result.Severity) string {
	switch s {
	case result.Outage:
		return "outage"
	case result.Disruption:
		return "disruption"
	case result.Risk:
		return "risk"
	case result.NotAssessed:
		return "notAssessed"
	default:
		return "info"
	}
}

func ensure(p *redact.Pseudonymizer) *redact.Pseudonymizer {
	if p == nil {
		return redact.NewPseudonymizer("")
	}
	return p
}

// PayloadForWhy copies the allowlisted half of a why report: the split, the verdicts, and the
// resource fields of every suspect change. Titles, reasons, pod names and gap text stay home.
func PayloadForWhy(r *result.WhyReport, p *redact.Pseudonymizer) Payload {
	pl := Payload{Kind: "why", Counts: map[string]int{}}
	if r == nil {
		return pl
	}
	p = ensure(p)

	pl.Counts["failing"] = r.Failing.Count
	pl.Counts["healthy"] = r.Healthy.Count
	pl.Counts["suspects"] = len(r.Suspects)
	pl.Counts["dimensions"] = len(r.Dimensions)
	pl.Counts["revisions"] = len(r.Revisions)
	pl.Counts["gaps"] = len(r.Gaps)

	pl.add(Fact{ID: "scope.context", Field: "context", After: p.Name("context", r.Context)})
	pl.add(Fact{ID: "scope.namespace", Field: "namespace", After: p.Name("namespace", r.Namespace)})
	pl.add(Fact{ID: "scope.workload", Field: "workload", After: p.Name("workload", r.Workload)})
	pl.add(Fact{ID: "scope.mode", Field: "mode", After: string(r.Mode)})
	if canonical, ok := allowField(r.CohortKey); ok {
		pl.add(Fact{ID: "scope.cohortKey", Field: "cohortKey", After: canonical})
	}

	for i, d := range r.Dimensions {
		id := "dim-" + strconv.Itoa(i+1)
		pl.add(Fact{
			ID:    id + ".separation",
			Field: "dimension." + fieldSlug(d.Name),
			After: string(d.Separation),
			Note:  "purity " + strconv.FormatFloat(d.Purity, 'f', 2, 64),
		})
		canonical, ok := allowField(d.Name)
		if !ok {
			continue
		}
		pl.add(Fact{
			ID:     id + ".values",
			Field:  canonical,
			Before: safeValue(canonical, d.FailingValues, p),
			After:  safeValue(canonical, d.HealthyValues, p),
			Note:   "before is the failing cohort, after is the healthy cohort",
		})
	}

	for i, s := range r.Suspects {
		// The citation id is positional on purpose: a suspect's own id is built from the object
		// it names (replicaset/<name>, argocd/<app>), and a name must never leave the machine.
		id := fmt.Sprintf("chg-%04d", i+1)
		pl.add(Fact{
			ID:    id + ".verdict",
			Field: "verdict",
			After: string(s.Verdict),
			Note:  "actor " + string(s.Actor),
		})
		if canonical, ok := allowField(s.Dimension); ok {
			pl.add(Fact{ID: id + ".dimension", Field: "dimension", After: canonical})
		}
		for _, line := range s.Diff {
			field, before, after, ok := parseDiff(line)
			if !ok {
				continue
			}
			canonical, ok := allowField(field)
			if !ok {
				continue
			}
			pl.add(Fact{
				ID:     id + ".diff." + canonical,
				Field:  "diff." + canonical,
				Before: safeValue(canonical, before, p),
				After:  safeValue(canonical, after, p),
			})
		}
	}

	for i, rev := range r.Revisions {
		id := "rev-" + strconv.Itoa(i+1)
		pl.add(Fact{
			ID:    id + ".replicas",
			Field: "replicas",
			After: strconv.Itoa(rev.Replicas),
			Note:  "revision " + strconv.Itoa(rev.Number),
		})
	}
	return pl
}

// PayloadForImpact copies severities, the verdict, the exit code and the shape of the blast
// radius. Finding reasons and Terraform addresses stay home.
func PayloadForImpact(r *result.ImpactReport, p *redact.Pseudonymizer) Payload {
	pl := Payload{Kind: "impact", Counts: map[string]int{}}
	if r == nil {
		return pl
	}
	p = ensure(p)

	pl.Counts["nodes"] = len(r.Nodes)
	pl.Counts["findings"] = len(r.Findings)
	pl.Counts["ignored"] = len(r.Ignored)
	pl.Counts["gaps"] = len(r.Gaps)
	pl.Counts["exitCode"] = r.ExitCode
	for _, key := range []string{"outage", "disruption", "risk", "notAssessed", "info"} {
		pl.Counts[key] = 0
	}
	for _, f := range r.Findings {
		pl.Counts[severitySlug(f.Severity)]++
	}

	pl.add(Fact{ID: "scope.context", Field: "context", After: p.Name("context", r.Context)})
	pl.add(Fact{
		ID:    "scope.verdict",
		Field: "verdict",
		After: string(r.Verdict),
		Note:  "exit code " + strconv.Itoa(r.ExitCode),
	})
	if len(r.Nodes) > 0 {
		names := make([]string, 0, len(r.Nodes))
		for _, n := range r.Nodes {
			names = append(names, p.Name("node", n))
		}
		pl.add(Fact{
			ID:    "scope.nodes",
			Field: "nodes",
			After: strings.Join(names, " "),
			Note:  strconv.Itoa(len(names)) + " nodes in scope",
		})
	}

	for i, f := range r.Findings {
		pl.add(Fact{
			ID:    fmt.Sprintf("imp-%04d.severity", i+1),
			Field: "finding." + fieldSlug(f.Kind),
			After: string(f.Severity),
			Note:  "object " + pseudoObject(p, f.Kind, f.Object),
		})
	}
	return pl
}

// PayloadForEstate copies per cluster and per pool counts, zones, and risk severities.
// Context names, pool names, state file paths and Terraform addresses stay home.
func PayloadForEstate(r *result.EstateReport, p *redact.Pseudonymizer) Payload {
	pl := Payload{Kind: "estate", Counts: map[string]int{}}
	if r == nil {
		return pl
	}
	p = ensure(p)

	pl.Counts["clusters"] = len(r.Clusters)
	pl.Counts["pools"] = len(r.Pools)
	pl.Counts["risks"] = len(r.Risks)
	pl.Counts["states"] = len(r.States)
	pl.Counts["gaps"] = len(r.Gaps)

	totalNodes, totalPods, totalWorkloads, totalNotReady := 0, 0, 0, 0
	for i, c := range r.Clusters {
		id := "cluster-" + strconv.Itoa(i+1)
		name := p.Name("context", c.Context)
		totalNodes += c.Nodes
		totalPods += c.Pods
		totalWorkloads += c.Workloads
		totalNotReady += c.NodesNotReady
		pl.add(Fact{ID: id + ".nodes", Field: "nodes", After: strconv.Itoa(c.Nodes), Note: "cluster " + name})
		pl.add(Fact{ID: id + ".notReady", Field: "nodesNotReady", After: strconv.Itoa(c.NodesNotReady), Note: "cluster " + name})
		pl.add(Fact{ID: id + ".pods", Field: "pods", After: strconv.Itoa(c.Pods), Note: "cluster " + name})
		pl.add(Fact{ID: id + ".workloads", Field: "workloads", After: strconv.Itoa(c.Workloads), Note: "cluster " + name})
		pl.add(Fact{ID: id + ".risks", Field: "risks", After: strconv.Itoa(c.Risks), Note: "cluster " + name})
		pl.add(Fact{ID: id + ".outages", Field: "outages", After: strconv.Itoa(c.Outages), Note: "cluster " + name})
	}
	pl.Counts["nodes"] = totalNodes
	pl.Counts["pods"] = totalPods
	pl.Counts["workloads"] = totalWorkloads
	pl.Counts["nodesNotReady"] = totalNotReady

	for i, pool := range r.Pools {
		id := "pool-" + strconv.Itoa(i+1)
		name := p.Name("pool", pool.Pool)
		owned := "not owned by any state file that was read"
		if pool.TerraformAddress != "" {
			owned = "owned by a Terraform address"
		}
		pl.add(Fact{ID: id + ".nodes", Field: "nodes", After: strconv.Itoa(pool.Nodes), Note: "pool " + name + ", " + owned})
		pl.add(Fact{ID: id + ".cpuPercent", Field: "cpu.request.percent", After: strconv.FormatFloat(pool.CPUPercent, 'f', 1, 64), Note: "pool " + name})
		pl.add(Fact{ID: id + ".memPercent", Field: "memory.request.percent", After: strconv.FormatFloat(pool.MemPercent, 'f', 1, 64), Note: "pool " + name})
		pl.add(Fact{ID: id + ".atRisk", Field: "workloadsAtRisk", After: strconv.Itoa(pool.AtRisk), Note: "pool " + name})
		if zones := safeValue("zone", pool.Zones, p); zones != "" {
			pl.add(Fact{ID: id + ".zone", Field: "zone", After: zones, Note: "pool " + name})
		}
	}

	for i, f := range r.Risks {
		pl.add(Fact{
			ID:    fmt.Sprintf("risk-%04d.severity", i+1),
			Field: "risk." + fieldSlug(f.Kind),
			After: string(f.Severity),
			Note:  "object " + pseudoObject(p, f.Kind, f.Object),
		})
	}

	resources, matched, unmatched := 0, 0, 0
	for _, s := range r.States {
		resources += s.Resources
		matched += s.Matched
		unmatched += s.Unmatched
	}
	pl.Counts["stateResources"] = resources
	pl.Counts["stateMatched"] = matched
	pl.Counts["stateUnmatched"] = unmatched
	return pl
}

// renderFact is the single rendering of a fact, used by the prompt and by nothing else.
func renderFact(f Fact) string {
	var b strings.Builder
	b.WriteString("[" + f.ID + "] " + f.Field)
	switch {
	case f.Before != "" && f.After != "":
		b.WriteString(": " + f.Before + " -> " + f.After)
	case f.After != "":
		b.WriteString(": " + f.After)
	case f.Before != "":
		b.WriteString(": was " + f.Before)
	}
	if f.Note != "" {
		b.WriteString(" (" + f.Note + ")")
	}
	return b.String()
}

// Prompt renders the exact text that would be sent. It is deterministic, so the same payload
// always gives the same PromptSHA, and a reviewer can read it with --show-prompt before anyone
// enables a backend.
func Prompt(pl Payload) string {
	var b strings.Builder
	b.WriteString("You are explaining a report that has already been computed. You are not analysing anything.\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("1. Use only the facts listed below. Do not add any other fact, cause or recommendation.\n")
	b.WriteString("2. End every sentence with the id of the fact it rests on, in square brackets, for example [scope.verdict].\n")
	b.WriteString("3. Every number you write must appear in the fact you cite. Do not compute new numbers.\n")
	b.WriteString("4. Never change, soften, reorder or contradict a verdict, a severity, a separation or a count.\n")
	b.WriteString("5. Names such as ns-1, wl-2, node-3 and img-4 are placeholders. Copy them exactly.\n")
	b.WriteString("6. Write at most six sentences of plain prose. No lists, no headings, no markdown.\n")
	b.WriteString("7. If a fact does not support a sentence, leave the sentence out.\n\n")
	b.WriteString("Report: " + pl.Kind + "\n")

	if len(pl.Counts) > 0 {
		keys := make([]string, 0, len(pl.Counts))
		for k := range pl.Counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("\nCounts:\n")
		for _, k := range keys {
			b.WriteString("[count." + k + "] " + k + ": " + strconv.Itoa(pl.Counts[k]) + "\n")
		}
	}

	b.WriteString("\nFacts:\n")
	for _, f := range pl.Facts {
		b.WriteString(renderFact(f) + "\n")
	}
	return b.String()
}

// factIndex maps every citable id to the text a sentence citing it is allowed to rely on.
func factIndex(pl Payload) map[string]string {
	index := make(map[string]string, len(pl.Facts)+len(pl.Counts))
	for _, f := range pl.Facts {
		index[f.ID] = strings.Join([]string{f.ID, f.Field, f.Before, f.After, f.Note}, " ")
	}
	for k, v := range pl.Counts {
		id := "count." + k
		index[id] = id + " " + k + " " + strconv.Itoa(v)
	}
	return index
}

// Validate keeps the sentences that cite a fact of this payload and whose numbers all appear
// in a cited fact, and counts the rest as dropped. This is the guarantee that a model cannot
// add a cause, a number or a recommendation that the deterministic core never produced.
func Validate(text string, pl Payload) (kept string, dropped int) {
	v := newValidator(pl)
	sentences := splitSentences(text)
	out := make([]string, 0, len(sentences))
	for _, s := range sentences {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		cited := citedIDs(trimmed, v.index)
		if len(cited) == 0 {
			dropped++
			continue
		}
		if !v.supports(trimmed, cited) {
			dropped++
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, " "), dropped
}

// citations lists, in order, the distinct fact ids the kept text rests on.
func citations(text string, pl Payload) []string {
	index := factIndex(pl)
	seen := map[string]bool{}
	var out []string
	for _, s := range splitSentences(text) {
		for _, id := range citedIDs(strings.TrimSpace(s), index) {
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func citedIDs(sentence string, index map[string]string) []string {
	ids := make([]string, 0, 2)
	for id := range index {
		if containsToken(sentence, id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// validator holds what a sentence is allowed to rest on: the text of every citable fact, and
// the text of the whole payload, which is what a placeholder such as wl-2 is checked against.
type validator struct {
	index map[string]string
	all   string
}

func newValidator(pl Payload) validator {
	index := factIndex(pl)
	ids := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(" ")
		b.WriteString(index[id])
	}
	b.WriteString(" ")
	return validator{index: index, all: b.String()}
}

// supports reports whether every number and every name written in a sentence was actually sent.
func (v validator) supports(sentence string, cited []string) bool {
	for _, tok := range sentenceTokens(sentence) {
		if !v.tokenOK(tok, cited) {
			return false
		}
	}
	return true
}

func (v validator) tokenOK(tok string, cited []string) bool {
	if tok == "" || !hasDigit(tok) {
		return true
	}
	for _, id := range cited {
		if containsToken(v.index[id], tok) {
			return true
		}
	}
	// A token that starts with a letter is a name or an id, not a number. It is allowed as
	// soon as the payload carried it, because a placeholder is sent once and used anywhere.
	if !isDigit(tok[0]) && containsToken(v.all, tok) {
		return true
	}
	for _, n := range numberRuns(tok) {
		supported := false
		for _, id := range cited {
			if numberIn(v.index[id], n) {
				supported = true
				break
			}
		}
		if !supported {
			return false
		}
	}
	return true
}

// sentenceTokens cuts a sentence into the identifiers, values and numbers it is made of.
func sentenceTokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		if !isIDChar(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isIDChar(s[j]) {
			j++
		}
		if tok := strings.Trim(s[i:j], ".-_"); tok != "" {
			out = append(out, tok)
		}
		i = j
	}
	return out
}

func hasDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if isDigit(s[i]) {
			return true
		}
	}
	return false
}

// splitSentences cuts on a terminator that actually ends a sentence. A dot inside a fact id
// such as chg-0007.diff.memory.limit, or inside a version, is not a terminator.
func splitSentences(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '\n' {
			out = append(out, text[start:i])
			start = i + 1
			continue
		}
		if c != '.' && c != '!' && c != '?' {
			continue
		}
		if i+1 < len(text) && !isBreak(text[i+1]) {
			continue
		}
		out = append(out, text[start:i+1])
		start = i + 1
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

func isBreak(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', ')', ']':
		return true
	}
	return false
}

func isIDChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '.', c == '-', c == '_':
		return true
	}
	return false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// containsToken finds needle in hay as a whole token, so cluster-1.nodes never matches inside
// cluster-1.nodesNotReady.
func containsToken(hay, needle string) bool {
	if needle == "" {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); {
		j := strings.Index(hay[i:], needle)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(needle)
		leftOK := start == 0 || !isIDChar(hay[start-1])
		rightOK := end == len(hay) || !isIDChar(hay[end])
		if leftOK && rightOK {
			return true
		}
		i = start + 1
	}
	return false
}

// numberRuns lists every number written inside one token, so 512Mi yields 512 and 0.98 yields
// itself.
func numberRuns(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		if !isDigit(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) {
			if isDigit(s[j]) {
				j++
				continue
			}
			if s[j] == '.' && j+1 < len(s) && isDigit(s[j+1]) {
				j++
				continue
			}
			break
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}

// numberIn reports whether a number appears in a fact as a number, so 2 never matches inside
// 256Mi while 512 still matches inside 512Mi. The digits of a placeholder such as wl-2 or
// ns-1 are part of a name, never a number a sentence may rest on.
func numberIn(hay, tok string) bool {
	if tok == "" {
		return false
	}
	for i := 0; i+len(tok) <= len(hay); {
		j := strings.Index(hay[i:], tok)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(tok)
		leftOK := start == 0 || (!isDigit(hay[start-1]) && hay[start-1] != '.')
		rightOK := end == len(hay) || (!isDigit(hay[end]) && hay[end] != '.')
		if leftOK && rightOK && !placeholderShaped(tokenAround(hay, start, end)) {
			return true
		}
		i = start + 1
	}
	return false
}

// tokenAround widens [start, end) to the whole identifier it sits in.
func tokenAround(s string, start, end int) string {
	for start > 0 && isIDChar(s[start-1]) {
		start--
	}
	for end < len(s) && isIDChar(s[end]) {
		end++
	}
	return s[start:end]
}

// placeholderShaped reports whether a token is letters, one hyphen, digits: the shape every
// placeholder has, and one a version or an image name never has.
func placeholderShaped(tok string) bool {
	letters, hyphen, digits := 0, 0, 0
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		switch {
		case (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') && hyphen == 0:
			letters++
		case c == '-' && letters > 0 && hyphen == 0:
			hyphen++
		case isDigit(c) && hyphen == 1:
			digits++
		default:
			return false
		}
	}
	return letters > 0 && hyphen == 1 && digits > 0
}

// Narrator produces the optional prose for one payload. A nil narrative is a valid answer and
// simply means the report is rendered without prose.
type Narrator interface {
	Narrate(ctx context.Context, pl Payload) (*result.Narrative, error)
}

// noop is the narrator of an offline run: it sends nothing and returns nothing.
type noop struct{}

func (noop) Narrate(context.Context, Payload) (*result.Narrative, error) { return nil, nil }

// wire is the vendor specific half of an HTTP narrator. Implementations live in a build
// tagged file, so an air gapped build never links a vendor client it must not use.
type wire interface {
	name() string
	url(base string) string
	body(model, prompt string) ([]byte, error)
	headers(key string) map[string]string
	text(raw []byte) (string, error)
}

// joinURL appends a backend path to a configured base, and leaves a base that already carries
// that path alone.
func joinURL(base, suffix string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if suffix == "" || strings.HasSuffix(b, suffix) {
		return b
	}
	return b + suffix
}

// New builds a narrator. Backend "none", which is the default, returns a narrator that sends
// nothing, so a caller never has to special case the offline run.
func New(o Options) (Narrator, error) {
	backend := strings.ToLower(strings.TrimSpace(o.Backend))
	if backend == "" || backend == "none" {
		return noop{}, nil
	}
	w, err := newWire(backend)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(o.BaseURL) == "" {
		return nil, fmt.Errorf("narrate: backend %q needs a base URL", backend)
	}
	u, err := url.Parse(strings.TrimSpace(o.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("narrate: base URL is not usable: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("narrate: base URL must be http or https, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("narrate: base URL %q names no host", strings.TrimSpace(o.BaseURL))
	}
	if strings.TrimSpace(o.Model) == "" {
		return nil, fmt.Errorf("narrate: backend %q needs a model", backend)
	}
	if strings.TrimSpace(o.APIKeyEnv) == "" {
		return nil, fmt.Errorf("narrate: backend %q needs the name of the environment variable holding the API key", backend)
	}
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	o.Backend = backend
	client := &http.Client{
		Timeout: o.Timeout,
		// A redirect would carry the body and the key to whatever host the endpoint names,
		// past the allowlist. A model endpoint has no reason to redirect, so none is followed.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("the endpoint redirected to host %q, refusing to follow a redirect", req.URL.Hostname())
		},
	}
	return &httpNarrator{opts: o, w: w, client: client}, nil
}

// httpNarrator is the only code path that can send anything off the machine, and it refuses
// unless the resolved host was named in the allowlist.
type httpNarrator struct {
	opts   Options
	w      wire
	client *http.Client
}

func (n *httpNarrator) Narrate(ctx context.Context, pl Payload) (*result.Narrative, error) {
	prompt := Prompt(pl)
	if n.opts.ShowPrompt {
		fmt.Fprintln(os.Stderr, "spanline prompt, exactly as it would be sent:")
		fmt.Fprintln(os.Stderr, prompt)
	}

	// The allowlist is checked on the URL that is actually requested, not on the base it was
	// derived from, and before the key is read: a refusal never touches the environment.
	endpoint := n.w.url(n.opts.BaseURL)
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("narrate: base URL is not usable: %w", err)
	}
	if err := allowHost(u, n.opts.AllowHosts); err != nil {
		return nil, err
	}

	key := strings.TrimSpace(os.Getenv(n.opts.APIKeyEnv))
	if key == "" {
		return nil, fmt.Errorf("narrate: environment variable %s is empty, no API key to send", n.opts.APIKeyEnv)
	}
	if !usableKey(key) {
		return nil, fmt.Errorf("narrate: environment variable %s holds a value with control characters, refusing to send it", n.opts.APIKeyEnv)
	}

	payload, err := n.w.body(n.opts.Model, prompt)
	if err != nil {
		return nil, fmt.Errorf("narrate: could not build the %s request: %w", n.w.name(), err)
	}

	callCtx, cancel := context.WithTimeout(ctx, n.opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("narrate: could not build the %s request: %w", n.w.name(), err)
	}
	for k, v := range n.w.headers(key) {
		req.Header.Set(k, v)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("narrate: the %s call failed: %w", n.w.name(), err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return nil, fmt.Errorf("narrate: could not read the %s answer: %w", n.w.name(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("narrate: the %s endpoint returned HTTP %d", n.w.name(), resp.StatusCode)
	}

	answer, err := n.w.text(raw)
	if err != nil {
		return nil, fmt.Errorf("narrate: could not read the %s answer: %w", n.w.name(), err)
	}

	kept, dropped := Validate(answer, pl)
	return &result.Narrative{
		Text:       kept,
		Model:      n.opts.Model,
		Citations:  citations(kept, pl),
		Dropped:    dropped,
		PromptSHA:  sha256Hex(prompt),
		AnswerSHA:  sha256Hex(answer),
		Unverified: true,
	}, nil
}

// allowHost is the egress gate. An empty allowlist refuses every host, because an operator who
// never named an endpoint never agreed to any egress. An entry is a host name or IP, matched
// whole and case insensitively, on any port; an entry that carries a port matches that port
// only. Nothing is resolved, nothing is matched by suffix.
func allowHost(u *url.URL, allow []string) error {
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("narrate: the endpoint names no host, refusing to send")
	}
	if len(allow) == 0 {
		return fmt.Errorf("narrate: no host is in the allowlist, refusing to send anything to %q", host)
	}
	for _, entry := range allow {
		e := strings.TrimSpace(entry)
		if e == "" {
			continue
		}
		// An IPv6 literal may be written with or without its brackets.
		bare := strings.TrimSuffix(strings.TrimPrefix(e, "["), "]")
		if strings.EqualFold(bare, host) || strings.EqualFold(e, u.Host) {
			return nil
		}
	}
	return fmt.Errorf("narrate: host %q is not in the allowlist %v, refusing to send", host, allow)
}

// usableKey refuses a key that could not be a header value, so a bad paste never reaches the
// wire and never has to be quoted back in an error.
func usableKey(key string) bool {
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] == 0x7f {
			return false
		}
	}
	return true
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
