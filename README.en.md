# GitHub Project Control

[Source repository](https://github.com/yes8080/projectctl) · [Download releases](https://github.com/yes8080/projectctl/releases/latest)

Version-aware operations require Git. Remote GitHub synchronization additionally requires an authenticated `gh` CLI. The installed executable itself needs no Go or Python runtime. Try the [hello-go exercise](examples/hello-go/README.md) for a fresh-project workflow.

A reusable agent skill and a local, deterministic project ledger, distributed as a single Go executable with embedded skill resources. Project memory lives in versioned source material, task contracts, and verification evidence, so a fresh agent can resume without the old chat.

[中文文档](README.md) · [Architecture](docs/architecture.md) · [CLI reference](skills/github-project-control/references/cli.md)

## Build or obtain the executable

No language runtime is required to run the binary. Building from source requires Go 1.22 or later and uses only the Go standard library:

```bash
go build -o dist/projectctl ./cmd/projectctl
./dist/projectctl --help
```

Put the executable on PATH, or replace `projectctl` below with its full path. On Windows, use `projectctl.exe`. GitHub synchronization additionally requires authenticated `gh`; task-specific tests require their own project tools.

Build release archives for macOS, Linux, and Windows on amd64 / arm64, plus `SHA256SUMS`:

```bash
go run ./tools/release --version v1.0.0
```

Download prebuilt archives from [GitHub Releases](https://github.com/yes8080/projectctl/releases/latest). See [release instructions](docs/releases.md) for verification and maintainer workflows.

## Install into a project

```bash
projectctl install --project /absolute/project --agent codex --dry-run
projectctl install --project /absolute/project --agent codex
```

Agent options are `codex` (default), `claude`, and `generic`. Repeat `--agent` to install for multiple agents. Skill resources are installed into `.agents/skills/github-project-control`, `.claude/skills/github-project-control`, or `.project-control/skill`, respectively. The installer does not edit `AGENTS.md`, `CLAUDE.md`, Git settings, or CI.

From the target project:

```bash
.project-control/bin/projectctl doctor
.project-control/bin/projectctl status
```

The project root defaults to the current working directory. Use the global `--root /absolute/project` option before the subcommand when needed.

## Use the skill

Ask a supported agent:

> Use `$github-project-control` to inspect this project's design and implementation, register the authoritative sources, and plan the next task with explicit acceptance criteria. Preserve approved requirements and record unresolved business decisions.

After losing a session:

> Use `$github-project-control` in resume mode. Reconstruct the state from the ledger, inspect unfinished attempts and source revisions, and continue the next eligible action.

The workflow supports planning, development, independent review, QA, audit, and status reporting. See the [task contract example](skills/github-project-control/references/contracts.md) and [workflow](skills/github-project-control/references/workflow.md).

## What this release does

- Stores source snapshots, task contracts, execution events, and evidence references.
- Tracks dependencies, leases, submissions, verification, review, acceptance, and integration.
- Builds role-specific context and rebuilds projections from recorded events.
- Provides explicit GitHub synchronization planning and execution commands.

**This is not a persistent autonomous controller.** It contains no model API integration or background scheduler. An agent must invoke the tool. It does not implement a complete code graph, vector retrieval, GitHub App isolation, or a production approval service.

`actor` names are labels, not authenticated identities. Hashes are integrity checks, not signatures. A developer cannot create independent review by changing its actor name. Verification commands run on the local machine, without a sandbox, and must be within the user's authorization. GitHub writes require authorization too.

## Upgrade and uninstall

```bash
projectctl install --project /absolute/project --upgrade
projectctl uninstall --project /absolute/project
```

Uninstall preserves project data and an installation receipt for later reinstallation. Purging requires an external ZIP backup:

```bash
projectctl uninstall --project /absolute/project --purge --backup /external/backups/project-control.zip
```

MIT licensed. See [LICENSE](LICENSE).
