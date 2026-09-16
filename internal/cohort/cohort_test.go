package cohort

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arnocho/spanline/internal/collect"
	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
)

func load(t *testing.T, scenario string) (*model.Snapshot, Options) {
	t.Helper()
	src, err := collect.NewFixtureSource(scenario)
	if err != nil {
		t.Fatalf("open fixture %s: %v", scenario, err)
	}
	meta := src.Meta()
	snap, err := src.Snapshot(context.Background(), "", meta.Namespace)
	if err != nil {
		t.Fatalf("snapshot %s: %v", scenario, err)
	}
	return snap, Options{Workload: meta.Workload, Namespace: meta.Namespace, Now: src.Now()}
}

func analyze(t *testing.T, scenario string) *result.WhyReport {
	t.Helper()
	snap, o := load(t, scenario)
	rep, err := Analyze(snap, o)
	if err != nil {
		t.Fatalf("Analyze %s: %v", scenario, err)
	}
	return rep
}

func dimByName(t *testing.T, rep *result.WhyReport, name string) result.Dimension {
	t.Helper()
	for _, d := range rep.Dimensions {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("dimension %q is absent from the report, got %v", name, dimensionNames(rep))
	return result.Dimension{}
}

func dimensionNames(rep *result.WhyReport) []string {
	var out []string
	for _, d := range rep.Dimensions {
		out = append(out, d.Name)
	}
	return out
}

func topSuspect(t *testing.T, rep *result.WhyReport) result.Suspect {
	t.Helper()
	if len(rep.Suspects) == 0 {
		t.Fatal("no suspect in the report")
	}
	return rep.Suspects[0]
}

func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func hasSubstring(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

func TestOOMRolloutBlocked(t *testing.T) {
	rep := analyze(t, "oom-rollout-blocked")

	if rep.Mode != result.ModeCohort {
		t.Errorf("mode = %q, want %q", rep.Mode, result.ModeCohort)
	}
	if rep.Workload != "deployment/checkout" {
		t.Errorf("workload = %q, want deployment/checkout", rep.Workload)
	}
	if rep.Failing.Count != 6 {
		t.Errorf("failing = %d (%v), want 6", rep.Failing.Count, rep.Failing.Pods)
	}
	if rep.Healthy.Count != 4 {
		t.Errorf("healthy = %d (%v), want 4", rep.Healthy.Count, rep.Healthy.Pods)
	}

	hash := dimByName(t, rep, "pod-template-hash")
	if hash.Separation != result.Total {
		t.Errorf("pod-template-hash separation = %q, want %q", hash.Separation, result.Total)
	}
	if hash.Purity != 1 {
		t.Errorf("pod-template-hash purity = %v, want 1", hash.Purity)
	}
	if hash.FailingValues != "7d9f5" || hash.HealthyValues != "5c4b8" {
		t.Errorf("pod-template-hash values = %q vs %q, want 7d9f5 vs 5c4b8", hash.FailingValues, hash.HealthyValues)
	}
	if rep.CohortKey != "pod-template-hash" {
		t.Errorf("cohort key = %q, want pod-template-hash", rep.CohortKey)
	}
	if rep.Dimensions[0].Name != "pod-template-hash" {
		t.Errorf("top dimension = %q, want pod-template-hash (listed order is the tie break)", rep.Dimensions[0].Name)
	}

	wantOnset := time.Date(2026, 9, 16, 3, 2, 14, 0, time.UTC)
	if !rep.OnsetAt.Equal(wantOnset) {
		t.Errorf("onset = %s, want %s", rep.OnsetAt.Format(time.RFC3339), wantOnset.Format(time.RFC3339))
	}
	if !strings.Contains(rep.OnsetSignal, "lastState.terminated.finishedAt") {
		t.Errorf("onset signal = %q, want the terminated finishedAt field", rep.OnsetSignal)
	}

	top := topSuspect(t, rep)
	if top.Verdict != result.Splits {
		t.Errorf("top verdict = %q, want %q (suspects: %v)", top.Verdict, result.Splits, suspectIDs(rep))
	}
	if top.ID != "replicaset/checkout-7d9f5" {
		t.Errorf("top suspect = %q, want replicaset/checkout-7d9f5", top.ID)
	}
	if top.Dimension != "pod-template-hash" {
		t.Errorf("top suspect dimension = %q, want pod-template-hash", top.Dimension)
	}
	if !hasLine(top.Diff, "resources.limits.memory 512Mi -> 256Mi") {
		t.Errorf("top suspect diff = %v, want the memory limit line", top.Diff)
	}
	if top.Actor != result.ActorController {
		t.Errorf("top suspect actor = %q, want %q", top.Actor, result.ActorController)
	}
	if !strings.Contains(top.Attribution, "a1b2c3d4e5f6") {
		t.Errorf("top suspect attribution = %q, want the Argo revision", top.Attribution)
	}

	// The hand edit on the deployment is retained, and it is not a split.
	var edit *result.Suspect
	for i := range rep.Suspects {
		if strings.Contains(rep.Suspects[i].ID, "kubectl-edit") {
			edit = &rep.Suspects[i]
		}
	}
	if edit == nil {
		t.Fatalf("the kubectl-edit managedFields entry is missing from %v", suspectIDs(rep))
	}
	if edit.Verdict != result.NoSplit {
		t.Errorf("kubectl-edit verdict = %q, want %q", edit.Verdict, result.NoSplit)
	}
	if edit.Actor != result.ActorHuman {
		t.Errorf("kubectl-edit actor = %q, want %q", edit.Actor, result.ActorHuman)
	}
}

func TestNodeImageDrift(t *testing.T) {
	rep := analyze(t, "node-image-drift")

	if rep.Mode != result.ModeCohort {
		t.Errorf("mode = %q, want %q", rep.Mode, result.ModeCohort)
	}
	hash := dimByName(t, rep, "pod-template-hash")
	if hash.Separation != result.None {
		t.Errorf("pod-template-hash separation = %q, want %q", hash.Separation, result.None)
	}
	image := dimByName(t, rep, "node image version")
	if image.Separation != result.Total {
		t.Errorf("node image version separation = %q, want %q", image.Separation, result.Total)
	}
	if image.Purity != 1 {
		t.Errorf("node image version purity = %v, want 1", image.Purity)
	}
	if rep.Dimensions[0].Name != "node image version" {
		t.Errorf("top dimension = %q, want node image version (purity outranks the listed order)", rep.Dimensions[0].Name)
	}

	top := topSuspect(t, rep)
	if strings.HasPrefix(top.ID, "replicaset/") {
		t.Errorf("top suspect = %q, want a change that is not the ReplicaSet", top.ID)
	}
	if top.Verdict != result.Splits {
		t.Errorf("top verdict = %q, want %q", top.Verdict, result.Splits)
	}
	if top.Dimension != "node image version" {
		t.Errorf("top suspect dimension = %q, want node image version", top.Dimension)
	}
	if !hasSubstring(top.Evidence, "AKSUbuntu-2204gen2containerd-202609.10.0") {
		t.Errorf("top suspect evidence = %v, want the node image version of the failing nodes", top.Evidence)
	}
	// The checksum annotation is on neither side here: that is unresolved, never NONE.
	sum := dimByName(t, rep, annotationChecksum)
	if sum.Separation != result.Unresolved {
		t.Errorf("checksum/config separation = %q, want %q", sum.Separation, result.Unresolved)
	}
	if !hasSubstring(rep.Gaps, "\"checksum/config\" is unresolved") {
		t.Errorf("gaps = %v, want the unresolved checksum dimension", rep.Gaps)
	}
}

func TestRolloutCompleted(t *testing.T) {
	rep := analyze(t, "rollout-completed")

	if rep.Mode != result.ModeRevision {
		t.Errorf("mode = %q, want %q", rep.Mode, result.ModeRevision)
	}
	if rep.Healthy.Count != 0 {
		t.Errorf("healthy = %d, want 0", rep.Healthy.Count)
	}
	if len(rep.Revisions) != 2 {
		t.Fatalf("revisions = %d (%v), want 2", len(rep.Revisions), rep.Revisions)
	}
	if rep.Revisions[0].Name != "cart-3e7a9" || rep.Revisions[0].Number != 12 || rep.Revisions[0].Active != "yes" {
		t.Errorf("current revision = %+v, want cart-3e7a9 number 12 active", rep.Revisions[0])
	}
	if rep.Revisions[1].Name != "cart-91bd4" || rep.Revisions[1].Number != 11 || rep.Revisions[1].Active != "no" {
		t.Errorf("previous revision = %+v, want cart-91bd4 number 11 inactive", rep.Revisions[1])
	}
	if len(rep.Dimensions) != 0 {
		t.Errorf("dimensions = %v, want none in the fallback", dimensionNames(rep))
	}

	top := topSuspect(t, rep)
	if top.Verdict != result.Temporal {
		t.Errorf("top verdict = %q, want %q (suspects: %v)", top.Verdict, result.Temporal, suspectIDs(rep))
	}
	if !hasSubstring(rep.Gaps, "retained events") {
		t.Errorf("gaps = %v, want one that rests the previous revision on retained events", rep.Gaps)
	}
	for _, s := range rep.Suspects {
		if s.Verdict == result.Splits {
			t.Errorf("suspect %q splits the cohorts, but there is no healthy cohort", s.ID)
		}
	}
}

func TestClassifyPod(t *testing.T) {
	ready := func(status string) []model.PodCondition {
		if status == "" {
			return nil
		}
		return []model.PodCondition{{Type: "Ready", Status: status,
			LastTransitionTime: time.Date(2026, 9, 16, 3, 0, 0, 0, time.UTC)}}
	}
	status := func(cs model.ContainerStatus) []model.ContainerStatus {
		cs.Name = "app"
		return []model.ContainerStatus{cs}
	}

	cases := []struct {
		name   string
		pod    model.Pod
		want   podState
		signal string
	}{
		{
			name: "oom killed in the last state is failing",
			pod: model.Pod{Status: model.PodStatus{
				Conditions:        ready("True"),
				ContainerStatuses: status(model.ContainerStatus{LastState: model.ContainerState{Terminated: &model.StateTerminated{Reason: "OOMKilled"}}}),
			}},
			want:   stateFailing,
			signal: "app lastState.terminated.reason OOMKilled",
		},
		{
			name: "error in the last state is failing",
			pod: model.Pod{Status: model.PodStatus{
				Conditions:        ready("True"),
				ContainerStatuses: status(model.ContainerStatus{LastState: model.ContainerState{Terminated: &model.StateTerminated{Reason: "Error"}}}),
			}},
			want:   stateFailing,
			signal: "app lastState.terminated.reason Error",
		},
		{
			name: "completed in the last state is not a failing signal",
			pod: model.Pod{Status: model.PodStatus{
				Conditions:        ready("True"),
				ContainerStatuses: status(model.ContainerStatus{Ready: true, LastState: model.ContainerState{Terminated: &model.StateTerminated{Reason: "Completed"}}}),
			}},
			want: stateHealthy,
		},
		{
			name: "crash loop back off is failing",
			pod: model.Pod{Status: model.PodStatus{
				Conditions:        ready("True"),
				ContainerStatuses: status(model.ContainerStatus{State: model.ContainerState{Waiting: &model.StateWaiting{Reason: "CrashLoopBackOff"}}}),
			}},
			want:   stateFailing,
			signal: "app state.waiting.reason CrashLoopBackOff",
		},
		{
			name: "image pull back off is failing",
			pod: model.Pod{Status: model.PodStatus{
				Conditions:        ready("True"),
				ContainerStatuses: status(model.ContainerStatus{State: model.ContainerState{Waiting: &model.StateWaiting{Reason: "ImagePullBackOff"}}}),
			}},
			want:   stateFailing,
			signal: "app state.waiting.reason ImagePullBackOff",
		},
		{
			name: "create container config error is failing",
			pod: model.Pod{Status: model.PodStatus{
				Conditions:        ready("True"),
				ContainerStatuses: status(model.ContainerStatus{State: model.ContainerState{Waiting: &model.StateWaiting{Reason: "CreateContainerConfigError"}}}),
			}},
			want:   stateFailing,
			signal: "app state.waiting.reason CreateContainerConfigError",
		},
		{
			name: "a waiting reason outside the list is not a failing signal",
			pod: model.Pod{Status: model.PodStatus{
				Conditions:        ready("True"),
				ContainerStatuses: status(model.ContainerStatus{State: model.ContainerState{Waiting: &model.StateWaiting{Reason: "ContainerCreating"}}}),
			}},
			want: stateHealthy,
		},
		{
			name: "three restarts is failing",
			pod: model.Pod{Status: model.PodStatus{
				Conditions:        ready("True"),
				ContainerStatuses: status(model.ContainerStatus{Ready: true, RestartCount: 3}),
			}},
			want:   stateFailing,
			signal: "app restartCount 3",
		},
		{
			name: "two restarts and ready is healthy",
			pod: model.Pod{Status: model.PodStatus{
				Conditions:        ready("True"),
				ContainerStatuses: status(model.ContainerStatus{Ready: true, RestartCount: 2}),
			}},
			want: stateHealthy,
		},
		{
			name: "ready false is failing",
			pod: model.Pod{Status: model.PodStatus{
				Conditions:        ready("False"),
				ContainerStatuses: status(model.ContainerStatus{}),
			}},
			want:   stateFailing,
			signal: "conditions[Ready].status False",
		},
		{
			name: "ready true and no signal is healthy",
			pod: model.Pod{Status: model.PodStatus{
				Conditions:        ready("True"),
				ContainerStatuses: status(model.ContainerStatus{Ready: true}),
			}},
			want: stateHealthy,
		},
		{
			name: "no ready condition and no signal is in neither cohort",
			pod: model.Pod{Status: model.PodStatus{
				ContainerStatuses: status(model.ContainerStatus{}),
			}},
			want: stateUnclassified,
		},
		{
			name: "an unknown ready condition is in neither cohort",
			pod: model.Pod{Status: model.PodStatus{
				Conditions:        ready("Unknown"),
				ContainerStatuses: status(model.ContainerStatus{}),
			}},
			want: stateUnclassified,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, signals := classifyPod(tc.pod)
			if got != tc.want {
				t.Fatalf("state = %s, want %s (signals %v)", got, tc.want, signals)
			}
			if tc.signal != "" && !hasLine(signals, tc.signal) {
				t.Errorf("signals = %v, want %q", signals, tc.signal)
			}
			if tc.want != stateFailing && len(signals) != 0 {
				t.Errorf("signals = %v, want none for a %s pod", signals, tc.want)
			}
		})
	}
}

func TestReportNeverSaysCause(t *testing.T) {
	for _, scenario := range []string{"oom-rollout-blocked", "node-image-drift", "rollout-completed"} {
		t.Run(scenario, func(t *testing.T) {
			b, err := json.Marshal(analyze(t, scenario))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if strings.Contains(strings.ToLower(string(b)), "cause") {
				t.Errorf("the report contains the word this package must never print: %s", b)
			}
		})
	}
}

func TestAnalyzeIsDeterministic(t *testing.T) {
	for _, scenario := range []string{"oom-rollout-blocked", "node-image-drift", "rollout-completed"} {
		t.Run(scenario, func(t *testing.T) {
			first, err := json.Marshal(analyze(t, scenario))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			for i := 0; i < 5; i++ {
				again, err := json.Marshal(analyze(t, scenario))
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				if string(first) != string(again) {
					t.Fatalf("run %d differs:\n%s\n%s", i, first, again)
				}
			}
		})
	}
}

func TestResolveWorkloadErrors(t *testing.T) {
	snap, o := load(t, "oom-rollout-blocked")

	if _, err := Analyze(nil, o); err != ErrNoSnapshot {
		t.Errorf("nil snapshot error = %v, want %v", err, ErrNoSnapshot)
	}
	empty := o
	empty.Workload = "  "
	if _, err := Analyze(snap, empty); err != ErrNoWorkload {
		t.Errorf("empty workload error = %v, want %v", err, ErrNoWorkload)
	}

	missing := o
	missing.Workload = "deploy/absent"
	_, err := Analyze(snap, missing)
	if err == nil || !strings.Contains(err.Error(), "no deployment named \"absent\"") {
		t.Errorf("absent workload error = %v, want a clear not found", err)
	}

	badKind := o
	badKind.Workload = "cronjob/checkout"
	if _, err := Analyze(snap, badKind); err == nil || !strings.Contains(err.Error(), "unknown workload kind") {
		t.Errorf("unknown kind error = %v, want a clear kind error", err)
	}

	bare := o
	bare.Workload = "checkout"
	if _, err := Analyze(snap, bare); err != nil {
		t.Errorf("a bare name must match any kind, got %v", err)
	}

	// A name carried by two kinds is ambiguous, and the error says so.
	twin := *snap
	twin.StatefulSets = append(twin.StatefulSets, model.Workload{
		Kind: "StatefulSet",
		Metadata: model.ObjectMeta{
			Name: "checkout", Namespace: "payments",
			Labels: map[string]string{"app": "checkout"},
		},
		Spec: model.WorkloadSpec{Selector: &model.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}}},
	})
	if _, err := Analyze(&twin, bare); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("ambiguous workload error = %v, want an ambiguity error", err)
	}
}

func TestWindowExcludesOlderChanges(t *testing.T) {
	snap, o := load(t, "oom-rollout-blocked")
	o.Window = time.Minute // nothing changed in the last minute of the recording
	rep, err := Analyze(snap, o)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(rep.Suspects) != 1 || rep.Suspects[0].Verdict != result.Unknown {
		t.Fatalf("suspects = %v, want a single UNKNOWN when nothing is retained in the window", suspectIDs(rep))
	}
	// The cohorts and the split are still reported: only the change list is empty.
	if rep.Failing.Count != 6 || rep.CohortKey != "pod-template-hash" {
		t.Errorf("cohorts = %d failing, key %q, want 6 and pod-template-hash", rep.Failing.Count, rep.CohortKey)
	}
}

func suspectIDs(rep *result.WhyReport) []string {
	var out []string
	for _, s := range rep.Suspects {
		out = append(out, s.ID+"="+string(s.Verdict))
	}
	return out
}
