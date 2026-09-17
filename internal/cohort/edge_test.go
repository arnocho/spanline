package cohort

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
)

// Hand-built snapshots for the edges the recorded scenarios do not cover: sparse objects,
// pods outside both cohorts, foreign events, revisions without annotations, and the exact
// labels each controller kind stamps on its pods.

// now is the reference time of every hand-built snapshot in this file.
var now = time.Date(2026, 9, 16, 3, 0, 0, 0, time.UTC)

// ago returns the time that many minutes before now.
func ago(minutes int) time.Time { return now.Add(-time.Duration(minutes) * time.Minute) }

func intPtr(n int) *int { return &n }

func workload(kind, name, ns string) model.Workload {
	return model.Workload{
		Kind: kind,
		Metadata: model.ObjectMeta{
			Name: name, Namespace: ns, UID: "uid-" + name, CreationTimestamp: ago(600),
		},
		Spec: model.WorkloadSpec{
			Replicas: intPtr(2),
			Selector: &model.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: model.PodTemplateSpec{Spec: model.PodSpec{
				Containers: []model.Container{{Name: name, Image: name + ":1"}},
			}},
		},
	}
}

func template(name, image string) model.PodTemplateSpec {
	return model.PodTemplateSpec{Spec: model.PodSpec{
		Containers: []model.Container{{Name: name, Image: image}},
	}}
}

func ownerRef(w model.Workload) []model.OwnerReference {
	return []model.OwnerReference{{Kind: w.Kind, Name: w.Metadata.Name, UID: w.Metadata.UID}}
}

// replicaSet builds one ReplicaSet of the workload; an empty revision leaves the annotation out.
func replicaSet(w model.Workload, hash, revision string, created time.Time, image string) model.ReplicaSet {
	rs := model.ReplicaSet{
		Metadata: model.ObjectMeta{
			Name: w.Metadata.Name + "-" + hash, Namespace: w.Metadata.Namespace,
			Labels:            map[string]string{"app": w.Metadata.Name, labelPodTemplateHash: hash},
			CreationTimestamp: created,
			OwnerReferences:   ownerRef(w),
		},
		Spec: model.WorkloadSpec{Replicas: intPtr(1), Template: template(w.Metadata.Name, image)},
	}
	if revision != "" {
		rs.Metadata.Annotations = map[string]string{annotationRevision: revision}
	}
	return rs
}

// controllerRevision builds one ControllerRevision the way the controller history library
// does: named <owner>-<hash>, labelled with the bare hash.
func controllerRevision(w model.Workload, hash string, revision int, created time.Time, image string) model.ControllerRevision {
	return model.ControllerRevision{
		Metadata: model.ObjectMeta{
			Name: w.Metadata.Name + "-" + hash, Namespace: w.Metadata.Namespace,
			Labels:            map[string]string{"app": w.Metadata.Name, labelControllerRevision: hash},
			CreationTimestamp: created,
			OwnerReferences:   ownerRef(w),
		},
		Revision: revision,
		Data: model.ControllerData{Spec: model.ControllerDataSpec{
			Template: template(w.Metadata.Name, image),
		}},
	}
}

// podOf builds one Running pod of the workload. A healthy pod is Ready; a failing pod has
// been not Ready since readyAt and waits in CrashLoopBackOff.
func podOf(w model.Workload, name, hashLabel, hashValue string, healthy bool, readyAt time.Time) model.Pod {
	p := model.Pod{
		Metadata: model.ObjectMeta{
			Name: name, Namespace: w.Metadata.Namespace, CreationTimestamp: ago(30),
			Labels: map[string]string{"app": w.Metadata.Name},
		},
		Spec:   model.PodSpec{Containers: []model.Container{{Name: w.Metadata.Name}}},
		Status: model.PodStatus{Phase: "Running"},
	}
	if hashLabel != "" {
		p.Metadata.Labels[hashLabel] = hashValue
	}
	status := "True"
	cs := model.ContainerStatus{Name: w.Metadata.Name, Ready: true, ImageID: "img@sha256:" + hashValue}
	if !healthy {
		status = "False"
		cs.Ready = false
		cs.State = model.ContainerState{Waiting: &model.StateWaiting{Reason: "CrashLoopBackOff"}}
	}
	p.Status.Conditions = []model.PodCondition{{Type: "Ready", Status: status, LastTransitionTime: readyAt}}
	p.Status.ContainerStatuses = []model.ContainerStatus{cs}
	return p
}

