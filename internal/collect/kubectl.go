package collect

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/arnocho/spanline/internal/model"
)

// allowed is the whole set of kubectl invocations spanline may run: a verb, and under config
// and auth the exact subcommands, because those two verbs also carry writes (config set,
// config use-context and config set-credentials rewrite the kubeconfig, auth reconcile writes
// RBAC into the cluster). A nil list means the verb is complete on its own. Anything absent is
// refused before a process starts.
var allowed = map[string][]string{
	"get":           nil,
	"version":       nil,
	"api-resources": nil,
	"config":        {"view", "get-contexts", "current-context"},
	"auth":          {"can-i", "whoami"},
}

// refusedFlags turn a read into something else, so they are refused wherever they appear:
// --raw makes get fetch any API path, Secrets included, and makes config view print
// credentials; --as, --as-group and --as-uid impersonate, and spanline never impersonates.
var refusedFlags = []string{"--raw", "--as", "--as-group", "--as-uid"}

// valueFlags are the kubectl flags spanline knows take a separate value, so that value is never
// mistaken for a resource name: "-n secrets" names a namespace, not a Secret. A flag missing
// from this list makes its value look positional, which can only refuse more, never less.
var valueFlags = map[string]bool{
	"-n": true, "--namespace": true, "-o": true, "--output": true, "-l": true, "--selector": true,
	"--context": true, "--cluster": true, "--user": true, "--kubeconfig": true, "-s": true,
	"--server": true, "--field-selector": true, "--sort-by": true, "--template": true,
	"--chunk-size": true, "--request-timeout": true, "-L": true, "--label-columns": true,
	"--subresource": true, "--cache-dir": true, "--certificate-authority": true,
	"--client-certificate": true, "--client-key": true, "--tls-server-name": true, "--token": true,
	"--username": true, "--password": true, "--profile": true, "--profile-output": true,
	"-f": true, "--filename": true, "-k": true, "--kustomize": true, "-v": true, "--v": true,
	"--vmodule": true, "--log-flush-frequency": true,
}

// ErrForbiddenVerb is returned when calling code tries to run anything outside the allowlist.
type ErrForbiddenVerb struct{ Verb string }

func (e ErrForbiddenVerb) Error() string {
	return fmt.Sprintf("refusing to run kubectl %s: spanline only runs get (never Secrets), "+
		"config view, config get-contexts, config current-context, auth can-i, auth whoami, "+
		"version and api-resources, never --raw and never impersonation", e.Verb)
}

// checkArgs enforces the allowlist. It never runs anything.
func checkArgs(args []string) error {
	if len(args) == 0 {
		return ErrForbiddenVerb{Verb: "(none)"}
	}
	verb := args[0]
	subs, ok := allowed[verb]
	if !ok {
		return ErrForbiddenVerb{Verb: verb}
	}
	rest := args[1:]
	if subs != nil {
		if len(rest) == 0 {
			return ErrForbiddenVerb{Verb: verb + " (no subcommand)"}
		}
		if !contains(subs, rest[0]) {
			return ErrForbiddenVerb{Verb: verb + " " + rest[0]}
		}
		rest = rest[1:]
	}
	for _, a := range rest {
		for _, f := range refusedFlags {
			if a == f || strings.HasPrefix(a, f+"=") {
				return ErrForbiddenVerb{Verb: verb + " " + a}
			}
		}
	}
	if verb == "get" {
		for _, res := range positionals(rest) {
			if namesSecrets(res) {
				return ErrForbiddenVerb{Verb: "get " + res}
			}
		}
	}
	return nil
}

