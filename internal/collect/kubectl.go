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

// allowedVerbs is the whole set of kubectl subcommands spanline may run.
// Anything that can change a cluster is absent by construction, and refused at run time.
var allowedVerbs = map[string]bool{
	"get":           true,
	"version":       true,
	"config":        true,
	"auth":          true,
	"api-resources": true,
}

// ErrForbiddenVerb is returned when calling code tries to run a non-read subcommand.
type ErrForbiddenVerb struct{ Verb string }

func (e ErrForbiddenVerb) Error() string {
	return fmt.Sprintf("refusing to run kubectl %s: spanline only runs read-only subcommands", e.Verb)
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
	return fmt.Sprintf("%s, read-only subcommand allowlist (get, version, config, auth, api-resources)", k.Binary)
}

func (k *KubectlSource) Now() time.Time { return k.now }

func (k *KubectlSource) Extra(rel string) ([]byte, error) {
	return nil, fmt.Errorf("a live cluster source carries no extra file %q", rel)
}

// Run executes one allowlisted kubectl invocation and returns stdout.
func (k *KubectlSource) Run(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, ErrForbiddenVerb{Verb: "(none)"}
	}
	verb := args[0]
	if !allowedVerbs[verb] {
		return nil, ErrForbiddenVerb{Verb: verb}
	}
	cctx, cancel := context.WithTimeout(ctx, k.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, k.Binary, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", k.Binary, strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
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

// kubectlResource maps a snapshot resource to its kubectl name and scope.
type kubectlResource struct {
	name       string
	kubectlArg string
	namespaced bool
}

var kubectlResources = []kubectlResource{
	{"nodes", "nodes", false},
	{"pods", "pods", true},
	{"replicasets", "replicasets", true},
	{"controllerrevisions", "controllerrevisions", true},
	{"deployments", "deployments", true},
	{"statefulsets", "statefulsets", true},
	{"daemonsets", "daemonsets", true},
	{"events", "events", true},
	{"pdbs", "poddisruptionbudgets", true},
	{"pvcs", "persistentvolumeclaims", true},
	{"pvs", "persistentvolumes", false},
	{"argoapps", "applications.argoproj.io", true},
}

func (k *KubectlSource) Snapshot(ctx context.Context, kubeContext, namespace string) (*model.Snapshot, error) {
	snap := &model.Snapshot{Context: kubeContext, CollectedAt: time.Now().UTC(), Namespace: namespace}
	for _, r := range kubectlResources {
		args := []string{"get", r.kubectlArg, "-o", "json"}
		if kubeContext != "" {
			args = append(args, "--context", kubeContext)
		}
		if r.namespaced {
			if namespace != "" {
				args = append(args, "-n", namespace)
			} else {
				args = append(args, "--all-namespaces")
			}
		}
		out, err := k.Run(ctx, args...)
		if err != nil {
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
