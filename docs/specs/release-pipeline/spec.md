# 功能规格：CI/CD 发布管线（GitHub Actions）

> 状态：工作区已实现，远端 Actions 待验　·　关联 PRD：FR-173　·　关联 ADR：[ADR-036](../../adr/036-release-pipeline-github.md)、[ADR-074](../../adr/074-release-version-provenance-and-smoke.md)

## 1. 目标与当前边界

发布管线为 Control Plane 与 Worker 产出可被 GitHub Releases 和自更新链路消费的固定命名制品，并在发布前统一完成版本溯源、内嵌资产构建、质量门禁、交叉编译与目标系统原生烟测。

当前工作区已经实现：

- 独立 `metadata` job 和 [`scripts/release-metadata.mjs`](../../../scripts/release-metadata.mjs)；
- Bot Worker 质量门禁、生产构建与 CP 内嵌归档；
- Go 1.26.2、Node.js 22、JDK21 发布工具链；
- Control Plane / Worker × Linux / Windows 共四个二进制；
- Linux 与 Windows 原生 runner 上逐产物执行 `--version` 的 smoke；
- `checksums.txt`、CHANGELOG 发布说明与正式/滚动 Release 发布。

本次用户选择暂不 push，因此 GitHub-hosted runner、artifact 传递、权限和实际 Release 创建仍为**远端 Actions 待验**，不得标记为已远端通过。

## 2. 发布契约

### 2.1 触发与渠道

- push 到 `master`：覆盖固定 tag `latest` 的滚动预发布，`prerelease=true`，发布说明取 `CHANGELOG.md` 的 `[Unreleased]` 段。
- push tag `vX.Y.Z`：创建正式 Release，`prerelease=false`，发布说明取 CHANGELOG 对应 `X.Y.Z` 版本段。
- 普通分支源码若已是裸 `X.Y.Z` 且当前提交存在精确 tag `vX.Y.Z`，只执行构建验证，不重复覆盖 `latest`。

### 2.2 产物

发布资产固定为：

- `control-plane-linux-amd64`
- `worker-linux-amd64`
- `control-plane-windows-amd64.exe`
- `worker-windows-amd64.exe`
- `checksums.txt`

`checksums.txt` 覆盖四个二进制，行格式为 `<sha256>  <filename>`。命名与完整性契约继续遵循 ADR-036。

### 2.3 内嵌资产

- Control Plane：React 前端、Bot Worker 归档、ServerProbe jar 与离线依赖缓存、客户端更新器两件套、Linux/Windows Worker 二进制及 manifest。
- Worker：CFR 反编译器。

任一必需内嵌目录缺失或构建失败时 fail-fast，不允许生成“可发布但资产不完整”的二进制。

## 3. 版本来源与发布元数据

### 3.1 单一真源

`internal/version/version.go` 的 `Version` 是源码版本单一真源。当前正式发布提交为：

```text
0.18.0
```

`metadata` job 使用 `actions/checkout` 的 `fetch-depth: 0`，执行 `node scripts/release-metadata.mjs`，读取源码版本并联合校验 Git ref、提交 SHA 与当前提交的精确 tag。后续 job 只消费它输出的 `version`、`release_tag`、`is_release`、`publish_release`。

### 3.2 正式发布

正式 ref 必须为 `refs/tags/vX.Y.Z`，源码必须在同一提交写裸 `X.Y.Z`：

| 用途 | 值 |
|---|---|
| Git tag / GitHub Release | `vX.Y.Z` |
| 二进制 `--version` | `X.Y.Z` |
| Bot Worker 归档版本 | `X.Y.Z` |
| CP 内嵌 Worker manifest | `X.Y.Z` |

正式 tag 的 `v` 与二进制裸版本明确分离。tag 格式非法或源码版本不一致时，metadata job 直接失败。

### 3.3 开发构建

开发分支源码保持 `X.Y.Z-dev`（候选期允许 `X.Y.Z-rc.N`），构建时追加 SemVer 构建元数据：

```text
X.Y.Z-dev+g<7位提交SHA>
```

例如开发源码 `0.19.0-dev` 在提交 `abcdef0…` 上构建为 `0.19.0-dev+gabcdef0`。提交 SHA 不写回源码；源码仍保持下一目标版本。普通分支出现无同 SHA 正式 tag 的裸版本必须失败。

## 4. Workflow 结构

当前发布链路为：

```text
metadata → prepare-embeds → test → build → smoke → release
                         ↘ build 同时消费 metadata
```

