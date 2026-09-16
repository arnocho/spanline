// Package cohort answers one incident question, deterministically: which attribute
// separates the failing pods of a workload from the healthy ones, and which change
// created that attribute. No model is involved. Every value in the report is read
// from a field of the snapshot, and everything that was not collected or that expired
// is listed as a gap instead of being guessed.
package cohort

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
)

// DefaultWindow is how far back Analyze considers changes when Options.Window is zero.
const DefaultWindow = 24 * time.Hour

const (
	labelPodTemplateHash    = "pod-template-hash"
	labelControllerRevision = "controller-revision-hash"
	annotationChecksum      = "checksum/config"
	annotationRevision      = "deployment.kubernetes.io/revision"
)

// Options drives one analysis. Now is the reference time: Analyze never reads the wall clock.
type Options struct {
	Workload  string        // "deploy/checkout", "statefulset/redis", or a bare name
	Namespace string        // empty means every namespace the snapshot holds
	Now       time.Time     // reference time, falls back to the snapshot's own collection time
	Window    time.Duration // how far back to consider changes, default 24h
}

// Errors callers can test for.
var (
	ErrNoSnapshot = errors.New("cohort: no snapshot to analyse")
	ErrNoWorkload = errors.New("cohort: no workload named in the options")
)

// podState is the cohort a pod lands in. A pod that carries no failing signal and no
// ready condition is deliberately left out of both cohorts.
type podState int

const (
	stateUnclassified podState = iota
	stateFailing
	stateHealthy
)

func (s podState) String() string {
	switch s {
	case stateFailing:
		return "failing"
	case stateHealthy:
		return "healthy"
	default:
		return "unclassified"
	}
}

// podFacts is one selected pod plus the node it runs on, resolved once.
type podFacts struct {
	pod     model.Pod
	node    model.Node
	hasNode bool
	state   podState
	signals []string
}

func (p podFacts) name() string { return p.pod.Metadata.Name }

// analysis is the working state of one Analyze call.
type analysis struct {
	snap    *model.Snapshot
	opts    Options
	now     time.Time
	window  time.Duration
	work    model.Workload
	hashKey string

	selected []podFacts
	failing  []podFacts
	healthy  []podFacts
	other    []podFacts

	dims     []dimResult
	mode     result.WhyMode
	onsetAt  time.Time
	onsetSig string

	gaps []string
	seen map[string]bool
}

func (a *analysis) gap(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if a.seen == nil {
		a.seen = map[string]bool{}
	}
	if a.seen[line] {
		return
	}
	a.seen[line] = true
	a.gaps = append(a.gaps, line)
}

// Analyze separates the failing pods of one workload from the healthy ones and ranks the
// changes that could have created the separating attribute.
func Analyze(snap *model.Snapshot, o Options) (*result.WhyReport, error) {
	if snap == nil {
		return nil, ErrNoSnapshot
	}
	if strings.TrimSpace(o.Workload) == "" {
		return nil, ErrNoWorkload
	}

	a := &analysis{snap: snap, opts: o}
	a.now = o.Now
	if a.now.IsZero() {
		a.now = snap.CollectedAt
	}
	a.window = o.Window
	if a.window <= 0 {
		a.window = DefaultWindow
	}

	w, err := resolveWorkload(snap, o.Workload, o.Namespace)
	if err != nil {
		return nil, err
	}
	a.work = w
	a.hashKey = labelPodTemplateHash
	if w.Kind == "StatefulSet" || w.Kind == "DaemonSet" {
		a.hashKey = labelControllerRevision
	}

	for _, g := range sortedGaps(snap.Gaps) {
		a.gap("%s were not collected in this snapshot: %s", g.Resource, g.Reason)
	}

	a.selectPods()
	a.classify()
	a.findOnset()

	switch {
	case len(a.failing) == 0:
		a.mode = result.ModeCohort
		a.gap("no pod of %s carries a failing signal: there is nothing to separate", a.workloadRef())
	case len(a.healthy) == 0:
		a.mode = result.ModeRevision
	default:
		a.mode = result.ModeCohort
		a.computeDimensions()
	}

	report := &result.WhyReport{
		Context:     snap.Context,
		Namespace:   w.Metadata.Namespace,
		Workload:    a.workloadRef(),
		Mode:        a.mode,
		OnsetAt:     a.onsetAt,
		OnsetSignal: a.onsetSig,
		Failing:     cohortOf(a.failing),
		Healthy:     cohortOf(a.healthy),
		GeneratedAt: a.now,
	}

	for _, d := range a.dims {
		report.Dimensions = append(report.Dimensions, d.Dimension)
	}
	if len(a.dims) > 0 && a.dims[0].Separation == result.Total {
		report.CohortKey = a.dims[0].Name
	}

	if a.mode == result.ModeRevision {
		report.Revisions = a.revisions()
	}

	report.Suspects = a.suspects(a.collectChanges())
	a.reportGaps()
	report.Gaps = a.gaps
	return report, nil
}

