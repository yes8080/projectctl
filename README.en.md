# Projectctl

Projectctl is designed as a bounded automated development pipeline. Starting with product documents, development documents, and engineering constraints, it completes and freezes the design, plans the full delivery scope, and coordinates independent agents through implementation, verification, limited rework, merge, and branch cleanup. It then validates the complete delivery and stops.

GitHub is the sole persistent execution ledger. Each executable Issue that changes code owns a fixed development branch, and each implementation PR delivers one Issue. Development, acceptance, and control permissions are isolated. Design gaps and scope changes enter an explicit change process instead of continuously expanding the task queue.

**The product is being redesigned; it is not yet released as a runnable pipeline and has no remotely approved baseline.** The [pipeline design](docs/pipeline-design.md) is the sole target specification. Existing code and installable skills remain as [prototype references](docs/prototype.md); the runtime engine has not been rewritten to implement the new design. Prototype interfaces and compatibility requirements do not constrain the new product.

[中文](README.md) · [Target specification](docs/pipeline-design.md) · [Prototype and source validation](docs/prototype.md)

## Documentation

- [Architecture index](docs/architecture.md): fact ownership, components, and permission boundaries.
- [Workflow index](docs/team-workflow.md): design freeze, execution, acceptance, and cleanup.
- [Bug workflow index](docs/bug-workflow.md): reporting, triage, regression, and delivery gates.
- [Operations index](docs/operations.md): budgets, reconciliation, recovery, and termination.

These pages link to the specification; they do not maintain separate protocols or progress ledgers.

## Inspect the current source

Build the existing prototype with Go 1.22 or later:

```bash
go build -o dist/projectctl ./cmd/projectctl
./dist/projectctl --help
go test ./...
```

These commands validate the current source, not completion of the target pipeline. See the [prototype notes](docs/prototype.md) for installation and CLI usage. [MIT licensed](LICENSE).