| Job | 职责 | 关键门禁 |
|---|---|---|
| `metadata` | 读取源码版本，校验 ref/tag/SHA，输出全链路唯一发布元数据 | 正式 tag 与源码裸版本不一致即失败；开发分支非法版本即失败 |
| `prepare-embeds` | 构建全部平台无关内嵌资产 | Bot Worker、前端、探针、客户端更新器、CFR 任一步失败即停止 |
| `test` | 在发布前运行 Go 与前端质量门禁 | `go build`、`go vet`、`go test`；前端 lint、vitest、build、Playwright E2E |
| `build` | 两目标 matrix 交叉编译，并为 CP 注入两平台 Worker | `linux/amd64`、`windows/amd64`；全部版本取 metadata output |
| `smoke` | 在目标系统原生执行四个最终发布产物 | Linux/Windows 各执行 CP、Worker 的 `--version` |
| `release` | 汇总资产、生成校验和、提取说明并创建 Release | 依赖全部 smoke 成功；`publish_release=false` 时跳过 |

## 5. Bot Worker 构建与内嵌

`prepare-embeds` 使用 Node.js 22，对 `apps/bot-worker` 顺序执行：

1. `npm ci`；
2. `npm run audit:prod`；
3. `npm run typecheck`；
4. `npm run lint`；
5. `npm run build`；
6. `go run ./scripts/embed-botworker.go ... --version <metadata.version>`。

归档包含构建后的 dist 与维持 ESM 语义所需的包元数据，输出到 `internal/controlplane/embed/botworker/`，随后作为 CP embed artifact 传给构建矩阵。发布包不要求运维者手工拷贝 bot-worker dist。

Node.js 22 是发布构建工具链；受管节点执行 Bot Worker 的运行时要求为 Node.js `>=22.13.0`，依赖分发与受控项目根遵循 ADR-072。

## 6. 四产物原生 smoke

`smoke` job 有四个矩阵项：

| Runner | 产物 |
|---|---|
| `ubuntu-latest` | `control-plane-linux-amd64` |
| `ubuntu-latest` | `worker-linux-amd64` |
| `windows-latest` | `control-plane-windows-amd64.exe` |
| `windows-latest` | `worker-windows-amd64.exe` |

每项执行 `<binary> --version`，必须同时满足：

- 进程退出码为 0；
- stdout 去除首尾空白后严格等于 `metadata.version`；
- stderr 文件大小为 0。

`release` 直接依赖 `smoke`。这项门禁验证的是最终上传产物，而不是源码入口或中间构建文件。

## 7. 工具链与本地验证

发布 workflow 固定：

- Go `1.26.2`；
- Node.js `22`；
- JDK `21`。

本地可执行的针对性验证包括：

```bash
node --test scripts/release-metadata.test.mjs
```

仓库还包含 release workflow 契约测试，用于检查 metadata 输出消费、四项原生 smoke、release 对 smoke 的依赖及关键工具链值。完整发布构建可使用：

```bash
task dist
```

本地验证不能替代远端 GitHub Actions；尤其是 `ubuntu-latest` / `windows-latest` runner、artifact 上传下载、`GITHUB_TOKEN` 权限与 Release 创建必须推送后实跑。

## 8. 验收状态

| 验收项 | 当前状态 |
|---|---|
| `release-metadata.mjs` 从源码读取版本并校验正式 tag | 工作区已实现 |
| 正式 `vX.Y.Z` 与二进制 `X.Y.Z` 分离 | 工作区已实现 |
| 开发构建为 `X.Y.Z-dev+g<sha>` | 工作区已实现 |
| 正式发布提交源码版本为 `0.18.0` | 已确认 |
| Bot Worker build / audit / typecheck / lint 后内嵌 CP | 工作区已实现 |
| Go 1.26.2 / Node.js 22 工具链 | 工作区已实现 |
| 四个最终二进制在 Linux/Windows 原生 runner smoke | 工作区已实现 |
| release 仅在 smoke 全绿后执行 | 工作区已实现 |
| push `master` 实际覆盖 `latest` 预发布 | **远端 Actions 待验（本次不 push）** |
| push `vX.Y.Z` 实际创建正式 Release | **远端 Actions 待验（本次不 push）** |

## 9. 不在本规格范围

- 新增目标平台或架构（如 linux/arm64、darwin）；
- 二进制签名；
- Docker 镜像发布；
- 修改自更新的 GitHub Releases 消费契约；
- 在本次文档同步中 push、创建 tag 或实际触发远端 Actions。
