package cohort

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arnocho/spanline/internal/model"
	"github.com/arnocho/spanline/internal/result"
)

// change is one dated modification spanline can still read in the snapshot. values holds
// the cohort attributes the change carries, keyed by dimension name: that is what lets a
// change be matched against a dimension that splits the cohorts.
type change struct {
	id          string
	at          time.Time
	title       string
	values      map[string]string
	diff        []string
	actor       result.Actor
	attribution string
	evidence    []string
	onOwner     bool
}

func (a *analysis) inWindow(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	if a.now.IsZero() {
		// No reference time: the window cannot be applied, which Analyze lists as a gap.
		return true
	}
	if t.After(a.now) {
		return false
	}
	return !t.Before(a.now.Add(-a.window))
}

// ownedReplicaSets returns the workload's ReplicaSets, oldest revision first.
func (a *analysis) ownedReplicaSets() []model.ReplicaSet {
	var out []model.ReplicaSet
	for _, rs := range a.snap.ReplicaSets {
		if rs.Metadata.Namespace != a.work.Metadata.Namespace {
			continue
		}
		if !ownedBy(rs.Metadata.OwnerReferences, a.work) {
			continue
		}
		out = append(out, rs)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, _ := revisionNumber(out[i].Metadata)
		rj, _ := revisionNumber(out[j].Metadata)
		if ri != rj {
			return ri < rj
		}
		if !out[i].Metadata.CreationTimestamp.Equal(out[j].Metadata.CreationTimestamp) {
			return out[i].Metadata.CreationTimestamp.Before(out[j].Metadata.CreationTimestamp)
		}
		return out[i].Metadata.Name < out[j].Metadata.Name
	})
	return out
}

// ownedControllerRevisions returns the workload's ControllerRevisions, oldest first.
func (a *analysis) ownedControllerRevisions() []model.ControllerRevision {
	var out []model.ControllerRevision
	for _, cr := range a.snap.ControllerRevisions {
		if cr.Metadata.Namespace != a.work.Metadata.Namespace {
			continue
		}
		if !ownedBy(cr.Metadata.OwnerReferences, a.work) {
			continue
		}
		out = append(out, cr)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Revision != out[j].Revision {
			return out[i].Revision < out[j].Revision
		}
		return out[i].Metadata.Name < out[j].Metadata.Name
	})
	return out
}

// revisionKey is the value the workload's pods carry in their controller-revision-hash
// label for one ControllerRevision. A StatefulSet stamps the revision's name on its pods;
// a DaemonSet stamps the bare hash the revision itself is labelled with.
func (a *analysis) revisionKey(cr model.ControllerRevision) string {
	if a.work.Kind == "StatefulSet" {
		return cr.Metadata.Name
	}
	if hash := cr.Metadata.Labels[labelControllerRevision]; hash != "" {
		return hash
	}
	return cr.Metadata.Name
}

func ownedBy(refs []model.OwnerReference, w model.Workload) bool {
	for _, r := range refs {
		if r.Kind != w.Kind || r.Name != w.Metadata.Name {
			continue
		}
		if r.UID != "" && w.Metadata.UID != "" && r.UID != w.Metadata.UID {
			continue
		}
		return true
	}
	return false
}

// revisionNumber reads the Deployment revision annotation. ok is false when the annotation
// is absent or unreadable: the number is then 0 and is never printed as a fact.
func revisionNumber(m model.ObjectMeta) (int, bool) {
	v, ok := m.Annotations[annotationRevision]
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, false
	}
	return n, true
}

// revisionLabel names a ReplicaSet's revision for a title or an evidence line.
func revisionLabel(m model.ObjectMeta) string {
	if n, ok := revisionNumber(m); ok {
		return fmt.Sprintf("at revision %d", n)
	}
	return "with no revision annotation"
}

func replicasOf(spec model.WorkloadSpec) int {
	if spec.Replicas == nil {
		return 0
	}
	return *spec.Replicas
}

// collectChanges reads every change source the snapshot still holds, in a fixed order.
func (a *analysis) collectChanges() []change {
	var out []change
	out = append(out, a.revisionChanges()...)
	out = append(out, a.managedFieldChanges()...)
	out = append(out, a.argoChanges()...)
	out = append(out, a.nodeChanges()...)
	return out
}

