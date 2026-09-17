package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arnocho/spanline/internal/collect"
	"github.com/arnocho/spanline/internal/result"
)

// fakeKubectl writes a shell script that records every invocation and answers from files, so
// no real kubectl and no cluster is ever touched. See the collect package for the key scheme:
// "<arg1>_<arg2>_<arg3>_<arg4>.out" on stdout with exit 0, ".no" on stdout with exit 1, ".err"
// on stderr with exit 1, shorter keys tried in turn, then "default.out".
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

func check(rep *result.DoctorReport, name string) (result.Check, bool) {
	for _, c := range rep.Checks {
		if c.Name == name {
			return c, true
		}
	}
	return result.Check{}, false
}

func anyLineContains(lines []string, needle string) bool {
	for _, l := range lines {
		if strings.Contains(l, needle) {
			return true
		}
	}
	return false
}

const whoamiJSON = `{"kind":"SelfSubjectReview","apiVersion":"authentication.k8s.io/v1",` +
	`"status":{"userInfo":{"username":"oidc:Jane Doe","groups":["system:authenticated"]}}}`

func TestFixtureRunNamesTheNarratorEgressOnlyWhenExplainIsOn(t *testing.T) {
	src, err := collect.NewFixtureSource("estate")
	if err != nil {
		t.Fatal(err)
	}
	off := Run(src, Options{Explain: false, LLMHost: "https://gateway.internal/v1", Now: time.Now()})
	if anyLineContains(off.Egress, "gateway.internal") {
		t.Errorf("explain is off, yet the egress names the narrator host: %q", off.Egress)
	}
	if !anyLineContains(off.Egress, "nothing leaves this machine") {
		t.Errorf("explain is off on fixtures, the egress should say nothing leaves: %q", off.Egress)
	}

	on := Run(src, Options{Explain: true, LLMHost: "https://gateway.internal/v1", Now: time.Now()})
	if !anyLineContains(on.Egress, "gateway.internal") {
		t.Errorf("explain is on, yet the egress never names the narrator host: %q", on.Egress)
	}
	if anyLineContains(on.Egress, "nothing leaves this machine") || anyLineContains(on.Egress, "no network") {
		t.Errorf("explain is on, so a payload may leave, yet the egress claims nothing does: %q", on.Egress)
	}
	if anyLineContains(on.Egress, "allowlisted explicitly") {
		t.Errorf("doctor cannot know the allowlist, yet it claims the host is allowlisted: %q", on.Egress)
	}
}

func TestLiveRunReportsAFailedProbeAsUnknownNeverAsNo(t *testing.T) {
	bin, _ := fakeKubectl(t, map[string]string{
		"auth_can-i.err":  "error: You must be logged in to the server (Unauthorized)",
		"auth_whoami.err": "error: You must be logged in to the server (Unauthorized)",
		"config_view.err": "error: no context",
	})
	for _, k := range []*collect.KubectlSource{
		collect.NewKubectlSource(bin, 10*time.Second, time.Time{}),
		collect.NewKubectlSource(filepath.Join(t.TempDir(), "absent-kubectl"), 10*time.Second, time.Time{}),
	} {
		rep := Run(k, Options{Context: "c1", Namespace: "payments", Now: time.Now()})
		if !rep.CanWrite {
			t.Errorf("%s: the write probe never ran, yet CanWrite is false, which reads as a verified safety", k.Binary)
		}
		if !rep.CanReadSec {
			t.Errorf("%s: the Secret probe never ran, yet CanReadSec is false", k.Binary)
		}
		for _, c := range rep.Checks {
			if c.Status == "no" || c.Status == "yes" {
				t.Errorf("%s: check %q answered %q although no probe could run", k.Binary, c.Name, c.Status)
			}
		}
		for _, name := range []string{"read pods", "write capability", "secret read capability"} {
			c, ok := check(rep, name)
			if !ok || c.Status != "unknown" {
				t.Errorf("%s: check %q = %+v, want status unknown", k.Binary, name, c)
			}
		}
		if !anyLineContains(rep.Gaps, "could not be checked") {
			t.Errorf("%s: gaps = %q, want one that says the probes could not be checked", k.Binary, rep.Gaps)
		}
		if rep.Identity != "unknown" {
			t.Errorf("%s: identity = %q, want unknown", k.Binary, rep.Identity)
		}
		if rep.Level != 0 {
			t.Errorf("%s: level = %d, want 0 when nothing was verified", k.Binary, rep.Level)
		}
	}
}

