# GitHub Project Control

让 Coding Agent 从项目资料、任务契约和证据恢复工作，而不是从聊天历史猜测项目状态。

本仓库提供可安装的 `github-project-control` skill，以及 Go 实现的本地账本 CLI。发布产物是包含 skill 资源的单个可执行文件，运行不依赖 Go、Python 或其他语言运行时。可用于 Codex、Claude Code 或能够读取 Markdown 指令并调用命令的其他 Agent。

[English quickstart](README.en.md) · [架构与边界](docs/architecture.md) · [命令参考](skills/github-project-control/references/cli.md) · [任务契约](skills/github-project-control/references/contracts.md)

## 已提供的能力

- 注册需求、约束、ADR 和设计原文，保存来源快照及内容摘要。
- 创建有依赖、范围、AC 和验证命令的任务，生成按角色区分的上下文。
- 记录领取、租约、提交、测试、review、验收与集成过程。
- 使用事件文件与哈希链发现记录损坏，从事件重建状态投影。
- 提供 GitHub Issue 的单向投影计划及显式同步入口。
- 安装/升级/卸载项目运行时和 skill；默认卸载保留数据。

**当前版本是由 Agent 调用的本地工具，不是无人值守服务。** 它没有模型 API、常驻调度器、向量检索、完整代码图、GitHub App 隔离或生产级外部验收服务。安装后仍由当前 Agent 根据 skill 规划和执行。生产演进方案与当前能力在[架构文档](docs/architecture.md)中分别说明。

## 获取可执行文件

从源码构建需要 Go 1.22 或更新版本，无外部 Go 依赖：

```bash
go build -o dist/projectctl ./cmd/projectctl
./dist/projectctl --help
```

将构建或解压后的 `projectctl` 放入 PATH，或在下文用它的完整路径替换 `projectctl`。Windows 使用 `projectctl.exe`。涉及代码版本的操作需要 `git`；GitHub 同步另外需要已安装并认证的 `gh`；任务自己的测试命令还需要目标项目的开发环境。

维护者可构建 macOS、Linux、Windows 的 amd64 / arm64 发布包及 `SHA256SUMS`：

```bash
go run ./tools/release --version v1.0.0
```

