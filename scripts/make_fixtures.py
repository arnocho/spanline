#!/usr/bin/env python3
"""Generate the mock cluster and Terraform fixtures spanline ships with.

Everything is deterministic: no clock, no randomness, so golden tests stay stable.
Run: python3 scripts/make_fixtures.py
Output: internal/fixtures/data/<scenario>/<context>/<resource>.json
"""
import json
import os
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
DATA = ROOT / "internal" / "fixtures" / "data"

T = lambda h, m, s=0: f"2026-09-16T{h:02d}:{m:02d}:{s:02d}Z"
NODE_IMAGE_OLD = "AKSUbuntu-2204gen2containerd-202608.20.0"
NODE_IMAGE_NEW = "AKSUbuntu-2204gen2containerd-202609.10.0"


def write(scenario, context, resource, items):
    d = DATA / scenario / context
    d.mkdir(parents=True, exist_ok=True)
    (d / f"{resource}.json").write_text(
        json.dumps({"apiVersion": "v1", "kind": "List", "items": items}, indent=1) + "\n"
    )


def write_raw(scenario, relpath, obj):
    p = DATA / scenario / relpath
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(json.dumps(obj, indent=1) + "\n")


def node(name, pool, zone, node_image=NODE_IMAGE_OLD, kernel="5.15.0-1073-azure",
         kubelet="v1.31.6", runtime="containerd://1.7.27", cpu="3860m", mem="12Gi",
         taints=None, ready=True, created=T(1, 0)):
    return {
        "metadata": {
            "name": name,
            "uid": f"uid-node-{name}",
            "creationTimestamp": created,
            "labels": {
                "kubernetes.io/hostname": name,
                "kubernetes.azure.com/agentpool": pool,
                "agentpool": pool,
                "topology.kubernetes.io/zone": zone,
                "kubernetes.azure.com/node-image-version": node_image,
                "kubernetes.io/os": "linux",
            },
        },
        "spec": {"taints": taints or [], "providerID": f"azure:///subscriptions/x/vm/{name}"},
        "status": {
            "nodeInfo": {
                "osImage": "Ubuntu 22.04.5 LTS",
                "kernelVersion": kernel,
                "kubeletVersion": kubelet,
                "containerRuntimeVersion": runtime,
            },
            "allocatable": {"cpu": cpu, "memory": mem, "pods": "110"},
            "capacity": {"cpu": "4", "memory": "16Gi", "pods": "110"},
            "conditions": [{"type": "Ready", "status": "True" if ready else "False"}],
        },
    }


def container(name, image, cpu_req="200m", mem_req="256Mi", cpu_lim="500m", mem_lim="512Mi"):
    return {
        "name": name,
        "image": image,
        "resources": {
            "requests": {"cpu": cpu_req, "memory": mem_req},
            "limits": {"cpu": cpu_lim, "memory": mem_lim},
        },
    }


def pod(name, ns, app, tmpl_hash, node_name, rs_name, image, image_id, healthy,
        started=T(2, 51), restarts=0, oom_at=None, ready_transition=T(2, 52),
        checksum=None, pvc=None, containers=None, reason=None):
    labels = {"app": app, "pod-template-hash": tmpl_hash}
    ann = {}
    if checksum:
        ann["checksum/config"] = checksum
    cstate = {"running": {"startedAt": started}}
    last = {}
    conditions = [
        {"type": "Ready", "status": "True" if healthy else "False",
         "lastTransitionTime": ready_transition, "reason": "" if healthy else "ContainersNotReady"},
        {"type": "ContainersReady", "status": "True" if healthy else "False",
         "lastTransitionTime": ready_transition},
    ]
    if not healthy:
        cstate = {"waiting": {"reason": reason or "CrashLoopBackOff",
                              "message": "back-off 40s restarting failed container"}}
        if oom_at:
            last = {"terminated": {"reason": "OOMKilled", "exitCode": 137,
                                   "startedAt": started, "finishedAt": oom_at}}
    return {
        "metadata": {
            "name": name, "namespace": ns, "uid": f"uid-pod-{name}",
            "creationTimestamp": started, "labels": labels, "annotations": ann,
            "ownerReferences": [{"kind": "ReplicaSet", "name": rs_name, "uid": f"uid-rs-{rs_name}"}],
        },
        "spec": {
            "nodeName": node_name,
            "containers": containers or [container(app, image)],
            "volumes": ([{"name": "data", "persistentVolumeClaim": {"claimName": pvc}}] if pvc else []),
        },
        "status": {
            "phase": "Running",
            "startTime": started,
            "conditions": conditions,
            "containerStatuses": [{
                "name": app, "ready": healthy, "restartCount": restarts,
                "image": image, "imageID": image_id, "state": cstate, "lastState": last,
            }],
        },
    }


