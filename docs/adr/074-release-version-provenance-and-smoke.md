# ADR-074: 发布版本溯源、Bot 内嵌与原生烟测

- **日期**: 2026-07-19
- **状态**: accepted
- **修订**: 追加修订 [ADR-036](036-release-pipeline-github.md) 的版本来源、Bot Worker 内嵌资产与发布前 smoke；不重写 ADR-036 的历史决策
- **部分被取代**: [ADR-083](083-artifact-version-library-serverprobe-distribution.md) 取代决策 3 中 ServerProbe 及其离线依赖缓存作为发布版 CP 内嵌资产的单项描述；Bot Worker 与其他资产决策保持有效。

## 上下文

ADR-036 确立了 GitHub Releases 的产物命名、校验与渠道契约，但其早期版本注入描述仍把正式 tag 与二进制版本混为同一字符串，并把开发构建写成固定 `0.0.0-dev+<shortsha>`。ADR-065 后续已把 `internal/version/version.go` 定为版本单一真源，并规定开发态使用下一目标版本 `X.Y.Z-dev`。如果发布 workflow、Bot Worker 归档、CP 内嵌 Worker 清单和最终二进制继续各自推导版本，就可能出现 Release 名称、二进制自报版本与内嵌资产版本不一致。

发布版 Control Plane 还承担 Bot Worker dist 的分发入口。仅构建 Go 二进制而不在发布链路内完成 Bot Worker 的依赖审计、类型检查、构建与归档内嵌，会产生“主程序发布成功但 Bot 资产缺失或版本不一致”的不完整制品。

最后，交叉编译成功只能证明目标文件生成，不能证明产物可在目标系统原生启动并准确报告注入版本。发布动作必须位于四个最终产物的原生执行验证之后。

## 决策

### 1. 发布元数据只有一个解析入口

`.github/workflows/release.yml` 使用独立 `metadata` job，在完整 Git 历史（`fetch-depth: 0`）上执行 `scripts/release-metadata.mjs`。脚本读取 `internal/version/version.go`，联合校验 `GITHUB_REF`、`GITHUB_SHA` 与当前提交的精确 tag，并统一输出：

- `version`：注入二进制、Bot Worker 归档和 CP 内嵌 Worker 清单的版本；
- `release_tag`：GitHub Release 使用的 tag；
- `is_release`：是否为正式 tag 发布；
- `publish_release`：本次是否应创建或覆盖 Release。

后续 `prepare-embeds`、`build`、`smoke` 与 `release` job 只消费这份 output，不再自行解析版本。

### 2. 正式 tag 与二进制版本分离

- 正式发布 ref 必须是 `refs/tags/vX.Y.Z`，且源码版本必须是裸 `X.Y.Z`；不一致立即失败。
- GitHub Release 与 Git tag 保持 `vX.Y.Z`，二进制内部及内嵌资产版本使用不带 `v` 的 `X.Y.Z`。
- 开发分支源码保持 `X.Y.Z-dev`（候选期可为 `X.Y.Z-rc.N`），构建版本追加提交溯源信息为 `X.Y.Z-dev+g<7位sha>`；滚动预发布 tag 仍为 `latest`。
- 普通分支若源码为裸 `X.Y.Z`，仅允许当前提交已存在同 SHA 的 `vX.Y.Z` tag；此时不重复发布 `latest`。没有对应 tag 的裸版本直接拒绝构建。
- 当前开发版本保持 **`0.18.0-dev`**，本 ADR 不触发版本号变更。

这项决策追加修订 ADR-036 §4：正式发布不再把 `vX.Y.Z` 注入二进制，开发构建也不再退化为与目标版本无关的 `0.0.0-dev`。

### 3. Bot Worker 是发布版 CP 的必备内嵌资产

`prepare-embeds` 在 Node.js 22 工具链下对 `apps/bot-worker` 依次执行：

1. `npm ci`；
2. 生产依赖审计；
3. TypeScript 类型检查；
4. ESLint；
5. 生产构建；
6. 通过 `scripts/embed-botworker.go` 把 dist 与 ESM 所需包元数据打成确定性归档，写入 CP 的 `go:embed` 目录，并使用 `metadata.version` 标记版本。

发布版 Control Plane 因而同时内嵌前端、Bot Worker、探针及离线依赖缓存、客户端更新器和两平台 Worker；发布版 Worker 内嵌 CFR。Bot Worker 的 Node.js **构建工具链为 Node 22**，受管节点的运行时最低版本仍按 ADR-072 为 **Node.js >=22.13.0**。

