# Candidate-only complete planning

Issue #8 adds a read-only admission gate, strict structured Planner output and
deterministic contract-derived coverage/DAG validation. It does not publish
Issues, milestones, dependency edges, approval records or product work. There is
no local database/cache, no scheduler and no automatic approval.

## Inputs and authority

The trusted Controller supplies exact native Baseline and startup manifest pins.
The gate re-fetches native records, verifies Controller authorship and the exact
Approval → independent PASS Acceptance → designer evidence chain, and checks
the reviewed design against a same-repository merged PR and matching Git trees.
The input baseline must be the Controller-selected active revision; this library
does not choose the latest comment or infer supersession from timestamps.
The current target is separately pinned and may be later than the design merge;
this interface does not prove commit ancestry. Establishing the active baseline
and target ancestry remains the Controller's responsibility, not a model claim.

The model receives only the approved design document and fixed project policy.
Original unapproved requirements, comment discussions, repository files and other
Agent conversations are not model inputs. The canonical input identity also binds
the exact request, manifest/source/record pins, prompt and output schema. Mutable
refs, missing/edited records, changed authors, repository/branch drift and open
design questions block. The immutable design must explicitly identify its final
integration requirement with ID `integration`; a missing ID requires an approved
design revision, never a new requirement invented by this Planner.

Native baseline publication is a trusted Controller responsibility: design
`Freeze` establishes the full author/reviewer Run-input identity and merge gate.
This reader independently rechecks its pinned review/evidence/Run links and native
merge facts; it does not introduce a competing implementation of design `Freeze`.

## Temporary candidate format

`projectctl.plan-candidate/v1` is a transient structured-output DTO, not a durable
GitHub fact schema. Every field is required; duplicate/unknown/case-alias fields,
nulls and oversized documents are rejected. Only the existing `protocol` records
may carry persistent project decisions. A future publisher maps candidate keys
to newly created native Issue IDs and dependency edges; no fake IDs are produced.

Each contract draft declares unique requirement ownership, explicit criteria and
criterion-to-requirement/check links, executable argv, environment, impact and
risk/mitigation. Every baseline requirement has exactly one owner; every owned
requirement has an AC; every AC names an existing check. Structural validation
cannot prove prose correctness or command adequacy: independent Plan review and
explicit approval are still required.

Exactly one integration slice owns the pre-existing `integration` requirement.
It transitively depends on every other slice and declares the fixed candidate
binding policy `exact_source_artifact_environment`. The future integration worker
must freeze the actual source SHA, artifact digest and environment identity before
running verification. Planning cannot know those future values and does not fake
them. Integration does not duplicate ownership of other slices' requirements.

Coverage, sorted dependency edges and deterministic topological order are derived
from the contract drafts whenever validated. They are disposable summaries, not
another editable task table. Missing/duplicate coverage, dangling references,
cycles, unresolved environment placeholders and open decisions reject a candidate.

## Bounded execution and cold start

`Admit` requires a trusted `FreshRunSource`: it reserves a new Controller-authored
`protocol.Run` on GitHub before returning. The Run role is `planner`, its ordered
`Inputs` are exactly `[baseline]`, and its `InputSHA256` is the supplied canonical
input digest. Its instance/operation/attempt must be unique. Every listed planner
Run counts, including abandoned attempts. Full native comments are reread on each
admission; restarting the Engine never resets the finite planning-round bound.
Run budgets retain the exact fixed project ceilings; only the per-run timeout may
be lowered. A different declared wall/round/cost ceiling is rejected, not ignored.

Admission is an opaque nonserializable one-use capability; all copies share an
atomic consumed flag. It is consumed before checking or dispatch, so failures
cannot refund it. The Controller must ensure new allocation events across process
restarts, enforce the project-wide total/cost budget, fence cancelled workers and
serialize publication. This package enforces the visible planner-Issue run/round
limits, per-run timeout and cycle wall time using the native control-Issue creation
timestamp. It does not claim distributed exactly-once or atomically consistent
GitHub reads. Inputs and the selected Run are checked again after execution.

Use the existing `design/codexexec.Adapter` as Runtime with an explicitly selected
model. That adapter uses a fresh ephemeral read-only process, strict events/output
schema, output limits and process containment. No GitHub credentials, repository
workspace, provider fallback or tool/code-mode host is passed to the worker. The
host must independently isolate its authentication and publisher credentials;
the same documented Windows Job Object and malicious escape boundaries apply.
Injected Runtime/Remote ports are trusted host code, never model-supplied objects.

## Verification

```text
go test ./internal/pipeline/planner/...
go vet ./internal/pipeline/planner/...
go test ./...
go vet ./...
go test -race ./...
```

Ordinary tests never invoke a model. The opt-in `TestPlannerCodexIntegration`
requires `PROJECTCTL_PLANNER_LIVE=1`; this iteration authorizes only one real
invocation with the Controller-selected `gpt-5.5`, no fallback and no retry. The
test exercises the complete strict adapter path with a bounded synthetic approved
baseline fixture, not live GitHub publication or final product integration.
Developer tests and runtime success are not independent Acceptance PASS.

Issue #8 Developer attempt 1 used exactly one real `gpt-5.5` invocation. The full
Engine → Codex exec → strict decode/coverage/DAG → post-input-recheck path returned
a valid two-contract/two-requirement candidate in 26.577 seconds. Output SHA-256:
`8698d4d7567597d0fb336b68a5f8002329a2b2ea2172e886d3aa2a2a62209ca9`.
Reported usage was 11,952 input and 1,079 output tokens. No fallback, retry,
publication or product execution occurred. This is synthetic component runtime
evidence, not independent Plan approval or the later full GitHub pipeline E2E.
