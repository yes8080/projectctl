# 来源、任务与验收契约

## 原文是依据，摘要是索引

为每个来源分配稳定 ID。`requirement` 表示需求，`constraint` 表示跨任务约束，`adr` 表示架构决策依据，`design` 表示设计说明。保留原文文件与精确快照的哈希；来源说明和任务上下文不能替代原文。

使用全局 `--worktree` 时，来源路径、快照和任务账本仍由 `--root` 决定；worker 的代码分支不会另建规范权威。需要修改批准依据时回到 central root 的来源变更流程，再让受影响任务重新准备。

```bash
.project-control/bin/projectctl source add --id REQ-001 --kind requirement --path docs/requirements.md --title "订单取消规则"
.project-control/bin/projectctl source approve REQ-001 --actor human --reason "已核对产品范围与验收条件"
```

仅当用户已明确批准对应内容时使用 `--approve --actor human` 或 `source approve`。`human` 是声明标签，工具不能证明用户真的批准了；Agent 必须保留批准依据。来源文件变更会使原批准失去对当前内容的覆盖，必须重新审阅。

约束应写出：适用范围、不可破坏的规则、原因、检查方法、允许例外的批准流程。例如“已结算订单不得物理删除”，应对应数据访问检查和回归测试，而不只是提示词。所有来源类型均支持 `scope`；constraint 的 scope 为空表示全局约束，非空时与任务 scope 有交集便自动注入。

新增适用约束或原文变更会使旧上下文失效。处理批准与任务契约后，使用 `task retry` 或 `task revise` 回到规划流程，再次 `task ready`；不要跳过 stale 检查。

## 任务 JSON

```json
{
  "id": "TASK-001",
  "title": "实现订单取消权限校验",
  "goal": "允许订单所属区域的运营人员取消待确认订单",
  "requirements": ["REQ-001"],
  "constraints": ["CON-001"],
  "decisions": [],
  "sources": [],
  "scope": ["orders"],
  "dependencies": [],
  "acceptance": [
    {"id": "AC-001", "description": "同区域运营人员可以取消待确认订单"},
    {"id": "AC-002", "description": "跨区域请求被拒绝且不修改订单状态"}
  ],
  "verification": [
    {"id": "CHECK-ORDERS", "argv": ["go", "test", "./internal/orders", "-run", "TestCancelPermission"]}
  ],
  "priority": 10,
  "non_goals": ["退款和结算流程"],
  "files": ["internal/orders/permissions.go", "internal/orders/permissions_test.go"]
}
```

示例路径和命令须替换成目标项目真实存在的内容，不能直接当作已实现的能力。来源、任务、AC、check 和 decision 等 ID 使用大写稳定标识，例如 `REQ-001`、`TASK-001`、`AC-001`、`CHECK-ORDERS`、`DEC-001`。创建任务前确认：

- 每项 AC 可被检查，并覆盖拒绝路径、边界条件和相关权限范围。
- 依赖、原始需求与约束有稳定 ID；任务范围足够小，能在一次独立 review 内判断。
- `files` 是初始定位线索，不是所有影响面已被发现的保证。
- `verification.argv` 是参数数组，不是 shell 字符串。它仍可调用具有副作用的程序，须在授权范围内执行。
- 优先级数值越小越优先；依赖、批准与阻塞条件优先于排序。

## 运行、测试与 review 的绑定

所有结果绑定同一任务 attempt、代码 SHA 与上下文包。开发者继续提交代码、规范发生变化或上下文依据变化时，旧结果不能自动复用为新快照的 PASS。

Review 数据应包含 `actor`、`run_id`、`head_sha`、`package_digest`、逐项 `criteria`、总 `decision` 与 `reason`。`criteria` 是按 AC ID 索引的对象，必须包含所有 AC；其证据 ID 必须来自当前 run 的验证记录。例如：

```json
{
  "actor": "reviewer-b",
  "run_id": "<当前 run ID>",
  "head_sha": "<当前已提交完整 SHA>",
  "package_digest": "<当前 context 摘要>",
  "criteria": {
    "AC-001": {
      "result": "PASS",
      "reason": "同区域待确认订单取消后状态正确，已核对实现与实际测试",
      "evidence": ["<当前 run 产生的证据 ID>"]
    },
    "AC-002": {
      "result": "PASS",
      "reason": "跨区域请求被拒绝，数据库记录保持不变",
      "evidence": ["<当前 run 产生的证据 ID>"]
    }
  },
  "decision": "PASS",
  "reason": "所有 AC 均有当前代码版本上的验证证据"
}
```

从当前 `task show` / `context` / `verify` 输出获取实际 ID 和摘要，不自行猜测或复制旧值。上例是格式示意，不可复制为未经执行的验收结论。`PASS` 代表对应 AC 已有充分证据，不能用“看起来没问题”批量填充。

工具检查结构、绑定关系和配置的门槛；它不会判断业务测试是否充分、Reviewer 是否真的独立、自然语言理由是否真实。这些是 Agent 流程和生产权限体系需要补齐的部分。

## 待决问题 JSON

`decision create --file decision.json` 的三个必填字段是 `id`、`question`、`options`：

```json
{
  "id": "DEC-001",
  "question": "REQ-001 与当前结算接口冲突：已结算订单是否允许业务取消？影响 TASK-001。",
  "options": [
    "拒绝取消，继续保持已结算状态",
    "允许提交冲正申请，由另一个经批准的流程处理"
  ]
}
```

选择方案后由有相应授权的行动者执行：

```bash
.project-control/bin/projectctl decision resolve DEC-001 --actor human --resolution "保持结算状态；若用户需要冲正，另行设计并批准流程"
```

上例不替代真实授权。工具保存 resolution 与 resolved_by；已解决 decision 不可重写。后续需要推翻原决定时新建 decision，在 question 中引用被替代 ID、原因和影响范围；当前没有专门的 `supersedes` 字段。

## Context Package

开发包至少提供目标、scope / non-goals、依赖、规范引用、自动注入约束、AC、文件线索、验证命令和当前 attempt 信息。评审包基于相同规范与代码版本，但不继承开发者自评结论。

`--max-chars` 是字符预算，不是 tokenizer 精确 token 数。预算不足不能成为丢弃硬约束的理由：缩小任务，扩大已授权预算，或分次读取原文。当前工具不提供完整的 AST / LSP 代码图或语义检索；调用关系、运行时影响和遗漏约束仍需 Agent 检查。
