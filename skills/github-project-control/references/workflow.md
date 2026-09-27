# 执行、验收与冷启动

## 一个任务的正常路径

1. `doctor` / `status`：检查账本和项目状态。分离代码工作区时，所有相关调用使用相同的 `--root` 和各自的 `--worktree`。
2. `task create --file ...`：保存任务契约，核查原文与来源批准。
3. `task ready ID` / `next`：确认任务可领取；按依赖、风险和优先级选择。
4. `task claim ID --actor developer-a --lease-seconds 1800`：建立 attempt，保存返回的 `run_id`。
5. `context ID --role developer`：读取当前契约和引用原文，确认代码基线；在独立工作区实现与自检。
6. 长任务在租约失效前执行 `task heartbeat`；`lease_until` 是 Unix 秒时间戳。失联恢复后重新核查状态，不能由陈旧 worker 继续提交。
7. `task submit ID --run RUN_ID --head FULL_SHA`：绑定完成实现的精确提交，可附带 `--pr URL`。
8. 独立 QA/Reviewer 获得对应角色的包，在用户已授权的测试范围内运行 `verify ... --execute` 并写入 `review`。
9. `accept ID --actor controller`：由工具检查已记录前提，转为 `ACCEPTED_FOR_MERGE`；失败时保留证据，修复或交接。
10. 经授权完成集成后 `close ID --merged-sha FULL_SHA --actor controller`，可附 `--pr NUMBER` 核查远端合并事实；任务成为 `DONE` 后选择下一个 Ready 任务。

不要把 `claim`、`submit` 或 `accept` 当作实际的代码执行、PR 创建、Git 合并或部署操作；执行记录与外部事实需一致。`close` 检查目标 SHA 是选定 worktree 的当前 HEAD，且排除 `.project-control` 后的代码树与受审版本相同；否则须重新提交新的 head 并验收。提供 PR 时还检查远端为 MERGED 且 merge commit 匹配；无 PR 只记录本地集成，不能宣称远程已合并或发布。

## 本机多个 Agent 与唯一账本

Runner 先准备同一个 Git 仓库的工作区，例如：`/central/project` 保存唯一账本和原文，`/worktrees/task-001` 用于开发，`/worktrees/qa-001` 用于检查指定提交。两份 worktree 必须与 central project 共享同一个 Git common directory，不能只是同一远端的独立 clone。

```bash
projectctl --root /central/project --worktree /worktrees/task-001 task claim TASK-001 --actor developer-a --lease-seconds 1800
projectctl --root /central/project --worktree /worktrees/task-001 context TASK-001 --role developer
projectctl --root /central/project --worktree /worktrees/task-001 task submit TASK-001 --run RUN_ID --head FULL_SHA
projectctl --root /central/project --worktree /worktrees/qa-001 context TASK-001 --role reviewer
projectctl --root /central/project --worktree /worktrees/qa-001 verify TASK-001 --run RUN_ID --check CHECK-ORDERS --actor qa --execute
projectctl --root /central/project --worktree /worktrees/qa-001 review TASK-001 --file /handoff/review.json
projectctl --root /central/project --worktree /worktrees/qa-001 accept TASK-001 --actor controller
```

ID、SHA 和文件路径要替换成实际值，QA 工作区在验证前须 checkout 到提交的 SHA。集成关闭时同样用 `--worktree` 指定集成后的工作区。来源注册与快照始终相对于 root，不会改为读 worker 分支里的原文。

不同任务可以并行占用不同 worktree；账本更新由本机文件锁串行化，同一任务保留 attempt 与租约保护。不要把 `.project-control` 复制到各分支后各自继续写入，也不要让两个任务同时改同一个代码工作区。

这是可直接执行的本机协作方式。工具不会自动创建工作区、独立测试环境或沙箱；同一用户权限下的进程仍可能访问其他工作区。跨机器分布式多写者不在当前实现范围内。

