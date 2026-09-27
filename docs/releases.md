# 构建与发布

## 本地构建

需要 Go 1.22 或更新版本。本项目使用标准库，无需获取外部 Go 模块。

```bash
go build -o dist/projectctl ./cmd/projectctl
./dist/projectctl --help
go test ./...
```

运行发布文件不需要安装 Go。skill 的 `SKILL.md`、references 和 Agent metadata 通过 Go embed 打进可执行文件，项目安装时从该内嵌内容释放。与仓库有关的版本核查仍需要系统中的 `git`；可选 GitHub 同步需要已安装并认证的 `gh`。单可执行文件不代表目标项目的测试命令也不需要其开发环境。

## 跨平台发布包

```bash
go run ./tools/release --version v1.0.0
```

发布工具面向 `darwin`、`linux`、`windows` 的 `amd64`、`arm64` 组合，生成六个 ZIP 和一个清单：

```text
dist/projectctl_v1.0.0_darwin_amd64.zip
dist/projectctl_v1.0.0_darwin_arm64.zip
dist/projectctl_v1.0.0_linux_amd64.zip
dist/projectctl_v1.0.0_linux_arm64.zip
dist/projectctl_v1.0.0_windows_amd64.zip
dist/projectctl_v1.0.0_windows_arm64.zip
dist/SHA256SUMS
```

每个 ZIP 包含 `projectctl`（Windows 为 `projectctl.exe`）、中英文 README、`LICENSE`、`docs/`、`skills/github-project-control/` 下的 Skill/引用资料/Agent metadata，以及 `examples/hello-go/`。这样解压后 README 的文档链接仍可离线阅读，也可以分享完整 skill。可执行文件仍内嵌安装所需资源，单独携带它即可安装；旁边的文档不是运行依赖。

发布工具只收集这些已知目录的普通文件，不打包 `dist`、活动 `.project-control`、已安装 `.agents` 或 skill 脚本目录。选择与目标系统和 CPU 匹配的归档，解压后运行对应文件。版本参数接受 `v1.0.0` 或 `v1.0.0-rc.1` 这类受限格式，不接受路径、空白或 shell 表达式。

构建设置 `CGO_ENABLED=0`、`-trimpath`，通过 `-ldflags` 注入版本。使用本机 Go 工具链与标准库，关闭自动工具链和模块网络下载。归档在临时目录全部完成后写入 `dist`，最后发布 `SHA256SUMS`；中途失败返回非零状态，不生成部分清单。发布输出时如果发生文件系统故障，则不留下新清单，修复后重新运行即可。

同一目录请串行运行发布工具。`SHA256SUMS` 只列出本次指定版本的六个包；旧版本包可能仍在 `dist`，上传时应按版本选择，不能把所有历史 ZIP 一起上传。

跨平台编译成功不等于已经在每个平台实机验收。发布记录应分别列出交叉编译结果、实际运行过的平台和安装/卸载测试。

## 下载与校验

项目仓库为 [yes8080/projectctl](https://github.com/yes8080/projectctl)。从 [GitHub Releases](https://github.com/yes8080/projectctl/releases/latest) 下载与系统及 CPU 匹配的 ZIP 和 `SHA256SUMS`。

在下载目录根据系统可用命令计算归档的 SHA-256，再与可信来源的 `SHA256SUMS` 比较：

```bash
# Linux
sha256sum -c SHA256SUMS

# macOS
shasum -a 256 <下载的归档文件名>
```

Windows PowerShell 可使用 `Get-FileHash <归档文件名> -Algorithm SHA256`。`sha256sum -c` 示例要求清单中的六个归档都在当前目录；只下载一个平台包时单独计算并比较其清单行。哈希文件与归档都来自同一个未受信来源时，哈希只能检查内容一致性，不能证明发布者身份；当前项目不声称已经提供签名发行。

## CI 与 Release 草稿

`.github/workflows/ci.yml` 在分支 push 和 pull request 上配置 Ubuntu、macOS、Windows 的原生 `go test ./...` 与 `go vet ./...`，使用 Go 1.22。CI 仅请求仓库读取权限，不使用 `pull_request_target`。

`.github/workflows/release.yml` 仅在 `v*` tag push 时运行；先完成同样的三个系统测试，再构建六个平台的包并检查清单。只有创建草稿的 release job 请求 `contents: write`。标签必须通过构建工具的版本校验。

工作流使用 `gh release create --draft --verify-tag --generate-notes` 准备草稿，随后上传该版本的包和清单。已有草稿可重跑并替换同名附件；已公开发布的版本会被拒绝修改。维护者应等工作流结束再人工发布草稿，避免人工发布与附件上传并发。草稿检查与附件更新不是远端原子事务。[创建 Release](https://cli.github.com/manual/gh_release_create)、[上传附件](https://cli.github.com/manual/gh_release_upload)

配置工作流不代表本地已经运行了 GitHub CI，也不代表已创建远端 Release。工作流采用官方 [checkout v4](https://github.com/actions/checkout/tree/v4) 与 [setup-go v5](https://github.com/actions/setup-go/tree/v5)。

## 安装行为

将可执行文件置于 PATH 后执行 `projectctl install --project <项目路径> --agent codex`；也可直接使用可执行文件的绝对路径，无需系统级安装。使用 `--dry-run` 查看文件计划，已有安装升级使用 `--upgrade`。

安装与卸载均在本地进行；不会自动发布 GitHub Release、修改 Git/CI、创建 PR 或部署项目。外部发布动作必须在具体用户授权范围内执行。