// revisionChanges turns each retained ReplicaSet or ControllerRevision into a change,
// diffed against the revision immediately below it.
func (a *analysis) revisionChanges() []change {
	var out []change
	if a.hashKey == labelPodTemplateHash {
		sets := a.ownedReplicaSets()
		if len(sets) < 2 {
			a.gap("only %d ReplicaSet(s) of %s are retained: an older template cannot be compared",
				len(sets), a.workloadRef())
		}
		for i, rs := range sets {
			if _, ok := revisionNumber(rs.Metadata); !ok {
				a.gap("ReplicaSet %s carries no readable %s annotation: its revision number is unknown and it is ordered by creation time",
					rs.Metadata.Name, annotationRevision)
			}
			if rs.Metadata.CreationTimestamp.IsZero() {
				a.gap("ReplicaSet %s carries no creationTimestamp: it cannot be placed in the window and is not ranked", rs.Metadata.Name)
			}
			if !a.inWindow(rs.Metadata.CreationTimestamp) {
				continue
			}
			c := change{
				id:      "replicaset/" + rs.Metadata.Name,
				at:      rs.Metadata.CreationTimestamp,
				title:   fmt.Sprintf("ReplicaSet %s created %s", rs.Metadata.Name, revisionLabel(rs.Metadata)),
				onOwner: true,
				evidence: []string{
					fmt.Sprintf("replicaset %s/%s metadata.creationTimestamp %s",
						rs.Metadata.Namespace, rs.Metadata.Name, rs.Metadata.CreationTimestamp.UTC().Format(time.RFC3339)),
				},
			}
			if n, ok := revisionNumber(rs.Metadata); ok {
				c.evidence = append(c.evidence, fmt.Sprintf("replicaset %s/%s metadata.annotations[%s] = %d",
					rs.Metadata.Namespace, rs.Metadata.Name, annotationRevision, n))
			} else {
				c.evidence = append(c.evidence, fmt.Sprintf("replicaset %s/%s metadata.annotations[%s] is absent",
					rs.Metadata.Namespace, rs.Metadata.Name, annotationRevision))
			}
			if hash := rs.Metadata.Labels[labelPodTemplateHash]; hash != "" {
				c.values = map[string]string{labelPodTemplateHash: hash}
				c.evidence = append(c.evidence, fmt.Sprintf("replicaset %s/%s metadata.labels[%s] = %s",
					rs.Metadata.Namespace, rs.Metadata.Name, labelPodTemplateHash, hash))
			}
			if i > 0 {
				prev := sets[i-1]
				c.diff = templateDiff(prev.Spec.Template, rs.Spec.Template,
					replicasOf(prev.Spec), replicasOf(rs.Spec))
				c.evidence = append(c.evidence, fmt.Sprintf("diffed against replicaset %s/%s %s",
					prev.Metadata.Namespace, prev.Metadata.Name, revisionLabel(prev.Metadata)))
			} else {
				c.evidence = append(c.evidence, "no older ReplicaSet is retained: no template diff is available")
			}
			a.attribute(&c, rs.Metadata.ManagedFields)
			out = append(out, c)
		}
		return out
	}

	revs := a.ownedControllerRevisions()
	if len(revs) == 0 {
		a.gap("no ControllerRevision of %s is retained: no template can be compared", a.workloadRef())
	} else if len(revs) < 2 {
		a.gap("only %d ControllerRevision(s) of %s are retained: an older template cannot be compared",
			len(revs), a.workloadRef())
	}
	for i, cr := range revs {
		if cr.Metadata.CreationTimestamp.IsZero() {
			a.gap("ControllerRevision %s carries no creationTimestamp: it cannot be placed in the window and is not ranked", cr.Metadata.Name)
		}
		if !a.inWindow(cr.Metadata.CreationTimestamp) {
			continue
		}
		c := change{
			id:      "controllerrevision/" + cr.Metadata.Name,
			at:      cr.Metadata.CreationTimestamp,
			title:   fmt.Sprintf("ControllerRevision %s created at revision %d", cr.Metadata.Name, cr.Revision),
			onOwner: true,
			evidence: []string{
				fmt.Sprintf("controllerrevision %s/%s metadata.creationTimestamp %s",
					cr.Metadata.Namespace, cr.Metadata.Name, cr.Metadata.CreationTimestamp.UTC().Format(time.RFC3339)),
				fmt.Sprintf("controllerrevision %s/%s revision = %d", cr.Metadata.Namespace, cr.Metadata.Name, cr.Revision),
			},
		}
		c.values = map[string]string{labelControllerRevision: a.revisionKey(cr)}
		if i > 0 {
			prev := revs[i-1]
			c.diff = templateDiff(prev.Data.Spec.Template, cr.Data.Spec.Template, 0, 0)
			c.evidence = append(c.evidence, fmt.Sprintf("diffed against controllerrevision %s/%s at revision %d",
				prev.Metadata.Namespace, prev.Metadata.Name, prev.Revision))
		} else {
			c.evidence = append(c.evidence, "no older ControllerRevision is retained: no template diff is available")
		}
		a.attribute(&c, cr.Metadata.ManagedFields)
		out = append(out, c)
	}
	return out
}

