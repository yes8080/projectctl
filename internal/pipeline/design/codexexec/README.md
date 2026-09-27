# Pinned document-only Codex exec adapter

This adapter selects Codex CLI `0.158.0-alpha.2.1`. The default executable is the
ChatGPT application's bundled macOS CLI; another absolute, trusted executable
must report that exact version. `New` checks version without starting a model.
The existing host ChatGPT login is used (`forced_login_method="chatgpt"`); no API
key, alternative provider, login flow or account purchase is performed.
`Config.Model` is mandatory and is always passed as `--model`: no CLI default,
automatic model selection or fallback is allowed. The trusted Controller must
preselect a model visible to the existing entitlement and compatible with this
document-only adapter. A catalog entry is not proof of a successful model run.

Each `Run` creates an empty private temporary workspace, uses a fresh `exec
--ephemeral`, ignores user config and execpolicy rules, disables the shared
daemon, denies approval escalation, and selects `read-only`. It never resumes,
forks, opens a product checkout, or imports a parent transcript. The caller puts
authorized original inputs into stdin. The schema is a duplicate-key-free JSON
object saved outside the empty workspace. No model output file is created.

Known shell, exec, browser, app/plugin, hook, multi-agent, memory and other tool
features are disabled; host skill discovery and project instructions are skipped,
MCP config is empty, and web search is disabled. An observed tool/command/file
event fails the run. This is defense in depth, **not** a claim that read-only
means no secret reads or that post-event rejection can undo a tool effect.
The trusted host must isolate credentials, processes, managed configuration,
filesystem/network access and account usage. Host authentication paths/keychain
remain accessible to the trusted CLI. The strict environment allowlist excludes
GitHub/API keys, access tokens, proxy/provider overrides and loader injections.
The deliberately selected `skip_host_skill_discovery` development feature is
acknowledged with the documented `suppress_unstable_features_warning=true`.
Runtime error events are still rejected; they are never filtered from the stream.
Neither model selection nor metadata can cause this adapter to enable
`code_mode`/`code_mode_host` or install an execution host. Models which require
Code Mode remain incompatible with these restrictions.

Only runtime `thread.started`, ordered turn events, a single final
`agent_message`, and `turn.completed` usage produce a result. Model-authored
thread IDs/usage are never trusted. Missing, failed, replayed, ambiguous or tool
events fail closed. The final response must be duplicate-key-free JSON object
data; the design core still strictly decodes its own schema and semantics.

Bounds: 128 KiB prompt/output, 64 KiB schema/stderr, 2 MiB stdout, 4,096 events,
and a required timeout no longer than 30 minutes. Overflow cancels the process;
errors do not include raw stdout/stderr/provider text. Parser failures provide a
fixed `EventError.Reason` and line number while retaining `errors.Is(ErrEvent)`;
they never include unknown event names or model/provider messages. Linux/macOS
uses `waitid(WNOWAIT)` to retain the direct child's PID until its owned process
group has stopped. Every normal/error/cancel/limit path kills and checks the group
using trusted `/bin/ps` PID/PGID/state-only snapshots, then reaps the direct child
and drains pipes. Retaining that PID prevents a late signal targeting a reused
group ID. Only after confirmed shutdown is the private workspace removed.
An inconclusive shutdown returns `ErrContainment` and preserves the workspace;
the host must resolve containment before releasing resources or retrying. The
adapter reaps only its direct child; the host/init reaps descendant zombies.
Hostile `setsid`/`setpgid` escape requires external containment. Other hosts kill
the direct CLI and bound pipe wait; Windows
process-tree termination needs an external Job Object boundary. This is not a
complete Runtime/Sandbox Manager or cost-enforcement implementation.

Offline tests use the Go test binary as a portable fake subprocess, not a shell
script. The real `TestCodexExecIntegration` is skipped unless
`PROJECTCTL_CODEXEXEC_LIVE=1` and an explicitly authorized
`PROJECTCTL_CODEXEXEC_MODEL`. Run it only with explicit authorization and existing
ChatGPT entitlement. Developer verification is not independent Acceptance PASS.

For Issue #7 Round 2 only, the Controller selected `gpt-5.5` after the fixed
binary's bundled catalog and the authenticated catalog showed it as visible with
`tool_mode=null`. It is not a product-global default. The prior implicit-model
path produced a native Code Mode-host-disabled error. Two bounded Round 2
diagnostics retained those failures; final complete `Adapter.Run` proof for the
explicit selection belongs to independent Acceptance, not this Developer run.

References checked alongside the pinned executable's local `exec --help` and
`features list`:

- [Non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode)
- [Configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference)

The flags and event envelope come from the first reference. The second documents
the sandbox/config, tool, environment and forced-auth settings. The additional
`skip_host_skill_discovery` flag was verified in the selected binary's feature list.
