# Bounded startup and fixed inputs

Issue #6 implements a deterministic startup core, not an Agent, scheduler, CLI,
live bootstrap publisher or design approval engine. It does not import the old
prototype or save files, a database, a checkpoint, a policy copy or cached progress.
All returned state is a disposable observation, never an input authorizing work.

## Authority and policy

The trusted Controller supplies an `Anchor`: exact GitHub repository native IDs
and owner/name, plus a full commit SHA, normalized path and raw SHA-256 of a
startup manifest. The root reference must come from the authorized GitHub input
chain; choosing or approving that root is not an Agent decision made here. This
package does not use a local config file as an alternative policy authority.

`projectctl.startup/v1` is a separate, strictly decoded input-document schema,
not a different wire shape under `projectctl.record/v1`. The manifest contains
the goal/non-goals, input-storage and write authorization, native publisher-role
identities, complete finite budgets, serial merge policy, completion criteria,
all six terminal states, required environment/capabilities and exact allowed
design-document paths. Omitted and null budget limits are rejected; explicit
zero cost, defect and change allowances are meaningful finite limits. Unknown,
duplicate and case-alias JSON keys, invalid paths and mutable refs are rejected.
The policy requires distinct design/review and development/acceptance identities;
Controller publication is separate from development and acceptance publication.
Native identity declarations alone do not prove separate Agent instances.

The policy content lives only at its pinned Git blob. The project-control Issue
holds `projectctl.control/v1`, a discovery marker and exact manifest reference,
repository identity, cycle and stable operation ID. It does not copy contracts,
policy contents, milestone/dependency membership, task progress or delivery.
This startup pointer is not a baseline/plan approval; those decisions remain
strongly referenced records handled by later slices.

Every input is read from the same pinned repository using full commit/path/digest.
The Gateway walks native commit/tree/blob objects, rejects missing or nonregular
paths, checks complete nonrecursive trees, verifies Git blob SHA-1 framing and
raw SHA-256, and returns exact bytes. The authenticated native API is trusted for
commit/tree linkage; this is not signed-commit verification. Content is bounded
to 4 MiB, paths to 4 KiB/64 segments and traversal to 64 MiB. No local checkout,
branch/tag resolution, symlink, submodule or cached content substitutes for it.

## Deterministic select-or-create

`Reconcile` always re-reads repository identity, manifest, inputs, authenticated
Controller, complete Issues and current capabilities. Zero active marked Issues
means `bootstrap_required`, not permission to POST. Exactly one must match native
identity, author, exact body/digest and the expected cycle/operation/anchor. More
than one, even a seemingly newer one, is a conflict. Every visible marked Issue
is strictly validated before open/closed classification. A closed record is
excluded only when its exact pinned manifest confirms a different cycle and
operation in this repository, with its own historical Controller as author.
Malformed, edited or inaccessible history blocks even beside a valid open Issue;
historical host readiness and historical input files are not re-probed. A closed
matching cycle or operation does not silently start over. Conflicting facts
block; an unobserved deleted/recreated history cannot be proved from absent data.

`Initialize` can select an existing exact Issue without a permit. First creation
requires **both** an injected trusted `BootstrapCreator` and an opaque admission
from `AdmitBootstrap`. A `FreshBootstrapSource` consumes a newly authorized event
and supplies a pinned GitHub authorization source. That strict authorization
binds repository, operation ID, exact body digest, native publisher and positive
generation. Its exact bytes are re-read before dispatch. The callback's freshness
and authorization provenance are a trusted Controller obligation, not something
that a Git filename, local counter or an `APPROVED` string proves by itself.

Copies of an admission share one atomic consumption flag, cannot be serialized
or restored and allow at most one port invocation. Existing-object, canceled or
failed attempts consume authority too. The port response is never success proof:
one bounded fresh full reconciliation decides the outcome. A lost response with
an observed exact Issue recovers; an invisible or unreadable result is uncertain.
Reusing the permit or constructing a new Engine without a new authorized event
only reconciles and never sends another create. Positive duplicate/conflicting
objects remain conflicts. No automatic retry, sleep, distributed lock or GitHub
exactly-once guarantee is claimed. The single Controller must serialize separate
publisher channels and allocate any genuinely new recovery generation explicitly.

## Preflight and design-only boundary

Repository settings, explicit permissions and read access are tri-state. Every
policy-required capability must be `supported`; missing, `unknown`, `unsupported`
or malformed observations block. Auto-merge requires a separately required
protected merge gate. Unrequired unknown capabilities are still reported, never
upgraded. Settings are observed, not changed. Host probes separately establish
runtime, workspace and publisher isolation, role identities, cancellation,
usage/cost bounds and acceptance environment. Missing host observations block.
Probes are trusted integration adapters, not Agent-provided boolean assertions.
Public reason codes are deterministic and omit raw provider/credential errors.

`CheckProposal` revalidates remote facts and allows only Designer/DesignReviewer
input/design/startup documents at explicitly allowed paths. It never dispatches
an Agent, writes an artifact, or permits Developer/Acceptor/Planner/product code.
Even a later approval does not bypass this package: a future frozen-plan/launch
gate must independently authorize product execution. Filesystem enforcement of
the proposal must be supplied by the isolated workspace/publisher, not this DTO.

## Explicit live integration status

`State.BootstrapAdapter` always reports **`REQUIRED_NOT_IMPLEMENTED_FOR_LIVE`**
in this slice. A missing publisher port cannot report successful creation. The
core select-or-create behavior is implemented and tested with a deterministic
remote/host/creator fixture; it is not a claim that live first bootstrap works.

The existing concrete Gateway requires an already known control Issue. Its
comments probe depends on that Issue and its dependency probe reports unknown
without an explicit Issue. A production pre-control discovery/capability adapter,
fresh authorization source, bootstrap publisher and actual isolated-host probes
remain Controller integration work. The current REST probe also leaves native
auto-close and protected trusted merge gating unknown. Required unknown results
therefore block, rather than borrowing a fixture's supported values. No guessed
Issue number, synthetic run or automatically permissive adapter is installed.

## Verification

Normal tests are offline, using deterministic injected boundaries and the
Gateway's loopback HTTP fixtures. They include required-policy omission/null
matrices, duplicate/edited control Issues, source drift, tri-state preflight,
design-only proposals, fresh reconstruction and accepted-but-invisible lost
responses. The shared-admission concurrent regression repeats 100 times.

```text
go test ./internal/pipeline/startup/...
go vet ./internal/pipeline/startup/...
go test ./...
go vet ./...
go test -race ./internal/pipeline/startup/... ./internal/pipeline/github/...
```

An explicit developer/acceptance read-only smoke is available with:

```text
PROJECTCTL_STARTUP_LIVE_READONLY=1 go test ./internal/pipeline/startup -run TestLiveReadOnlyStartupInputs -count=1 -v
```

This test-only host adapter obtains existing `gh` authentication in memory,
does not print token/command output, hard-rejects every HTTP method except GET,
reads the exact #6 base design Git blob twice, and reports capability states.
Default tests never contact GitHub or discover credentials. The smoke proves
those native reads only, not live bootstrap writes, full host isolation, design
approval, Agent execution or M1 delivery. Developer tests are not Acceptance PASS.