// managedFieldChanges reads who last wrote the workload object, and when.
func (a *analysis) managedFieldChanges() []change {
	type indexed struct {
		model.ManagedFieldsEntry
		index int // position in the object's own managedFields, which the evidence path names
	}
	var entries []indexed
	for i, e := range a.work.Metadata.ManagedFields {
		// A status write is the controller reporting on the workload, not a change to it.
		if e.Subresource == "status" {
			continue
		}
		entries = append(entries, indexed{ManagedFieldsEntry: e, index: i})
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].Time.Equal(entries[j].Time) {
			return entries[i].Time.Before(entries[j].Time)
		}
		if entries[i].Manager != entries[j].Manager {
			return entries[i].Manager < entries[j].Manager
		}
		return entries[i].index < entries[j].index
	})
	object := fmt.Sprintf("%s %s/%s", strings.ToLower(a.work.Kind), a.work.Metadata.Namespace, a.work.Metadata.Name)
	var out []change
	seen := map[string]bool{}
	for _, e := range entries {
		if !a.inWindow(e.Time) {
			continue
		}
		actor := classifyActor(e.Manager)
		id := fmt.Sprintf("managedfields/%s#%s@%s", a.work.Metadata.Name, e.Manager, e.Time.UTC().Format(time.RFC3339))
		if e.Subresource != "" {
			id += "/" + e.Subresource
		}
		if seen[id] {
			// Twin entries: the object's own index keeps every id unique, so the order holds.
			id = fmt.Sprintf("%s[%d]", id, e.index)
		}
		seen[id] = true
		c := change{
			id:      id,
			at:      e.Time,
			title:   fmt.Sprintf("field manager %s ran %s on %s", e.Manager, strings.ToLower(orUnknown(e.Operation)), a.workloadRef()),
			onOwner: true,
			actor:   actor,
			evidence: []string{
				fmt.Sprintf("%s metadata.managedFields[%d].manager = %s", object, e.index, e.Manager),
				fmt.Sprintf("%s metadata.managedFields[%d].time = %s", object, e.index, e.Time.UTC().Format(time.RFC3339)),
			},
		}
		if e.Subresource != "" {
			c.evidence = append(c.evidence, fmt.Sprintf("%s metadata.managedFields[%d].subresource = %s", object, e.index, e.Subresource))
		}
		c.attribution = a.attributionFor(actor, e.Manager, e.Time)
		out = append(out, c)
	}
	if len(out) > 0 {
		a.gap("managedFields records the field manager and the time, not the field values: those entries carry no diff")
	}
	return out
}

