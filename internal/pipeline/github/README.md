# GitHub-authoritative Gateway

This package implements Issue #5's HTTP boundary on top of
`internal/pipeline/protocol`. It does not use the prototype `internal/github`,
invoke an Agent, schedule work, install protection rules or keep a local database,
checkpoint, credential file or authoritative cache. Returned objects and graph
maps are disposable observations; a fresh Gateway reads GitHub again.

## Trusted construction and credentials

`New(Config)` requires an explicit `protocol.Project` and host-supplied credential
provider. Production requests only go to `https://api.github.com`; an explicit
loopback-only option exists for `httptest`. The client is copied and redirects
are disabled, including same-origin redirects. Pagination cannot forward a bearer
token to another origin, repository, endpoint or filter set. Tokens are never
read from environment/files, included in request bodies, or emitted in errors.
HTTP errors omit response bodies; provider/transport error strings are redacted.

The credential provider, injected HTTP transport and `Authorize` callback are
trusted Controller components, **not Agent-controlled configuration**. The Gateway
does not itself create a sandbox or hide credentials from code running in the
Controller process. Candidate code must execute elsewhere without these handles.

Mutations require both an explicit native `Publisher` identity and an authorization
callback. `/user` verifies that the current credential belongs to that identity;
one credential snapshot is retained only for that operation and its reconciliation.
A rotating provider cannot change the account between identity check and POST.
The snapshot is scoped to this Gateway, not other clients invoked by the callback.
This user-authenticated publisher path does not claim installation-token `/user`
support or prove multi-publisher credential isolation.

## Native reads and pagination

Public reads include repository/issue/milestone/comment lookup, current user,
complete Issues/comments/Milestones collections, and both `blocked_by` and
`blocking` dependency directions. Issue collections filter genuine PR entries;
Issue lookup and dependency reads reject PR masquerades. Milestones correctly use
`creator` and `description`, not Issue `user`/`body` fields.

`ReadPullRequest` keeps API `2026-03-10`, which removed `merge_commit_sha` from
PR responses. For a merged PR it reads every page of that exact Issue's native
events and requires exactly one `merged` event. All event database/node IDs,
event URLs and actors are validated; duplicated identities, malformed or missing
merge events, and wrong-repository/malformed commit IDs or URLs block the read.
An optional embedded Issue must identify that exact PR. A present legacy field
can only expose a contradiction; it never supplies or overrides the merge SHA.
Unmerged PRs do not read events or expose a test-merge SHA. No API downgrade,
head/base inference, caller-supplied merge identity or local cached fact is used.
The returned commit still passes the existing downstream exact commit/tree gates.
These reads share one credential snapshot but are not an atomic GitHub snapshot.

All paginated reads follow `Link rel="next"`, even after a short page. They retain
the requested filters, traverse consecutive pages, and reject malformed/multiple
next links, origin/path/filter escape, cycles, duplicate native identities and
failed later pages. No partial collection is returned as complete. Safety bounds
(16 MiB/response, 64 MiB/traversal, 1,000 pages, 100,000 objects) produce errors,
not truncation. Callers supply an overall operation deadline; the default HTTP
client also has a 30-second request timeout.

Native JSON permits newly added API fields, but rejects duplicate keys, aliases
that would case-fold over a modeled field, invalid Unicode and trailing JSON.
Objects expose stable database/node IDs, native author, exact decoded raw Markdown,
and raw-body SHA-256. A null body remains null, distinct from an empty string.
Marked protocol records also expose a canonical digest and separate schema-valid
flag; a canonical digest alone is **not authorization**.

## Pinned reference selection

`FetchRecord` reads precisely the referenced comment/review and delegates native
author, repository, object, publication location and raw/canonical digest checks
to `protocol.VerifyGitHubRecord`. Missing/inaccessible, edited, moved or recreated
objects fail; a newer comment is never substituted.