## Developer

从任务目标和 AC 开始，在改变代码前阅读关联原文。优先找到项目现有实现模式及相关测试。若新发现影响面超出任务 scope，保留发现并判断是否需要拆分；跨模块的重大契约变化要先处理 decision。

在实现后记录：改变的行为、每项 AC 对应的代码和测试、实际运行的命令、结果以及尚存限制。自检只支撑提交给独立评审，不授予最终完成状态。

## Reviewer 与 QA

Reviewer 从需求、约束、AC 和指定代码 SHA 开始。先形成自己的验证点，再查看代码和测试。开发者“已测试”不是执行证据；本地日志也不是完整行为证明。

QA 的验证命令应在已检查出的正确代码快照上运行，独立工作区通过全局 `--worktree` 传入，账本继续使用相同的 `--root`。`verify --execute` 不创建沙箱，也不会替你装好运行环境。检查依赖、外部服务及可能的数据写入；需要隔离时由 Runner 先准备临时工作区/测试环境。测试默认超时 300 秒，可用 `--timeout` 设置经授权的时间预算。

尚未 `submit` 时，context 中 `candidate_submitted` 为 false，代码 SHA 仅是起始基线，不是已完成候选；实际任务状态以 `task show` 为准。包会保留 run ID，交接必须明确哪些实现和测试尚未进行。

不能获得独立 Agent 时，当前开发 Agent 可整理 `context --role reviewer` 及证据供交接，但不能通过改 actor 名称为自己做最终验收。冷启动的新会话有助于上下文隔离，不单独构成凭据隔离或组织意义上的独立性。

## 失败与停止条件

验收失败保留原始记录。使用 `task retry ID --reason ...` 恢复到 `PROPOSED`，重新 `task ready`、`claim` 后开启新 attempt；或拆成关联整改任务，不得覆盖失败证据。重试可用于 BLOCKED、过期 IMPLEMENTING、CHANGES_REQUESTED 或上下文被判定过期的未完成任务。过期表现为 `status` 的 `problems` 或命令的 stale 错误，不是持久化的独立状态。修改契约使用 `task revise --file task.json --reason ...`，不得直接编辑投影。默认最多 3 次 attempt，同时受用户预算、工具配置及更严格的项目规则限制。

若失败根因是缺少业务规则、规范互相冲突、测试环境不可用或权限不足，使用 `task block` 并说明所需行动。不要不断重跑同一失败命令、用假 PASS 消除 blocker，或在没有依据时“选一个合理答案”。

Decision 应描述冲突、受影响需求与任务、可选方案、代价和建议。只有在授权范围内才能解决；批准后先更新规范，再重新构建受影响任务的上下文。

## 无会话恢复

恢复所需材料是仓库、`.project-control` 中的账本/快照/证据及安装运行时，不是聊天历史。

1. 运行 `doctor`，确认原始记录可读取且链条未损坏。
2. 若投影缺失，执行 `rebuild`，随后读取 `status`。
3. 检查未完成 attempt、租约及代码工作区；恢复相同的 root / worktree 配置，避免重复领取正在运行的工作或意外使用另一份账本。
4. 对可继续任务重新生成 context，核对来源、代码 SHA 与证据绑定。
5. 对无法继续的 attempt 记录失败或 blocker，按预算开新 attempt。

遗失原始来源快照或长期证据时，不能靠投影或摘要重新“证明”验收；应报告不可恢复的证据缺口。

## 进度报告

分别报告 Ready、开发中、待验收、验收通过、已集成、失败和阻塞。若给出整体百分比，必须说明分母和权重；任务完成比例不等于产品需求覆盖率，生成更多小任务也不应改变产品完成度。

日报应列出具体任务 ID、最新事实、失败原因、可执行的下一步以及需要人工决策的事项。没有常驻调度器时，不承诺会在会话关闭后继续运行或主动发送日报。