def template(app, image, mem_lim="512Mi", cpu_lim="500m", tmpl_hash=None, checksum=None):
    meta = {"labels": {"app": app}, "creationTimestamp": None}
    if tmpl_hash:
        meta["labels"]["pod-template-hash"] = tmpl_hash
    if checksum:
        meta["annotations"] = {"checksum/config": checksum}
    return {"metadata": meta,
            "spec": {"containers": [container(app, image, cpu_lim=cpu_lim, mem_lim=mem_lim)]}}


def replicaset(name, ns, app, tmpl_hash, revision, replicas, ready, image, mem_lim,
               created, owner, checksum=None, manager="argocd-application-controller"):
    return {
        "metadata": {
            "name": name, "namespace": ns, "uid": f"uid-rs-{name}",
            "creationTimestamp": created,
            "labels": {"app": app, "pod-template-hash": tmpl_hash},
            "annotations": {"deployment.kubernetes.io/revision": str(revision)},
            "ownerReferences": [{"kind": "Deployment", "name": owner, "uid": f"uid-deploy-{owner}"}],
            "managedFields": [{"manager": manager, "operation": "Apply", "time": created}],
        },
        "spec": {"replicas": replicas,
                 "selector": {"matchLabels": {"app": app, "pod-template-hash": tmpl_hash}},
                 "template": template(app, image, mem_lim=mem_lim, tmpl_hash=tmpl_hash, checksum=checksum)},
        "status": {"replicas": replicas, "readyReplicas": ready, "availableReplicas": ready},
    }


def deployment(name, ns, app, replicas, ready, image, mem_lim, created, revision,
               managers=None, annotations=None):
    ann = {"deployment.kubernetes.io/revision": str(revision)}
    ann.update(annotations or {})
    return {
        "kind": "Deployment",
        "metadata": {
            "name": name, "namespace": ns, "uid": f"uid-deploy-{name}",
            "creationTimestamp": created, "labels": {"app": app}, "annotations": ann,
            "managedFields": managers or [
                {"manager": "argocd-application-controller", "operation": "Apply", "time": created}],
        },
        "spec": {"replicas": replicas, "selector": {"matchLabels": {"app": app}},
                 "template": template(app, image, mem_lim=mem_lim)},
        "status": {"replicas": replicas, "readyReplicas": ready, "availableReplicas": ready},
    }


def event(name, ns, kind, obj, reason, message, first, last, count=1, etype="Warning"):
    return {
        "metadata": {"name": name, "namespace": ns, "creationTimestamp": first},
        "involvedObject": {"kind": kind, "name": obj, "namespace": ns},
        "reason": reason, "message": message, "type": etype, "count": count,
        "firstTimestamp": first, "lastTimestamp": last,
    }


def pdb(name, ns, app, allowed, healthy, desired, expected, min_available="100%"):
    return {
        "metadata": {"name": name, "namespace": ns, "creationTimestamp": T(1, 0)},
        "spec": {"selector": {"matchLabels": {"app": app}}, "minAvailable": min_available},
        "status": {"disruptionsAllowed": allowed, "currentHealthy": healthy,
                   "desiredHealthy": desired, "expectedPods": expected},
    }


