# Publish, independently review and freeze a native plan

Issue #9 implements one deterministic publication step at a time and a read-only
approval gate. It contains no Developer scheduler, merge command, local database,
second task table or in-place contract editor. Only a trusted Controller holding
the Gateway's publisher authority may invoke a mutation; the Developer used only
local HTTP fixtures, not live GitHub writes.

## Authoritative inputs and recovery

`Request` comes from the trusted Controller's GitHub-restored pins: the original
Planner request, immutable candidate artifact, Planner Run, immutable native-role
authorization policy and a stable publication prefix. A candidate artifact is a
historical proposal, not an editable execution ledger. Existing `planner` input
verification is reused without duplicating its identity algorithm. The original
Run must match that exact baseline and canonical input digest. Source bytes,
authorization policy, native baseline, repository, control Issue and design merge
objects are reread; no successful local observation is cached.

The Controller must select the active authorized pins, not an arbitrary older
approval, and must recover these pins from GitHub when restarting. This package
does not discover the active project root, select a latest comment or turn a saved
local Request into authority. Injected InputReader/Gateway implementations are
trusted host code, never model-supplied JSON; the default InputReader invokes the
production `planner.InspectInputs` gate.

## One-shot publication

`Inspect` replays the complete deterministic prefix: Milestone, each Issue and its
contract comment, native blocking edges, then the exact candidate `protocol.Plan`.
Keys become bounded SHA-256-based operation identifiers. Issue/Milestone bodies
contain navigation and operation markers, not duplicated contract ACs. Contract
criteria retain exact requirement/check references and fixed-candidate policy in
their canonical textual AC content; no milestone/dependency/progress fields are
added to a Contract. Native relations remain exclusively on GitHub.

The Gateway reconciles every operation by complete remote reads, exact content,
stable object identity and native publisher. `Admit` asks a trusted fresh-allocation
channel for authority for only the currently missing step; `Apply` consumes that
opaque capability once, including failed attempts. Copies share one atomic flag.
Neither an empty list nor a restart grants fresh authority. A hidden accepted
intent/effect remains uncertain and cannot be retried by minting a permit from
absence. The Controller owns fresh-generation uniqueness, cross-process fencing,
global budgets and serialized publishing; GitHub does not offer exactly-once or
a multi-object transaction. These are explicit existing Gateway trust boundaries.

Every completed prefix is reread before the next mutation. Once all operations
exist, all Issue/contract identities and content, Milestone identity/content,
membership and both directions of native blocking relations are verified. A
second complete read catches observed changes. Missing/inaccessible, contradictory,
duplicate or drifted facts block. Partial publication yields `ErrPartial` and an
exact next proposal, never an active Plan. A complete unapproved Plan remains a
candidate even if every native Issue exists.

An explicitly pinned existing Milestone can be used without editing it. Only
selected members enter this plan's topology; unrelated Issues in a shared stage
are not appropriated. For a newly created plan-exclusive Milestone, unexpected
members block publication. Native Issue open/closed progress is not copied into
the frozen topology digest. Selected member IDs, content, authors, Milestone
relation and all incident native blocking edges are frozen.

## Independent approval graph

The only new persistent record kind is strict `protocol.PlanAcceptance`:

```text
Controller Approval → independent PlanAcceptance → exact Plan
                               ↓                     ↓
                          reviewer Run        native members/contracts
```

It identifies a Milestone, precise Plan reference, reviewer Run, six assessments,
unresolved findings and a decision. It does not copy contracts, coverage or DAGs,
and does not misuse a code PR or `Binding.ContractSHA256`. The shared strict codec,
authorization selection and Gateway traversal support only this explicit new
kind; old record forms remain unchanged.

`ReviewInput` supplies the independently reread approved design, fixed policy,
native contract bodies, exact Plan and six checks: coverage, DAG, acceptance,
environment, budgets and completion. It includes no Planner conversation or
self-assessment. Its canonical identity binds the entire context and publication
input. Reviewer Run Inputs must be exactly `[plan, baseline, planner_run]`, the
reviewer instance must differ from the Planner's and its native role must be
authorized. The review's native author must differ from the Controller publication
identity. Each PASS assessment references the exact Plan as the inspected data,
not a fabricated executable Evidence record. The Controller then approves that
precise PASS/no-findings PlanAcceptance, never an unreviewed Plan directly.

`Activate` independently rereads the full graph twice. Wrong subject, policy,
candidate, Run/input identity, native author, instance, operation, missing check,
failure, unresolved finding or deleted/edited reference blocks. The Plan freezes
exact member native IDs and contract comment IDs/digests, baseline, topology and
budget. Its existing `Completion` field holds one canonical representation of
the complete fixed completion/terminal policy; there is no synonymous second
TerminalConditions field. A returned Frozen value is disposable, not durable
execution permission.

`CheckFrozen` replays the same approval graph for later execution admission. The
original planning branch observation is reconstructed from its exact immutable
Git commit through `planner.ReplayInputs`, so a normal later product merge does
not force re-planning. All other original source/policy/approval/control-wall
checks remain. Native Issue closure is treated as progress, not topology drift.
The Controller separately proves the current head's ancestry, legitimate task
completion, worker fencing and execution authority. This package dispatches none.

The legacy Contract.DesignBaseline.Approval navigation field now accepts either
the original exact design-PR comment URL or the exact project-control Issue
comment URL used by formal Approval records. It is never authorization by itself;
the native strong-reference chain remains authoritative. Wrong repository,
arbitrary Issue/PR and malformed comment fragments remain rejected.

## Evidence and live boundary

```text
go test ./internal/pipeline/plan/...
go vet ./internal/pipeline/plan/...
go test ./...
go vet ./...
go test -race ./...
```

Tests use the real Gateway against httptest for native writes/reconciliation and
injected immutable Git/input observations. They cover accepted-hidden intent and
effects for every action, restart, concurrency, duplicates, partial publication,
native drift, independent-review binding and frozen-state rechecks. They do not
prove live token permissions, dependency availability or Controller cleanup.
See [LIVE_ACCEPTANCE.md](LIVE_ACCEPTANCE.md) for the uniquely scoped Controller
fixture plan and mandatory cleanup report. Cleanup failure is a live acceptance
blocker, not a reason to erase evidence or call publication successful. No cleanup
executor or full host Controller loop is claimed by this slice.
