# 功能规格：updater-core 构建元信息内嵌与展示

> 状态：已实现　·　关联 PRD：FR-266　·　分支：当前工作树

## 1. 背景与目标

当前 updater-core 归档列表的「版本」主要来自构建脚本/上传表单传入的整数，容易出现多次归档都显示 `v1`，也无法追溯某个 jar 对应的 Git commit。FR-259 已把 updater-core 分发改为「归档多版本 + 频道选定 + wedge 按分发版本拉取」，但缺少 jar 自身携带的构建元信息。

本规格目标：让 updater-core.jar 自身内嵌可读版本号与 Git commit hash，后端归档时优先读取 jar 内元信息，前端 Core 版本页展示该元信息，减少手填版本错误并提升线上排障可追溯性。

## 2. 需求（要什么）

- 范围内：
  - updater-core.jar 构建产物内必须包含可读版本号与 Git commit hash。
  - 元信息至少包含：`version`、`gitCommit`、`dirty`、`buildTime`。
  - Java updater-core 运行时可读取自身内嵌元信息，用于日志、运行态心跳与安全画像中的 core 版本标识。
  - Control Plane 归档 updater-core.jar 时，优先读取 jar 内元信息作为归档展示信息；上传表单的 `version` 只作为缺省兜底。
  - Core 版本 Tab 展示 jar 内版本、Git commit 短 hash 与 dirty 标记；频道级分发版本仍为后端内部递增值，不混入版本列表列。
  - 内嵌构建链路 `make embed-client-updater` 与 release workflow 构建出的 updater-core.jar 也必须带同一套元信息。
  - 平台管理员紧急修复 updater-core 时，仍可直接在 Core 版本 Tab 上传 jar 并勾选立即选用；元信息读取失败不得阻断 hotfix 上传。
- 范围外：
  - 不修改 wedge 的冻结协议；wedge 仍只读取 `/updater-core` 的 `{version, sha256, downloadUrl, size}`。
  - 不把 Git commit hash 作为客户端信任根或鉴权依据；信任模型仍是 HTTPS + 拉取密钥 + sha256 完整性校验。
  - 不新增独立数据库表记录切换历史；审计日志仍记录 `client_core.upload` / `client_core.select`。
  - 不强制手动上传的第三方 jar 必须来自当前仓库；若读不到元信息则继续按现有兼容兜底。

## 3. 设计（怎么做）

### 3.1 jar 内元信息格式

updater-core 构建时生成资源文件：

```text
META-INF/jm-updater-core.properties
```

字段：

```properties
version=0.1.0-SNAPSHOT
gitCommit=<12位短 hash 或 unknown>
dirty=<true|false|unknown>
buildTime=<RFC3339 UTC 时间>
```

同时在 JAR Manifest 中写入可读属性，便于外部工具查看：

```text
Implementation-Version: 0.1.0-SNAPSHOT
JM-Updater-Core-Version: 0.1.0-SNAPSHOT
JM-Git-Commit: <12位短 hash 或 unknown>
JM-Git-Dirty: <true|false|unknown>
JM-Build-Time: <RFC3339 UTC 时间>
```

版本号来源：优先 `-PupdaterVersion=...`，否则使用 Gradle `project.version`。Git commit 来源：构建时执行 `git rev-parse --short=12 HEAD`，失败时为 `unknown`。dirty 来源：构建时检测 Git 工作区是否存在未提交的已跟踪文件变更，存在则 `dirty=true`；检测失败则 `dirty=unknown`。

### 3.2 Java 运行时读取

新增 `BuildInfo` 小类读取 `META-INF/jm-updater-core.properties`，提供：

- `BuildInfo.version()`
- `BuildInfo.gitCommit()`
- `BuildInfo.dirty()`
- `BuildInfo.display()`，例如 `0.1.0-SNAPSHOT+abc123def456`；dirty 构建显示为 `0.1.0-SNAPSHOT+abc123def456.dirty`

`Core.run` 中：

- 日志启动行打印 `coreBuild=<display>`。
- 运行态心跳 `RuntimeHeartbeat.build(...)` 与安全画像 `SecurityIdentity.coreVersion` 使用 `BuildInfo.display()`，不再只依赖 wedge 传入的频道分发版本。
- wedge 传入的 `coreVersion` 作为 `distributionVersion` 仅用于日志辅助，不作为 jar 真实版本展示。

### 3.3 后端归档读取 jar 元信息

`ClientVersionService.ArchiveCoreJar` 在计算 sha256 后读取 jar 内：

- `META-INF/jm-updater-core.properties`
- 若资源不存在，再尝试读取 manifest 属性。
- 若仍读不到，继续使用上传表单/内嵌常量传入的 `version`，并按现有自动递增兜底。

资产记录：

