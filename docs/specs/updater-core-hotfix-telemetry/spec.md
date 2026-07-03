# 功能规格：updater-core 手动 hotfix 与遥测玩家名修复

> 状态：开发中　·　关联 PRD：FR-094 / FR-259 / FR-264 / FR-265　·　分支：当前工作树

## 1. 背景与目标

客户端分发已支持 updater-core 多版本归档与频道选定，但当前主要来源是 Control Plane 启动时归档内嵌 core，线上需要紧急替换 updater-core 时操作链路偏长。同时，客户端观测页出现「玩家：—」时，排障需要从 SecurityHello、遥测、运行态三条链路确认玩家名是否被端到端保留。

本规格目标：
- 允许平台管理员在频道「Core 版本」Tab 直接上传 updater-core.jar，并可立即选为该频道当前 core 版本，用于 hotfix。
- 修复更新遥测与运行态心跳中的玩家名缺失：不要求所有请求强制携带 `X-Player-Name`，而是优先用 header，缺省时按 `channel + machineId + installId` 从最近安全画像反查玩家名。
- 将导航栏「防护中心」文案改为「客户端分发安全」，与 FR-264 页面命名保持一致。

## 2. 需求（要什么）

- 范围内：
  - 新增平台管理员 API：上传 updater-core.jar 到现有制品库，制品类型复用 `client-updater-core`。
  - 上传时可选 `version`，可选 `select=true`；选中后频道 `selected_core_sha256` 指向新上传版本。
  - 前端 Core 版本 Tab 增加手动上传入口，支持选择 jar、填写版本、勾选上传后立即选用。
  - updater-core 后续遥测 / 心跳请求继续携带稳定标识 `X-Machine-Id`、`X-Install-Id`、`X-Client-Key`；不强制新增 `X-Player-Name`。
  - 后端写 `client_telemetry.player_name` 与 `client_runtime_states.player_name` 时，优先使用请求头兼容现有客户端，缺省按 `channel + machineId + installId` 从最近安全画像反查 `playerName`。
  - Mock API 与类型同步，保证前端测试路径能覆盖新能力。
  - 导航栏 i18n 文案同步中英文。
- 范围外：
  - 不新增数据库表或新的 core 发布状态机。
  - 不改变玩家侧拉取鉴权与 updater-core 查询协议。
  - 不把上传的 jar 纳入 Git 仓库。
  - 不实现跨频道批量切换或自动回滚策略。

## 3. 设计（怎么做）

### 3.1 手动上传 updater-core

- 后端路由新增：`POST /api/v1/client-channels/:id/updater-core/versions`。
- 权限：沿用客户端频道管理接口的 JWT + 平台管理员要求。
- 请求：`multipart/form-data`。
  - `file`：必填，updater-core.jar。
  - `version`：可选；空时由服务端用现有归档策略兜底。
  - `select`：可选布尔值，`true` 时上传成功后立即调用现有选定逻辑。
- 服务：复用 `ClientVersionService.ArchiveCoreJar` 写入 `assets(type=client-updater-core)`；复用 `SelectCoreVersion` 更新 `client_channels.selected_core_sha256`。
- 响应：返回 `CoreVersionSummary` 结构，前端刷新列表后展示最新选定状态。

### 3.2 遥测 / 心跳玩家名补全

- Java updater-core：启动早期已通过 `POST /client-security/hello` body 上报 `channel / machineId / installId / playerName`，保持该入口作为玩家名来源。
- Java updater-core：后续遥测与心跳请求不强制携带 `X-Player-Name`，只要求稳定标识 `X-Machine-Id / X-Install-Id / X-Client-Key` 可用于后端关联。
- Go 后端：新增安全画像反查能力，按 `channelId + machineId + installId` 读取 `client_security_profiles.player_name`。
- Go 后端：`client-telemetry` 与 `telemetry/heartbeat` 写库时先兼容现有 `X-Player-Name`，为空则反查安全画像补全。
- 前端不需要新增展示字段，只需让后端数据源不再为空。

### 3.3 导航文案

- `web/src/i18n/zh.json`：`nav.clientDistSecurity` 改为「客户端分发安全」。
- `web/src/i18n/en.json`：同步改为 `Client Distribution Security`。

## 4. 任务拆分

- [ ] 补充 Go 后端遥测安全画像反查玩家名测试。
- [ ] 补充 Go 后端运行态心跳安全画像反查玩家名测试。
- [ ] 补充 Go 后端 updater-core 手动上传 API 测试。
- [ ] 实现遥测玩家名 body + 后端兜底。
- [ ] 实现手动上传 updater-core API。
- [ ] 实现前端 API mutation 与 Core 版本 Tab 上传模态。
- [ ] 修改导航栏 i18n 文案。
- [ ] 同步 PRD、ARCHITECTURE、API、CHANGELOG。
- [ ] 运行相关 Go / Java / 前端测试与类型检查。

## 5. 验收标准

- 上传 hotfix：平台管理员可在 Core 版本 Tab 上传 jar；上传后列表出现新 core 版本；勾选立即选用时频道当前选定 core 指向该 sha256。
- 上传 API：缺文件返回 400；频道不存在返回 404；非平台管理员不可上传；成功归档为 `client-updater-core`；`select=true` 会更新频道选定 core。
- 遥测 / 心跳玩家名：客户端已通过 `client-security/hello` body 上报玩家名后，后续遥测与心跳即使不带 `X-Player-Name`，后端也能按 `channel + machineId + installId` 从安全画像补全 `player_name`。
- 导航栏：中文显示「客户端分发安全」，英文显示 `Client Distribution Security`。
- 验证命令至少覆盖：
  - `go test ./internal/controlplane/service ./internal/controlplane/router`
  - `cd client-updater && ./gradlew :updater-core:test`
  - `cd web && npx tsc -b`
  - 前端相关测试或 lint（按项目可用脚本执行）。

## 6. 风险 / 待定

- 上传文件大小沿用现有 HTTP multipart 限制；本次不新增独立大文件分块上传，因为 updater-core.jar 体积较小。
- `playerName` 仍是客户端声明值，可伪造，仅用于排障和观测，不参与鉴权。
- 若上传的 jar 不是合法 updater-core，服务端仅做归档与分发，不在本次实现 JVM 级运行校验。