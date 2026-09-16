// Package model holds the minimal Kubernetes and estate types spanline decodes.
// Only the fields the analyses need are kept: everything else is ignored on decode.
package model

import "time"

type OwnerReference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type ManagedFieldsEntry struct {
	Manager     string    `json:"manager"`
	Operation   string    `json:"operation"`
	Time        time.Time `json:"time"`
	Subresource string    `json:"subresource,omitempty"`
}

type ObjectMeta struct {
	Name              string               `json:"name"`
	Namespace         string               `json:"namespace,omitempty"`
	UID               string               `json:"uid,omitempty"`
	Labels            map[string]string    `json:"labels,omitempty"`
	Annotations       map[string]string    `json:"annotations,omitempty"`
	CreationTimestamp time.Time            `json:"creationTimestamp"`
	OwnerReferences   []OwnerReference     `json:"ownerReferences,omitempty"`
	ManagedFields     []ManagedFieldsEntry `json:"managedFields,omitempty"`
}

type LabelSelector struct {
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

type ResourceRequirements struct {
	Limits   map[string]string `json:"limits,omitempty"`
	Requests map[string]string `json:"requests,omitempty"`
}

type Container struct {
	Name      string               `json:"name"`
	Image     string               `json:"image"`
	Resources ResourceRequirements `json:"resources,omitempty"`
}

type Volume struct {
	Name                  string           `json:"name"`
	PersistentVolumeClaim *PVCVolumeSource `json:"persistentVolumeClaim,omitempty"`
}

type PVCVolumeSource struct {
	ClaimName string `json:"claimName"`
}

type PodSpec struct {
	NodeName          string            `json:"nodeName,omitempty"`
	NodeSelector      map[string]string `json:"nodeSelector,omitempty"`
	Containers        []Container       `json:"containers,omitempty"`
	Volumes           []Volume          `json:"volumes,omitempty"`
	PriorityClassName string            `json:"priorityClassName,omitempty"`
}

type StateWaiting struct {
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type StateRunning struct {
	StartedAt time.Time `json:"startedAt"`
}

type StateTerminated struct {
	Reason     string    `json:"reason,omitempty"`
	ExitCode   int       `json:"exitCode"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

type ContainerState struct {
	Waiting    *StateWaiting    `json:"waiting,omitempty"`
	Running    *StateRunning    `json:"running,omitempty"`
	Terminated *StateTerminated `json:"terminated,omitempty"`
}

type ContainerStatus struct {
	Name         string         `json:"name"`
	Ready        bool           `json:"ready"`
	RestartCount int            `json:"restartCount"`
	Image        string         `json:"image,omitempty"`
	ImageID      string         `json:"imageID,omitempty"`
	State        ContainerState `json:"state,omitempty"`
	LastState    ContainerState `json:"lastState,omitempty"`
}

type PodCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason,omitempty"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

type PodStatus struct {
	Phase             string            `json:"phase,omitempty"`
	Conditions        []PodCondition    `json:"conditions,omitempty"`
	ContainerStatuses []ContainerStatus `json:"containerStatuses,omitempty"`
	StartTime         time.Time         `json:"startTime,omitempty"`
}

type Pod struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PodSpec    `json:"spec"`
	Status   PodStatus  `json:"status"`
}

type Taint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

type NodeSpec struct {
	Taints        []Taint `json:"taints,omitempty"`
	Unschedulable bool    `json:"unschedulable,omitempty"`
	ProviderID    string  `json:"providerID,omitempty"`
}

type NodeSystemInfo struct {
	OSImage                 string `json:"osImage,omitempty"`
	KernelVersion           string `json:"kernelVersion,omitempty"`
	KubeletVersion          string `json:"kubeletVersion,omitempty"`
	ContainerRuntimeVersion string `json:"containerRuntimeVersion,omitempty"`
}

type NodeCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type NodeStatus struct {
	NodeInfo    NodeSystemInfo    `json:"nodeInfo,omitempty"`
	Allocatable map[string]string `json:"allocatable,omitempty"`
	Capacity    map[string]string `json:"capacity,omitempty"`
	Conditions  []NodeCondition   `json:"conditions,omitempty"`
}

type Node struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     NodeSpec   `json:"spec"`
	Status   NodeStatus `json:"status"`
}

type PodTemplateSpec struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PodSpec    `json:"spec"`
}

type WorkloadSpec struct {
	Replicas *int            `json:"replicas,omitempty"`
	Selector *LabelSelector  `json:"selector,omitempty"`
	Template PodTemplateSpec `json:"template"`
}

type WorkloadStatus struct {
	Replicas               int `json:"replicas,omitempty"`
	ReadyReplicas          int `json:"readyReplicas,omitempty"`
	AvailableReplicas      int `json:"availableReplicas,omitempty"`
	DesiredNumberScheduled int `json:"desiredNumberScheduled,omitempty"`
	NumberReady            int `json:"numberReady,omitempty"`
}

// Workload covers Deployment, StatefulSet and DaemonSet: they share the fields spanline reads.
type Workload struct {
	Kind     string         `json:"kind"`
	Metadata ObjectMeta     `json:"metadata"`
	Spec     WorkloadSpec   `json:"spec"`
	Status   WorkloadStatus `json:"status"`
}

type ReplicaSet struct {
	Metadata ObjectMeta     `json:"metadata"`
	Spec     WorkloadSpec   `json:"spec"`
	Status   WorkloadStatus `json:"status"`
}

type ControllerRevision struct {
	Metadata ObjectMeta     `json:"metadata"`
	Revision int            `json:"revision"`
	Data     ControllerData `json:"data,omitempty"`
}

type ControllerData struct {
	Spec ControllerDataSpec `json:"spec,omitempty"`
}

type ControllerDataSpec struct {
	Template PodTemplateSpec `json:"template,omitempty"`
}

type InvolvedObject struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type Event struct {
	Metadata       ObjectMeta     `json:"metadata"`
	InvolvedObject InvolvedObject `json:"involvedObject"`
	Reason         string         `json:"reason"`
	Message        string         `json:"message,omitempty"`
	Type           string         `json:"type,omitempty"`
	Count          int            `json:"count,omitempty"`
	FirstTimestamp time.Time      `json:"firstTimestamp,omitempty"`
	LastTimestamp  time.Time      `json:"lastTimestamp,omitempty"`
}

type PDBSpec struct {
	Selector       *LabelSelector `json:"selector,omitempty"`
	MinAvailable   string         `json:"minAvailable,omitempty"`
	MaxUnavailable string         `json:"maxUnavailable,omitempty"`
}

type PDBStatus struct {
	DisruptionsAllowed int `json:"disruptionsAllowed"`
	CurrentHealthy     int `json:"currentHealthy"`
	DesiredHealthy     int `json:"desiredHealthy"`
	ExpectedPods       int `json:"expectedPods"`
}

type PodDisruptionBudget struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PDBSpec    `json:"spec"`
	Status   PDBStatus  `json:"status"`
}

type PVCSpec struct {
	VolumeName       string `json:"volumeName,omitempty"`
	StorageClassName string `json:"storageClassName,omitempty"`
}

type PersistentVolumeClaim struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PVCSpec    `json:"spec"`
	Status   struct {
		Phase string `json:"phase,omitempty"`
	} `json:"status"`
}

type NodeSelectorRequirement struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}

type NodeSelectorTerm struct {
	MatchExpressions []NodeSelectorRequirement `json:"matchExpressions,omitempty"`
}

type VolumeNodeAffinity struct {
	Required *struct {
		NodeSelectorTerms []NodeSelectorTerm `json:"nodeSelectorTerms,omitempty"`
	} `json:"required,omitempty"`
}

type ClaimRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type PVSpec struct {
	ClaimRef         *ClaimRef           `json:"claimRef,omitempty"`
	NodeAffinity     *VolumeNodeAffinity `json:"nodeAffinity,omitempty"`
	StorageClassName string              `json:"storageClassName,omitempty"`
}

type PersistentVolume struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PVSpec     `json:"spec"`
	Status   struct {
		Phase string `json:"phase,omitempty"`
	} `json:"status"`
}

type ArgoSource struct {
	RepoURL        string `json:"repoURL,omitempty"`
	Path           string `json:"path,omitempty"`
	TargetRevision string `json:"targetRevision,omitempty"`
}

type ArgoHistoryEntry struct {
	ID         int       `json:"id"`
	Revision   string    `json:"revision"`
	DeployedAt time.Time `json:"deployedAt"`
}

type ArgoInitiatedBy struct {
	Username  string `json:"username,omitempty"`
	Automated bool   `json:"automated,omitempty"`
}

type ArgoApplication struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		Source      ArgoSource `json:"source,omitempty"`
		Destination struct {
			Namespace string `json:"namespace,omitempty"`
		} `json:"destination,omitempty"`
		SyncPolicy struct {
			Automated *struct {
				SelfHeal bool `json:"selfHeal,omitempty"`
				Prune    bool `json:"prune,omitempty"`
			} `json:"automated,omitempty"`
		} `json:"syncPolicy,omitempty"`
	} `json:"spec"`
	Status struct {
		Sync struct {
			Status   string `json:"status,omitempty"`
			Revision string `json:"revision,omitempty"`
		} `json:"sync,omitempty"`
		Health struct {
			Status string `json:"status,omitempty"`
		} `json:"health,omitempty"`
		History        []ArgoHistoryEntry `json:"history,omitempty"`
		OperationState struct {
			Phase       string          `json:"phase,omitempty"`
			FinishedAt  time.Time       `json:"finishedAt,omitempty"`
			InitiatedBy ArgoInitiatedBy `json:"initiatedBy,omitempty"`
		} `json:"operationState,omitempty"`
	} `json:"status"`
}

// CoverageGap records something spanline could not read, so no analysis silently claims coverage.
type CoverageGap struct {
	Resource string `json:"resource"`
	Reason   string `json:"reason"`
}

// Snapshot is everything spanline read for one kube context at one moment.
type Snapshot struct {
	Context             string                  `json:"context"`
	CollectedAt         time.Time               `json:"collectedAt"`
	Namespace           string                  `json:"namespace,omitempty"`
	Pods                []Pod                   `json:"pods,omitempty"`
	Nodes               []Node                  `json:"nodes,omitempty"`
	ReplicaSets         []ReplicaSet            `json:"replicaSets,omitempty"`
	ControllerRevisions []ControllerRevision    `json:"controllerRevisions,omitempty"`
	Deployments         []Workload              `json:"deployments,omitempty"`
	StatefulSets        []Workload              `json:"statefulSets,omitempty"`
	DaemonSets          []Workload              `json:"daemonSets,omitempty"`
	Events              []Event                 `json:"events,omitempty"`
	PDBs                []PodDisruptionBudget   `json:"pdbs,omitempty"`
	PVCs                []PersistentVolumeClaim `json:"pvcs,omitempty"`
	PVs                 []PersistentVolume      `json:"pvs,omitempty"`
	ArgoApps            []ArgoApplication       `json:"argoApps,omitempty"`
	Gaps                []CoverageGap           `json:"gaps,omitempty"`
}

// Workloads returns every workload kind in one slice, Kind field set.
func (s *Snapshot) Workloads() []Workload {
	out := make([]Workload, 0, len(s.Deployments)+len(s.StatefulSets)+len(s.DaemonSets))
	for _, w := range s.Deployments {
		w.Kind = "Deployment"
		out = append(out, w)
	}
	for _, w := range s.StatefulSets {
		w.Kind = "StatefulSet"
		out = append(out, w)
	}
	for _, w := range s.DaemonSets {
		w.Kind = "DaemonSet"
		out = append(out, w)
	}
	return out
}

// NodeByName indexes nodes for the analyses that walk pods.
func (s *Snapshot) NodeByName() map[string]Node {
	m := make(map[string]Node, len(s.Nodes))
	for _, n := range s.Nodes {
		m[n.Metadata.Name] = n
	}
	return m
}

// PodsOnNode lists the pods scheduled on one node.
func (s *Snapshot) PodsOnNode(node string) []Pod {
	var out []Pod
	for _, p := range s.Pods {
		if p.Spec.NodeName == node {
			out = append(out, p)
		}
	}
	return out
}

// SelectorMatches reports whether labels satisfy every matchLabels entry.
func SelectorMatches(sel *LabelSelector, labels map[string]string) bool {
	if sel == nil || len(sel.MatchLabels) == 0 {
		return false
	}
	for k, v := range sel.MatchLabels {
		if labels[k] != v {
			return false
		}
	}
	return true
}
