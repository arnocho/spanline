// Package doctor answers the two questions an operator and a client security team ask first:
// what identity is this running as, and exactly what will it touch.
package doctor

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/arnocho/spanline/internal/collect"
	"github.com/arnocho/spanline/internal/result"
)

// Options configures one doctor run.
type Options struct {
	Context   string
	Namespace string
	Now       time.Time
	Explain   bool
	LLMHost   string
	Profile   string // "standard" or "strict"
}

// tools are the external binaries spanline may call, all of them read-only.
var tools = []struct{ name, why string }{
	{"kubectl", "reads the cluster, read subcommands only"},
	{"git", "reads the environment repository for attribution"},
	{"helm", "renders a chart locally, never reads release storage"},
	{"argocd", "reads application history with your own session"},
	{"terraform", "not called by spanline, listed because its JSON output is read"},
	{"tofu", "not called by spanline, listed because its JSON output is read"},
}

// Run assembles the report. A missing tool or a refused read is a coverage gap, never a hard error.
func Run(src collect.Source, o Options) *result.DoctorReport {
	rep := &result.DoctorReport{Context: o.Context, Profile: o.Profile, GeneratedAt: o.Now}
	if rep.Profile == "" {
		rep.Profile = "standard"
	}

	k, live := src.(*collect.KubectlSource)
	if !live {
		rep.Identity = "none, replaying recorded fixtures"
		rep.Checks = append(rep.Checks, result.Check{Name: "source", Status: "fixtures", Detail: src.Describe()})
		rep.Egress = []string{"none: no cluster, no network, nothing leaves this machine"}
		rep.AuditFoot = []string{"none: no API call is made"}
		rep.Gaps = append(rep.Gaps, "every verdict comes from recorded data, not from a live cluster")
		return rep
	}

	rep.Identity = whoami(k, o.Context)
	rep.Checks = append(rep.Checks, result.Check{Name: "source", Status: "live", Detail: src.Describe()})

	probes := []struct{ name, verb, resource string }{
		{"read pods", "get", "pods"},
		{"list nodes", "list", "nodes"},
		{"list replicasets", "list", "replicasets"},
		{"list events", "list", "events"},
		{"list disruption budgets", "list", "poddisruptionbudgets"},
		{"list persistent volumes", "list", "persistentvolumes"},
		{"read Argo applications", "list", "applications.argoproj.io"},
	}
	for _, p := range probes {
		status := "no"
		if canI(k, o.Context, o.Namespace, p.verb, p.resource) {
			status = "yes"
		} else {
			rep.Gaps = append(rep.Gaps, p.name+" is refused for this identity, so anything derived from it is NOT ASSESSED")
		}
		rep.Checks = append(rep.Checks, result.Check{Name: p.name, Status: status})
	}

	rep.CanWrite = canI(k, o.Context, o.Namespace, "delete", "pods") ||
		canI(k, o.Context, o.Namespace, "patch", "deployments")
	rep.CanReadSec = canI(k, o.Context, o.Namespace, "list", "secrets")
	writeDetail := "this identity cannot change the cluster"
	if rep.CanWrite {
		writeDetail = "this identity could write: spanline still never will, and has no code path that does"
	}
	rep.Checks = append(rep.Checks, result.Check{Name: "write capability", Status: boolWord(rep.CanWrite), Detail: writeDetail})
	secDetail := "this identity cannot read Secrets"
	if rep.CanReadSec {
		secDetail = "this identity could read Secrets: spanline never asks for them"
	}
	rep.Checks = append(rep.Checks, result.Check{Name: "secret read capability", Status: boolWord(rep.CanReadSec), Detail: secDetail})

	for _, t := range tools {
		status := "absent"
		if _, err := exec.LookPath(t.name); err == nil {
			status = "present"
		}
		rep.Checks = append(rep.Checks, result.Check{Name: "tool " + t.name, Status: status, Detail: t.why})
	}

	rep.Level = 0
	if canI(k, o.Context, o.Namespace, "list", "applications.argoproj.io") {
		rep.Level = 1
	}

	rep.Egress = []string{
		"cockpit, why, impact: the Kubernetes API of the selected contexts",
		"attribution when Argo or Flux is readable: the same API, plus your git remote when you pass a repository",
	}
	if o.Explain && o.LLMHost != "" {
		rep.Egress = append(rep.Egress, "explain: "+o.LLMHost+", allowlisted explicitly")
	} else {
		rep.Egress = append(rep.Egress, "explain: off, no model endpoint is contacted")
	}
	rep.Egress = append(rep.Egress, "nothing else: no telemetry, no update check")

	rep.AuditFoot = []string{
		"get and list on the resources listed above, under your own identity",
		"create on selfsubjectaccessreviews and selfsubjectrulesreviews, which is how a permission check asks",
		"no other write appears, because no other write is reachable",
	}
	return rep
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func whoami(k *collect.KubectlSource, kubeContext string) string {
	args := []string{"auth", "whoami"}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	if out, err := k.Run(context.Background(), args...); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && strings.EqualFold(f[0], "Username") {
				return f[1]
			}
		}
	}
	args = []string{"config", "view", "--minify", "-o", "jsonpath={.contexts[0].context.user}"}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	if out, err := k.Run(context.Background(), args...); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	return "unknown"
}

func canI(k *collect.KubectlSource, kubeContext, namespace, verb, resource string) bool {
	args := []string{"auth", "can-i", verb, resource}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	out, err := k.Run(context.Background(), args...)
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(out)), "yes")
}