func TestLiveRunReadsWhoamiJSONAndTheCanIAnswers(t *testing.T) {
	bin, record := fakeKubectl(t, map[string]string{
		"auth_whoami_-o_json.out":         whoamiJSON,
		"auth_can-i_delete_pods.no":       "no\n",
		"auth_can-i_patch_deployments.no": "no\n",
		"auth_can-i_list_secrets.no":      "no - RBAC: denied\n",
		"default.out":                     "yes\n",
	})
	k := collect.NewKubectlSource(bin, 10*time.Second, time.Time{})
	rep := Run(k, Options{Context: "c1", Namespace: "payments", Now: time.Now()})

	if rep.Identity != "oidc:Jane Doe" {
		t.Errorf("identity = %q, want the whole username from the JSON answer", rep.Identity)
	}
	if rep.CanWrite || rep.CanReadSec {
		t.Errorf("CanWrite %v CanReadSec %v, want both false when every write probe answered no", rep.CanWrite, rep.CanReadSec)
	}
	if rep.Level != 1 {
		t.Errorf("level = %d, want 1 when Argo applications are readable", rep.Level)
	}
	for _, name := range []string{"read pods", "list nodes", "read Argo applications"} {
		if c, ok := check(rep, name); !ok || c.Status != "yes" {
			t.Errorf("check %q = %+v, want yes", name, c)
		}
	}
	for _, name := range []string{"write capability", "secret read capability"} {
		if c, ok := check(rep, name); !ok || c.Status != "no" {
			t.Errorf("check %q = %+v, want no", name, c)
		}
	}
	if len(rep.Gaps) != 0 {
		t.Errorf("gaps = %q, want none when everything answered", rep.Gaps)
	}

	argoProbes := 0
	for _, c := range recorded(t, record) {
		verb := strings.Fields(c)[0]
		switch verb {
		case "auth", "config", "get", "version", "api-resources":
		default:
			t.Errorf("doctor ran %q, which is not a read subcommand", c)
		}
		if strings.HasPrefix(c, "auth can-i") && !strings.Contains(c, "--context c1") {
			t.Errorf("probe %q ignores the selected context", c)
		}
		if c == "auth can-i list applications.argoproj.io --context c1 --all-namespaces" {
			argoProbes++
		}
		if c == "auth can-i delete pods --context c1 -n payments" {
			argoProbes += 100
		}
	}
	if argoProbes%100 != 1 {
		t.Errorf("the Argo probe ran %d times across all namespaces, want exactly once (it is what the collector reads)", argoProbes%100)
	}
	if argoProbes < 100 {
		t.Error("the delete pods probe never ran in the selected namespace")
	}
}

func TestLiveRunFallsBackToTheWhoamiTableThenTheKubeconfigUser(t *testing.T) {
	bin, _ := fakeKubectl(t, map[string]string{
		"auth_whoami_-o_json.err": "error: unknown shorthand flag: 'o'",
		"auth_whoami.out":         "ATTRIBUTE   VALUE\nUsername    Jane Doe\nGroups      [system:authenticated]\n",
		"default.out":             "yes\n",
	})
	rep := Run(collect.NewKubectlSource(bin, 10*time.Second, time.Time{}), Options{Context: "c1", Now: time.Now()})
	if rep.Identity != "Jane Doe" {
		t.Errorf("identity = %q, want the whole Username row of the table", rep.Identity)
	}

	bin, _ = fakeKubectl(t, map[string]string{
		"auth_whoami.err": "error: unknown command \"whoami\" for \"kubectl auth\"",
		"config_view.out": "clusterUser_rg_aks\n",
		"default.out":     "yes\n",
	})
	rep = Run(collect.NewKubectlSource(bin, 10*time.Second, time.Time{}), Options{Context: "c1", Now: time.Now()})
	if !strings.Contains(rep.Identity, "clusterUser_rg_aks") || !strings.Contains(rep.Identity, "kubeconfig") {
		t.Errorf("identity = %q, want the kubeconfig user, labelled as such", rep.Identity)
	}
}

func TestLiveEgressMentionsTheNarratorHostOnlyWhenExplainIsOn(t *testing.T) {
	bin, _ := fakeKubectl(t, map[string]string{"auth_whoami_-o_json.out": whoamiJSON, "default.out": "yes\n"})
	k := collect.NewKubectlSource(bin, 10*time.Second, time.Time{})

	off := Run(k, Options{Context: "c1", LLMHost: "https://gateway.internal:8443/v1", Now: time.Now()})
	if anyLineContains(off.Egress, "gateway.internal") {
		t.Errorf("explain is off, yet the egress names the narrator host: %q", off.Egress)
	}
	on := Run(k, Options{Context: "c1", Explain: true, LLMHost: "https://gateway.internal:8443/v1", Now: time.Now()})
	if !anyLineContains(on.Egress, "gateway.internal:8443") {
		t.Errorf("explain is on, yet the egress never names the host and port: %q", on.Egress)
	}
	if anyLineContains(on.Egress, "allowlisted explicitly") {
		t.Errorf("doctor cannot know the allowlist, yet it claims the host is allowlisted: %q", on.Egress)
	}
	bare := Run(k, Options{Context: "c1", Explain: true, Now: time.Now()})
	if !anyLineContains(bare.Egress, "no endpoint") {
		t.Errorf("explain is on with no endpoint, the egress should say so: %q", bare.Egress)
	}
	for _, tool := range []string{"helm", "argocd", "git"} {
		c, ok := check(on, "tool "+tool)
		if !ok || !strings.Contains(c.Detail, "not called by spanline") {
			t.Errorf("tool %s = %+v, want it labelled as never called, because no code path calls it", tool, c)
		}
	}
}
