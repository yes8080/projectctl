# Projectctl

Projectctl 的目标是有边界的自动开发流水线：输入产品文档、开发文档和工程约束，先形成完整开发方案并批准冻结，再一次性规划全部交付切片，由独立 Agent 完成开发、验收、有限整改、合并和分支清理，最后对固定交付目标整体验收并结束。

GitHub 是唯一持久执行账本。每个需要代码变更的执行 Issue 使用固定开发分支，实现 PR 只交付该 Issue；开发、验收和控制权限隔离。开发中遇到设计缺口或范围变化，进入明确的变更流程，不持续扩张任务。

**当前处于全新设计阶段，尚未发布为可运行产品，也未形成远端批准基线。** [流水线设计](docs/pipeline-design.md)是唯一目标规范，定义产品边界、对象、门禁、Bug 流程、恢复及实施里程碑。现有代码和可安装 skill 作为[原型参考](docs/prototype.md)保留，运行引擎尚未按新设计重写；原型接口和兼容需求不约束新产品。

[English](README.en.md) · [唯一目标规范](docs/pipeline-design.md) · [原型与源码验证](docs/prototype.md)

## 阅读入口

- [架构索引](docs/architecture.md)：事实归属、组件与权限边界。
- [工作流索引](docs/team-workflow.md)：设计冻结、执行、验收和收尾。
- [Bug 流程索引](docs/bug-workflow.md)：记录、分诊、回归与交付阻断。
- [运行与恢复索引](docs/operations.md)：预算、对账、暂停和终止。

这些入口只导航到主规范，不各自维护一套协议或进度表。

## 查看现有源码

现有原型使用 Go 1.22 及更新版本构建：

```bash
go build -o dist/projectctl ./cmd/projectctl
./dist/projectctl --help
go test ./...
```

这些命令验证当前源码，不表示目标流水线已实现。安装和 CLI 试用见[原型说明](docs/prototype.md)。项目采用 [MIT License](LICENSE)。
