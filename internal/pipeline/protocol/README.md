# Pipeline protocol v1

This package is a pure protocol boundary, not a ledger, GitHub client, Controller,
Agent runner or acceptance gate. Production code performs no I/O. GitHub remains
the only persistent execution fact source; callers re-read native facts and rebuild
the ephemeral values here. No prototype package or compatibility ledger is used.

## Wire records

`projectctl.record/v1` uses common **top-level** fields `schema`, `kind`,
`project`, `subject`, `operation_id`, and `version`, followed by the exact fields
of one supported kind: `baseline`, `plan`, `contract`, `run`, `evidence`,
`acceptance`, `delivery`, or `approval`. `version` is a positive record revision;
the schema suffix is the wire version. Changes to incompatible wire shapes need
a new schema version. Unknown kinds and fields are never silently accepted.

`Decode` and `Encode` validate the closed, typed union. Missing required fields,
null fields, duplicate keys at any depth (including escaped key aliases),
case-insensitive field aliases, trailing JSON and unknown nested fields fail.
Optional fields use `omitempty`; they may be absent, not null. Every record pins
its relevant inputs through typed references. Contracts contain no native
Milestone, dependency, progress or delivery fields, including a second subject.

`DecodeComment` requires one leading `<!-- projectctl:record:v1 -->` marker,
followed only by a JSON record and optional whitespace. Raw comment hashes include
the marker and **all** whitespace exactly as GitHub returned it. The package
limits JSON/comment input to 256 KiB and nesting to 64; the Gateway must also apply
GitHub endpoint-specific publication limits.

### Current bootstrap records

The selected Issue #4 v2 contract is supported **without changing its existing
wire shape**. `testdata/issue-4-contract-v2.json` reproduces the authoritative
JSON from comment `5858260000`; tests verify the exact raw body digest
`0d514a4ddcd5d54c8ff5b2eb3a10c6a7b4779013f9bea5c5d9115ccb40808395`
and canonical digest
`7992cb33e1db6df9c78819ad984610d960a7b60a88d647c25d112d654c0ddcdd`
selected by M1 candidate v4 / freeze comment `5858335757`.

Its `design_baseline.approval` URL and `parent_revision` are historical navigation
and content pins, **not** verified authorization capabilities. Resolving these
references requires native Gateway observations and policy checks. Legacy checks
without `id` remain readable; the later planner must establish unambiguous check
identity before execution.

The manual bootstrap `plan_candidate` and `plan_freeze` kinds are not silently
reinterpreted as product `plan` / `approval` records. They currently fail with an
unsupported-kind error. They remain authoritative bootstrap facts for this
development cycle, not target-product authorization proofs. A later explicit,
reviewed adapter or approved product records with full native references are
required before product activation; do not rename the kind, drop unknown fields,
or trust `authorized_by` text. This is not a migration of the old prototype ledger.

## Canonical digest dialect

`CanonicalJSON` is a documented restricted dialect, **not RFC 8785**:

- Input is valid UTF-8; lone UTF-16 surrogate escapes are rejected.
- Object keys sort recursively by UTF-8 bytes; array order is preserved.
- Strings have no Unicode normalization. Non-ASCII, `<`, `>`, `&`, `/`, U+2028 and
  U+2029 remain literal UTF-8. Quotes/backslashes and controls are JSON-escaped;
  the short escapes are used where available, other controls use lowercase hex.
- Numbers are signed 64-bit integers. Fractions, exponents, overflow and `-0`
  fail. Costs use integer minor units, not floats.
- Output is compact UTF-8 with no trailing newline. Canonical SHA-256 is lowercase
  hex. Missing values, null (in generic JSON), and array order remain distinct.

`SHA256` hashes exact bytes. `DigestJSON` hashes canonical JSON but **does not
validate a record schema**; use `Decode` / `Encode` at the record boundary.
`testdata/canonical-v1.json` provides independently hashed golden vectors.

## Identity and approval trust boundary

`Reference` pins GitHub object kind, stable database/node IDs, exact repository
URL, raw/canonical digests and native author identity. Only github.com issue
comments and PR reviews are supported in v1; Git objects use full SHA-1 IDs plus
repository-relative paths and SHA-256 content digests. New host/object/hash
support must be explicit, not guessed from an arbitrary URL.

The authenticated Gateway, **not an Agent JSON payload**, constructs
`GitHubObservation` from a complete native API response. `VerifyGitHubRecord`
checks it against an already pinned reference, including publication location and
content. It cannot independently prove network provenance, credential isolation,
or a malicious in-process caller's honesty. Supplying untrusted data to that
trusted boundary is a caller bug; this package does not invent authentication.

The resulting `VerifiedRecord` is sealed, non-serializable, and returns defensive
copies. A zero value cannot authorize anything. Policy is independently supplied
from a trusted, pinned policy source. Grants match stable native ID, node ID and
user/bot type, not display login or an `actor` string. `RequireIndependent`
rejects the same native author even if Agent labels differ; run/instance and
credential isolation remain additional Runtime/Gateway requirements.

`SelectApproved` requires a verified `APPROVED` record signed by a policy-granted
Controller and bound to the current exact policy digest. It selects only the
candidate whose full reference/digests match, with identical project and subject.
A newer unapproved candidate cannot replace it. Missing/edited/recreated objects,
unverified inputs and conflicting operation IDs fail closed. Repeated observations
of the same native object are idempotent; separate objects with the same operation
ID are blocked even when their JSON is identical. The Controller must supply the
authoritative approval
after resolving explicit supersession and conflicting decisions; this function
does not choose a newest approval, perform dispatch, validate native topology,
resolve all referenced evidence, or claim end-to-end acceptance.

## Offline checks

```
go test ./internal/pipeline/protocol/...
go vet ./internal/pipeline/protocol/...
```

Tests use local immutable fixtures, no network, credentials or live provider calls.
They are developer verification, not independent Acceptance PASS.
