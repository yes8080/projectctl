# Executable Issue #9 Controller live fixture

**Developer did not run this test. No live PASS or cleanup is claimed.**
`TestControllerLivePlan` in `live_test.go` is executable and skipped by default.
No production bootstrap/allocation API was added.

## Scope and immutable inputs

This tests the plan package boundary under [the Controller ruling](https://github.com/yes8080/projectctl/pull/17#issuecomment-5861150346)
and [one PR #3 comment authorization](https://github.com/yes8080/projectctl/pull/17#issuecomment-5861156252).
Startup/design Agent/new design PR/Planner execution are Issue #11 integration
work, not claimed here. The Controller-owned fixture InputReader fetches pinned
JSON through real Gateway Git-object reads each time. It is not an Agent input
or fake GitHub remote. The production planner.InspectInputs default stays strict.

The exact tested commit contains `testdata/live/{bundle,candidate,inputs,
authorization,request-blueprint}.json`. SHA-256 expectations come from these
embedded files; every file is fetched at the full supplied commit by ReadSource.
The blueprint pins existing resources, not invented future native IDs. The host
constructs the exact Request from real readbacks and emits it with ReviewContext;
that disposable output is not a local authority or second project ledger.

Historical navigation is actual merged PR #3, commit
`1da6dfa438d4deaa13d42475e33fe84492c3d40b`, `docs/pipeline-design.md`, SHA-256
`3c5d9585a9fec44455af7a2a4c2a4090a6d78b297cd6f07a196a09bd2a5184c8`.
The structured Design DTO is **not** JSON decoded from that Markdown. Fixture
Acceptance does not replace historical approval or prove upstream startup.

Only the Controller holds the token. The fixed policy grants its stable native
identity Controller/Publisher/PlanReviewer roles. The actual separate Agent's
instance must differ from the fixture Planner instance and review the exact
Run/input chain. This is transparent same-login publication, **not native
credential isolation**. Other policies still require their own native principals.

## Exact write accounting

| Stage | New comments |
|---|---:|
| Seed Contract, Designer Run, fixture Acceptance on PR #3, fixture Approval, Baseline, Planner Run | 6 |
| Create one Issue: Publisher Run + intent | 2 |
| Publish Contract: Publisher Run + intent + Contract | 3 |
| Publish Plan: Publisher Run + intent + Plan | 3 |
| Reviewer Run, externally published PlanAcceptance, Controller Approval | 3 |
| Optional Controller result evidence | 1 |
| Planned maximum | **18** |

The seed Contract is the acyclic leaf required by nonempty Run.Inputs; it is
fixture context, not a product task. There is **1 new Issue, 0 edges, 0 Milestones**.
Hard caps remain 3/24/2/0. Each phase recounts all matching native comments on #2,
PR #3 and its new Issue, including external review. Milestone #1 is read-only.
All comments are retained; only the new Issue may be closed.

## Controller invocation

Supply `PROJECTCTL_GITHUB_TOKEN` through the existing secret provider, not argv,
fixture JSON, logs or pasted shell history. Choose a never-used 12-hex nonce.
The exact candidate commit must already be readable on GitHub.

```sh
PROJECTCTL_PLAN_LIVE=CONTROLLER_EXPLICIT_OPT_IN \
PROJECTCTL_PLAN_LIVE_PHASE=publish \
PROJECTCTL_PLAN_LIVE_COMMIT=<exact-40-char-head> \
PROJECTCTL_PLAN_LIVE_PREFIX=issue9-<head12>-<nonce12> \
PROJECTCTL_PLAN_REVIEWER_INSTANCE=<actual-independent-agent-instance> \
go test ./internal/pipeline/plan -run '^TestControllerLivePlan$' -count=1 -v
```

`publish` refuses any used prefix. Host append calls perform one POST and no
automatic retry. Never rerun publish after an uncertain result. Fresh allocation
and cross-process fencing remain Controller obligations; no exactly-once claim.
The test starts no model. Issue/contract/Plan/intent publication uses real Gateway
Admit/Apply with one-use permissions, while root and Publisher Run records use
the explicitly authorized test-only host boundary.

Publish stops at `AWAITING_INDEPENDENT_AGENT_REVIEW`, emitting exact Request,
Plan, reviewer Run, ReviewContext and cleanup native IDs/URLs. **This is not PASS.**
The separate Agent must inspect the six checks, then the Controller publishes one
strict PlanAcceptance on #2 with:

- operation_id `<prefix>/independent-plan-review`, subject milestone 1;
- exact emitted Plan candidate and reviewer Run references;
- assessments `coverage`, `dag`, `acceptance`, `environment`, `budgets`,
  `completion`, each referencing that Plan with honest result/reason;
- explicit decision/reason and unresolved_findings. Never invent PASS.

After separately authorizing the exact successful independent review, use the
same command/pins with phase `finish`. It reads existing roots without allocating
replacements, validates the review, appends Approval once, invokes Activate and
a fresh Engine's CheckFrozen, closes its new Issue, verifies closure and rechecks
the freeze. An unknown finish POST result must **not** be retried: inspect the
native operation read-only and resolve through Controller authority, or BLOCK.

## Cleanup and uncertainty

Use phase `cleanup` with the same pins to abandon a partial run. It permits only
own-Issue closure. The single-slice fixture expects exactly one native Issue;
zero/duplicate/hidden/deleted observations do not mean successful empty cleanup.
Identity, milestone and body digest are verified before PATCH, followed by a
closure reread. Edited/ambiguous objects need investigation, not replacement.

Report every remaining native URL. Refused/timed-out/unconfirmed cleanup is
**CLEANUP INCOMPLETE / FAIL**, even if activation passed. Never edit/delete old
objects, Milestone #1, comments, branches or settings. This test is not upstream
E2E, a scheduler, credential isolation or a general crash-safe allocation host.