def argo_app(name, ns_dest, revision, prev_revision, deployed_at, sync="Synced",
             health="Degraded", automated=True, self_heal=False):
    return {
        "metadata": {"name": name, "namespace": "argocd", "creationTimestamp": T(1, 0)},
        "spec": {
            "source": {"repoURL": "https://gitlab.internal/platform/env-prod.git",
                       "path": f"apps/{ns_dest}", "targetRevision": "main"},
            "destination": {"namespace": ns_dest},
            "syncPolicy": {"automated": {"selfHeal": self_heal, "prune": True} if automated else None},
        },
        "status": {
            "sync": {"status": sync, "revision": revision},
            "health": {"status": health},
            "history": [
                {"id": 16, "revision": prev_revision, "deployedAt": T(1, 12)},
                {"id": 17, "revision": revision, "deployedAt": deployed_at},
            ],
            "operationState": {"phase": "Succeeded", "finishedAt": deployed_at,
                               "initiatedBy": {"automated": True}},
        },
    }


def pvc(name, ns, volume, sc="managed-csi"):
    return {"metadata": {"name": name, "namespace": ns, "creationTimestamp": T(1, 0)},
            "spec": {"volumeName": volume, "storageClassName": sc},
            "status": {"phase": "Bound"}}


def pv(name, claim_ns, claim_name, zone, sc="managed-csi"):
    return {
        "metadata": {"name": name, "creationTimestamp": T(1, 0)},
        "spec": {
            "claimRef": {"name": claim_name, "namespace": claim_ns},
            "storageClassName": sc,
            "nodeAffinity": {"required": {"nodeSelectorTerms": [
                {"matchExpressions": [{"key": "topology.kubernetes.io/zone",
                                       "operator": "In", "values": [zone]}]}]}},
        },
        "status": {"phase": "Bound"},
    }


IMG_OLD = "registry.internal/payments/checkout:1.8.2"
IMG_NEW = "registry.internal/payments/checkout:1.9.0"
ID_OLD = "registry.internal/payments/checkout@sha256:5c4b8aa1"
ID_NEW = "registry.internal/payments/checkout@sha256:e1a077bd"


