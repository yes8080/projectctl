# Issue #9 live Controller checklist

Status: **NOT RUN / authorization-dependent**. Offline fake-GitHub tests are
Developer self-checks, not independent Acceptance or live evidence. This document
does not authorize network writes and is not a project execution ledger.

## Authority and exact scope

- A trusted Controller must explicitly authorize the exact repository, immutable
  candidate head, fixture prefix, actions, finite run budget and cleanup actions.
  Use one unique prefix `issue9-<candidate-head>-<nonce>-` for all new artifacts
  and operation IDs; never adopt an existing object based only on its title.
- All temporary Issue fixtures must be assigned to the **existing Milestone #1**.
  Verify its native database ID and node ID read-only before any fixture write.
  Do not edit, close, delete, reassign, or otherwise mutate that pre-existing
  Milestone or any other pre-existing Issue, PR, comment, dependency or setting.
- **Pending authorization clarification:** a uniquely prefixed temporary
  Milestone may be needed to exercise `CreateMilestone`. Creating it is not yet
  authorized by this checklist. Nor is assigning Issue fixtures to that new
  Milestone authorized: that conflicts with the explicit Milestone #1 constraint.
  If the publication path requires that assignment, its full live test is
  BLOCKED until the Controller resolves the scope. Do not silently substitute
  a weaker test or report full-path success.
- Publisher credentials remain with the Controller. Do not expose them to
  models, candidate code, logs, evidence bodies, or fixture JSON. Native identity
  separation and existing Gateway authorization callbacks remain mandatory.

## Before the first write

1. Read the current Issue contract, approved baseline/plan input and PR head from
   GitHub. Confirm the tested candidate head is still exact and record evidence
   on that PR, not in a local parallel ledger.
2. Resolve the repository, Milestone #1 and any authorized temporary control
   Issue to complete native identities. List matching objects with complete
   pagination. Existing matches, unavailable reads or ambiguous identity block
   allocation; an empty list is never evidence of a fresh publisher generation.
3. The Controller must supply a once-only fresh-allocation event for each exact
   Gateway request. Register a native publisher Run bound to the Intent request
   digest. Do not reconstruct an admission from saved JSON or a prior Run.
4. Establish a cleanup allowlist from the exact native objects created in this
   run. Keep pre-existing resources out of it. Define whether closing or deletion
   is authorized for each newly created object before execution.

## Bounded publication and recovery evidence

1. Observe deterministic publication ordering: Milestone (if authorized), Issues,
   exact contract comments, native dependency edges, then the protocol Plan
   candidate record. Inspect the real GitHub objects and full native identities.
2. At each incomplete prefix, reconstruct with a new Engine/Gateway using only
   the same remote references. Verify it cannot report an active plan, invent
   missing member contracts, infer dependencies from prose, or dispatch work.
3. For any real timeout or lost response, preserve the exact request and remote
   Run/intent references. Resume through read-only reconciliation. Do not issue
   another POST merely because an accepted effect is temporarily invisible.
   An intentionally injected network-fault scenario needs separate authorization;
   a deterministic httptest simulation must be labeled offline.
4. Reconcile a fully visible result with a fresh Engine/Gateway and confirm no
   duplicate Milestone, Issue, contract comment, edge, intent, or Plan candidate.
   Each intended operation must resolve to one exact native effect.
5. The final Plan is still a **candidate**, not activation or dispatch authority.
   Independent review and exact Controller approval are separate gates. Never
   turn partial publication, an unapproved newer revision, or a successful write
   response into an active-plan claim.

## Cleanup and final outcome

- Operate only on the cleanup allowlist of newly created native objects. Re-read
  identity before each authorized cleanup action; changed or ambiguous identity
  blocks that action. Preserve failed-run evidence rather than deleting it to
  make the test appear clean.
- Verify each authorized cleanup result from GitHub, including that temporary
  Issue fixtures are closed/removed as authorized and any separately authorized
  temporary Milestone is cleaned up. Do not alter Milestone #1 itself.
- If cleanup is denied, times out, or cannot be confirmed, report
  **CLEANUP INCOMPLETE / BLOCKED**, list the exact remaining URLs, and do not claim
  the live acceptance run completed successfully. Recovery must not create
  replacement fixtures or replay publication.
- Publish a concise outcome on the implementation PR with tested head SHA,
  exact fixture/evidence URLs, native identity checks, observed recovery behavior,
  approval boundary, cleanup status and unresolved blockers. Never label missing
  authorization, skipped steps or simulated results as live PASS.
