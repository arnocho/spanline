# One page for a security team

Give this page to whoever has to approve the tool. Every claim below is enforced in code, and the
file that enforces it is named.

## What it is

A single static binary, run by a human on their own workstation, under their own identity. Nothing
is installed in a cluster, nothing is installed in a cloud account. No daemon, no listening port,
no database, no account, no licence server.

## What it can do

Read. Through the `kubectl` already on the machine, restricted to an allowlist of read subcommands:
`get`, `version`, `config`, `auth`, `api-resources`. The allowlist is enforced before every
execution in `internal/collect/kubectl.go`, and any other subcommand returns a refusal rather than
running. There is no code path that patches, applies, deletes, scales, evicts, cordons or drains.

It also reads Terraform or OpenTofu JSON you already have: a state file, or a plan your pipeline
exported with `terraform show -json`. It never runs `terraform`, so no provider is downloaded, no
backend is contacted, and the binary plan file is never opened.

## What it cannot do

- **Secrets are never requested.** Helm ownership is inferred from the `meta.helm.sh` annotations on
  live objects, not from release storage, precisely so no Secret read is needed.
- **No impersonation.** Calls carry your identity, so the audit log stays truthful.
- **No persistence of cluster data.** Nothing is cached on disk. Exports are written only when you
  ask for them.
- **No network egress you did not ask for.** No telemetry, no update check, no crash reporting.

## What a model sees, if you enable one

Nothing, unless `--explain` is passed. When it is:

- the payload is an allowlist, not a filter: field paths, counts, severities, verdicts, and before
  and after values limited to resource requests and limits, replica counts, image tags, node image,
  kernel, kubelet and zone;
- namespaces, workloads, nodes and images are replaced by placeholders before the call, and
  rehydrated locally in the answer;
- values whose key looks like a credential become a salted hash, so a change is provable without
  disclosure;
- the endpoint must be allowlisted by host, or the call is refused;
- `--show-prompt` prints the exact bytes that would be sent;
- the answer can never change a verdict, an order or an exit code, and every sentence that does not
  cite a field present in the payload is dropped before you see it.

Build with `-tags airgap` and the hosted backends are not compiled into the binary at all.

## Audit footprint

In the API server audit log you will see `get` and `list` under the operator's identity, plus
`create` on `selfsubjectaccessreviews` and `selfsubjectrulesreviews`, which is how a permission
check asks the API whether it is allowed. Nothing else, because nothing else is reachable.

Run `spanline doctor` for the exact list, per context, before approving.

## Minimum rights

The built-in `view` role in the namespaces of interest, plus `get` and `list` on `nodes` and
`persistentvolumes`, which are cluster scoped. Argo CD application reads are optional and only add
attribution. Terraform state is read from a file you provide, so no backend credential is needed.

## Supply chain

Apache-2.0, source public, no code generation at build time. For a restricted site the supported
path is: mirror the source into your own forge, build it there, sign it with your key, publish it in
your own artifact repository. `make airgap` produces the reduced binary. Dependencies are the Go
standard library plus the terminal interface libraries, and `go mod vendor` works offline.