def scenario_oom_rollout_blocked():
    """Six pods on the new template hash OOMKill, four on the old hash stay healthy."""
    s, ctx, ns = "oom-rollout-blocked", "aks-prod-weu", "payments"
    nodes = [node(f"aks-apps-1000{i}", "apps", str((i % 3) + 1)) for i in range(6)]
    nodes += [node(f"aks-system-2000{i}", "system", str((i % 2) + 1),
                   taints=[{"key": "CriticalAddonsOnly", "value": "true", "effect": "NoSchedule"}])
              for i in range(2)]
    pods = []
    for i in range(6):
        pods.append(pod(f"checkout-7d9f5-{'xkqrtv'[i]}{i}", ns, "checkout", "7d9f5",
                        f"aks-apps-1000{i}", "checkout-7d9f5", IMG_NEW, ID_NEW, False,
                        started=T(2, 51, 40), restarts=3 + (i % 3), oom_at=T(3, 2, 14 + i),
                        ready_transition=T(3, 2, 20 + i), checksum="9e1f2a"))
    for i in range(4):
        pods.append(pod(f"checkout-5c4b8-{'abcd'[i]}{i}", ns, "checkout", "5c4b8",
                        f"aks-apps-1000{i}", "checkout-5c4b8", IMG_OLD, ID_OLD, True,
                        started=T(1, 12, 5), ready_transition=T(1, 12, 30), checksum="9e1f2a"))
    # noise: another healthy workload sharing the pool
    for i in range(2):
        pods.append(pod(f"ledger-6a1b2-{i}", ns, "ledger", "6a1b2", f"aks-apps-1000{i+3}",
                        "ledger-6a1b2", "registry.internal/payments/ledger:3.1.0",
                        "registry.internal/payments/ledger@sha256:aa11", True, started=T(0, 40)))
    rs = [
        replicaset("checkout-7d9f5", ns, "checkout", "7d9f5", 12, 6, 0, IMG_NEW, "256Mi",
                   T(2, 51, 12), "checkout", checksum="9e1f2a"),
        replicaset("checkout-5c4b8", ns, "checkout", "5c4b8", 11, 4, 4, IMG_OLD, "512Mi",
                   T(1, 12, 0), "checkout", checksum="9e1f2a"),
        replicaset("ledger-6a1b2", ns, "ledger", "6a1b2", 4, 2, 2,
                   "registry.internal/payments/ledger:3.1.0", "512Mi", T(0, 40), "ledger"),
    ]
    deploys = [
        deployment("checkout", ns, "checkout", 10, 4, IMG_NEW, "256Mi", T(1, 0), 12, managers=[
            {"manager": "argocd-application-controller", "operation": "Apply", "time": T(2, 51, 10)},
            {"manager": "kubectl-edit", "operation": "Update", "time": T(2, 58, 3)},
        ]),
        deployment("ledger", ns, "ledger", 2, 2, "registry.internal/payments/ledger:3.1.0",
                   "512Mi", T(0, 40), 4),
    ]
    events = []
    for i in range(6):
        events.append(event(f"checkout-oom-{i}", ns, "Pod", f"checkout-7d9f5-{'xkqrtv'[i]}{i}",
                            "OOMKilling", "Memory cgroup out of memory: Killed process (java)",
                            T(3, 2, 14 + i), T(3, 9, 40), count=3 + i))
        events.append(event(f"checkout-backoff-{i}", ns, "Pod", f"checkout-7d9f5-{'xkqrtv'[i]}{i}",
                            "BackOff", "Back-off restarting failed container checkout",
                            T(3, 3, 0), T(3, 18, 0), count=9))
    events.append(event("checkout-scaled", ns, "ReplicaSet", "checkout-7d9f5", "SuccessfulCreate",
                        "Created pod: checkout-7d9f5-x0", T(2, 51, 30), T(2, 51, 40), etype="Normal"))
    write(s, ctx, "nodes", nodes)
    write(s, ctx, "pods", pods)
    write(s, ctx, "replicasets", rs)
    write(s, ctx, "deployments", deploys)
    write(s, ctx, "statefulsets", [])
    write(s, ctx, "daemonsets", [])
    write(s, ctx, "controllerrevisions", [])
    write(s, ctx, "events", events)
    write(s, ctx, "pdbs", [pdb("checkout", ns, "checkout", 0, 4, 10, 10)])
    write(s, ctx, "pvcs", [])
    write(s, ctx, "pvs", [])
    write(s, ctx, "argoapps", [argo_app("payments-prod", ns, "a1b2c3d4e5f6", "9e8f7a6b5c4d", T(2, 51, 5))])
    write_raw(s, "meta.json", {
        "name": s,
        "description": "Rollout blocked: 6 pods OOMKilled on the new template hash, 4 healthy on the old one.",
        "contexts": [ctx], "namespace": ns, "workload": "deploy/checkout", "now": T(3, 20),
    })


