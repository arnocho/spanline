# spanline impact: aks-prod-weu

**3 workloads would lose every replica**

| field | value |
| --- | --- |
| context | aks-prod-weu |
| source | nodes pool=apps |
| snapshot | 2026-09-16T03:20:00Z |
| expires | 2026-09-16T05:20:00Z |
| generated | 2026-09-16T03:20:00Z |
| nodes | 6 |

Nodes: aks-apps-10000, aks-apps-10001, aks-apps-10002, aks-apps-10003, aks-apps-10004, aks-apps-10005

## Findings

| severity | kind | object | reason | context |
| --- | --- | --- | --- | --- |
| OUTAGE | Deployment | orders/orders | every live pod of this workload stands on a node that goes away, so it drops from 1 to 0 | - |
| OUTAGE | StatefulSet | orders/redis | every live pod of this workload stands on a node that goes away, so it drops from 1 to 0 | - |
| OUTAGE | Deployment | payments/payments-api | every live pod of this workload stands on a node that goes away, so it drops from 4 to 0 | - |
| DISRUPTION | PersistentVolumeClaim | orders/redis-data-redis-0 | this volume is pinned to zone 2 and no surviving schedulable node of pool apps satisfies that, so the pod can only come back on a node outside its own pool | replacement and surge nodes the change creates are not modelled, so this describes the window, not the end state |
| DISRUPTION | PodDisruptionBudget | payments/payments-api | this budget allows 0 disruptions while it covers pods on nodes that go away, so every eviction of those pods is refused and the drain does not finish | spanline never calls the Eviction API, which is a write verb, so this is read from status, not from a trial eviction |
| INFO | Capacity | aks-prod-weu | the load that has to move fits on what stays: it needs cpu 1200m and memory 1.5Gi, and the surviving schedulable nodes offer cpu 3860m and memory 12.0Gi | this is a sum, not a scheduling simulation: a load that fits in total can still fail to place |
| INFO | Simulation | pool=apps | 6 of 9 nodes go away, 3 stay, and 1 of those still accept new pods | - |

### Evidence

Deployment orders/orders:

- spec.nodeName: 1 of 1 live pods stand on nodes that go away
- pods that go away: orders/orders-5b9c3-0
- spec.selector.matchLabels of Deployment orders matches metadata.labels of the pod, and its owning ReplicaSet orders-5b9c3 is not in the snapshot
- status.phase: a live pod is one that is neither Succeeded nor Failed
- spec.replicas: 1
- nodes that go away and carry those pods: aks-apps-10004

StatefulSet orders/redis:

- spec.nodeName: 1 of 1 live pods stand on nodes that go away
- pods that go away: orders/redis-0
- spec.selector.matchLabels of StatefulSet redis matches metadata.labels of the pod, and its owning ReplicaSet redis is not in the snapshot
- status.phase: a live pod is one that is neither Succeeded nor Failed
- spec.replicas: 1
- nodes that go away and carry those pods: aks-apps-10001

Deployment payments/payments-api:

- spec.nodeName: 4 of 4 live pods stand on nodes that go away
- pods that go away: payments/payments-api-77f4a-0, payments/payments-api-77f4a-1, payments/payments-api-77f4a-2, payments/payments-api-77f4a-3
- spec.selector.matchLabels of Deployment payments-api matches metadata.labels of the pod, and its owning ReplicaSet payments-api-77f4a is not in the snapshot
- status.phase: a live pod is one that is neither Succeeded nor Failed
- spec.replicas: 4
- nodes that go away and carry those pods: aks-apps-10000, aks-apps-10001, aks-apps-10002, aks-apps-10003

PersistentVolumeClaim orders/redis-data-redis-0:

- spec.volumes[].persistentVolumeClaim.claimName: redis-data-redis-0
- PersistentVolumeClaim orders/redis-data-redis-0 spec.volumeName: pv-redis-z2, status.phase: Bound
- PersistentVolume pv-redis-z2 spec.nodeAffinity.required.nodeSelectorTerms[].matchExpressions: topology.kubernetes.io/zone In [2]
- pod orders/redis-0 spec.nodeName: aks-apps-10001, which goes away
- surviving schedulable nodes whose metadata.labels satisfy it: aks-data-30000 (kubernetes.azure.com/agentpool=data)
- a node is counted out when spec.unschedulable is true or it carries a taint with effect NoSchedule or NoExecute
- pool of the pod's node, from kubernetes.azure.com/agentpool: apps

PodDisruptionBudget payments/payments-api:

- status.disruptionsAllowed: 0
- status.currentHealthy: 4, status.desiredHealthy: 4, status.expectedPods: 4
- spec.selector.matchLabels: app=payments-api
- spec.minAvailable: 100%
- pods it covers that stand on nodes going away: payments/payments-api-77f4a-0, payments/payments-api-77f4a-1, payments/payments-api-77f4a-2, payments/payments-api-77f4a-3

Capacity aks-prod-weu:

- sum of spec.containers[].resources.requests over the 6 live pods that go away: cpu 1200m, memory 1.5Gi
- sum of status.allocatable minus the requests already on them, over the 1 surviving schedulable nodes: cpu 3860m, memory 12.0Gi
- surviving schedulable nodes counted: aks-data-30000
- surviving nodes left out, as they take no new pod: aks-system-20000 (spec.taints: CriticalAddonsOnly=true:NoSchedule), aks-system-20001 (spec.taints: CriticalAddonsOnly=true:NoSchedule)

Simulation pool=apps:

- nodes that go away: aks-apps-10000, aks-apps-10001, aks-apps-10002, aks-apps-10003, aks-apps-10004, aks-apps-10005
- spec.nodeName: 6 live pods run on them
- spec.unschedulable and spec.taints with effect NoSchedule or NoExecute decide which survivors accept pods


## Ignored

- autoscaler scale up
- surge nodes created by the replacement
- topology spread constraints
- pod priority and preemption
- DaemonSet overhead
- HPA reactions

## Coverage and gaps

gaps: none recorded

## Verdict

**OUTAGE**, exit code 3
