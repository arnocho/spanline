// Package doctor answers the two questions an operator and a client security team ask first:
// what identity is this running as, and exactly what will it touch.
package doctor

import (
	"context"
	"encoding/json"
	"net/url"
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

// tools are the external binaries a security team may ask about. Only kubectl is ever
// executed; the others are listed so their presence on the machine is stated, not implied.
var tools = []struct{ name, why string }{
	{"kubectl", "the one binary spanline runs, read subcommands only"},
	{"git", "not called by spanline"},
	{"helm", "not called by spanline: Helm ownership is read from annotations, never from release storage"},
	{"argocd", "not called by spanline: Argo applications are read through kubectl like any other object"},
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
		if o.Explain {
			// The narrator does not care where the report came from: a payload may still leave.
			rep.Egress = []string{"no cluster: every read is replayed from recorded data", explainEgress(o)}
		} else {
			rep.Egress = []string{"none: no cluster, no network, nothing leaves this machine"}
		}
		rep.AuditFoot = []string{"none: no API call is made"}
		rep.Gaps = append(rep.Gaps, "every verdict comes from recorded data, not from a live cluster")
		return rep
	}

	rep.Identity = whoami(k, o.Context)
	rep.Checks = append(rep.Checks, result.Check{Name: "source", Status: "live", Detail: src.Describe()})

	// Each probe mirrors a read the collector performs, with the same scope: Argo applications
	// are read across namespaces because they live apart from the workloads they name.
	probes := []struct {
		name, verb, resource string
		everyNamespace       bool
	}{
		{"read pods", "get", "pods", false},
		{"list nodes", "list", "nodes", false},
		{"list replicasets", "list", "replicasets", false},
		{"list events", "list", "events", false},
		{"list disruption budgets", "list", "poddisruptionbudgets", false},
		{"list persistent volumes", "list", "persistentvolumes", false},
		{"read Argo applications", "list", "applications.argoproj.io", true},
	}
	var argo answer
	for _, p := range probes {
		a := canI(k, o.Context, o.Namespace, p.verb, p.resource, p.everyNamespace)
		if p.resource == "applications.argoproj.io" {
			argo = a
		}
		rep.Checks = append(rep.Checks, result.Check{Name: p.name, Status: a.status, Detail: a.detail})
		switch a.status {
		case "no":
			rep.Gaps = append(rep.Gaps, p.name+" is refused for this identity, so anything derived from it is NOT ASSESSED")
		case "unknown":
			rep.Gaps = append(rep.Gaps, p.name+" could not be checked ("+a.detail+"), so anything derived from it is NOT ASSESSED")
		}
	}

	// A probe that did not run is not a no. The report fails closed: an unverified identity
	// is reported as able to write and to read Secrets, and the check says why.
	write := either(canI(k, o.Context, o.Namespace, "delete", "pods", false),
		canI(k, o.Context, o.Namespace, "patch", "deployments", false))
	rep.CanWrite = write.status != "no"
	writeDetail := "this identity cannot change the cluster"
	switch write.status {
	case "yes":
		writeDetail = "this identity could write: spanline still never will, and has no code path that does"
	case "unknown":
		writeDetail = "not verified (" + write.detail + "): treated as able to write until a check succeeds"
	}
	rep.Checks = append(rep.Checks, result.Check{Name: "write capability", Status: write.status, Detail: writeDetail})

	sec := canI(k, o.Context, o.Namespace, "list", "secrets", false)
	rep.CanReadSec = sec.status != "no"
	secDetail := "this identity cannot read Secrets"
	switch sec.status {
	case "yes":
		secDetail = "this identity could read Secrets: spanline never asks for them"
	case "unknown":
		secDetail = "not verified (" + sec.detail + "): treated as able to read Secrets until a check succeeds"
	}
	rep.Checks = append(rep.Checks, result.Check{Name: "secret read capability", Status: sec.status, Detail: secDetail})
	if write.status == "unknown" || sec.status == "unknown" {
		rep.Gaps = append(rep.Gaps, "the write and Secret capabilities could not be checked, so they are reported as present, not as absent")
	}

	for _, t := range tools {
		status := "absent"
		if _, err := exec.LookPath(t.name); err == nil {
			status = "present"
		}
		rep.Checks = append(rep.Checks, result.Check{Name: "tool " + t.name, Status: status, Detail: t.why})
	}

	rep.Level = 0
	if argo.status == "yes" {
		rep.Level = 1
	}

	rep.Egress = []string{
		"cockpit, why, impact: the Kubernetes API of the selected contexts",
		"attribution when Argo or Flux is readable: the same API, nothing else",
		explainEgress(o),
		"nothing else: no telemetry, no update check",
	}

	rep.AuditFoot = []string{
		"get and list on the resources listed above, under your own identity",
		"create on selfsubjectaccessreviews and selfsubjectrulesreviews, which is how a permission check asks",
		"no other write appears, because no other write is reachable",
	}
	return rep
}