def scenario_node_image_drift():
    """The failing pods share the template hash with healthy ones: only the node image separates them."""
    s, ctx, ns = "node-image-drift", "aks-prod-weu", "payments"
    nodes = [node(f"aks-apps-1000{i}", "apps", str((i % 3) + 1),
                  node_image=NODE_IMAGE_NEW if i < 3 else NODE_IMAGE_OLD,
                  kernel="5.15.0-1081-azure" if i < 3 else "5.15.0-1073-azure",
                  created=T(2, 10) if i < 3 else T(1, 0)) for i in range(6)]
    pods = []
    for i in range(3):
        pods.append(pod(f"api-8f2c1-n{i}", ns, "api", "8f2c1", f"aks-apps-1000{i}",
                        "api-8f2c1", "registry.internal/payments/api:2.4.0",
                        "registry.internal/payments/api@sha256:bb22", False,
                        started=T(2, 30), restarts=6, ready_transition=T(2, 35),
                        reason="CrashLoopBackOff"))
    for i in range(3, 6):
        pods.append(pod(f"api-8f2c1-h{i}", ns, "api", "8f2c1", f"aks-apps-1000{i}",
                        "api-8f2c1", "registry.internal/payments/api:2.4.0",
                        "registry.internal/payments/api@sha256:bb22", True, started=T(1, 5)))
    rs = [replicaset("api-8f2c1", ns, "api", "8f2c1", 7, 6, 3,
                     "registry.internal/payments/api:2.4.0", "1Gi", T(1, 5), "api")]
    deploys = [deployment("api", ns, "api", 6, 3, "registry.internal/payments/api:2.4.0",
                          "1Gi", T(0, 30), 7)]
    events = [event(f"api-probe-{i}", ns, "Pod", f"api-8f2c1-n{i}", "Unhealthy",
                    "Readiness probe failed: dial tcp 10.0.3.4:8080: connect: connection refused",
                    T(2, 32, 10 + i), T(3, 10), count=22) for i in range(3)]
    write(s, ctx, "nodes", nodes)
    write(s, ctx, "pods", pods)
    write(s, ctx, "replicasets", rs)
    write(s, ctx, "deployments", deploys)
    write(s, ctx, "statefulsets", [])
    write(s, ctx, "daemonsets", [])
    write(s, ctx, "controllerrevisions", [])
    write(s, ctx, "events", events)
    write(s, ctx, "pdbs", [])
    write(s, ctx, "pvcs", [])
    write(s, ctx, "pvs", [])
    write(s, ctx, "argoapps", [])
    write_raw(s, "meta.json", {
        "name": s,
        "description": "Same template hash everywhere: only the node image version separates failing from healthy.",
        "contexts": [ctx], "namespace": ns, "workload": "deploy/api", "now": T(3, 20),
    })


def scenario_rollout_completed():
    """Every pod is failing, the old ReplicaSet is at zero: only the revision fallback is left."""
    s, ctx, ns = "rollout-completed", "aks-prod-weu", "payments"
    nodes = [node(f"aks-apps-1000{i}", "apps", str((i % 3) + 1)) for i in range(6)]
    pods = [pod(f"cart-3e7a9-{i}", ns, "cart", "3e7a9", f"aks-apps-1000{i % 6}",
                "cart-3e7a9", "registry.internal/payments/cart:4.2.0",
                "registry.internal/payments/cart@sha256:cc33", False,
                started=T(2, 51, 30), restarts=4, oom_at=T(3, 9, 40 + i),
                ready_transition=T(3, 9, 50)) for i in range(6)]
    rs = [
        replicaset("cart-3e7a9", ns, "cart", "3e7a9", 12, 6, 0,
                   "registry.internal/payments/cart:4.2.0", "256Mi", T(2, 51, 10), "cart"),
        replicaset("cart-91bd4", ns, "cart", "91bd4", 11, 0, 0,
                   "registry.internal/payments/cart:4.1.3", "512Mi", T(1, 12), "cart"),
    ]
    deploys = [deployment("cart", ns, "cart", 6, 0, "registry.internal/payments/cart:4.2.0",
                          "256Mi", T(0, 50), 12)]
    events = [event(f"cart-oom-{i}", ns, "Pod", f"cart-3e7a9-{i}", "OOMKilling",
                    "Memory cgroup out of memory", T(3, 9, 40 + i), T(3, 19), count=4)
              for i in range(6)]
    write(s, ctx, "nodes", nodes)
    write(s, ctx, "pods", pods)
    write(s, ctx, "replicasets", rs)
    write(s, ctx, "deployments", deploys)
    write(s, ctx, "statefulsets", [])
    write(s, ctx, "daemonsets", [])
    write(s, ctx, "controllerrevisions", [])
    write(s, ctx, "events", events)
    write(s, ctx, "pdbs", [])
    write(s, ctx, "pvcs", [])
    write(s, ctx, "pvs", [])
    write(s, ctx, "argoapps", [argo_app("payments-prod", ns, "f7e6d5c4b3a2", "a1b2c3d4e5f6", T(2, 51))])
    write_raw(s, "meta.json", {
        "name": s,
        "description": "Rollout completed then failed: no healthy cohort, the revision fallback runs.",
        "contexts": [ctx], "namespace": ns, "workload": "deploy/cart", "now": T(3, 20),
    })