func (a *analysis) workloadRef() string {
	return strings.ToLower(a.work.Kind) + "/" + a.work.Metadata.Name
}

func sortedGaps(in []model.CoverageGap) []model.CoverageGap {
	out := append([]model.CoverageGap(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Resource != out[j].Resource {
			return out[i].Resource < out[j].Resource
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// kindAliases maps what an operator types to the kind the snapshot carries.
var kindAliases = map[string]string{
	"deploy":       "Deployment",
	"deployment":   "Deployment",
	"deployments":  "Deployment",
	"sts":          "StatefulSet",
	"statefulset":  "StatefulSet",
	"statefulsets": "StatefulSet",
	"ds":           "DaemonSet",
	"daemonset":    "DaemonSet",
	"daemonsets":   "DaemonSet",
}

func splitRef(ref string) (kind, name string, err error) {
	ref = strings.TrimSpace(ref)
	prefix, rest, found := strings.Cut(ref, "/")
	if !found {
		return "", ref, nil
	}
	k, ok := kindAliases[strings.ToLower(strings.TrimSpace(prefix))]
	if !ok {
		return "", "", fmt.Errorf("cohort: unknown workload kind %q: use deploy/, statefulset/ or daemonset/, or a bare name", prefix)
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", fmt.Errorf("cohort: %q names a kind but no workload", ref)
	}
	return k, rest, nil
}

func resolveWorkload(snap *model.Snapshot, ref, ns string) (model.Workload, error) {
	kind, name, err := splitRef(ref)
	if err != nil {
		return model.Workload{}, err
	}
	var matches []model.Workload
	for _, w := range snap.Workloads() {
		if ns != "" && w.Metadata.Namespace != ns {
			continue
		}
		if w.Metadata.Name != name {
			continue
		}
		if kind != "" && w.Kind != kind {
			continue
		}
		matches = append(matches, w)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Kind != matches[j].Kind {
			return matches[i].Kind < matches[j].Kind
		}
		return matches[i].Metadata.Namespace < matches[j].Metadata.Namespace
	})

	where := "in any namespace of this snapshot"
	if ns != "" {
		where = fmt.Sprintf("in namespace %q", ns)
	}
	switch len(matches) {
	case 0:
		what := "workload"
		if kind != "" {
			what = strings.ToLower(kind)
		}
		return model.Workload{}, fmt.Errorf("cohort: no %s named %q %s", what, name, where)
	case 1:
		return matches[0], nil
	default:
		var labels []string
		for _, m := range matches {
			labels = append(labels, fmt.Sprintf("%s/%s in %s", strings.ToLower(m.Kind), m.Metadata.Name, m.Metadata.Namespace))
		}
		return model.Workload{}, fmt.Errorf("cohort: %q is ambiguous %s: %s; name the kind, for example deploy/%s",
			name, where, strings.Join(labels, ", "), name)
	}
}

// selectPods matches the workload selector against pod labels. A pod also carries the
// revision hash its controller stamped on it: when the selector does not carry that key,
// the extra label is simply not part of the comparison, which is what this loop does.
func (a *analysis) selectPods() {
	sel := a.work.Spec.Selector
	if sel == nil || len(sel.MatchLabels) == 0 {
		a.gap("%s has no spec.selector.matchLabels: no pod could be selected", a.workloadRef())
		return
	}
	nodes := a.snap.NodeByName()
	var out []podFacts
	for _, p := range a.snap.Pods {
		if p.Metadata.Namespace != a.work.Metadata.Namespace {
			continue
		}
		if !selectorMatches(sel.MatchLabels, p.Metadata.Labels) {
			continue
		}
		f := podFacts{pod: p}
		if n, ok := nodes[p.Spec.NodeName]; ok {
			f.node, f.hasNode = n, true
		} else if p.Spec.NodeName != "" {
			a.gap("pod %s runs on node %s, which was not collected: node attributes are unresolved for it",
				p.Metadata.Name, p.Spec.NodeName)
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name() < out[j].name() })
	a.selected = out
	if len(out) == 0 {
		a.gap("no pod matches the selector of %s in this snapshot", a.workloadRef())
	}
}

func selectorMatches(sel, labels map[string]string) bool {
	if len(sel) == 0 {
		return false
	}
	keys := make([]string, 0, len(sel))
	for k := range sel {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if labels[k] != sel[k] {
			return false
		}
	}
	return true
}

// failing signals, exactly as step 3 defines them.
var (
	terminatedReasons = map[string]bool{"OOMKilled": true, "Error": true}
	waitingReasons    = map[string]bool{
		"CrashLoopBackOff":           true,
		"ImagePullBackOff":           true,
		"CreateContainerConfigError": true,
	}
)

const restartThreshold = 3

// classifyPod reports the cohort of one pod and the exact fields that put it there.
func classifyPod(p model.Pod) (podState, []string) {
	var signals []string
	statuses := append([]model.ContainerStatus(nil), p.Status.ContainerStatuses...)
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	for _, cs := range statuses {
		if cs.LastState.Terminated != nil && terminatedReasons[cs.LastState.Terminated.Reason] {
			signals = append(signals, fmt.Sprintf("%s lastState.terminated.reason %s", cs.Name, cs.LastState.Terminated.Reason))
		}
		if cs.State.Waiting != nil && waitingReasons[cs.State.Waiting.Reason] {
			signals = append(signals, fmt.Sprintf("%s state.waiting.reason %s", cs.Name, cs.State.Waiting.Reason))
		}
		if cs.RestartCount >= restartThreshold {
			signals = append(signals, fmt.Sprintf("%s restartCount %d", cs.Name, cs.RestartCount))
		}
	}
	ready, hasReady := readyCondition(p)
	if hasReady && !ready {
		signals = append(signals, "conditions[Ready].status False")
	}
	if len(signals) > 0 {
		return stateFailing, signals
	}
	if hasReady && ready {
		return stateHealthy, nil
	}
	return stateUnclassified, nil
}

func readyCondition(p model.Pod) (ready, present bool) {
	conds := append([]model.PodCondition(nil), p.Status.Conditions...)
	sort.Slice(conds, func(i, j int) bool { return conds[i].Type < conds[j].Type })
	for _, c := range conds {
		if c.Type != "Ready" {
			continue
		}
		switch c.Status {
		case "True":
			return true, true
		case "False":
			return false, true
		default:
			return false, false
		}
	}
	return false, false
}

func (a *analysis) classify() {
	for i := range a.selected {
		st, sig := classifyPod(a.selected[i].pod)
		a.selected[i].state = st
		a.selected[i].signals = sig
		switch st {
		case stateFailing:
			a.failing = append(a.failing, a.selected[i])
		case stateHealthy:
			a.healthy = append(a.healthy, a.selected[i])
		default:
			a.other = append(a.other, a.selected[i])
		}
	}
	if len(a.other) > 0 {
		var names []string
		for _, p := range a.other {
			names = append(names, p.name())
		}
		a.gap("%d pod(s) match the selector but carry neither a failing signal nor a ready condition, so they are in neither cohort: %s",
			len(names), strings.Join(names, ", "))
	}
}

// onsetCandidate is one dated failing signal. rank keeps the tie break deterministic.
type onsetCandidate struct {
	at    time.Time
	rank  int
	field string
}

// findOnset takes the earliest failing signal, and records the exact field it came from.
func (a *analysis) findOnset() {
	if len(a.failing) == 0 {
		a.onsetSig = "no failing pod: no onset"
		return
	}
	failingNames := map[string]bool{}
	for _, p := range a.failing {
		failingNames[p.name()] = true
	}

	var cands []onsetCandidate
	for _, p := range a.failing {
		statuses := append([]model.ContainerStatus(nil), p.pod.Status.ContainerStatuses...)
		sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
		for _, cs := range statuses {
			t := cs.LastState.Terminated
			if t == nil || !terminatedReasons[t.Reason] || t.FinishedAt.IsZero() {
				continue
			}
			cands = append(cands, onsetCandidate{
				at:   t.FinishedAt,
				rank: 1,
				field: fmt.Sprintf("pod %s status.containerStatuses[%s].lastState.terminated.finishedAt (%s)",
					p.name(), cs.Name, t.Reason),
			})
		}
		conds := append([]model.PodCondition(nil), p.pod.Status.Conditions...)
		sort.Slice(conds, func(i, j int) bool { return conds[i].Type < conds[j].Type })
		for _, c := range conds {
			if c.Type != "Ready" || c.Status != "False" || c.LastTransitionTime.IsZero() {
				continue
			}
			cands = append(cands, onsetCandidate{
				at:    c.LastTransitionTime,
				rank:  2,
				field: fmt.Sprintf("pod %s status.conditions[Ready].lastTransitionTime (Ready=False)", p.name()),
			})
		}
	}

	events := a.warningEvents(failingNames)
	for _, e := range events {
		if e.FirstTimestamp.IsZero() {
			continue
		}
		cands = append(cands, onsetCandidate{
			at:   e.FirstTimestamp,
			rank: 3,
			field: fmt.Sprintf("event %s firstTimestamp (reason %s on pod %s)",
				e.Metadata.Name, e.Reason, e.InvolvedObject.Name),
		})
	}
	if len(events) == 0 {
		a.gap("no Warning event is retained for the failing pods: onset rests on container and condition timestamps only")
	}

	if len(cands) == 0 {
		a.onsetSig = "no dated failing signal is retained: onset is unknown"
		a.gap("no failing signal carries a timestamp: the onset could not be dated")
		return
	}
	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].at.Equal(cands[j].at) {
			return cands[i].at.Before(cands[j].at)
		}
		if cands[i].rank != cands[j].rank {
			return cands[i].rank < cands[j].rank
		}
		return cands[i].field < cands[j].field
	})
	a.onsetAt = cands[0].at
	a.onsetSig = cands[0].field
}

