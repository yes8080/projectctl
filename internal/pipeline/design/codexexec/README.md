# Pinned document-only Codex exec adapter

This adapter selects Codex CLI `0.158.0-alpha.2.1`. The default executable is the
ChatGPT application's bundled macOS CLI; another absolute, trusted executable
must report that exact version. `New` checks version without starting a model.
The existing host ChatGPT login is used (`forced_login_method="chatgpt"`); no API
key, alternative provider, login flow or account purchase is performed.

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

Only runtime `thread.started`, ordered turn events, a single final
`agent_message`, and `turn.completed` usage produce a result. Model-authored
thread IDs/usage are never trusted. Missing, failed, replayed, ambiguous or tool
events fail closed. The final response must be duplicate-key-free JSON object
data; the design core still strictly decodes its own schema and semantics.

Bounds: 128 KiB prompt/output, 64 KiB schema/stderr, 2 MiB stdout, 4,096 events,
and a required timeout no longer than 30 minutes. Overflow cancels the process;
errors do not include raw stdout/stderr/provider text. Parser failures provide a
fixed `EventError.Reason` and line number while retaining `errors.Is(ErrEvent)`;
they never include unknown event names or model/provider messages. Temporary directories are
removed on success/failure/cancellation. Linux/macOS cancellation kills the owned
process group. Other hosts kill the direct CLI and bound pipe wait; Windows
process-tree termination needs an external Job Object boundary. This is not a
complete Runtime/Sandbox Manager or cost-enforcement implementation.

Offline tests use the Go test binary as a portable fake subprocess, not a shell
script. The real `TestCodexExecIntegration` is skipped unless
`PROJECTCTL_CODEXEXEC_LIVE=1`; optionally select the already-authorized model with
`PROJECTCTL_CODEXEXEC_MODEL`. Run it only with explicit authorization and existing
ChatGPT entitlement. Developer verification is not independent Acceptance PASS.

References checked alongside the pinned executable's local `exec --help` and
`features list`:

- [Non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode)
- [Configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference)

The flags and event envelope come from the first reference. The second documents
the sandbox/config, tool, environment and forced-auth settings. The additional
`skip_host_skill_discovery` flag was verified in the selected binary's feature list.