// argoChanges reads the sync history of the Argo CD applications that target this namespace.
// The person who started a sync is never read, and never reported.
func (a *analysis) argoChanges() []change {
	apps := a.argoApps()
	if len(apps) == 0 {
		a.gap("no Argo CD application targets namespace %q: no GitOps attribution is available",
			a.work.Metadata.Namespace)
		return nil
	}
	var out []change
	for _, app := range apps {
		history := append([]model.ArgoHistoryEntry(nil), app.Status.History...)
		sort.Slice(history, func(i, j int) bool {
			if !history[i].DeployedAt.Equal(history[j].DeployedAt) {
				return history[i].DeployedAt.Before(history[j].DeployedAt)
			}
			return history[i].ID < history[j].ID
		})
		for _, h := range history {
			if !a.inWindow(h.DeployedAt) {
				continue
			}
			out = append(out, change{
				id:          fmt.Sprintf("argocd/%s#%d", app.Metadata.Name, h.ID),
				at:          h.DeployedAt,
				title:       fmt.Sprintf("Argo CD application %s deployed revision %s", app.Metadata.Name, shortRevision(h.Revision)),
				actor:       result.ActorController,
				attribution: fmt.Sprintf("argocd/%s revision %s", app.Metadata.Name, shortRevision(h.Revision)),
				evidence: []string{
					fmt.Sprintf("application %s/%s status.history[id=%d].revision = %s",
						app.Metadata.Namespace, app.Metadata.Name, h.ID, h.Revision),
					fmt.Sprintf("application %s/%s status.history[id=%d].deployedAt = %s",
						app.Metadata.Namespace, app.Metadata.Name, h.ID, h.DeployedAt.UTC().Format(time.RFC3339)),
				},
			})
		}
	}
	return out
}

func (a *analysis) argoApps() []model.ArgoApplication {
	var out []model.ArgoApplication
	for _, app := range a.snap.ArgoApps {
		if app.Spec.Destination.Namespace != a.work.Metadata.Namespace {
			continue
		}
		out = append(out, app)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Name < out[j].Metadata.Name })
	return out
}

// nodeChanges reports the nodes that joined carrying the value of the node attribute that
// separates the cohorts best. It runs only when a node dimension separates them totally.
func (a *analysis) nodeChanges() []change {
	var top *dimResult
	for i := range a.dims {
		if a.dims[i].node && a.dims[i].Separation == result.Total && a.dims[i].discriminator != "" {
			top = &a.dims[i]
			break
		}
	}
	if top == nil {
		return nil
	}
	set := dimensionSet(a.hashKey)
	var d *dimension
	for i := range set {
		if set[i].name == top.Name {
			d = &set[i]
			break
		}
	}
	if d == nil {
		return nil
	}

	var names []string
	var evidence []string
	at := time.Time{}
	var managed []model.ManagedFieldsEntry
	for _, p := range a.failing {
		if !p.hasNode {
			continue
		}
		v, ok := d.value(p)
		if !ok || v != top.discriminator {
			continue
		}
		if contains(names, p.node.Metadata.Name) {
			continue
		}
		if !a.inWindow(p.node.Metadata.CreationTimestamp) {
			continue
		}
		names = append(names, p.node.Metadata.Name)
		evidence = append(evidence, fmt.Sprintf("node %s metadata.creationTimestamp %s, %s = %s",
			p.node.Metadata.Name, p.node.Metadata.CreationTimestamp.UTC().Format(time.RFC3339), top.Name, v))
		if at.IsZero() || p.node.Metadata.CreationTimestamp.Before(at) {
			at = p.node.Metadata.CreationTimestamp
		}
		managed = append(managed, p.node.Metadata.ManagedFields...)
	}
	if len(names) == 0 {
		a.gap("the %s that separates the cohorts is not carried by any node that joined inside the window: the change that set it is not retained",
			top.Name)
		return nil
	}
	sort.Strings(names)
	sort.Strings(evidence)

	c := change{
		id:    fmt.Sprintf("nodes/%s=%s", top.Name, top.discriminator),
		at:    at,
		title: fmt.Sprintf("%d node(s) hosting the failing pods joined with %s %s", len(names), top.Name, top.discriminator),
		values: map[string]string{
			top.Name: top.discriminator,
		},
		evidence: evidence,
	}
	a.attribute(&c, managed)
	if c.actor == result.ActorUnknown {
		a.gap("no field manager is retained for node(s) %s: the change that joined them is unattributed",
			strings.Join(names, ", "))
	}
	return []change{c}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// attribute fills the actor and the attribution of a change from its field managers.
func (a *analysis) attribute(c *change, entries []model.ManagedFieldsEntry) {
	manager := lastManager(entries)
	c.actor = classifyActor(manager)
	c.attribution = a.attributionFor(c.actor, manager, c.at)
	if manager != "" {
		c.evidence = append(c.evidence, "field manager "+manager)
	}
}

func lastManager(entries []model.ManagedFieldsEntry) string {
	sorted := append([]model.ManagedFieldsEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].Time.Equal(sorted[j].Time) {
			return sorted[i].Time.Before(sorted[j].Time)
		}
		return sorted[i].Manager < sorted[j].Manager
	})
	for i := len(sorted) - 1; i >= 0; i-- {
		if sorted[i].Manager != "" {
			return sorted[i].Manager
		}
	}
	return ""
}