var onsetEventReasons = map[string]bool{
	"OOMKilling": true, "BackOff": true, "Unhealthy": true, "Failed": true,
}

func (a *analysis) warningEvents(pods map[string]bool) []model.Event {
	var out []model.Event
	for _, e := range a.snap.Events {
		if e.Type != "Warning" || !onsetEventReasons[e.Reason] {
			continue
		}
		if e.InvolvedObject.Kind != "Pod" || !pods[e.InvolvedObject.Name] {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FirstTimestamp.Equal(out[j].FirstTimestamp) {
			return out[i].FirstTimestamp.Before(out[j].FirstTimestamp)
		}
		return out[i].Metadata.Name < out[j].Metadata.Name
	})
	return out
}

func cohortOf(pods []podFacts) result.Cohort {
	c := result.Cohort{Count: len(pods)}
	for _, p := range pods {
		c.Pods = append(c.Pods, p.name())
	}
	sort.Strings(c.Pods)
	if len(c.Pods) > 0 {
		c.Sample = c.Pods[0]
	}
	return c
}

// reportGaps appends the standing gaps: what a snapshot structurally cannot hold.
func (a *analysis) reportGaps() {
	for _, d := range a.dims {
		if d.Separation != result.Unresolved {
			continue
		}
		a.gap("dimension %q is unresolved (the value is missing on %s), so it is not a match and not a clean split either",
			d.Name, d.missingSide)
	}
	a.gap("Helm release history is not collected: a Helm upgrade would not appear in this change list")
	a.gap("container env var names are not part of the collected snapshot, so no diff line can report an env change")
}