// positionals returns the arguments that are not flags and not the value of a flag.
func positionals(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && valueFlags[a] {
				i++
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// namesSecrets reports whether a resource argument, in any of kubectl's spellings (secrets,
// secret/name, secrets.v1., pods,secrets), reaches the Secret resource.
func namesSecrets(res string) bool {
	for _, part := range strings.Split(res, ",") {
		typ := strings.ToLower(strings.TrimSpace(part))
		if i := strings.Index(typ, "/"); i >= 0 {
			typ = typ[:i]
		}
		if i := strings.Index(typ, "."); i >= 0 {
			typ = typ[:i]
		}
		if typ == "secret" || typ == "secrets" {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// KubectlSource reads a live cluster through the kubectl already on the operator's PATH,
// reusing their existing credentials and exec plugins. Nothing is installed anywhere.
type KubectlSource struct {
	Binary  string
	Timeout time.Duration
	now     time.Time
}

// NewKubectlSource builds a read-only source. An empty binary means "kubectl".
func NewKubectlSource(binary string, timeout time.Duration, now time.Time) *KubectlSource {
	if binary == "" {
		binary = "kubectl"
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &KubectlSource{Binary: binary, Timeout: timeout, now: now}
}

func (k *KubectlSource) Describe() string {
	return fmt.Sprintf("%s, read-only allowlist: get (never Secrets), config view, config get-contexts, "+
		"config current-context, auth can-i, auth whoami, version, api-resources", k.Binary)
}

func (k *KubectlSource) Now() time.Time { return k.now }

func (k *KubectlSource) Extra(rel string) ([]byte, error) {
	return nil, fmt.Errorf("a live cluster source carries no extra file %q", rel)
}

// Run executes one allowlisted kubectl invocation and returns stdout. When the command fails,
// stdout still travels with the error, because kubectl auth can-i answers "no" with exit
// status 1 and a caller must be able to tell that answer from a check that never ran.
func (k *KubectlSource) Run(ctx context.Context, args ...string) ([]byte, error) {
	if err := checkArgs(args); err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, k.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, k.Binary, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s %s: %w: %s", k.Binary, strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func (k *KubectlSource) Contexts() ([]string, error) {
	out, err := k.Run(context.Background(), "config", "get-contexts", "-o", "name")
	if err != nil {
		return nil, err
	}
	var ctxs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			ctxs = append(ctxs, line)
		}
	}
	return ctxs, nil
}

// scope says how the namespace filter applies to one resource.
type scope int

const (
	// clusterScope objects carry no namespace flag at all.
	clusterScope scope = iota
	// namespaceScope objects are read in the selected namespace, or in every namespace.
	namespaceScope
	// everyNamespace objects are always read across namespaces: an Argo application lives in
	// its own namespace and names the workload's, so a namespace filter would lose it.
	everyNamespace
)

// kubectlResource maps a snapshot resource to its kubectl name and scope.
type kubectlResource struct {
	name       string
	kubectlArg string
	scope      scope
}

var kubectlResources = []kubectlResource{
	{"nodes", "nodes", clusterScope},
	{"pods", "pods", namespaceScope},
	{"replicasets", "replicasets", namespaceScope},
	{"controllerrevisions", "controllerrevisions", namespaceScope},
	{"deployments", "deployments", namespaceScope},
	{"statefulsets", "statefulsets", namespaceScope},
	{"daemonsets", "daemonsets", namespaceScope},
	{"events", "events", namespaceScope},
	{"pdbs", "poddisruptionbudgets", namespaceScope},
	{"pvcs", "persistentvolumeclaims", namespaceScope},
	{"pvs", "persistentvolumes", clusterScope},
	{"argoapps", "applications.argoproj.io", everyNamespace},
}

// eventsNewGroup is the same event storage served under events.k8s.io, read only when the core
// group refuses, so a role that grants one group and not the other still yields the events.
var eventsNewGroup = kubectlResource{"events", "events.events.k8s.io", namespaceScope}

func getArgs(r kubectlResource, kubeContext, namespace string) []string {
	args := []string{"get", r.kubectlArg, "-o", "json"}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	switch r.scope {
	case namespaceScope:
		if namespace != "" {
			args = append(args, "-n", namespace)
		} else {
			args = append(args, "--all-namespaces")
		}
	case everyNamespace:
		args = append(args, "--all-namespaces")
	}
	return args
}

// Snapshot reads every resource once. A read that fails or cannot be decoded is a coverage gap
// and leaves nothing behind for that resource, so a snapshot never carries half a list next to
// a gap that says the list was unreadable.
func (k *KubectlSource) Snapshot(ctx context.Context, kubeContext, namespace string) (*model.Snapshot, error) {
	snap := &model.Snapshot{Context: kubeContext, CollectedAt: time.Now().UTC(), Namespace: namespace}
	for _, r := range kubectlResources {
		out, err := k.Run(ctx, getArgs(r, kubeContext, namespace)...)
		if err != nil {
			if r.name == "events" {
				snap.Events, err = k.eventsFallback(ctx, kubeContext, namespace, err)
				if err != nil {
					snap.Gaps = append(snap.Gaps, model.CoverageGap{Resource: r.name, Reason: err.Error()})
				}
				continue
			}
			snap.Gaps = append(snap.Gaps, model.CoverageGap{Resource: r.name, Reason: shortError(err)})
			continue
		}
		if err := assign(snap, r.name, out); err != nil {
			snap.Gaps = append(snap.Gaps, model.CoverageGap{Resource: r.name, Reason: "unreadable response: " + err.Error()})
		}
	}
	setKinds(snap)
	return snap, nil
}

// eventsFallback reads events.k8s.io after the core group refused, and folds the answer into
// the core shape. The error it returns is the gap reason, naming both attempts.
func (k *KubectlSource) eventsFallback(ctx context.Context, kubeContext, namespace string, coreErr error) ([]model.Event, error) {
	out, err := k.Run(ctx, getArgs(eventsNewGroup, kubeContext, namespace)...)
	if err != nil {
		return nil, fmt.Errorf("%s; events.k8s.io: %s", shortError(coreErr), shortError(err))
	}
	events, err := decodeNewEvents(out)
	if err != nil {
		return nil, fmt.Errorf("%s; events.k8s.io: unreadable response: %s", shortError(coreErr), err.Error())
	}
	return events, nil
}

// newEvent is the events.k8s.io/v1 shape: regarding for involvedObject, note for message, an
// eventTime and a series in place of the first and last timestamps and the count.
type newEvent struct {
	Metadata  model.ObjectMeta     `json:"metadata"`
	Regarding model.InvolvedObject `json:"regarding"`
	Reason    string               `json:"reason"`
	Note      string               `json:"note"`
	Type      string               `json:"type"`
	EventTime time.Time            `json:"eventTime"`
	Series    *struct {
		Count            int       `json:"count"`
		LastObservedTime time.Time `json:"lastObservedTime"`
	} `json:"series"`
	DeprecatedCount          int       `json:"deprecatedCount"`
	DeprecatedFirstTimestamp time.Time `json:"deprecatedFirstTimestamp"`
	DeprecatedLastTimestamp  time.Time `json:"deprecatedLastTimestamp"`
}

func decodeNewEvents(raw []byte) ([]model.Event, error) {
	var items []newEvent
	if err := decodeInto(raw, &items); err != nil {
		return nil, err
	}
	out := make([]model.Event, 0, len(items))
	for _, e := range items {
		first := e.EventTime
		if first.IsZero() {
			first = e.DeprecatedFirstTimestamp
		}
		last := e.DeprecatedLastTimestamp
		count := e.DeprecatedCount
		if e.Series != nil {
			if e.Series.Count > 0 {
				count = e.Series.Count
			}
			if !e.Series.LastObservedTime.IsZero() {
				last = e.Series.LastObservedTime
			}
		}
		if last.IsZero() {
			last = first
		}
		if count == 0 {
			count = 1
		}
		out = append(out, model.Event{
			Metadata:       e.Metadata,
			InvolvedObject: e.Regarding,
			Reason:         e.Reason,
			Message:        e.Note,
			Type:           e.Type,
			Count:          count,
			FirstTimestamp: first,
			LastTimestamp:  last,
		})
	}
	return out, nil
}

func shortError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "forbidden"):
		return "forbidden for this identity"
	case strings.Contains(msg, "the server doesn't have a resource type"):
		return "resource type absent from this cluster"
	case strings.Contains(msg, "context deadline exceeded"):
		return "timed out"
	}
	if len(msg) > 120 {
		return msg[:120] + "..."
	}
	return msg
}