// agentManagers is the declared list of agent field managers.
var agentManagers = []string{"kubectl-ai", "claude", "codex", "cursor", "mcp"}

// classifyActor maps a field manager to a role. It never returns a person's name.
func classifyActor(manager string) result.Actor {
	m := strings.ToLower(strings.TrimSpace(manager))
	if m == "" {
		return result.ActorUnknown
	}
	switch {
	case strings.Contains(m, "argocd"), strings.Contains(m, "argo-cd"), strings.Contains(m, "flux"):
		return result.ActorController
	case strings.Contains(m, "gitlab"), strings.Contains(m, "jenkins"), strings.Contains(m, "github"):
		return result.ActorPipeline
	}
	for _, agent := range agentManagers {
		if strings.Contains(m, agent) {
			return result.ActorAgent
		}
	}
	if strings.Contains(m, "kubectl") {
		return result.ActorHuman
	}
	return result.ActorUnknown
}

// attributionFor names what the actor acted through: the Argo revision for a controller,
// the field manager otherwise. It is a role or a revision, never a person.
func (a *analysis) attributionFor(actor result.Actor, manager string, at time.Time) string {
	if actor == result.ActorController {
		if app, rev, ok := a.argoRevisionAt(at); ok {
			return fmt.Sprintf("argocd/%s revision %s", app, shortRevision(rev))
		}
	}
	if manager == "" {
		return ""
	}
	return "field manager " + manager
}

// argoRevisionAt returns the revision the Argo application had deployed at that moment.
func (a *analysis) argoRevisionAt(at time.Time) (app, revision string, ok bool) {
	if at.IsZero() {
		return "", "", false
	}
	best := time.Time{}
	for _, application := range a.argoApps() {
		history := append([]model.ArgoHistoryEntry(nil), application.Status.History...)
		sort.Slice(history, func(i, j int) bool {
			if !history[i].DeployedAt.Equal(history[j].DeployedAt) {
				return history[i].DeployedAt.Before(history[j].DeployedAt)
			}
			return history[i].ID < history[j].ID
		})
		for _, h := range history {
			if h.DeployedAt.IsZero() || h.DeployedAt.After(at) {
				continue
			}
			if best.IsZero() || h.DeployedAt.After(best) || h.DeployedAt.Equal(best) {
				best, app, revision, ok = h.DeployedAt, application.Metadata.Name, h.Revision, true
			}
		}
	}
	return app, revision, ok
}

// shortRevision keeps the first 12 characters of a revision, whole runes only, so a
// non-ASCII revision never turns into invalid UTF-8.
func shortRevision(rev string) string {
	const keep = 12
	if utf8.RuneCountInString(rev) <= keep {
		return rev
	}
	return string([]rune(rev)[:keep])
}

func orUnknown(s string) string {
	if s == "" {
		return "an update"
	}
	return s
}