def scenario_estate():
    """Two clusters for the cockpit: an AKS one owned by Terraform, and an on-prem one."""
    s = "estate"
    ctx_a, ctx_b = "aks-prod-weu", "onprem-int"
    # AKS cluster
    nodes_a = [node(f"aks-apps-1000{i}", "apps", str((i % 3) + 1)) for i in range(6)]
    nodes_a += [node(f"aks-system-2000{i}", "system", str((i % 2) + 1),
                     taints=[{"key": "CriticalAddonsOnly", "value": "true", "effect": "NoSchedule"}])
                for i in range(2)]
    nodes_a += [node("aks-data-30000", "data", "2", cpu="7860m", mem="28Gi")]
    pods_a = []
    # payments: 4 replicas, healthy, PDB with no budget left
    for i in range(4):
        pods_a.append(pod(f"payments-api-77f4a-{i}", "payments", "payments-api", "77f4a",
                          f"aks-apps-1000{i}", "payments-api-77f4a",
                          "registry.internal/payments/api:2.4.0",
                          "registry.internal/payments/api@sha256:bb22", True, started=T(1, 5)))
    # orders: single replica on the apps pool
    pods_a.append(pod("orders-5b9c3-0", "orders", "orders", "5b9c3", "aks-apps-10004",
                      "orders-5b9c3", "registry.internal/orders/api:1.2.0",
                      "registry.internal/orders/api@sha256:dd44", True, started=T(1, 20)))
    # redis: stateful, zonal volume in zone 2
    pods_a.append(pod("redis-0", "orders", "redis", "", "aks-apps-10001", "redis",
                      "docker.io/library/redis:7.2", "docker.io/library/redis@sha256:ee55",
                      True, started=T(1, 25), pvc="redis-data-redis-0"))
    # data pool workload
    for i in range(2):
        pods_a.append(pod(f"warehouse-2c8d1-{i}", "analytics", "warehouse", "2c8d1", "aks-data-30000",
                          "warehouse-2c8d1", "registry.internal/analytics/warehouse:0.9.1",
                          "registry.internal/analytics/warehouse@sha256:ff66", True,
                          started=T(0, 55),
                          containers=[container("warehouse", "registry.internal/analytics/warehouse:0.9.1",
                                                cpu_req="2", mem_req="8Gi", cpu_lim="3", mem_lim="12Gi")]))
    deploys_a = [
        deployment("payments-api", "payments", "payments-api", 4, 4,
                   "registry.internal/payments/api:2.4.0", "1Gi", T(0, 30), 7,
                   annotations={"meta.helm.sh/release-name": "payments",
                                "meta.helm.sh/release-namespace": "payments"}),
        deployment("orders", "orders", "orders", 1, 1, "registry.internal/orders/api:1.2.0",
                   "512Mi", T(0, 35), 3),
        deployment("warehouse", "analytics", "warehouse", 2, 2,
                   "registry.internal/analytics/warehouse:0.9.1", "12Gi", T(0, 20), 2),
    ]
    sts_a = [{
        "kind": "StatefulSet",
        "metadata": {"name": "redis", "namespace": "orders", "uid": "uid-sts-redis",
                     "creationTimestamp": T(0, 45), "labels": {"app": "redis"}, "annotations": {}},
        "spec": {"replicas": 1, "selector": {"matchLabels": {"app": "redis"}},
                 "template": template("redis", "docker.io/library/redis:7.2")},
        "status": {"replicas": 1, "readyReplicas": 1},
    }]
    write(s, ctx_a, "nodes", nodes_a)
    write(s, ctx_a, "pods", pods_a)
    write(s, ctx_a, "replicasets", [])
    write(s, ctx_a, "deployments", deploys_a)
    write(s, ctx_a, "statefulsets", sts_a)
    write(s, ctx_a, "daemonsets", [])
    write(s, ctx_a, "controllerrevisions", [])
    write(s, ctx_a, "events", [])
    write(s, ctx_a, "pdbs", [pdb("payments-api", "payments", "payments-api", 0, 4, 4, 4)])
    write(s, ctx_a, "pvcs", [pvc("redis-data-redis-0", "orders", "pv-redis-z2")])
    write(s, ctx_a, "pvs", [pv("pv-redis-z2", "orders", "redis-data-redis-0", "2")])
    write(s, ctx_a, "argoapps", [argo_app("payments-prod", "payments", "a1b2c3d4e5f6",
                                          "9e8f7a6b5c4d", T(2, 51), health="Healthy")])
    # On-prem cluster, no Terraform ownership, Helm through CI
    nodes_b = [node(f"onprem-worker-{i}", "workers", "", node_image="", kernel="5.14.0-427.el9",
                    kubelet="v1.30.4", cpu="7860m", mem="30Gi") for i in range(4)]
    for n in nodes_b:
        n["metadata"]["labels"].pop("kubernetes.azure.com/agentpool", None)
        n["metadata"]["labels"].pop("kubernetes.azure.com/node-image-version", None)
        n["metadata"]["labels"].pop("topology.kubernetes.io/zone", None)
        n["metadata"]["labels"]["node-role.kubernetes.io/worker"] = ""
    nodes_b[3]["status"]["conditions"] = [{"type": "Ready", "status": "False"}]
    pods_b = []
    for i in range(3):
        pods_b.append(pod(f"ingest-4d2f8-{i}", "pipeline", "ingest", "4d2f8", f"onprem-worker-{i}",
                          "ingest-4d2f8", "registry.internal/pipeline/ingest:5.0.2",
                          "registry.internal/pipeline/ingest@sha256:1122", True, started=T(0, 15)))
    pods_b.append(pod("scheduler-9a3e7-0", "pipeline", "scheduler", "9a3e7", "onprem-worker-3",
                      "scheduler-9a3e7", "registry.internal/pipeline/scheduler:2.1.0",
                      "registry.internal/pipeline/scheduler@sha256:3344", False,
                      started=T(0, 20), restarts=2, reason="CrashLoopBackOff"))
    deploys_b = [
        deployment("ingest", "pipeline", "ingest", 3, 3, "registry.internal/pipeline/ingest:5.0.2",
                   "2Gi", T(0, 10), 9,
                   annotations={"meta.helm.sh/release-name": "pipeline",
                                "meta.helm.sh/release-namespace": "pipeline"}),
        deployment("scheduler", "pipeline", "scheduler", 1, 0,
                   "registry.internal/pipeline/scheduler:2.1.0", "1Gi", T(0, 12), 4,
                   annotations={"meta.helm.sh/release-name": "pipeline",
                                "meta.helm.sh/release-namespace": "pipeline"}),
    ]
    write(s, ctx_b, "nodes", nodes_b)
    write(s, ctx_b, "pods", pods_b)
    write(s, ctx_b, "replicasets", [])
    write(s, ctx_b, "deployments", deploys_b)
    write(s, ctx_b, "statefulsets", [])
    write(s, ctx_b, "daemonsets", [])
    write(s, ctx_b, "controllerrevisions", [])
    write(s, ctx_b, "events", [])
    write(s, ctx_b, "pdbs", [])
    write(s, ctx_b, "pvcs", [])
    write(s, ctx_b, "pvs", [])
    write(s, ctx_b, "argoapps", [])
    write_raw(s, "meta.json", {
        "name": s,
        "description": "Two clusters: AKS owned by Terraform, and an on-prem cluster delivered by Helm.",
        "contexts": [ctx_a, ctx_b], "now": T(3, 20),
        "tfstate": "terraform/state.json", "tfplan": "terraform/plan.json",
    })
    # Terraform state: owns the AKS cluster and its pools
    state = {
        "version": 4, "terraform_version": "1.9.8", "serial": 318,
        "lineage": "8f2b1c44-0000-4a1b-9c3d-77aa11bb22cc",
        "resources": [
            {"mode": "managed", "type": "azurerm_kubernetes_cluster", "name": "main",
             "provider": "provider[\"registry.terraform.io/hashicorp/azurerm\"]",
             "instances": [{"attributes": {"name": "aks-prod-weu", "resource_group_name": "rg-platform-prod",
                                           "kubernetes_version": "1.31.6"}}]},
            {"mode": "managed", "type": "azurerm_kubernetes_cluster_node_pool", "name": "apps",
             "provider": "provider[\"registry.terraform.io/hashicorp/azurerm\"]",
             "instances": [{"attributes": {"name": "apps", "vm_size": "Standard_D4s_v5",
                                           "node_count": 6, "zones": ["1", "2", "3"]}}]},
            {"mode": "managed", "type": "azurerm_kubernetes_cluster_node_pool", "name": "data",
             "provider": "provider[\"registry.terraform.io/hashicorp/azurerm\"]",
             "instances": [{"attributes": {"name": "data", "vm_size": "Standard_D8s_v5",
                                           "node_count": 1, "zones": ["2"]}}]},
            {"mode": "managed", "type": "helm_release", "name": "payments",
             "provider": "provider[\"registry.terraform.io/hashicorp/helm\"]",
             "instances": [{"attributes": {"name": "payments", "namespace": "payments",
                                           "chart": "payments", "version": "1.4.2"}}]},
            {"mode": "managed", "type": "azurerm_public_ip", "name": "unused",
             "provider": "provider[\"registry.terraform.io/hashicorp/azurerm\"]",
             "instances": [{"attributes": {"name": "pip-legacy-egress", "ip_address": "20.4.5.6"}}]},
        ],
    }
    write_raw(s, "terraform/state.json", state)
    # Terraform plan: the apps pool is replaced, the cluster gets an unmodelled in-place update
    changes = [
        {"address": "azurerm_kubernetes_cluster_node_pool.apps",
         "type": "azurerm_kubernetes_cluster_node_pool", "name": "apps", "provider_name": "registry.terraform.io/hashicorp/azurerm",
         "change": {"actions": ["delete", "create"],
                    "before": {"name": "apps", "vm_size": "Standard_D4s_v5", "node_count": 6},
                    "after": {"name": "apps", "vm_size": "Standard_D8s_v5", "node_count": 6},
                    "replace_paths": [["vm_size"]]},
         "action_reason": "replace_because_cannot_update"},
        {"address": "azurerm_kubernetes_cluster.main",
         "type": "azurerm_kubernetes_cluster", "name": "main", "provider_name": "registry.terraform.io/hashicorp/azurerm",
         "change": {"actions": ["update"],
                    "before": {"name": "aks-prod-weu", "upgrade_settings": [{"max_surge": "10%"}]},
                    "after": {"name": "aks-prod-weu", "upgrade_settings": [{"max_surge": "33%"}]}}},
        {"address": "azurerm_public_ip.egress",
         "type": "azurerm_public_ip", "name": "egress", "provider_name": "registry.terraform.io/hashicorp/azurerm",
         "change": {"actions": ["create"], "before": None, "after": {"name": "pip-egress-new"}}},
    ]
    for i in range(40):
        changes.append({
            "address": f"azurerm_resource_group.rg[{i}]", "type": "azurerm_resource_group",
            "name": "rg", "provider_name": "registry.terraform.io/hashicorp/azurerm",
            "change": {"actions": ["update"],
                       "before": {"name": f"rg-{i}", "tags": {"owner": "platform"}},
                       "after": {"name": f"rg-{i}", "tags": {"owner": "platform", "cost-center": "CC-42"}}},
        })
    write_raw(s, "terraform/plan.json", {
        "format_version": "1.2", "terraform_version": "1.9.8", "resource_changes": changes,
    })


def main():
    if DATA.exists():
        for p in sorted(DATA.rglob("*.json")):
            p.unlink()
    scenario_oom_rollout_blocked()
    scenario_node_image_drift()
    scenario_rollout_completed()
    scenario_estate()
    files = sorted(DATA.rglob("*.json"))
    total = sum(p.stat().st_size for p in files)
    print(f"wrote {len(files)} fixture files, {total // 1024} KiB, under {DATA.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
