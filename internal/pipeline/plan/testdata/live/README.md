# Issue 9 plan-only live input fixture

This is a committed, digest-pinned Controller input DTO authorized by
[the scope ruling](https://github.com/yes8080/projectctl/pull/17#issuecomment-5861150346).
It is not a project ledger or an execution result, and does not authorize a run.

- This fixture covers only the plan.InputReader boundary. It does not claim
  startup, design Agent, merged structured design, or Planner end-to-end validation.
- The historical source pin identifies the actual approved Markdown file at
  PR #3's merge commit. Design is a separate explicit plan-stage fixture DTO;
  it is not falsely presented as JSON decoded from that Markdown file.
- Manifest is a policy-shaped DTO, not a startup-ready manifest. Its role values
  all identify the same real yes8080 publisher. It intentionally cannot pass
  startup.DecodeManifest's independent-role gate. No synthetic native identity
  is introduced. Production planner.InspectInputs remains strict and unchanged.
- authorization.json permits only the real Controller principal. Independent
  Agent-instance review requires an explicit separate host workflow; this data
  cannot prove native publisher or credential separation.
- Inputs.SHA256 is intentionally empty in the file. After strict decoding, the
  runner computes protocol.DigestJSON of JSON {Design, Manifest}; it must not
  accept a supplied digest as evidence of upstream validation.
- bundle.json aggregates the exact candidate, inputs and authorization DTOs.
  Their separate source files allow ordinary production Gateway.ReadSource pins.
  Every file must be committed and read at the exact tested implementation SHA.
- request-blueprint.json pins pre-existing resources and fixed scope only. It is
  not a complete Request and contains no invented runtime native IDs. The runner
  resolves newly-created objects from native GitHub responses and retains exact
  immutable references. A unique operation prefix and tested source commit come
  from the explicit Controller invocation, never a mutable fixture fallback.
- The minimal candidate has one integration-kind slice and zero dependency edges.
  This does not replace Issue 11's dependency/startup/design integration testing.
- Maximum authorized mutations: 24 comments, 3 Issues, 2 edges, 0 Milestones.
  Reuse existing Milestone #1; do not modify any pre-existing object. Close and
  reread each newly-created Issue. Preserve immutable fixture comments. Incomplete
  cleanup, scope drift, unapproved review or uncertain remote facts mean FAIL.

No real GitHub mutation, acceptance PASS, or cleanup completion is recorded here.
