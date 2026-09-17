// Package result holds the report types every analysis produces and every renderer consumes.
// Analyses never format; renderers never compute.
package result

import "time"

// Severity is the deterministic verdict attached to a finding. NotAssessed is never an implicit pass.
type Severity string

const (
	Outage      Severity = "OUTAGE"
	Disruption  Severity = "DISRUPTION"
	Risk        Severity = "RISK"
	Info        Severity = "INFO"
	NotAssessed Severity = "NOT ASSESSED"
)

// Rank orders severities for sorting and for the highest-wins exit code.
func (s Severity) Rank() int {
	switch s {
	case Outage:
		return 4
	case Disruption:
		return 3
	case Risk:
		return 2
	case NotAssessed:
		return 1
	default:
		return 0
	}
}

// Finding is one deterministic observation. Evidence lists the exact fields it was derived from.
type Finding struct {
	Severity Severity `json:"severity"`
	Kind     string   `json:"kind"`
	Object   string   `json:"object"`
	Reason   string   `json:"reason"`
	Evidence []string `json:"evidence,omitempty"`
	Context  string   `json:"context,omitempty"`
}

// Separation says how cleanly one dimension splits failing pods from healthy ones.
type Separation string

const (
	Total      Separation = "TOTAL"
	Partial    Separation = "PARTIAL"
	None       Separation = "NONE"
	Unresolved Separation = "UNRESOLVED"
)

// Dimension is one attribute compared across the two cohorts.
type Dimension struct {
	Name          string     `json:"name"`
	FailingValues string     `json:"failingValues"`
	HealthyValues string     `json:"healthyValues"`
	Separation    Separation `json:"separation"`
	Purity        float64    `json:"purity"`
}

// Verdict is the strength of a suspect's link to the incident. Never the word "cause".
type Verdict string

const (
	Splits   Verdict = "SPLITS"
	Temporal Verdict = "TEMPORAL"
	NoSplit  Verdict = "NO-SPLIT"
	Unknown  Verdict = "UNKNOWN"
)

// Actor is a role, never a person's name.
type Actor string

const (
	ActorPipeline   Actor = "pipeline"
	ActorController Actor = "controller"
	ActorHuman      Actor = "human or agent, unattributed"
	ActorAgent      Actor = "agent"
	ActorUnknown    Actor = "unknown"
)

// Suspect is one change ranked against the incident.
type Suspect struct {
	ID          string    `json:"id"`
	Verdict     Verdict   `json:"verdict"`
	At          time.Time `json:"at"`
	Title       string    `json:"title"`
	Dimension   string    `json:"dimension,omitempty"`
	Diff        []string  `json:"diff,omitempty"`
	Attribution string    `json:"attribution,omitempty"`
	Actor       Actor     `json:"actor"`
	Evidence    []string  `json:"evidence,omitempty"`
}

// Cohort counts one side of the split.
type Cohort struct {
	Pods   []string `json:"pods"`
	Count  int      `json:"count"`
	Sample string   `json:"sample,omitempty"`
}

// WhyMode records which analysis ran: the live cohort split, or the revision fallback.
type WhyMode string

const (
	ModeCohort   WhyMode = "cohort"
	ModeRevision WhyMode = "revision-fallback"
)

// WhyReport answers: what separates the failing pods from the healthy ones, and which change made it.
type WhyReport struct {
	Context     string      `json:"context"`
	Namespace   string      `json:"namespace"`
	Workload    string      `json:"workload"`
	Mode        WhyMode     `json:"mode"`
	CohortKey   string      `json:"cohortKey,omitempty"`
	OnsetAt     time.Time   `json:"onsetAt"`
	OnsetSignal string      `json:"onsetSignal"`
	Failing     Cohort      `json:"failing"`
	Healthy     Cohort      `json:"healthy"`
	Dimensions  []Dimension `json:"dimensions,omitempty"`
	Revisions   []Revision  `json:"revisions,omitempty"`
	Suspects    []Suspect   `json:"suspects,omitempty"`
	Gaps        []string    `json:"gaps,omitempty"`
	Narrative   *Narrative  `json:"narrative,omitempty"`
	GeneratedAt time.Time   `json:"generatedAt"`
}

// Revision describes one controller revision in the fallback comparison.
type Revision struct {
	Name     string    `json:"name"`
	Number   int       `json:"number"`
	Active   string    `json:"active"`
	Signals  string    `json:"signals"`
	Created  time.Time `json:"created"`
	Replicas int       `json:"replicas"`
}