源码仓库：[yes8080/projectctl](https://github.com/yes8080/projectctl)。预编译安装包见 [GitHub Releases](https://github.com/yes8080/projectctl/releases/latest)，选择对应系统与 CPU 架构的 ZIP。发布与校验方法见[发布说明](docs/releases.md)。

## 安装到项目

执行可执行文件，将 `/absolute/project` 替换为目标项目：

```bash
projectctl install --project /absolute/project --agent codex --dry-run
projectctl install --project /absolute/project --agent codex
```

`--agent` 支持 `codex`、`claude`、`generic`，可以重复；默认 `codex`。

```bash
projectctl install --project /absolute/project --agent codex --agent claude --upgrade
```

安装器把 CLI 放到 `.project-control/bin/projectctl`（Windows 为 `projectctl.exe`），并从可执行文件内嵌资源安装 skill：

| Agent | Skill 路径 |
|---|---|
| Codex | `.agents/skills/github-project-control/` |
| Claude Code | `.claude/skills/github-project-control/` |
| Generic | `.project-control/skill/` |

它不自动修改 `AGENTS.md`、`CLAUDE.md`、Git 配置或 CI。

在目标项目运行：

```bash
.project-control/bin/projectctl doctor
.project-control/bin/projectctl status
```

CLI 默认以当前目录为项目根目录；跨目录运行使用全局 `--root`：

```bash
/absolute/project/.project-control/bin/projectctl --root /absolute/project status
```

## 开始使用

在支持 skill 的 Agent 中请求：

> 使用 `$github-project-control`，先检查当前项目设计和实现，建立来源与任务映射，再选择一个可以独立验收的任务。沿用已有批准的需求；缺失的重大业务规则形成待决项。

会话中断后请求：

> 使用 `$github-project-control` 的 resume 流程，从项目账本恢复当前状态，检查未完成 attempt、来源版本和证据，再继续下一项合法动作。

其他 Agent 可读取 `.project-control/skill/SKILL.md` 及引用资料。模式包括 `plan`、`next`、`develop`、`review`、`qa`、`audit`、`resume` 和 `status`。这些是 Agent 工作模式，不是全部对应同名 CLI 子命令。

需要同步 GitHub 时，配置 `.project-control/config.json` 的 `github.repository`，准备已认证的 `gh`，先执行 `github plan TASK-ID`。远端写入默认关闭，已有用户授权后才开启 `autonomy.remote_writes` 并使用 `github sync TASK-ID --apply`。详见 [GitHub 同步说明](skills/github-project-control/references/github.md)。

## 最小工作流

从空实现开始可复制 [hello-go 练习项目](examples/hello-go/README.md)，它提供原始需求、任务契约和验收测试，让新 Agent 实现一个小功能。

先准备项目真实的设计文件与任务 JSON；格式见[任务契约](skills/github-project-control/references/contracts.md)。

```bash
.project-control/bin/projectctl source add --id REQ-001 --kind requirement --path docs/requirements.md --title "需求基线"
.project-control/bin/projectctl source approve REQ-001 --actor human --reason "用户已确认该需求版本"
.project-control/bin/projectctl task create --file task.json
.project-control/bin/projectctl task ready TASK-001
.project-control/bin/projectctl next
.project-control/bin/projectctl task claim TASK-001 --actor developer-a --lease-seconds 1800
.project-control/bin/projectctl context TASK-001 --role developer
```

`source approve` 只有在相应内容已获批准时才应执行。复制示例命令不会生成真实的人类批准。保存领取返回的 run ID；实现后提交真实代码 SHA，再由独立 QA/Reviewer 记录结果。

```bash
.project-control/bin/projectctl task submit TASK-001 --run RUN_ID --head FULL_COMMIT_SHA
.project-control/bin/projectctl context TASK-001 --role reviewer
.project-control/bin/projectctl verify TASK-001 --run RUN_ID --check CHECK_ID --actor qa --execute
.project-control/bin/projectctl review TASK-001 --file review.json
.project-control/bin/projectctl accept TASK-001 --actor controller
```

上例中的 ID 与 SHA 都是占位符，须使用当前任务真实输出。开发者自检不算独立验收，`accept` 成功将任务变为 `ACCEPTED_FOR_MERGE`，不等于代码已合并或已上线。集成后使用 `close` 核对版本并记录 `DONE`；没有 PR 证明时只确认本地集成。

## 使用边界

- **执行权限**：`verify --execute` 在本机执行任务中的 argv，不是沙箱。确认命令及副作用在已有授权范围内，执行开关不能代替用户授权。
- **身份**：`actor` 是标签，不是强身份。换名字不能让同一个开发 Agent 成为独立 Reviewer。
- **完整性**：哈希链用于发现内容变化，不是数字签名或不可篡改存储。需要保护、备份原始账本和证据。
- **验收**：工具检查记录结构和状态条件，不能证明测试充分、自然语言结论正确或业务目标已经实现。
- **GitHub**：Issue / Projects 是协作视图；不会因关闭 Issue 自动获得可信验收。外部写入须有用户授权。

## 升级、恢复与卸载

```bash
projectctl install --project /absolute/project --upgrade
/absolute/project/.project-control/bin/projectctl --root /absolute/project rebuild
projectctl uninstall --project /absolute/project
```

默认卸载保留项目数据和安装收据，以支持后续重新安装恢复。彻底删除必须提供外部 ZIP 备份路径：

```bash
projectctl uninstall --project /absolute/project --purge --backup /external/backups/project-control.zip
```

查看[运维说明](docs/operations.md)了解恢复、备份和故障处理。项目采用 [MIT License](LICENSE)。