// suspects ranks every change against the cohorts. A verdict is never stronger than the
// evidence: SPLITS needs a total split, TEMPORAL needs the fallback and the owning object.
func (a *analysis) suspects(changes []change) []result.Suspect {
	if len(changes) == 0 {
		a.gap("no change of %s is retained inside the %s window: nothing can be ranked",
			a.workloadRef(), a.window)
		return []result.Suspect{{
			ID:       "none",
			Verdict:  result.Unknown,
			Title:    fmt.Sprintf("no retained change for %s inside the window", a.workloadRef()),
			Actor:    result.ActorUnknown,
			Evidence: []string{"no ReplicaSet, ControllerRevision, managedFields entry or Argo CD history entry falls inside the window"},
		}}
	}

	out := make([]result.Suspect, 0, len(changes))
	for _, c := range changes {
		s := result.Suspect{
			ID:          c.id,
			At:          c.at,
			Title:       c.title,
			Diff:        c.diff,
			Attribution: c.attribution,
			Actor:       c.actor,
			Evidence:    c.evidence,
		}
		if s.Actor == "" {
			s.Actor = result.ActorUnknown
		}
		s.Verdict, s.Dimension = a.verdict(c)
		out = append(out, s)
	}

	sort.Slice(out, func(i, j int) bool {
		vi, vj := verdictRank(out[i].Verdict), verdictRank(out[j].Verdict)
		if vi != vj {
			return vi > vj
		}
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (a *analysis) verdict(c change) (result.Verdict, string) {
	for _, d := range a.dims {
		if d.Separation != result.Total {
			continue
		}
		// In a total split every failing value is absent from the healthy side: a change
		// that carries any of them created a failing-only attribute, not just the top one.
		if v, ok := c.values[d.Name]; ok && d.failingValues[v] > 0 {
			return result.Splits, d.Name
		}
	}
	if c.at.IsZero() {
		return result.Unknown, ""
	}
	if a.mode == result.ModeRevision {
		if a.onsetAt.IsZero() {
			return result.Unknown, ""
		}
		if c.onOwner && !c.at.After(a.onsetAt) {
			return result.Temporal, ""
		}
	}
	return result.NoSplit, ""
}

func verdictRank(v result.Verdict) int {
	switch v {
	case result.Splits:
		return 3
	case result.Temporal:
		return 2
	case result.NoSplit:
		return 1
	default:
		return 0
	}
}

// revisions compares the current revision with the one below it, for the fallback where
// no healthy pod is left to compare against.
func (a *analysis) revisions() []result.Revision {
	type entry struct {
		name     string
		number   int
		created  time.Time
		replicas int
		hash     string
	}
	var entries []entry
	if a.hashKey == labelPodTemplateHash {
		for _, rs := range a.ownedReplicaSets() {
			number, _ := revisionNumber(rs.Metadata)
			entries = append(entries, entry{
				name:     rs.Metadata.Name,
				number:   number,
				created:  rs.Metadata.CreationTimestamp,
				replicas: replicasOf(rs.Spec),
				hash:     rs.Metadata.Labels[labelPodTemplateHash],
			})
		}
	} else {
		for _, cr := range a.ownedControllerRevisions() {
			entries = append(entries, entry{
				name:    cr.Metadata.Name,
				number:  cr.Revision,
				created: cr.Metadata.CreationTimestamp,
				hash:    a.revisionKey(cr),
			})
		}
	}
	if len(entries) == 0 {
		a.gap("no revision of %s is retained: the fallback has nothing to compare", a.workloadRef())
		return nil
	}

	// entries are oldest first: the current revision is the last one.
	pick := entries
	if len(entries) > 2 {
		pick = entries[len(entries)-2:]
	}
	out := make([]result.Revision, 0, len(pick))
	for i := len(pick) - 1; i >= 0; i-- {
		e := pick[i]
		active := "no"
		if i == len(pick)-1 {
			active = "yes"
		}
		out = append(out, result.Revision{
			Name:     e.name,
			Number:   e.number,
			Active:   active,
			Signals:  a.revisionSignals(e.hash, active == "yes"),
			Created:  e.created,
			Replicas: e.replicas,
		})
	}
	if len(pick) < 2 {
		a.gap("only one revision of %s is retained: the revision below it expired and cannot be compared",
			a.workloadRef())
	} else if total, _, _, _ := a.revisionPods(pick[0].hash); pick[0].hash != "" && total == 0 {
		a.gap("revision %s has no pod left in the cluster: its health rests on retained events, not on its pods",
			pick[0].name)
	}
	return out
}

// revisionPods counts the selected pods that carry one revision hash, how many of them
// fall in each cohort, and the container reasons they show, sorted.
func (a *analysis) revisionPods(hash string) (total, failing, healthy int, reasons []string) {
	if hash == "" {
		return 0, 0, 0, nil
	}
	seen := map[string]bool{}
	for _, p := range a.selected {
		if p.pod.Metadata.Labels[a.hashKey] != hash {
			continue
		}
		total++
		switch p.state {
		case stateFailing:
			failing++
		case stateHealthy:
			healthy++
		}
		for _, cs := range p.pod.Status.ContainerStatuses {
			if cs.LastState.Terminated != nil && cs.LastState.Terminated.Reason != "" {
				seen[cs.LastState.Terminated.Reason] = true
			}
			if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
				seen[cs.State.Waiting.Reason] = true
			}
		}
	}
	for r := range seen {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	return total, failing, healthy, reasons
}

func (a *analysis) revisionSignals(hash string, current bool) string {
	if hash == "" {
		return "no revision hash on this revision: its pods cannot be identified"
	}
	total, failing, healthy, reasons := a.revisionPods(hash)
	if total == 0 {
		if current {
			return "no pod retained for this revision"
		}
		return "no pod retained: health rests on retained events"
	}
	line := fmt.Sprintf("%d pod(s), %d failing, %d healthy", total, failing, healthy)
	if len(reasons) > 0 {
		line += " (" + strings.Join(reasons, ", ") + ")"
	}
	return line
}

// templateDiff reports the pod template fields that changed, one line per field. Env var
// values are never read: the snapshot does not carry them, which is listed as a gap.
func templateDiff(prev, cur model.PodTemplateSpec, prevReplicas, curReplicas int) []string {
	var out []string

	prevByName := map[string]model.Container{}
	for _, c := range prev.Spec.Containers {
		prevByName[c.Name] = c
	}
	curByName := map[string]model.Container{}
	for _, c := range cur.Spec.Containers {
		curByName[c.Name] = c
	}
	names := unionKeys(prevByName, curByName)
	prefix := func(name string) string {
		if len(cur.Spec.Containers) > 1 || len(prev.Spec.Containers) > 1 {
			return name + "."
		}
		return ""
	}

	for _, name := range names {
		p, hadPrev := prevByName[name]
		c, hasCur := curByName[name]
		switch {
		case !hadPrev:
			out = append(out, fmt.Sprintf("container %s added", name))
			continue
		case !hasCur:
			out = append(out, fmt.Sprintf("container %s removed", name))
			continue
		}
		if p.Image != c.Image {
			out = append(out, fmt.Sprintf("%simage %s -> %s", prefix(name), orAbsent(p.Image), orAbsent(c.Image)))
		}
		out = append(out, resourceDiff(prefix(name)+"resources.requests", p.Resources.Requests, c.Resources.Requests)...)
		out = append(out, resourceDiff(prefix(name)+"resources.limits", p.Resources.Limits, c.Resources.Limits)...)
	}

	if prevReplicas != curReplicas {
		out = append(out, fmt.Sprintf("replicas %d -> %d", prevReplicas, curReplicas))
	}
	prevSum := prev.Metadata.Annotations[annotationChecksum]
	curSum := cur.Metadata.Annotations[annotationChecksum]
	if prevSum != curSum {
		out = append(out, fmt.Sprintf("annotations.%s %s -> %s", annotationChecksum, orAbsent(prevSum), orAbsent(curSum)))
	}
	return out
}

func resourceDiff(field string, prev, cur map[string]string) []string {
	var out []string
	for _, k := range unionKeys(prev, cur) {
		if sameQuantity(k, prev[k], cur[k]) {
			continue
		}
		out = append(out, fmt.Sprintf("%s.%s %s -> %s", field, k, orAbsent(prev[k]), orAbsent(cur[k])))
	}
	return out
}

// sameQuantity compares two resource quantities by value, so that 512Mi and 536870912, or
// 0.5 and 500m, are never reported as a change. Values that do not parse compare as text.
func sameQuantity(resource, a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	if resource == "cpu" {
		x, okX := model.ParseCPU(a)
		y, okY := model.ParseCPU(b)
		return okX && okY && x == y
	}
	x, okX := model.ParseMemory(a)
	y, okY := model.ParseMemory(b)
	return okX && okY && x == y
}

func unionKeys[T any](a, b map[string]T) []string {
	seen := map[string]bool{}
	var keys []string
	for k := range a {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func orAbsent(v string) string {
	if v == "" {
		return "absent"
	}
	return v
}