// ImpactReport answers: what breaks if these nodes go away, or if this plan is applied.
type ImpactReport struct {
	Context     string     `json:"context"`
	Source      string     `json:"source"`
	SnapshotAt  time.Time  `json:"snapshotAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	Nodes       []string   `json:"nodes,omitempty"`
	PlanSHA     string     `json:"planSHA,omitempty"`
	PlanSummary string     `json:"planSummary,omitempty"`
	Mapping     []string   `json:"mapping,omitempty"`
	Findings    []Finding  `json:"findings"`
	Ignored     []string   `json:"ignored,omitempty"`
	Gaps        []string   `json:"gaps,omitempty"`
	Verdict     Severity   `json:"verdict"`
	ExitCode    int        `json:"exitCode"`
	Narrative   *Narrative `json:"narrative,omitempty"`
	GeneratedAt time.Time  `json:"generatedAt"`
}

// ClusterSummary is one row of the cockpit's cluster table.
type ClusterSummary struct {
	Context       string    `json:"context"`
	Nodes         int       `json:"nodes"`
	NodesNotReady int       `json:"nodesNotReady"`
	Pods          int       `json:"pods"`
	Namespaces    int       `json:"namespaces"`
	Workloads     int       `json:"workloads"`
	Risks         int       `json:"risks"`
	Outages       int       `json:"outages"`
	CollectedAt   time.Time `json:"collectedAt"`
}

// PoolSummary joins a node pool to the Terraform address that owns it and to what runs on it.
type PoolSummary struct {
	Context          string  `json:"context"`
	Pool             string  `json:"pool"`
	Nodes            int     `json:"nodes"`
	TerraformAddress string  `json:"terraformAddress,omitempty"`
	StateFile        string  `json:"stateFile,omitempty"`
	CPUPercent       float64 `json:"cpuPercent"`
	MemPercent       float64 `json:"memPercent"`
	Workloads        int     `json:"workloads"`
	AtRisk           int     `json:"atRisk"`
	Zones            string  `json:"zones,omitempty"`
}

// StateSummary describes one Terraform or OpenTofu state file that was read.
type StateSummary struct {
	Path      string `json:"path"`
	Resources int    `json:"resources"`
	NodePools int    `json:"nodePools"`
	Clusters  int    `json:"clusters"`
	Matched   int    `json:"matched"`
	Unmatched int    `json:"unmatched"`
}

// EstateReport is the cockpit: every cluster, every pool, what Terraform owns, what is at risk.
type EstateReport struct {
	Clusters    []ClusterSummary `json:"clusters"`
	Pools       []PoolSummary    `json:"pools"`
	Risks       []Finding        `json:"risks"`
	States      []StateSummary   `json:"states,omitempty"`
	Gaps        []string         `json:"gaps,omitempty"`
	Narrative   *Narrative       `json:"narrative,omitempty"`
	GeneratedAt time.Time        `json:"generatedAt"`
}

// Narrative is optional LLM output. It never changes a verdict, an order or an exit code.
type Narrative struct {
	Text       string   `json:"text"`
	Model      string   `json:"model"`
	Citations  []string `json:"citations,omitempty"`
	Dropped    int      `json:"droppedSentences"`
	PromptSHA  string   `json:"promptSHA,omitempty"`
	AnswerSHA  string   `json:"answerSHA,omitempty"`
	Unverified bool     `json:"unverified"`
}

// Check is one entry of the doctor report.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// DoctorReport tells an operator, and a client security team, exactly what spanline will touch.
type DoctorReport struct {
	Context     string    `json:"context"`
	Identity    string    `json:"identity"`
	Profile     string    `json:"profile"`
	CanWrite    bool      `json:"canWrite"`
	CanReadSec  bool      `json:"canReadSecrets"`
	Level       int       `json:"level"`
	Checks      []Check   `json:"checks"`
	Egress      []string  `json:"egress"`
	AuditFoot   []string  `json:"auditFootprint"`
	Gaps        []string  `json:"gaps,omitempty"`
	GeneratedAt time.Time `json:"generatedAt"`
}

// ExitCodeFor maps the highest severity present to spanline's documented exit codes.
func ExitCodeFor(findings []Finding) (Severity, int) {
	top := Severity(Info)
	for _, f := range findings {
		if f.Severity.Rank() > top.Rank() {
			top = f.Severity
		}
	}
	switch top {
	case Outage:
		return top, 3
	case Disruption:
		return top, 2
	case Risk, NotAssessed:
		// a risk is not a pass either: something would be left fragile, or was not looked at
		return top, 1
	default:
		return top, 0
	}
}
