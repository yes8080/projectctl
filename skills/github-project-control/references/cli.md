# 本地 CLI 参考

使用 Go 单可执行文件，无语言运行时依赖。以下示例在目标项目根目录运行，Windows 使用 `projectctl.exe`。全局参数 `--root`、`--worktree` 都放在子命令之前。

```bash
.project-control/bin/projectctl --root /absolute/project status
```

`--root` 默认当前工作目录，指定唯一账本及规范原文位置。`--worktree` 默认使用 root；显式指定时，只改变 Git/代码检查和验证命令的工作目录。两者必须共享同一个 Git common directory，工具会校验。每次调用都需要显式传入所需工作区，不会改变后续调用的默认值。

```bash
projectctl --root /central/project --worktree /worktrees/task-001 context TASK-001 --role developer
projectctl --root /central/project --worktree /worktrees/qa-001 verify TASK-001 --run RUN_ID --check CHECK-ORDERS --actor qa --execute
```

示例 worktree 须事先由 Runner 或用户准备；QA 工作区必须 checkout 到任务已提交的代码版本。多个任务可用不同本机 worktree 和同一个 root，不能复制账本到每个分支。普通 clone 即使指向同一远端也不满足 Git common directory 检查。工作区分离不是沙箱，也不是跨机器分布式协调。

每次写入前后以命令返回的真实状态为准；具体校验和错误信息以 `--help` 和当前运行时为准。

| 用途 | 命令 |
|---|---|
| 状态 | `status` |
| 完整性诊断 | `doctor` |
| 从事件恢复投影 | `rebuild` |
| 注册原文 | `source add --id ID --kind requirement\|constraint\|adr\|design --path PATH --title TITLE [--scope SCOPE] [--approve --actor ACTOR]` |
| 批准当前来源版本 | `source approve ID --actor ACTOR --reason REASON` |
| 创建任务 | `task create --file task.json [--actor ACTOR]` |
| 修订任务契约 | `task revise --file task.json --reason REASON [--actor ACTOR]` |
| 查看任务 | `task show ID` |
| 标记可执行 | `task ready ID` |
| 下一候选 | `next` |
| 领取 attempt | `task claim ID --actor ACTOR [--lease-seconds 3600]` |
| 续租 | `task heartbeat ID --run RUN_ID [--lease-seconds 3600]` |
| 提交代码版本 | `task submit ID --run RUN_ID --head FULL_SHA [--pr URL]` |
| 阻塞任务 | `task block ID --reason REASON` |
| 恢复到 PROPOSED 再规划 | `task retry ID --reason REASON` |
| 构建角色上下文 | `context ID --role developer\|reviewer\|qa\|auditor [--max-chars N]` |
| 执行验证 | `verify ID --run RUN_ID --check CHECK_ID --actor qa --execute [--timeout 300]` |
| 记录独立 review | `review ID --file review.json` |
| 检查验收前提 | `accept ID --actor ACTOR` |
| 记录集成关闭 | `close ID --merged-sha FULL_SHA --actor ACTOR [--pr NUMBER]` |
| 记录待决问题 | `decision create --file decision.json` |
| 解决决策 | `decision resolve ID --actor ACTOR --resolution TEXT` |
| 账本审计 | `audit` |
| 预览 GitHub 同步 | `github plan ID` |
| 执行 GitHub 同步 | `github sync ID --apply` |

`verify --execute` 直接在选定 worktree（未指定则是 root）启动任务 JSON 中指定的 argv，将退出码及日志保存到 root 的账本和证据目录。它不是测试沙箱，不保证命令无副作用；argv 数组也不是安全边界。先确认用户已授权相应测试行为。`--execute` 是显式本机执行开关，不能代替用户授权。

上下文过期会显示在 `status` 的任务 `problems` 中，并使有关命令返回 stale 错误；它不是单独存储的 `STALE` 状态。处理原因后，通过 `task retry` / `task revise` 返回 `PROPOSED` 再准备执行。

`github plan` 离线生成预期 Issue 内容；实际执行需要配置 `github.repository`、可用且已认证的 `gh`、配置 `autonomy.remote_writes: true` 与 `github sync --apply`。只有在用户已有授权时开启远端写入。同步不自动设置分支保护、创建可信 CI、完成 review 或合并。详见 [GitHub 同步](github.md)。

## 安装与卸载

使用发布的可执行文件运行（`projectctl` 须在 PATH，或换成完整路径）：

```bash
projectctl install --project /absolute/project --agent codex --dry-run
projectctl install --project /absolute/project --agent codex
projectctl install --project /absolute/project --agent codex --agent claude --upgrade
projectctl install --project /absolute/project --agent generic
projectctl install --project /absolute/project --upgrade
```

`--agent` 可重复，默认 `codex`。可执行文件内嵌 skill 及引用资料，安装不需要单独下载资源。安装器不修改 `AGENTS.md`、`CLAUDE.md`、Git 设置或 CI。已有安装的替换按安装器检查执行；升级前保存项目数据和自定义内容。

```bash
projectctl uninstall --project /absolute/project
projectctl uninstall --project /absolute/project --purge --backup /external/backups/project-control.zip
```

默认卸载仅移除安装可执行文件和 skill，保留项目数据和用于重新安装的收据。`--purge` 要求提供外部 ZIP 备份位置；彻底删除数据前检查备份结果。卸载不会撤销已发生的 GitHub 操作。