`ResolveApproved` starts from an externally pinned authoritative approval and
trusted policy. It validates Controller authorization and exact candidate binding,
walks every strong record reference, rejects object/operation conflicts and cycles,
checks declared reference target types, and verifies native Issue IDs for plan
members. It re-reads the graph before returning to catch edits during traversal.
This is not an atomic GitHub snapshot; the Controller must revalidate before use.

Approval supersession, native topology digests, budget
policy, design/AC completeness, review outcome semantics and launch/merge gates
remain the later Controller/Gate slices' responsibilities. Contract bootstrap
navigation URLs/parent hints are not silently upgraded into strong authorization
references. Unsupported bootstrap record kinds remain fail-closed under #4's
documented policy; reading them as content does not activate them.

Issue #6 adds the read-only `ReadSource` boundary for startup inputs: a full Git
commit SHA, exact regular-file path and raw SHA-256 are resolved through native
commit/tree/blob objects in this Gateway's repository. No branch/tag, symlink,
submodule, recursive/truncated tree, local checkout or cached bytes can substitute.
Blob SHA-1 framing and content SHA-256 are verified; native authenticated API
metadata remains the trust boundary for commit/tree linkage. Files are bounded
to 4 MiB, paths to 4 KiB/64 segments, and the full read to 64 MiB. Git data uses
the JSON media type through the same credential/origin/error protections. This
does not add a bootstrap writer or remove `New`'s existing control-Issue requirement.

## Explicit writes and remote recovery

The narrow `ApplyFirst(Request, FirstCreateAdmission)` supports only:

- Create an Issue, optionally assigned to one pinned native Milestone.
- Create a Milestone.
- Publish a typed protocol record as a comment on its permitted Issue/PR.
- Add one native `blocked_by` relation, using the blocking Issue's database ID.

There is no arbitrary REST write, overwrite, delete, push, PR creation or merge API.
Requests identify the project, stable operation ID, exact control Issue and target
resource. Existing resources include database/node IDs, not only numbers. The
authorization callback receives the verified publisher, precise resources and a
canonical request digest; returning success is a trusted policy decision, not an
authorization string supplied by an Agent.

`Apply` and `Reconcile` are **read-only recovery APIs**. Even a complete empty
collection cannot prove that an earlier accepted intent is not temporarily
invisible, so neither API creates an intent. `New` never grants first-write
eligibility, and ordinary `Authorize` success is not a first-create admission.

First creation additionally requires explicit `AdmitFirstCreate` with a trusted
Controller `FreshAllocationSource`. This source must consume a **new allocation
event**, not replay a stored run, perform a repeatable permission check, or infer
novelty from empty GitHub lists. It supplies a verified native publisher Run whose
`input_sha256` is the exact request digest. The Gateway re-reads that precise Run,
checks the publisher, role, positive assignment generation/attempt and digest,
then issues an opaque `FirstCreateAdmission`. Before the intent POST it checks
the Run again. The Run reference pins the authorization generation and content.

Admissions cannot be JSON encoded/restored; their zero value is unusable. Copies
share one atomic consumption flag. A valid `ApplyFirst` attempt consumes the flag
even if inspection fails or encounters an existing intent. This prevents an
unused permit from becoming writable later when a previously observed intent
temporarily disappears. Only that invocation may dispatch one intent and one
effect; failed/canceled calls never refund authority. A fresh Gateway or process
without a new allocation event only reconciles. Completed operations remain
recoverable without any admission.

The Controller allocation source is an explicit trusted integration boundary,
not implemented by this Gateway. It must enforce fresh-event uniqueness and
policy across restarts; rebuilding a permissive callback around an old Run is
misuse, not recovery. The package has no automatic allocation source. Retrying
an uncertain operation requires separately recorded, explicit Controller
authorization/new generation and intervention policy; incrementing a local
generation or observing an empty list cannot authorize it. No exactly-once or
distributed-lock guarantee is claimed.

