package collect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeKubectl writes a shell script that records every invocation, one line per call, and
// answers from the files in answers: "<arg1>_<arg2>_<arg3>_<arg4>.out" is printed on stdout with
// exit 0, ".no" is printed on stdout with exit 1 (how kubectl auth can-i says no), ".err" is
// printed on stderr with exit 1, and shorter keys are tried in turn down to "default.out".
// Nothing real is ever executed.
func fakeKubectl(t *testing.T, answers map[string]string) (bin, record string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range answers {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	record = filepath.Join(dir, "record.txt")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + record + `"
d="` + dir + `"
for key in "${1}_${2}_${3}_${4}" "${1}_${2}_${3}" "${1}_${2}" "${1}"; do
  k=$(printf '%s' "$key" | tr '/:' '__')
  if [ -f "$d/$k.out" ]; then cat "$d/$k.out"; exit 0; fi
  if [ -f "$d/$k.no" ]; then cat "$d/$k.no"; exit 1; fi
  if [ -f "$d/$k.err" ]; then cat "$d/$k.err" >&2; exit 1; fi
done
if [ -f "$d/default.out" ]; then cat "$d/default.out"; exit 0; fi
echo "fake kubectl: no answer for: $*" >&2
exit 1
`
	bin = filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

// recorded returns every invocation the fake kubectl saw, in order.
func recorded(t *testing.T, record string) []string {
	t.Helper()
	b, err := os.ReadFile(record)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func TestRunRefusesEverythingOutsideTheReadAllowlist(t *testing.T) {
	bin, record := fakeKubectl(t, map[string]string{"default.out": "should never be printed"})
	k := NewKubectlSource(bin, 10*time.Second, time.Time{})

	refused := [][]string{
		nil,
		{"apply", "-f", "x.yaml"},
		{"delete", "pod", "x"},
		{"patch", "deployment", "x", "-p", "{}"},
		{"exec", "x", "--", "sh"},
		{"cp", "x:/etc/passwd", "."},
		{"proxy"},
		{"port-forward", "x", "8080:80"},
		{"edit", "deployment", "x"},
		{"scale", "deployment", "x", "--replicas=0"},
		{"drain", "node-1"},
		{"cordon", "node-1"},
		{"GET", "pods"},
		{"kubectl-plugin"},
		{"krew", "install", "x"},
		// config: only view, get-contexts and current-context read; the rest writes the kubeconfig.
		{"config"},
		{"config", "set", "contexts.x.namespace", "y"},
		{"config", "use-context", "x"},
		{"config", "set-context", "x"},
		{"config", "set-credentials", "u", "--token=t"},
		{"config", "set-cluster", "c"},
		{"config", "delete-context", "x"},
		{"config", "rename-context", "a", "b"},
		{"config", "unset", "x"},
		{"config", "--kubeconfig", "x", "view"},
		{"config", "view", "--raw"},
		// auth: only can-i and whoami read; reconcile writes RBAC.
		{"auth"},
		{"auth", "reconcile", "-f", "rbac.yaml"},
		{"auth", "can-i", "list", "pods", "--as", "system:admin"},
		// get: never a raw API path, never a Secret, never impersonation.
		{"get", "--raw", "/api/v1/namespaces/x/secrets"},
		{"get", "--raw=/api/v1/secrets"},
		{"get", "secrets"},
		{"get", "secret", "db"},
		{"get", "secret/db"},
		{"get", "secrets/db", "-o", "json"},
		{"get", "secrets.v1.", "-A"},
		{"get", "Secrets", "-n", "x"},
		{"get", "pods,secrets"},
		{"get", "pods", "--as", "admin"},
		{"get", "pods", "--as=admin"},
		{"get", "pods", "--as-group", "system:masters"},
		{"get", "pods", "--as-uid", "1"},
		{"version", "--as", "admin"},
		// a flag before the verb is not a verb.
		{"--context", "x", "get", "pods"},
		{"-n", "x", "get", "pods"},
	}
	for _, args := range refused {
		out, err := k.Run(context.Background(), args...)
		var forbidden ErrForbiddenVerb
		if !errors.As(err, &forbidden) {
			t.Errorf("Run(%q) = %v, want ErrForbiddenVerb", args, err)
		}
		if out != nil {
			t.Errorf("Run(%q) returned output %q on a refusal", args, out)
		}
	}
	if got := recorded(t, record); len(got) != 0 {
		t.Fatalf("a refused invocation still ran kubectl: %q", got)
	}
}

func TestRunAllowsExactlyTheReadSubcommands(t *testing.T) {
	bin, record := fakeKubectl(t, map[string]string{"default.out": "ok\n"})
	k := NewKubectlSource(bin, 10*time.Second, time.Time{})

	allowed := [][]string{
		{"get", "pods", "-o", "json", "--context", "c1", "-n", "payments"},
		{"get", "pods", "-o", "json", "--all-namespaces"},
		{"get", "pods", "-n", "secrets"}, // a namespace merely named secrets is not a Secret read
		{"get", "events.events.k8s.io", "-o", "json", "-A"},
		{"get", "applications.argoproj.io", "-o", "json", "--all-namespaces"},
		{"get", "persistentvolumes", "-o", "json"},
		{"config", "view", "--minify", "-o", "jsonpath={.contexts[0].context.user}"},
		{"config", "get-contexts", "-o", "name"},
		{"config", "current-context"},
		{"auth", "can-i", "list", "pods", "-n", "payments"},
		{"auth", "can-i", "list", "applications.argoproj.io", "--all-namespaces"},
		{"auth", "whoami", "-o", "json"},
		{"version", "--client"},
		{"api-resources", "--verbs=list"},
	}
	for _, args := range allowed {
		out, err := k.Run(context.Background(), args...)
		if err != nil {
			t.Errorf("Run(%q) refused a read: %v", args, err)
			continue
		}
		if strings.TrimSpace(string(out)) != "ok" {
			t.Errorf("Run(%q) = %q, want the fake answer", args, out)
		}
	}
	got := recorded(t, record)
	if len(got) != len(allowed) {
		t.Fatalf("recorded %d invocations, want %d: %q", len(got), len(allowed), got)
	}
	for i, args := range allowed {
		if got[i] != strings.Join(args, " ") {
			t.Errorf("invocation %d = %q, want %q", i, got[i], strings.Join(args, " "))
		}
	}
}

const (
	oneNode = `{"items":[{"metadata":{"name":"node-1"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
	onePod  = `{"items":[{"metadata":{"name":"api-1","namespace":"payments"},"spec":{"nodeName":"node-1"}}]}`
	// One deployment whose replica count is not a number: the list cannot be trusted.
	badDeployments = `{"items":[{"metadata":{"name":"api","namespace":"payments"},"spec":{"replicas":"three"}},` +
		`{"metadata":{"name":"web","namespace":"payments"},"spec":{"replicas":2}}]}`
	coreEvents = `{"items":[{"metadata":{"name":"e1","namespace":"payments"},"involvedObject":{"kind":"Pod","name":"api-1","namespace":"payments"},` +
		`"reason":"OOMKilling","message":"killed","type":"Warning","count":2,"firstTimestamp":"2026-09-16T03:02:14Z","lastTimestamp":"2026-09-16T03:09:40Z"}]}`
	newEvents = `{"apiVersion":"events.k8s.io/v1","kind":"EventList","items":[{"apiVersion":"events.k8s.io/v1","kind":"Event",` +
		`"metadata":{"name":"e1","namespace":"payments"},"regarding":{"kind":"Pod","name":"checkout-1","namespace":"payments"},` +
		`"reason":"OOMKilling","note":"Memory cgroup out of memory","type":"Warning","eventTime":"2026-09-16T03:02:14.123456Z",` +
		`"series":{"count":3,"lastObservedTime":"2026-09-16T03:09:40.000000Z"},"reportingController":"kubelet","action":"Killing"}]}`
	empty = `{"items":[]}`
)

func TestSnapshotRecordsGapsAndNeverKeepsPartialData(t *testing.T) {
	bin, record := fakeKubectl(t, map[string]string{
		"get_nodes.out":                    oneNode,
		"get_pods.out":                     onePod,
		"get_deployments.out":              badDeployments,
		"get_poddisruptionbudgets.err":     "Error from server (Forbidden): poddisruptionbudgets.policy is forbidden: User \"jane\" cannot list resource",
		"get_events.out":                   coreEvents,
		"get_applications.argoproj.io.out": empty,
		"default.out":                      empty,
	})
	k := NewKubectlSource(bin, 10*time.Second, time.Time{})

	snap, err := k.Snapshot(context.Background(), "c1", "payments")
	if err != nil {
		t.Fatalf("Snapshot returned %v", err)
	}
	if snap.Context != "c1" || snap.Namespace != "payments" {
		t.Errorf("snapshot scope = %q %q, want c1 payments", snap.Context, snap.Namespace)
	}
	if len(snap.Nodes) != 1 || len(snap.Pods) != 1 || len(snap.Events) != 1 {
		t.Errorf("nodes %d pods %d events %d, want 1 each", len(snap.Nodes), len(snap.Pods), len(snap.Events))
	}

	gaps := map[string]string{}
	for _, g := range snap.Gaps {
		gaps[g.Resource] = g.Reason
	}
	if gaps["pdbs"] != "forbidden for this identity" {
		t.Errorf("pdbs gap = %q, want the forbidden reason", gaps["pdbs"])
	}
	if !strings.HasPrefix(gaps["deployments"], "unreadable response") {
		t.Errorf("deployments gap = %q, want an unreadable response", gaps["deployments"])
	}
	if len(snap.Deployments) != 0 {
		t.Errorf("an unreadable deployments list still left %d deployments in the snapshot next to its gap", len(snap.Deployments))
	}
	if len(gaps) != 2 {
		t.Errorf("gaps = %v, want exactly pdbs and deployments", snap.Gaps)
	}

	calls := recorded(t, record)
	want := map[string]bool{
		"get nodes -o json --context c1":                                     false,
		"get pods -o json --context c1 -n payments":                          false,
		"get persistentvolumes -o json --context c1":                         false,
		"get events -o json --context c1 -n payments":                        false,
		"get applications.argoproj.io -o json --context c1 --all-namespaces": false,
	}
	for _, c := range calls {
		if !strings.HasPrefix(c, "get ") {
			t.Errorf("Snapshot ran %q, which is not a get", c)
		}
		if strings.Contains(c, "secret") {
			t.Errorf("Snapshot asked for a Secret: %q", c)
		}
		if _, ok := want[c]; ok {
			want[c] = true
		}
	}
	for c, seen := range want {
		if !seen {
			t.Errorf("Snapshot never ran %q, calls were:\n%s", c, strings.Join(calls, "\n"))
		}
	}
}

func TestSnapshotReadsEventsFromTheOtherGroupWhenCoreEventsAreRefused(t *testing.T) {
	bin, record := fakeKubectl(t, map[string]string{
		"get_events.err":               "Error from server (Forbidden): events is forbidden: User \"jane\" cannot list resource \"events\" in API group \"\"",
		"get_events.events.k8s.io.out": newEvents,
		"default.out":                  empty,
	})
	k := NewKubectlSource(bin, 10*time.Second, time.Time{})

	snap, err := k.Snapshot(context.Background(), "c1", "")
	if err != nil {
		t.Fatalf("Snapshot returned %v", err)
	}
	for _, g := range snap.Gaps {
		if g.Resource == "events" {
			t.Fatalf("events were readable through events.k8s.io, yet a gap was recorded: %q", g.Reason)
		}
	}
	if len(snap.Events) != 1 {
		t.Fatalf("events = %d, want the one event read through events.k8s.io", len(snap.Events))
	}
	e := snap.Events[0]
	if e.InvolvedObject.Kind != "Pod" || e.InvolvedObject.Name != "checkout-1" || e.InvolvedObject.Namespace != "payments" {
		t.Errorf("involvedObject = %+v, want the regarding object", e.InvolvedObject)
	}
	if e.Reason != "OOMKilling" || e.Type != "Warning" || e.Message != "Memory cgroup out of memory" {
		t.Errorf("reason %q type %q message %q, want them copied from the events.k8s.io shape", e.Reason, e.Type, e.Message)
	}
	if e.Count != 3 {
		t.Errorf("count = %d, want the series count 3", e.Count)
	}
	if e.FirstTimestamp.IsZero() || !e.FirstTimestamp.Equal(time.Date(2026, 9, 16, 3, 2, 14, 123456000, time.UTC)) {
		t.Errorf("firstTimestamp = %v, want the eventTime", e.FirstTimestamp)
	}
	if !e.LastTimestamp.Equal(time.Date(2026, 9, 16, 3, 9, 40, 0, time.UTC)) {
		t.Errorf("lastTimestamp = %v, want the last observed time of the series", e.LastTimestamp)
	}
	sawFallback := false
	for _, c := range recorded(t, record) {
		if strings.HasPrefix(c, "get events.events.k8s.io -o json --context c1 --all-namespaces") {
			sawFallback = true
		}
	}
	if !sawFallback {
		t.Error("the events.k8s.io read was not attempted with the same scope flags")
	}

	// When both groups refuse, one gap names both.
	bin, _ = fakeKubectl(t, map[string]string{
		"get_events.err":               "Error from server (Forbidden): events is forbidden",
		"get_events.events.k8s.io.err": "Error from server (Forbidden): events.events.k8s.io is forbidden",
		"default.out":                  empty,
	})
	snap, err = NewKubectlSource(bin, 10*time.Second, time.Time{}).Snapshot(context.Background(), "c1", "")
	if err != nil {
		t.Fatalf("Snapshot returned %v", err)
	}
	var reasons []string
	for _, g := range snap.Gaps {
		if g.Resource == "events" {
			reasons = append(reasons, g.Reason)
		}
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "events.k8s.io") {
		t.Errorf("events gaps = %q, want one gap that names the events.k8s.io attempt too", reasons)
	}
}