- `assets.version` 继续保存数字归档版本（字符串形式，如 `1`/`2`/`3`），用于现有归档排序、兼容旧逻辑和未选定频道的分发兜底。
- jar 内可读版本不写入 `assets.version`，而写入 `assets.metadata.coreVersion`；前端版本列优先显示 `metadata.coreVersion`，缺失时再显示 `v${assets.version}`。
- `assets.metadata` 扩展 JSON：

```json
{
  "codec": "none",
  "source": "embedded-updater-core",
  "coreVersion": "0.1.0-SNAPSHOT",
  "gitCommit": "abc123def456",
  "dirty": true,
  "buildTime": "2026-07-03T00:00:00Z"
}
```

频道级分发版本继续存在于 `client_channels.selected_core_version`，只用于 wedge 比较，不作为前端归档版本列。

### 3.4 API 与前端展示

`CoreVersionSummary` 加性增加字段，保留既有数字 `version`：

```json
{
  "version": 3,
  "coreVersion": "0.1.0-SNAPSHOT",
  "displayVersion": "0.1.0-SNAPSHOT+abc123def456.dirty",
  "sha256": "...",
  "size": 12345,
  "createdAt": "...",
  "selected": true,
  "gitCommit": "abc123def456",
  "dirty": true,
  "buildTime": "2026-07-03T00:00:00Z"
}
```

兼容策略：

- 旧资产只有整数版本时，`coreVersion/gitCommit/buildTime` 可为空，前端回退显示 `v${version}`。
- 前端版本列优先显示 `displayVersion`，缺失时再显示 `v${version}`。
- Core 版本 Tab 增加「Commit」列或在 SHA 下方显示短 commit，dirty 时显示醒目的 `dirty` 标记，缺省显示 `—`。
- 上传弹窗的版本号输入改为可选兜底文案：「通常无需填写，后端优先读取 jar 内版本；紧急 hotfix 缺少元信息也可上传」。

## 4. 任务拆分

- [x] 为 Java `BuildInfo` 写单测：能读取 properties，缺失时回退为 `unknown`。
- [x] 修改 updater-core Gradle 构建：生成 properties + manifest 元信息，保持 Java 8 字节码与 fat jar 自包含。
- [x] 修改 `Core.run` / `RuntimeHeartbeat` / `SecurityHello` 使用 jar 内 `BuildInfo.display()` 上报真实 core 构建版本。
- [x] 为 Go 后端补 jar 元信息读取单测：properties 优先、manifest 兜底、缺失兼容。
- [x] 修改 `ArchiveCoreJar` / `ListCoreVersions` / 上传响应，返回并保存 coreVersion/gitCommit/dirty/buildTime，且缺失元信息不阻断 hotfix 上传。
- [x] 修改前端 API 类型、Core 版本 Tab 展示与上传文案。
- [x] 同步 PRD、ARCHITECTURE、API、CHANGELOG 与相关 spec/ADR 说明。
- [x] 运行 Java / Go / 前端相关测试。

## 5. 验收标准

- 构建产物验收：`client-updater/updater-core/build/libs/updater-core-*.jar` 内存在 `META-INF/jm-updater-core.properties`，内容包含非空 `version`、`gitCommit`、`dirty`、`buildTime`。
- Java 运行态验收：updater-core 启动日志、运行态心跳、安全画像中的 `coreVersion` 使用 `version+gitCommit` 形式；dirty 构建带 `.dirty` 标记，而不是单纯频道分发整数。
- 后端归档验收：上传/内嵌归档带元信息的 updater-core.jar 后，`GET /api/v1/client-channels/:id/updater-core/versions` 返回数字归档 `version` 以及 `coreVersion`、`displayVersion`、`gitCommit`、`dirty`、`buildTime`；缺少元信息的旧 jar 仍可归档并显示兜底版本。
- 前端验收：Core 版本 Tab 不再全量显示 `v1`，可看到真实版本与 Git commit；dirty 构建有明确标记；未知 commit 显示 `—`。
- 分发兼容验收：`GET /api/v1/client-channels/:id/updater-core` 的冻结字段 `{version, sha256, downloadUrl, size}` 不删不改，wedge 无需改动。
- 验证命令至少覆盖：
  - `cd client-updater && ./gradlew :updater-core:test`
  - `go test ./internal/controlplane/service ./internal/controlplane/router`
  - `cd web && npx tsc -b`
  - 前端相关组件测试。

## 6. 风险 / 待定

- dirty 只表示构建时存在未提交的已跟踪文件变更；不把 untracked 临时文件纳入 dirty 判断，避免 `.tmp/`、构建产物等导致发布版误标。
- 手动上传的外部 jar 若缺少元信息，仍按兼容路径归档，不强制拒绝，以免阻断线上 hotfix。
- `assets.version` 保持数字归档轴；真实 jar 版本放在 metadata/API 新字段，避免破坏 wedge 使用的频道级分发版本与既有回滚逻辑。
