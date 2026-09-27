# 从空实现开始的练习项目

此目录提供需求和测试，**故意没有 `Greet` 实现**。它使用独立 Go module，
不会进入分发仓库的 `go test ./...`。目标项目开发测试需要 Go，本工具运行不需要 Go。

复制本目录到新的临时目录；不覆盖已有项目。随后在新目录执行：

```sh
/path/to/projectctl install --project . --agent codex
git init
git add .
git commit -m "Initialize greeting specification and tests"
.project-control/bin/projectctl source add --id REQ-HELLO --kind requirement --path requirements.md --title "Greeting contract"
```

用户确认采用示例规则后，再运行：

```sh
.project-control/bin/projectctl source approve REQ-HELLO --actor human --reason "Approved the example greeting contract"
.project-control/bin/projectctl task create --file task.json
.project-control/bin/projectctl task ready TASK-HELLO
```

然后让 Agent：

> 使用 $github-project-control 完成 TASK-HELLO。读取原始需求，领取任务、实现和提交；
> 交给独立 Agent 做 QA 和 Reviewer。保留逐项证据，无法独立验收时停在交接状态。

这个练习验证安装、冷启动、开发与验收流程。它不构成真实 GitHub 合并或生产交付证明。
