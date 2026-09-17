# spanline

One read-only binary that spans your Terraform and your Kubernetes clusters, from your laptop.

No agent in the cluster. No SaaS. No server. It reuses the credentials already on the machine and
only ever runs read subcommands. The deterministic core works with no model at all; an optional
narrator can explain a report, and can never change a verdict.

```
spanline cockpit --contexts 'prod-*' --tfstate ./infra/state.json
spanline why deploy/checkout -n payments
spanline impact --nodes pool=apps
spanline impact --plan plan.json
spanline demo
```

## Why it exists

Two things are easy to get separately and hard to get together.

**Terraform knows what it declares. The cluster knows what is running.** Nothing local joins them.
Tools that do join them are hosted services you cannot point at a client estate on day one.

**During an incident you can list what changed. You cannot see what separates the pods that fail
from the pods that hold.** `spanline why` computes that separation from the Kubernetes API itself,
then names the change that created it. Observability suites do the same idea in the cloud, on
telemetry you had to ship beforehand.

## The three commands

**`cockpit`** is the overview. Every cluster you can reach, every node pool, the Terraform address
that owns each pool, and what is fragile: single replica workloads, disruption budgets with nothing
left to give, volumes pinned to one zone, pools with no headroom left for a drain.

**`why`** is the incident. Give it a workload. It splits the pods into failing and healthy, compares
them across template hash, image, node, pool, node image, kernel, kubelet, runtime, zone and config
checksum, names the dimension that separates them, and ranks changes by whether they created that
dimension. When every pod is failing there is no healthy cohort left, so it falls back to comparing
the current controller revision with the previous one and says that the verdict is weaker. Verdicts
are SPLITS, TEMPORAL, NO-SPLIT and UNKNOWN. The word "cause" is never printed.

**`impact`** is the change you are about to make. Simulate the loss of a pool, a zone or a node list
and see which workloads lose every replica, which budgets block the drain, which volumes are
stranded and whether the load fits on what survives. Feed it a plan exported by your CI with
`terraform show -json` and it maps pool replacements onto the same simulation. It exits 3 on an
outage, 2 on a disruption, 1 on a risk or when something could not be assessed, 0 when clean.

## Install

```bash
go install github.com/arnocho/spanline/cmd/spanline@latest
```

Or build from source, which is the only supported path on a restricted site:

```bash
git clone https://github.com/arnocho/spanline && cd spanline && make build
```

## Try it with no cluster

Four scenarios are recorded and embedded in the binary, so everything below runs offline, with no
cluster, no network and no account.

```bash
spanline cockpit --fixtures estate
```

![the interface: overview, an incident opened from a workload, an impact opened from a pool](docs/demo.gif)

The interface opens by naming every read it performs, which is also the shortest explanation of
what the tool touches and what it deliberately never touches. From the overview, `w` on a workload
opens its incident and `i` on a node pool simulates losing it, without leaving the application. Then one screen answers one question,
and the detail sits one keystroke away.

| key | what it does |
| --- | --- |
| up and down | move through the list |
| right | open the evidence behind the selected line |
| left or esc | back, close the panel, clear the filter |
| tab | next view, shift tab for the previous one |
| 1 2 3 | jump to overview, incident or impact |
| d | expand: every row, every detail, inline |
| / | filter |
| ? | the key map, and what each view answers |
| q | quit |

The three views:

**overview** answers where to look first. A prioritized list, then one line per cluster, then one
line per node pool with the Terraform address that owns it.

**incident** answers what separates the failing pods from the healthy ones. It leads with the
dimension that tells them apart and the change that created it, then lists what else differs and
the changes ranked below.

**impact** answers what breaks. It leads with the count of workloads that lose every replica, then
the findings worst first, with the verdict and the exit code pinned at the bottom.

Other scenarios, and the plain output for a pipe or a ticket:

```bash
spanline why deploy/api -n payments --fixtures node-image-drift
```

That one is the interesting case. Every pod shares the same template hash, so no deployment
explains the failure. Only the node image version separates the three pods that fail from the three
that hold, and the change list points at the three nodes that joined with that image.

Piping to a file, or passing `--json` or `--md`, prints the same answer without the interface.
`--details` adds the full dimension table, every ranked change and every evidence line.

## What it reads, and what it never does

Reads, through your own `kubectl`, with an allowlist of exact read subcommands enforced in code:
`get` (never Secrets, never `--raw`), `config view`, `config get-contexts`, `config
current-context`, `auth can-i`, `auth whoami`, `version` and `api-resources`, with impersonation
flags refused. What it gets: pods, nodes, replicasets, controllerrevisions, deployments,
statefulsets, daemonsets, events, disruption budgets, volume claims, volumes, and Argo CD
applications when present. Reads
Terraform or OpenTofu JSON you already have: a state file, or a plan exported by your pipeline.

Never writes, never patches, never evicts, never impersonates. Never reads Secrets, so Helm release
storage stays untouched and Helm ownership is inferred from the `meta.helm.sh` annotations instead.
Never runs `terraform`, so no provider is downloaded and no backend is contacted. No telemetry, no
update check, no cache of cluster objects on disk.

`spanline doctor` prints the identity in use, what it can and cannot read, every endpoint the run
will contact, and the audit footprint a security team will see.

## The optional narrator

Off by default. `--explain` sends an allowlisted, pseudonymized payload: field paths, counts,
severities, verdicts, and before and after values of resource limits, replica counts, image tags,
node image, kernel, kubelet and zone. Namespaces, workloads, nodes and images are replaced by
placeholders before anything leaves the machine, and rehydrated locally in the answer. Every
sentence must cite a field that exists in the payload, with matching numbers, or it is dropped.
The answer is labelled unverified. Endpoints must be allowlisted explicitly.

`--show-prompt` prints the exact bytes. Build with `-tags airgap` and the hosted backends are not
compiled in at all, leaving an OpenAI compatible endpoint you host yourself, or nothing.

## Status

Early and honest about it. The analyses are covered by tests against recorded fixtures, and have
not yet been validated against a live cluster or a corpus of replayed incidents. Treat the verdicts
as evidence to read, not as an answer to act on, until that corpus exists.

Prior art worth knowing: Radar is the strongest local Kubernetes change timeline, Steampipe can
query Terraform files and clusters side by side in SQL, Overmind maps a plan onto live dependencies
through its own service, and kubectl-revisions diffs controller revisions. spanline's bet is the
join between the two halves, plus the cohort separation, from one binary with no install.

## Licence

Apache-2.0.
