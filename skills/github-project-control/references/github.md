# GitHub 协作与生产边界

## 当前实现

本工具维护本地账本，并提供单向 Issue 投影计划及显式同步入口。它不创建长驻控制器，不提供 GitHub App 身份隔离，不自动配置 rulesets / merge queue / CI，也不保证远端状态与本地事件跨系统原子提交。

在 `.project-control/config.json` 中设置 `github.repository` 为 `owner/name`。`github plan TASK-ID` 在离线状态生成预期 Issue 内容，并不查询远端后判断操作一定是创建还是更新。实际同步需要系统可用的 GitHub CLI `gh` 及对应仓库认证。

仅在已有用户授权下，将 `autonomy.remote_writes` 设置为 `true`，并执行 `github sync TASK-ID --apply`。保留配置中的其他字段；开关本身不构成用户授权。同步会按稳定任务标记查找 Issue，创建缺失项或更新已存在的托管正文块；保留托管块之外的正文，不自动关闭 Issue 或合并 PR。

遇到远端失败先核查远端是否已经写入，不能只凭超时盲目重复创建。同一仓库/任务的同步应串行执行；任务标记不是 GitHub 提供的唯一键，多个控制器并行首次创建仍需要外部协调。

Issue 和 Projects 适合看板与协作入口。关闭 Issue、写一个 PASS 评论或修改状态字段，都不能单独证明符合原始需求。当前版本具体同步对象以计划输出为准，不假定所有 GitHub 对象都实现双向同步。

## 生产部署时的权威划分

| 数据 | 推荐权威来源 |
|---|---|
| 批准的需求、约束、ADR、任务契约 | 受保护 Git 仓库的版本化文件 |
| 验收与业务状态转移 | 受保护的 ledger 分支或独立仓库中的结构化事件 |
| 运行租约、调度队列、重试和幂等键 | PostgreSQL 等持久运行状态存储 |
| Issue / Projects 的显示状态 | 由账本生成的投影 |
| 测试与评审的长期产物 | 带保留策略的证据存储，账本引用 URI 和摘要 |

这张表是演进架构，不是当前可执行文件已经具备的能力。不要让数据库、Issue 与 Git 文件各自独立决定一个任务是否完成。

生产系统还应具备：可恢复队列、任务租约和过期拒绝、API 幂等与对账、签名验证的 webhook inbox、最小权限身份、预算和重试上限、长期证据备份。

## 已核对的 GitHub 限制

- Issue 描述及历史可被修改或移除，有权限的管理员可删除 Issue；它不是不可变账本。[编辑 Issue](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/editing-an-issue)、[删除 Issue](https://docs.github.com/en/issues/tracking-your-work-with-issues/administering-issues/deleting-an-issue)
- Rulesets 支持必需检查、PR 审批、失效审批处理和限制强推，也支持 bypass。生产 Gate 应限定可信 GitHub App 作为检查来源；开发者不能持有最终 Gate 的写入凭据。[Rulesets](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets)
- Merge queue 在与最新目标分支和队列变更组合的快照上运行检查；Actions 需要配置 `merge_group` 触发器。[Merge queue](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)
- GitHub 不自动重投失败 webhook；接收方要持久接收、去重、检测失败并对账。[失败 webhook](https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries)
- Actions 的 schedule 可能在高负载时延迟或丢弃，不能作为数月自治运行的唯一可靠计时器。[Schedule](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#schedule)
- 测试日志与 artifact 有保留期；长期验收证据需要归档，不能只保留短期下载链接。[保留策略](https://docs.github.com/en/organizations/managing-organization-settings/configuring-the-retention-period-for-github-actions-artifacts-and-logs-in-your-organization)

Git 的内容哈希可校验内容，不会自动证明写入者身份、事件真实发生或管理员无法删改。保护权限、备份、独立凭据和必要的签名体系应按真实风险增加。