Each new operation first registers a comment on the specified control Issue under
the separate `projectctl.operation/v1` schema / `projectctl:operation:v1` marker.
The journal contains `kind`, `project`, `subject`, `operation_id`, exact resource
identities and request digest, not another task contract or progress snapshot.
Created Issue/Milestone bodies contain an operation binding; record comments carry
their protocol operation ID; native dependencies are matched by exact endpoints.
The journal author and result author must match the verified publisher.

After the newly dispatched intent is observed uniquely, at most one effect request is sent. Both
successful responses and failed/timeout responses are followed by full remote
reconciliation. Observed exact results can complete despite a lost response.
Duplicate intents/objects, changed payloads, wrong authors or changed native
membership fail closed. Completed operations recover with a fresh Gateway and no
local files. Both recovery APIs perform reads only; they never create a missing object.

Intent and effect collections are not an atomic snapshot. If a read observes an
effect after previously finding no intent, reconciliation repeats **both complete
collections exactly once**, checking the same operation ID, request digest,
native identities and publisher. A matching pair returns `reconciled`. If the
pair is still missing, inaccessible, or disappears during the second observation,
the result is `ErrUncertain`, not an inferred orphan conflict or a new-write
opportunity. A positive wrong body/author, duplicate, or different effect identity
remains `ErrConflict`; rereading never erases such contradictory facts. There is
no sleep, unbounded retry, or claim of an atomic snapshot. A pre-existing native
dependency edge retains its explicit adoption behavior and is not a completed
operation without a matching intent.

An existing intent with an absent/unconfirmed effect, or an absent/invisible
intent without a fresh unspent admission, returns `ErrUncertain` and
**does not automatically retry**, including after restart. A fresh bounded read-only
context (30 seconds) reconciles a canceled/failed write; no new effect is dispatched
after cancellation. GitHub has no general idempotency key or atomic multi-object
transaction, so a successful list showing no result cannot prove a delayed write
will never appear. Such uncertainty needs Controller-directed reconciliation or
explicit intervention, not another POST. The Controller must serialize its single
publisher channel; these comments are not distributed locks. Deletion of every
unobserved/unindexed trace cannot be detected or reconstructed by this package.

## Capabilities are tri-state, not permission guesses

`ProbeCapabilities` distinguishes complete read access, enabled settings, and
explicit repository permissions. Both dependency directions must read successfully
before combined dependency access is supported. Explicit false settings/permissions
are `unsupported` **for that named precondition**, not a claim that GitHub lacks the
feature. Missing fields, 401/403/404/410, rate limits, network failures or malformed
responses are `unknown`. Unknown and zero values never satisfy `IsSupported()`.
Auto-close configuration and a protected trusted merge gate remain unknown: this
REST probe does not prove either, nor does `allow_auto_merge` prove auto-close.

## Offline verification and remaining live boundary

```
go test ./internal/pipeline/github/...
go vet ./internal/pipeline/github/...
go test ./...
go test -race ./internal/pipeline/github/...
```

Tests use loopback HTTP fixtures and synthetic tokens only, including pagination,
identity/content drift, transitive references, duplicate objects, one-shot
admissions and accepted-but-invisible intent/effect lost-response recovery. No
test writes to live GitHub. Contract #5 still requires a live smoke test
before M1 completion; that is not claimed by these unit tests or by this Developer.

Official API references used:

- [REST pagination](https://docs.github.com/en/rest/using-the-rest-api/using-pagination-in-the-rest-api)
- [Native Issue dependencies](https://docs.github.com/en/rest/issues/issue-dependencies)
- [Milestone fields](https://docs.github.com/en/rest/issues/milestones)
- [404 and authentication ambiguity](https://docs.github.com/en/rest/using-the-rest-api/troubleshooting-the-rest-api)
- [Repository settings and permissions](https://docs.github.com/en/rest/repos/repos#get-a-repository)
