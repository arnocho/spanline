# Contributing

The rules that keep this tool acceptable on a client machine are not negotiable, so a change that
breaks one of them will be refused whatever it adds.

1. **Read only.** No write verb, no patch, no eviction, no impersonation. The kubectl allowlist in
   `internal/collect/kubectl.go` is the boundary, and it is enforced in code, not in documentation.
2. **No Secret read.** Helm ownership comes from annotations. If you need release values, that is a
   separate, explicitly labelled opt-in, and it must be refused by default.
3. **Determinism first.** Analyses take the time as a parameter, sort every map, and never call a
   model. A model may only narrate, and its output can never change a verdict, an order, or an exit
   code.
4. **Never assume a pass.** Anything unread or unmodelled is NOT ASSESSED, and it must appear in the
   output.
5. **No telemetry, no update check, no cache of cluster objects on disk.**

Practical notes: `make test` must pass, `gofmt -l .` must be empty, and fixtures are regenerated
with `python3 scripts/make_fixtures.py` and committed. New analysis rules need a fixture that
exercises them.