// explainEgress states the narrator's egress. Doctor does not see the allowlist, so it never
// claims the host is allowlisted: the narrator refuses by itself when it is not.
func explainEgress(o Options) string {
	if !o.Explain {
		return "explain: off, no model endpoint is contacted"
	}
	host := narratorHost(o.LLMHost)
	if host == "" {
		return "explain: on, but no endpoint is configured, so nothing is sent"
	}
	return "explain: on, the one model endpoint that may be contacted is " + host + ", and only if it is named in --allow-host"
}

// narratorHost reduces whatever the caller passed, a URL or a bare host, to the host and port.
func narratorHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// answer is what one permission check said: yes, no, or unknown when the check itself did not
// run, which must never be read as a no.
type answer struct {
	status string // "yes", "no" or "unknown"
	detail string
}

// either combines two write probes: any yes is a yes, otherwise any unknown is unknown.
func either(a, b answer) answer {
	switch {
	case a.status == "yes":
		return a
	case b.status == "yes":
		return b
	case a.status == "unknown":
		return a
	case b.status == "unknown":
		return b
	}
	return a
}

func whoami(k *collect.KubectlSource, kubeContext string) string {
	withContext := func(args ...string) []string {
		if kubeContext != "" {
			args = append(args, "--context", kubeContext)
		}
		return args
	}
	// The structured answer first: a username is an arbitrary string and may contain spaces.
	if out, err := k.Run(context.Background(), withContext("auth", "whoami", "-o", "json")...); err == nil {
		var review struct {
			Status struct {
				UserInfo struct {
					Username string `json:"username"`
				} `json:"userInfo"`
			} `json:"status"`
		}
		if json.Unmarshal(out, &review) == nil {
			if name := strings.TrimSpace(review.Status.UserInfo.Username); name != "" {
				return name
			}
		}
	}
	// An older kubectl prints a table: the value is the rest of the Username row.
	if out, err := k.Run(context.Background(), withContext("auth", "whoami")...); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && strings.EqualFold(f[0], "Username") {
				if name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), f[0])); name != "" {
					return name
				}
			}
		}
	}
	// Without whoami only the kubeconfig entry is known, which is a name, not the API identity.
	args := withContext("config", "view", "--minify", "-o", "jsonpath={.contexts[0].context.user}")
	if out, err := k.Run(context.Background(), args...); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return "kubeconfig user " + s + " (auth whoami unavailable, so this is the kubeconfig entry, not the API identity)"
		}
	}
	return "unknown"
}

// canI asks the API through kubectl auth can-i. kubectl answers "no" with exit status 1, so the
// output is read before the error: only an empty or unreadable answer is unknown.
func canI(k *collect.KubectlSource, kubeContext, namespace, verb, resource string, everyNamespace bool) answer {
	args := []string{"auth", "can-i", verb, resource}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	switch {
	case everyNamespace:
		args = append(args, "--all-namespaces")
	case namespace != "":
		args = append(args, "-n", namespace)
	}
	out, err := k.Run(context.Background(), args...)
	reply := strings.ToLower(strings.TrimSpace(string(out)))
	switch {
	case strings.HasPrefix(reply, "yes"):
		return answer{status: "yes"}
	case strings.HasPrefix(reply, "no"):
		return answer{status: "no"}
	case err != nil:
		return answer{status: "unknown", detail: "the permission check did not run: " + shortReason(err)}
	}
	return answer{status: "unknown", detail: "the permission check gave an unreadable answer"}
}

// shortReason keeps the reason of a failed check to one readable line.
func shortReason(err error) string {
	msg := strings.TrimSpace(err.Error())
	if i := strings.LastIndex(msg, ": "); i >= 0 && i+2 < len(msg) {
		msg = msg[i+2:]
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 100 {
		return msg[:100] + "..."
	}
	return msg
}
