# 功能规格：探针依赖内联 / 缓存预置

> 状态：开发中（Worker 侧缓存预置已落地，断网真机待验）　·　关联 PRD：FR-114　·　关联 ADR：ADR-016

## 1. 背景与目标

ServerProbe 首启可能在线拉取 TabooLib 依赖，慢网或离线环境下会导致探针 enable 失败或耗时过长。FR-114 要保证离线/慢网首启探针可用。

## 2. 需求

- 慢网或离线情况下，已部署探针首次启动仍可 enable。
- 平台能在部署探针前准备依赖。
- 失败时给出明确诊断。

范围外：

- 改探针桥协议。
- 替换 ServerProbe 技术栈。
- 引入新的第三方构建工具。

## 3. 设计候选

### 方案 A：探针 jar 内联依赖

构建 ServerProbe 时把 TabooLib 依赖打入探针 jar，使部署到实例的单 jar 可离线启动。

优点：运行期最简单。
风险：可能需要修改外部子模块构建链，属于受保护构建脚本改动，需要单独确认。

### 方案 B：Worker 侧缓存预置

CP 或 Worker 在部署探针时，把 TabooLib 依赖缓存放入实例/节点预期目录，让探针首启命中本地缓存。

优点：不必改变探针 jar 构建。
风险：需要准确掌握 TabooLib 缓存目录和版本布局。

### 采用方案

先走方案 B：Worker 在 `DeployServerProbe` 收到 CP 下发的 ServerProbe jar 后，读取 jar 内 `META-INF/taboolib/env.properties` / `version.properties`，按 TabooLib 运行期约定把依赖预置到实例工作目录下的 `libraries/`（或 jar 元数据声明的相对 `file-libs` 目录）。只有确认可安全修改 ServerProbe 构建链时，再切换方案 A。

当前已从 TabooLib 6.3.0 运行时代码确认：

- 默认缓存目录来自 `file-libs`，默认值为 `libraries`。
- `libraries` 是相对服务端启动工作目录的 Maven local repository 布局。
- ServerProbe 发行 jar 会在 `env.properties` 中声明模块列表，在 `version.properties` 中声明 TabooLib/Kotlin 版本。

Worker 预置范围：

- Kotlin 标准库 / coroutines（版本非 `null` / `skip` 时）。
- `env.properties module=` 列出的 TabooLib 模块。
- 每个坐标下载 `.pom` 与 `.jar`，目标路径为 Maven local repository 标准路径。
- `file-libs` 必须是实例工作目录内相对路径，禁止绝对路径与 `..` 越界。

## 4. 任务拆分

- [x] 明确 ServerProbe/TabooLib 依赖缓存目录。
- [x] 实现 Worker 侧依赖预置。
- [x] 补部署探针前置检查与诊断。
- [x] 补断网首启验收脚本或手动 runbook（见 `docs/specs/probe-dependency-cache/runbook.md`）。
- [x] 文档同步：ARCHITECTURE、CHANGELOG。

## 5. 验收标准

- 单测覆盖缓存目录选择、缺依赖诊断。
- 单机断言：阻断外网后新实例首启探针正常 enable。
- 真机：断网首启 Paper + ServerProbe 可连接插件桥并上报指标。

当前验证：

- `go test ./internal/worker/grpc -run TestDeployServerProbe -count=1` 通过，覆盖缓存预置、缺依赖诊断、越界目录拒绝。
- `go test ./internal/worker/grpc -run TestDeployServerProbe_OverGRPCAllowsLargePayload -count=1` 通过：经 bufconn 真实 gRPC 传输下发 >4MiB 探针载荷不被 `ResourceExhausted` 拒收，堵住「jar + libraries_zip 合计超默认 4MiB gRPC 单消息上限致探针部署失败」的验证盲区（既有 handler 级测试不经编解码，覆盖不到）。
- 已补断网首启手动验收 runbook，用于 release 资产和真机环境可用时复验 Paper + ServerProbe 插件桥链路。

## 6. 风险 / 待定

- Worker 目前预置 jar 元数据声明的直接依赖；若未来 TabooLib 模块 POM 新增未声明的传递依赖，仍需断网真机验证暴露并补齐。
- 是否允许修改构建脚本需用户确认；未确认前不走方案 A。