这项决策追加修订 ADR-036 §5：Bot Worker 归档属于发布版 CP 必须具备的内嵌资产，不能由发布后手工补拷。

### 4. Release 必须经过四产物原生 runner smoke

`build` 仍交叉编译以下四个最终发布二进制：

- `control-plane-linux-amd64`
- `worker-linux-amd64`
- `control-plane-windows-amd64.exe`
- `worker-windows-amd64.exe`

随后独立 `smoke` job 使用四项矩阵，在 `ubuntu-latest` 原生执行两个 Linux 产物、在 `windows-latest` 原生执行两个 Windows 产物。每项执行 `<binary> --version` 并强制验证：

- 退出码为 0；
- stdout 去除首尾空白后与 `metadata.version` 完全相等；
- stderr 为空。

`release` 必须依赖 `smoke` 成功；任一产物不能启动、版本不一致或污染 stderr，均不得生成 GitHub Release。

### 5. 发布工具链固定

当前发布 workflow 固定使用：

- Go **1.26.2**；
- Node.js **22**；
- JDK 21（探针与客户端更新器构建）。

版本脚本本身使用 Node 内置模块并由 `node:test` 覆盖，不新增第三方依赖。

### 6. 远端验证边界

本地可以验证元数据脚本单测、workflow 契约、构建逻辑与文档链接，但 GitHub-hosted runner、artifact 传递、权限和 Release 创建只能通过远端 Actions 证明。用户本次明确选择**暂不 push**，因此当前状态是“工作区实现完成，远端 Actions 待验”，不得把未发生的远端运行描述为已通过。

## 理由

- 单一 metadata job 让二进制、Bot 归档、内嵌 Worker 与 Release 名称共享同一版本来源，消除多点推导漂移。
- tag 的 `v` 是 Git 发布命名约定，不应进入程序内部 SemVer；分离后自报版本、资产目录与版本比较均保持裸 SemVer。
- `+g<sha>` 保留下一目标版本语义，同时能把开发产物追溯到精确提交，且不改变 SemVer 优先级。
- Bot Worker 在发布前完成质量门禁与内嵌，避免发布一个无法自愈分发 Bot dist 的 CP。
- 原生 runner 执行覆盖了交叉编译无法发现的启动格式、入口参数与版本注入问题，且成本远低于完整部署测试。

## 后果

- 发布 job 链路成为 `metadata → prepare-embeds → test → build → smoke → release`；其中 `build` 同时依赖 metadata、embed 与测试门禁。
- 发正式版前必须先把源码版本从 `X.Y.Z-dev` 切为裸 `X.Y.Z`，再在同一提交创建 `vX.Y.Z` tag；tag 与源码任一不一致都会失败。
- 开发构建自报 `X.Y.Z-dev+g<sha>`，源码仍保持 `X.Y.Z-dev`，不得把提交 SHA 写回源码。
- 发布时增加 Bot Worker 质量检查和四个原生 smoke 矩阵项，流水线耗时增加，但不完整或版本错误的制品会在发布前被拦截。
- 当前 `0.18.0-dev` 不变；由于本次不 push，远端 Actions 与实际 GitHub Release 行为仍待后续验证。

## 被否方案

- **各 job 继续自行计算版本**：实现简单但极易造成 Bot 清单、二进制与 Release 漂移，否决。
- **正式二进制保留 `v` 前缀**：把 Git tag 命名泄漏进内部 SemVer，且与 ADR-065 的裸版本真源冲突，否决。
- **开发构建固定 `0.0.0-dev`**：丢失下一目标版本，无法和 PRD、CHANGELOG 及自更新资产版本对齐，否决。
- **只在 Linux 上用 Wine 或静态检查 Windows 产物**：不能证明 Windows 原生入口和输出行为，否决。
- **smoke 只测一个组件**：Control Plane 与 Worker 是独立入口，任一都可能遗漏 `--version` 或注入链路，否决。

## 关系

- **ADR-036**：本 ADR 追加修订其版本注入、内嵌资产和发布前验证决策；产物命名、sha256 与 stable/prerelease 渠道契约继续有效。
- **ADR-065**：沿用 `internal/version/version.go` 单一真源与 `X.Y.Z-dev` 下一目标版本语义。
- **ADR-072**：沿用 Bot Worker dist 自愈分发、受控依赖根与 Node.js >=22.13.0 运行时门槛。
- **FR-173**：发布管线的当前实现真貌与验证门禁。
- **FR-308**：Bot Worker 构建、内嵌与节点自愈分发。