func snapshotOf(w model.Workload) *model.Snapshot {
	snap := &model.Snapshot{Context: "test", CollectedAt: now}
	switch w.Kind {
	case "Deployment":
		snap.Deployments = append(snap.Deployments, w)
	case "StatefulSet":
		snap.StatefulSets = append(snap.StatefulSets, w)
	case "DaemonSet":
		snap.DaemonSets = append(snap.DaemonSets, w)
	}
	return snap
}

// run analyses a hand-built snapshot and checks the one invariant every report shares.
func run(t *testing.T, snap *model.Snapshot, ref string) *result.WhyReport {
	t.Helper()
	rep, err := Analyze(snap, Options{Workload: ref, Now: now})
	if err != nil {
		t.Fatalf("Analyze(%s): %v", ref, err)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(strings.ToLower(string(b)), "cause") {
		t.Errorf("the report contains the word this package must never print: %s", b)
	}
	if !utf8.Valid(b) {
		t.Errorf("the report is not valid UTF-8: %q", b)
	}
	return rep
}

func suspectByID(t *testing.T, rep *result.WhyReport, id string) result.Suspect {
	t.Helper()
	for _, s := range rep.Suspects {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("suspect %q is absent, got %v", id, suspectIDs(rep))
	return result.Suspect{}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPendingAndTerminalPodsAreInNeitherCohort(t *testing.T) {
	w := workload("Deployment", "web", "shop")
	snap := snapshotOf(w)

	healthy := podOf(w, "web-a", labelPodTemplateHash, "h1", true, ago(120))
	failing := podOf(w, "web-b", labelPodTemplateHash, "h2", false, ago(10))
	failing.Status.ContainerStatuses[0].LastState = model.ContainerState{
		Terminated: &model.StateTerminated{Reason: "OOMKilled", FinishedAt: ago(10)},
	}
	// Still pulling its image: Ready is False, but nothing has failed yet.
	creating := podOf(w, "web-c", labelPodTemplateHash, "h2", false, ago(1))
	creating.Status.Phase = "Pending"
	creating.Status.ContainerStatuses[0].State = model.ContainerState{Waiting: &model.StateWaiting{Reason: "ContainerCreating"}}
	creating.Status.ContainerStatuses[0].ImageID = ""
	// Ran to completion five hours ago: Ready is False with reason PodCompleted.
	completed := podOf(w, "web-d", labelPodTemplateHash, "h1", false, ago(300))
	completed.Status.Phase = "Succeeded"
	completed.Status.Conditions[0].Reason = "PodCompleted"
	completed.Status.ContainerStatuses[0].State = model.ContainerState{
		Terminated: &model.StateTerminated{Reason: "Completed", FinishedAt: ago(300)},
	}
	// Evicted ten hours ago: no container status is left on it.
	evicted := podOf(w, "web-e", labelPodTemplateHash, "h1", false, ago(600))
	evicted.Status.Phase = "Failed"
	evicted.Status.ContainerStatuses = nil
	// Pending with an explicit failing signal: that one is failing.
	pulling := podOf(w, "web-f", labelPodTemplateHash, "h2", false, ago(3))
	pulling.Status.Phase = "Pending"
	pulling.Status.ContainerStatuses[0].State = model.ContainerState{Waiting: &model.StateWaiting{Reason: "ImagePullBackOff"}}
	snap.Pods = []model.Pod{healthy, failing, creating, completed, evicted, pulling}

	rep := run(t, snap, "deploy/web")
	if !equalStrings(rep.Failing.Pods, []string{"web-b", "web-f"}) {
		t.Errorf("failing = %v, want [web-b web-f]", rep.Failing.Pods)
	}
	if !equalStrings(rep.Healthy.Pods, []string{"web-a"}) {
		t.Errorf("healthy = %v, want [web-a]", rep.Healthy.Pods)
	}
	if !rep.OnsetAt.Equal(ago(10)) {
		t.Errorf("onset = %s (%s), want %s: the terminal pods must not date it", rep.OnsetAt, rep.OnsetSignal, ago(10))
	}
	if len(rep.Dimensions) > 0 {
		if hash := dimByName(t, rep, labelPodTemplateHash); hash.Separation != result.Total {
			t.Errorf("pod-template-hash separation = %q, want %q once the pods outside both cohorts are set aside", hash.Separation, result.Total)
		}
	}
	for _, want := range []string{"web-c", "web-d", "web-e", "Pending", "Succeeded", "Failed"} {
		if !hasSubstring(rep.Gaps, want) {
			t.Errorf("gaps = %v, want %q named in the neither-cohort gap", rep.Gaps, want)
		}
	}
}

func TestOnsetIgnoresForeignAndStaleEvents(t *testing.T) {
	w := workload("Deployment", "web", "shop")
	event := func(name, ns, reason string, first time.Time) model.Event {
		return model.Event{
			Metadata:       model.ObjectMeta{Name: name, Namespace: ns},
			InvolvedObject: model.InvolvedObject{Kind: "Pod", Name: "web-a", Namespace: ns},
			Reason:         reason, Type: "Warning", FirstTimestamp: first,
		}
	}

	t.Run("same pod name in another namespace, and a previous incarnation", func(t *testing.T) {
		snap := snapshotOf(w)
		p := podOf(w, "web-a", labelPodTemplateHash, "h1", false, ago(5))
		p.Metadata.CreationTimestamp = ago(30)
		snap.Pods = []model.Pod{p}
		snap.Events = []model.Event{
			event("foreign", "other", "BackOff", ago(200)),
			event("stale", "shop", "OOMKilling", ago(120)),
			event("current", "shop", "BackOff", ago(8)),
		}
		rep := run(t, snap, "deploy/web")
		if !rep.OnsetAt.Equal(ago(8)) {
			t.Fatalf("onset = %s (%s), want %s from the event on this pod", rep.OnsetAt, rep.OnsetSignal, ago(8))
		}
		if !strings.Contains(rep.OnsetSignal, "event current") {
			t.Errorf("onset signal = %q, want the current event", rep.OnsetSignal)
		}
	})

	t.Run("an onset older than the window is kept and said so", func(t *testing.T) {
		snap := snapshotOf(w)
		snap.Pods = []model.Pod{podOf(w, "web-a", labelPodTemplateHash, "h1", false, ago(50*60))}
		rep := run(t, snap, "deploy/web")
		if !rep.OnsetAt.Equal(ago(50 * 60)) {
			t.Errorf("onset = %s, want %s: the earliest signal is never replaced", rep.OnsetAt, ago(50*60))
		}
		if !hasSubstring(rep.Gaps, "predates") {
			t.Errorf("gaps = %v, want one saying the onset predates the window", rep.Gaps)
		}
	})
}

func TestControllerRevisionValueMatchesPodLabel(t *testing.T) {
	t.Run("statefulset pods carry the revision name", func(t *testing.T) {
		w := workload("StatefulSet", "redis", "cache")
		snap := snapshotOf(w)
		old := controllerRevision(w, "aaa", 1, ago(300), "redis:7.0")
		cur := controllerRevision(w, "bbb", 2, ago(20), "redis:7.2")
		snap.ControllerRevisions = []model.ControllerRevision{old, cur}
		snap.Pods = []model.Pod{
			podOf(w, "redis-0", labelControllerRevision, old.Metadata.Name, true, ago(250)),
			podOf(w, "redis-1", labelControllerRevision, cur.Metadata.Name, false, ago(15)),
		}
		rep := run(t, snap, "statefulset/redis")
		if rep.CohortKey != labelControllerRevision {
			t.Fatalf("cohort key = %q, want %s (dimensions %v)", rep.CohortKey, labelControllerRevision, rep.Dimensions)
		}
		top := topSuspect(t, rep)
		if top.ID != "controllerrevision/redis-bbb" || top.Verdict != result.Splits {
			t.Errorf("top suspect = %s %s, want controllerrevision/redis-bbb SPLITS (suspects %v)", top.ID, top.Verdict, suspectIDs(rep))
		}
		if !hasLine(top.Diff, "image redis:7.0 -> redis:7.2") {
			t.Errorf("diff = %v, want the image line", top.Diff)
		}
	})

	t.Run("statefulset fallback finds the pods of each revision", func(t *testing.T) {
		w := workload("StatefulSet", "redis", "cache")
		snap := snapshotOf(w)
		old := controllerRevision(w, "aaa", 1, ago(300), "redis:7.0")
		cur := controllerRevision(w, "bbb", 2, ago(20), "redis:7.2")
		snap.ControllerRevisions = []model.ControllerRevision{old, cur}
		snap.Pods = []model.Pod{
			podOf(w, "redis-0", labelControllerRevision, old.Metadata.Name, false, ago(15)),
			podOf(w, "redis-1", labelControllerRevision, cur.Metadata.Name, false, ago(15)),
		}
		rep := run(t, snap, "statefulset/redis")
		if rep.Mode != result.ModeRevision || len(rep.Revisions) != 2 {
			t.Fatalf("mode = %q with %d revisions, want the fallback with 2", rep.Mode, len(rep.Revisions))
		}
		for _, r := range rep.Revisions {
			if !strings.HasPrefix(r.Signals, "1 pod(s), 1 failing") {
				t.Errorf("revision %s signals = %q, want its one failing pod counted", r.Name, r.Signals)
			}
		}
	})

	t.Run("daemonset pods carry the bare hash", func(t *testing.T) {
		w := workload("DaemonSet", "exporter", "monitoring")
		snap := snapshotOf(w)
		old := controllerRevision(w, "ccc", 1, ago(300), "exporter:1")
		cur := controllerRevision(w, "ddd", 2, ago(20), "exporter:2")
		snap.ControllerRevisions = []model.ControllerRevision{old, cur}
		snap.Pods = []model.Pod{
			podOf(w, "exporter-1", labelControllerRevision, "ccc", true, ago(250)),
			podOf(w, "exporter-2", labelControllerRevision, "ddd", false, ago(15)),
		}
		rep := run(t, snap, "ds/exporter")
		top := topSuspect(t, rep)
		if top.ID != "controllerrevision/exporter-ddd" || top.Verdict != result.Splits {
			t.Errorf("top suspect = %s %s, want controllerrevision/exporter-ddd SPLITS", top.ID, top.Verdict)
		}
	})
}

func TestTemplateDiffComparesQuantitiesByValue(t *testing.T) {
	tmpl := func(image, memLimit, cpuLimit, memRequest string) model.PodTemplateSpec {
		return model.PodTemplateSpec{Spec: model.PodSpec{Containers: []model.Container{{
			Name: "app", Image: image,
			Resources: model.ResourceRequirements{
				Limits:   map[string]string{"memory": memLimit, "cpu": cpuLimit},
				Requests: map[string]string{"memory": memRequest},
			},
		}}}}
	}

	same := templateDiff(tmpl("app:1.0", "512Mi", "0.5", "268435456"), tmpl("app:1.0", "536870912", "500m", "256Mi"), 3, 3)
	if len(same) != 0 {
		t.Errorf("diff = %v, want none: every quantity is equal, only the notation differs", same)
	}

	changed := templateDiff(tmpl("app:1.0", "512Mi", "500m", "256Mi"), tmpl("app:1.1", "1Gi", "1", "256Mi"), 3, 3)
	want := []string{
		"image app:1.0 -> app:1.1",
		"resources.limits.cpu 500m -> 1",
		"resources.limits.memory 512Mi -> 1Gi",
	}
	if !equalStrings(changed, want) {
		t.Errorf("diff = %v, want %v", changed, want)
	}

	text := templateDiff(tmpl("app:1.0", "many", "500m", "256Mi"), tmpl("app:1.0", "few", "500m", "256Mi"), 3, 3)
	if !equalStrings(text, []string{"resources.limits.memory many -> few"}) {
		t.Errorf("diff = %v, want the unparseable values compared as text", text)
	}
}

func TestReplicaSetWithoutRevisionAnnotation(t *testing.T) {
	w := workload("Deployment", "web", "shop")
	snap := snapshotOf(w)
	old := replicaSet(w, "h1", "", ago(300), "web:1")
	cur := replicaSet(w, "h2", "", ago(20), "web:2")
	snap.ReplicaSets = []model.ReplicaSet{cur, old}
	snap.Pods = []model.Pod{
		podOf(w, "web-1", labelPodTemplateHash, "h1", true, ago(250)),
		podOf(w, "web-2", labelPodTemplateHash, "h2", false, ago(15)),
	}

	rep := run(t, snap, "deploy/web")
	top := topSuspect(t, rep)
	if top.ID != "replicaset/web-h2" || top.Verdict != result.Splits {
		t.Fatalf("top suspect = %s %s, want replicaset/web-h2 SPLITS", top.ID, top.Verdict)
	}
	if strings.Contains(top.Title, "revision 0") {
		t.Errorf("title = %q, want no invented revision number", top.Title)
	}
	if hasSubstring(top.Evidence, "= 0") {
		t.Errorf("evidence = %v, want no invented revision number", top.Evidence)
	}
	if !hasLine(top.Diff, "image web:1 -> web:2") {
		t.Errorf("diff = %v, want the older set by creation time as the base", top.Diff)
	}
	if !hasSubstring(rep.Gaps, annotationRevision) {
		t.Errorf("gaps = %v, want the missing revision annotation reported", rep.Gaps)
	}
}

func TestManagedFieldsEntriesKeepTheirIndexAndSkipStatusWrites(t *testing.T) {
	w := workload("Deployment", "web", "shop")
	w.Metadata.ManagedFields = []model.ManagedFieldsEntry{
		{Manager: "kubectl-edit", Operation: "Update", Time: ago(10)},
		{Manager: "argocd-application-controller", Operation: "Apply", Time: ago(60)},
		{Manager: "kube-controller-manager", Operation: "Update", Time: ago(1), Subresource: "status"},
		{Manager: "kubectl-scale", Operation: "Update", Time: ago(5), Subresource: "scale"},
		{Manager: "kubectl-edit", Operation: "Update", Time: ago(10)},
	}
	snap := snapshotOf(w)
	snap.Pods = []model.Pod{podOf(w, "web-1", labelPodTemplateHash, "h1", false, ago(3))}

	rep := run(t, snap, "deploy/web")
	seen := map[string]bool{}
	for _, s := range rep.Suspects {
		if seen[s.ID] {
			t.Errorf("suspect id %q is not unique: the order between twins would be arbitrary", s.ID)
		}
		seen[s.ID] = true
		if strings.Contains(s.ID, "kube-controller-manager") {
			t.Errorf("suspect %q is a status write: it changes nothing on the workload", s.ID)
		}
	}
	var indexes []string
	for _, s := range rep.Suspects {
		for _, e := range s.Evidence {
			if strings.HasSuffix(e, ".manager = kubectl-edit") || strings.HasSuffix(e, ".manager = argocd-application-controller") {
				indexes = append(indexes, e[strings.Index(e, "managedFields"):])
			}
		}
	}
	want := []string{
		"managedFields[1].manager = argocd-application-controller",
		"managedFields[0].manager = kubectl-edit",
		"managedFields[4].manager = kubectl-edit",
	}
	for _, line := range want {
		if !hasLine(indexes, line) {
			t.Errorf("evidence indexes = %v, want %q: the path must point at the object's own entry", indexes, line)
		}
	}
	found := false
	for _, s := range rep.Suspects {
		if strings.Contains(s.ID, "kubectl-scale") {
			found = true
		}
	}
	if !found {
		t.Errorf("suspects = %v, want the scale write kept: it changes the workload", suspectIDs(rep))
	}
}

func TestRevisionFallbackGapOnlyWhenPreviousRevisionHasNoPod(t *testing.T) {
	w := workload("Deployment", "web", "shop")
	snap := snapshotOf(w)
	snap.ReplicaSets = []model.ReplicaSet{
		replicaSet(w, "h1", "1", ago(300), "web:1"),
		replicaSet(w, "h2", "2", ago(20), "web:2"),
	}
	snap.Pods = []model.Pod{
		podOf(w, "web-1", labelPodTemplateHash, "h1", false, ago(15)),
		podOf(w, "web-2", labelPodTemplateHash, "h2", false, ago(15)),
	}

	rep := run(t, snap, "deploy/web")
	if rep.Mode != result.ModeRevision || len(rep.Revisions) != 2 {
		t.Fatalf("mode = %q with %d revisions, want the fallback with 2", rep.Mode, len(rep.Revisions))
	}
	if hasSubstring(rep.Gaps, "has no pod left") {
		t.Errorf("gaps = %v, but revision web-h1 still has a pod", rep.Gaps)
	}
	if !strings.HasPrefix(rep.Revisions[1].Signals, "1 pod(s), 1 failing") {
		t.Errorf("previous revision signals = %q, want its failing pod counted", rep.Revisions[1].Signals)
	}
}

func TestEveryFailingOnlyValueSplits(t *testing.T) {
	w := workload("Deployment", "web", "shop")
	snap := snapshotOf(w)
	snap.ReplicaSets = []model.ReplicaSet{
		replicaSet(w, "h1", "1", ago(300), "web:1"),
		replicaSet(w, "h2", "2", ago(30), "web:2"),
		replicaSet(w, "h3", "3", ago(10), "web:3"),
	}
	snap.Pods = []model.Pod{
		podOf(w, "web-1", labelPodTemplateHash, "h1", true, ago(250)),
		podOf(w, "web-2", labelPodTemplateHash, "h1", true, ago(250)),
		podOf(w, "web-3", labelPodTemplateHash, "h2", false, ago(25)),
		podOf(w, "web-4", labelPodTemplateHash, "h2", false, ago(25)),
		podOf(w, "web-5", labelPodTemplateHash, "h3", false, ago(8)),
	}

	rep := run(t, snap, "deploy/web")
	hash := dimByName(t, rep, labelPodTemplateHash)
	if hash.Separation != result.Total {
		t.Fatalf("pod-template-hash separation = %q, want %q", hash.Separation, result.Total)
	}
	for _, id := range []string{"replicaset/web-h2", "replicaset/web-h3"} {
		if s := suspectByID(t, rep, id); s.Verdict != result.Splits || s.Dimension != labelPodTemplateHash {
			t.Errorf("%s = %s on %q, want SPLITS on pod-template-hash: its hash is on failing pods only", id, s.Verdict, s.Dimension)
		}
	}
	if s := suspectByID(t, rep, "replicaset/web-h1"); s.Verdict != result.NoSplit {
		t.Errorf("replicaset/web-h1 = %s, want NO-SPLIT: its hash is on the healthy side", s.Verdict)
	}
}

func TestSelectorEmptyValueRequiresTheKey(t *testing.T) {
	sel := map[string]string{"app": "web", "tier": ""}
	if selectorMatches(sel, map[string]string{"app": "web"}) {
		t.Error("a selector value of \"\" must require the label key to exist on the pod")
	}
	if !selectorMatches(sel, map[string]string{"app": "web", "tier": ""}) {
		t.Error("a pod carrying the key with an empty value must match")
	}
}

func TestShortRevisionCutsRunesNotBytes(t *testing.T) {
	long := "a" + strings.Repeat("€", 12)
	got := shortRevision(long)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 12 {
		t.Errorf("shortRevision(%q) = %q (valid UTF-8 %v, %d runes), want 12 whole runes", long, got, utf8.ValidString(got), utf8.RuneCountInString(got))
	}
	if got := shortRevision("a1b2c3d4e5f6a7b8"); got != "a1b2c3d4e5f6" {
		t.Errorf("shortRevision(sha) = %q, want the first 12 characters", got)
	}
	if got := shortRevision("short"); got != "short" {
		t.Errorf("shortRevision(short) = %q, want it unchanged", got)
	}
}

func TestNoReferenceTimeIsReportedNotSilentlyEmpty(t *testing.T) {
	w := workload("Deployment", "web", "shop")
	snap := snapshotOf(w)
	snap.CollectedAt = time.Time{}
	snap.ReplicaSets = []model.ReplicaSet{
		replicaSet(w, "h1", "1", ago(300), "web:1"),
		replicaSet(w, "h2", "2", ago(20), "web:2"),
	}
	snap.Pods = []model.Pod{
		podOf(w, "web-1", labelPodTemplateHash, "h1", true, ago(250)),
		podOf(w, "web-2", labelPodTemplateHash, "h2", false, ago(15)),
	}

	rep, err := Analyze(snap, Options{Workload: "deploy/web"})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !hasSubstring(rep.Gaps, "reference time") {
		t.Errorf("gaps = %v, want the missing reference time reported", rep.Gaps)
	}
	top := topSuspect(t, rep)
	if top.ID != "replicaset/web-h2" || top.Verdict != result.Splits {
		t.Errorf("top suspect = %s %s, want replicaset/web-h2 SPLITS: the retained changes are still ranked", top.ID, top.Verdict)
	}
	if !rep.GeneratedAt.IsZero() {
		t.Errorf("generatedAt = %s, want zero: no time is invented", rep.GeneratedAt)
	}
}

func TestClassifyActorRoles(t *testing.T) {
	cases := map[string]result.Actor{
		"":                              result.ActorUnknown,
		"kubectl-client-side-apply":     result.ActorHuman,
		"kubectl-edit":                  result.ActorHuman,
		"kubectl":                       result.ActorHuman,
		"kubectl-ai":                    result.ActorAgent,
		"Kubectl-AI":                    result.ActorAgent,
		"claude-code":                   result.ActorAgent,
		"argocd-application-controller": result.ActorController,
		"argocd-controller":             result.ActorController,
		"kustomize-controller":          result.ActorUnknown,
		"flux-controller":               result.ActorController,
		"gitlab-runner":                 result.ActorPipeline,
		"github-actions":                result.ActorPipeline,
		"helm":                          result.ActorUnknown,
		"kube-controller-manager":       result.ActorUnknown,
	}
	for manager, want := range cases {
		if got := classifyActor(manager); got != want {
			t.Errorf("classifyActor(%q) = %q, want %q", manager, got, want)
		}
	}
}

func TestSparseInputsDoNotPanic(t *testing.T) {
	type tc struct {
		name  string
		build func() (*model.Snapshot, string)
		gap   string
	}
	cases := []tc{
		{"nil selector", func() (*model.Snapshot, string) {
			w := workload("Deployment", "web", "shop")
			w.Spec.Selector = nil
			snap := snapshotOf(w)
			snap.Pods = []model.Pod{podOf(w, "web-1", labelPodTemplateHash, "h1", false, ago(5))}
			return snap, "deploy/web"
		}, "no spec.selector.matchLabels"},
		{"no pods at all", func() (*model.Snapshot, string) {
			return snapshotOf(workload("Deployment", "web", "shop")), "deploy/web"
		}, "no pod matches the selector"},
		{"pod with no status at all", func() (*model.Snapshot, string) {
			w := workload("Deployment", "web", "shop")
			snap := snapshotOf(w)
			snap.Pods = []model.Pod{{Metadata: model.ObjectMeta{Name: "web-1", Namespace: "shop", Labels: map[string]string{"app": "web"}}}}
			return snap, "deploy/web"
		}, "neither cohort"},
		{"statefulset without controllerrevisions", func() (*model.Snapshot, string) {
			w := workload("StatefulSet", "redis", "cache")
			snap := snapshotOf(w)
			snap.Pods = []model.Pod{
				podOf(w, "redis-0", labelControllerRevision, "redis-aaa", true, ago(250)),
				podOf(w, "redis-1", labelControllerRevision, "redis-bbb", false, ago(15)),
			}
			return snap, "sts/redis"
		}, "no ControllerRevision"},
		{"daemonset without controllerrevisions and every pod failing", func() (*model.Snapshot, string) {
			w := workload("DaemonSet", "exporter", "monitoring")
			snap := snapshotOf(w)
			snap.Pods = []model.Pod{podOf(w, "exporter-1", labelControllerRevision, "ddd", false, ago(15))}
			return snap, "ds/exporter"
		}, "no revision of daemonset/exporter is retained"},
		{"deployment without replicasets and every pod failing", func() (*model.Snapshot, string) {
			w := workload("Deployment", "web", "shop")
			snap := snapshotOf(w)
			snap.Pods = []model.Pod{podOf(w, "web-1", labelPodTemplateHash, "h1", false, ago(15))}
			return snap, "deploy/web"
		}, "no revision of deployment/web is retained"},
		{"replicaset without a creation timestamp", func() (*model.Snapshot, string) {
			w := workload("Deployment", "web", "shop")
			snap := snapshotOf(w)
			snap.ReplicaSets = []model.ReplicaSet{replicaSet(w, "h1", "1", time.Time{}, "web:1")}
			snap.Pods = []model.Pod{
				podOf(w, "web-1", labelPodTemplateHash, "h1", false, ago(15)),
				podOf(w, "web-2", labelPodTemplateHash, "h0", true, ago(150)),
			}
			return snap, "deploy/web"
		}, "no creationTimestamp"},
		{"failing pod on a node that was not collected", func() (*model.Snapshot, string) {
			w := workload("Deployment", "web", "shop")
			snap := snapshotOf(w)
			p := podOf(w, "web-1", labelPodTemplateHash, "h1", false, ago(15))
			p.Spec.NodeName = "gone"
			snap.Pods = []model.Pod{p, podOf(w, "web-2", labelPodTemplateHash, "h0", true, ago(150))}
			return snap, "deploy/web"
		}, "was not collected"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap, ref := c.build()
			rep := run(t, snap, ref)
			if !hasSubstring(rep.Gaps, c.gap) {
				t.Errorf("gaps = %v, want one containing %q", rep.Gaps, c.gap)
			}
			if len(rep.Suspects) == 0 {
				t.Errorf("suspects are empty: the report must always carry at least the UNKNOWN placeholder")
			}
		})
	}
}
