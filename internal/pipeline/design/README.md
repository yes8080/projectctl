# Bounded design and exact baseline selection

Issue #7 supplies a design core and the one preselected Codex exec adapter. It
does not implement product development, planning, a second provider, a scheduler,
GitHub writes, merging or an independently writable local execution ledger.

## Data and authority

`Document` and `Review` are strictly decoded artifact DTOs, not a new persistent
fact protocol. Every field is required, including empty `open_questions`; null,
duplicate, unknown and case-alias fields are rejected. A design must name original
requirements and explicitly address boundaries, workflows, architecture,
contracts, data, permissions, dependencies, technology, environments, tests,
integration, migration, risks, completion and rollback. Explicitly saying that
migration is unnecessary is valid; omitting the decision is not. The checks prove
structure and binding, not that model prose is correct. Independent review and
explicit approval remain required. Unresolved design questions prevent freezing.

All durable facts use the existing protocol: GitHub Run reservations, designer
Evidence, design-review Acceptance, Controller Approval and finally Baseline.
Candidate content is an exact repository `SourceReference`; its raw SHA-256 is
`Binding.DesignSHA256`. A `design-candidate` Evidence uses that content as its
log/artifact digest. Each required design section, plus requirements, has a PASS
assessment referencing that exact evidence. Approval selects the precise PASS
Acceptance, which already binds the candidate; the later Baseline references
the Acceptance and Approval. There is no cyclic Baseline/Approval self-reference.
`SelectApproved` now permits PASS Acceptance alongside its existing kinds, but
does not by itself prove independent review or a valid merge.

The startup anchor and contract pin come from the trusted Controller's approved
GitHub chain, not model output or a latest-comment heuristic. The design core
re-runs startup preflight, reads the manifest, contract and every original input,
and verifies stable repository/Issue identity and the exact default-branch SHA.
Policy grants are deterministically derived from the pinned startup roles and
manifest version; `Binding.PolicySHA256` is that protocol Policy's digest.

## Fresh bounded execution

`Admit` needs a trusted `FreshRunSource` that first publishes a newly authorized
Run reservation to the design Issue, with the exact input digest. The core reads
the complete Issue comment collection and re-fetches native verified records.
Unknown/malformed protocol records, conflicting operation or instance IDs,
non-contiguous/duplicate attempts, wrong author/role/input and excess reservations
block. Pending or abandoned reservations still count; constructing a new Engine
does not restore consumed budget. All design and review attempts count against
the finite total-run limit, with a separate bound on each role's rounds.

The wall-time origin is the native creation time of the project-control Issue,
not a local restart timestamp. Missing or future native time blocks. Runtime
timeout is the smaller of the reserved per-run allowance and remaining cycle
wall time; Codex exec has an additional 30-minute safety ceiling. Expired and late
results cannot become receipts. Scope/records/branch/input drift is checked again
after execution. A receipt is disposable output, not evidence until the trusted
publisher verifies and publishes the corresponding protocol records to GitHub.

An opaque admission is nonserializable and consumed before checking or dispatching.
Copies share one atomic flag (tested with 100 concurrent-copy scenarios). Failure,
cancellation or drift does not refund it. The fresh source must consume a genuinely
new Controller allocation event across restarts, and the Controller must serialize
publication and fence revoked workers. Replaying an old Run through a permissive
callback is not recovery. Those cross-process guarantees are trusted integration
boundaries, not local-file counters or distributed locks implemented here.

The author receives original inputs. The reviewer gets original inputs and exact
candidate bytes, never an author thread, conversation transcript or summary.
Run allocation IDs must differ; the concrete runtime independently creates fresh
ephemeral CLI processes and captures native `thread.started` identity. Receipt
links the host-assigned Run instance to that runtime thread. Persisting this
binding and publishing with separate native designer/reviewer identities is the
trusted publisher's responsibility. A runtime injected by the host is trusted;
an Agent must not supply a fake Runtime or Preflight implementation.

## Exact merged baseline gate

`Freeze` reads all facts twice and rejects drift. It validates native policy
authors and independence, exact Approval → Acceptance selection, PASS with no
unresolved finding, full section coverage, evidence/run links, differing designer
and reviewer instances, and identical head/base/design/contract/policy bindings.
It reads a same-repository non-draft merged PR, requires the configured default
target branch, exact reviewed head/base, and current target SHA equal to the merge
commit. Head and merge trees must match and the design bytes must be identical at
both commits. Auto-closed design Issues are permitted for this merge check only.
Unapproved candidate revisions, missing/edited records, moved targets, recreated
native objects or different trees block. The result is an existing typed
`protocol.Baseline`, not an already-published or accepted baseline.

These reads are not a GitHub atomic snapshot or merge authorization. The serialized
publisher must revalidate immediately before publication. Supersession/activation
of the authoritative approval is a Controller decision; the caller cannot choose
an obsolete approval to bypass a newer active revision. No GitHub write is hidden
inside this API. Native Git/PR reads were minimally added to the shared Gateway;
they do not independently prove approval, ancestry or Issue closing association.

## Validation and current live limitation

```text
go test ./internal/pipeline/design/...
go test ./internal/pipeline/design/... -run TestCodexExecIntegration
go test ./...
go vet ./...
go test -race ./...
```

Ordinary tests never call a model; the named integration skips without explicit
`PROJECTCTL_CODEXEXEC_LIVE=1`. The adapter supports only the prevalidated executable
version and existing ChatGPT host entitlement, not API-key fallback. See
[codexexec/README.md](codexexec/README.md) for exact sandbox, credential, cancellation
and cost-enforcement boundaries. Host read-only access is not proof that secrets
are inaccessible; publication credentials and untrusted execution must remain
separated by the managed host.

Two explicitly authorized real integration invocations failed. Invocation 1
failed with `ErrEvent` after a successful CLI exit; its raw event shape was not
retained. Fixed non-content diagnostic codes were then added without broadening
allowed events. Invocation 2 failed with `item_before_turn at line 2` (7.65
seconds), proving an item event preceded the expected turn start, but not which
item subtype or content. The exact compatibility cause remains **unproven**; no
event was silently ignored or newly allowed. No third model invocation occurred,
because there was not enough captured safe shape to establish the required
deterministic regression and justify a parser change. Real adapter acceptance
remains **BLOCKED**. Offline test success and local Developer checks are not
Acceptance PASS or an end-to-end live design/review demonstration.
