# 功能规格：OTA 拉取错误追踪 + 面板查询

> 状态：待审　·　关联 PRD：FR-249（增强 FR-093）　·　关联 ADR：无（加性 schema 字段 + 新查询维度 + 新前端视图）　·　分支：feature/fr-249-pull-error-tracking

## 1. 背景与目标

FR-093 已落客户端分发拉取/下载追踪：`model.ClientDistEvent`（有 `Status` HTTP 码字段）+ `ClientDistTrackingService`（`Record`/`QueryEvents`）+ 端点 `GET /client-dist/events`。真机审计三缺口：
1. **无语义错误原因**：事件只存 HTTP 码（404/503…），分不清 `ARTIFACT_NOT_FOUND` vs `SIGN_KEY_NOT_CONFIGURED` vs `NO_LATEST_VERSION`。
2. **鉴权失败(401)完全不记录**：`GetManifest`/`GetArtifact` 的追踪 `defer` 注册在 `authChannelKey`/`authAnyKey` 返回**之后**（`client_version.go` 现结构），鉴权失败直接 return，事件漏记——无法追踪「密钥无效」类失败拉取。
3. **查询无成功/失败维度**：`ClientDistEventFilter`/`ListEvents` 只有 channel/machine/ip/kind/version/time，前端亦**无任何页面消费 `/client-dist/events`**（该端点无 UI）。

**目标**：拉取失败（含鉴权失败）也记录追踪事件（含语义错误码）；新建面板「分发事件」视图，可按成功/失败筛选并查看错误详情。P1。**加性 schema + 查询维度 + 新前端视图，不改 manifest/制品协议、不改鉴权语义。**

## 2. 需求（要什么）

### 范围内
- **事件加错误码字段**：`ClientDistEvent` 增 `ErrCode string`（`varchar(48)`，index，json `errCode`）——成功为空，失败填语义码（`INVALID_CLIENT_KEY`/`NO_LATEST_VERSION`/`ARTIFACT_NOT_FOUND`/`SIGN_KEY_NOT_CONFIGURED`/`CHANNEL_NOT_FOUND`/`INTERNAL_ERROR`）。GORM AutoMigrate 加性加列（老库零改动，默认空）。
- **补记失败路径（含鉴权）**：重构 `GetManifest`/`GetArtifact`，把追踪 `defer` 提到**鉴权之前**注册，用闭包变量捕获最终 `Status` + `ErrCode`；鉴权/构建/取制品各失败分支落对应错误码。**best-effort、不阻断玩家**语义不变。
- **查询加成功/失败维度**：`ClientDistEventFilter` 增 `Outcome`（`""`/`success`/`failure`）：`failure ⟺ status>=400`，`success ⟺ status>0 且 <400`（304/200/206 为成功）；并支持 `ErrCode` 精确筛。`ListEvents` 端点加 `outcome`、`errCode` query 参数。
- **新建前端「分发事件」视图**（该端点原无 UI）：作为**分发监控页的一个 Tab/区块**（`ClientDistMonitoringPage`，已是平台管理员页），展示明细表（时间/频道/类型/版本或 sha/IP/机器码/状态/结果徽章）+ 筛选（频道、类型、结果 全部/成功/失败、时间范围、limit）+ 失败行展开错误码详情。`useClientDistEvents(filter)` hook。
- i18n zh/en（只追加自己键块）；暗亮主题 token。

### 不做（范围外）
- 改 FR-217/ADR-049 聚合观测底座（那是快照聚合，本 FR 是**明细**追踪，两码事，不动 `client_dist_observability*`）。
- 改鉴权逻辑本身（`VerifyKey`/`VerifyAnyKey` 不动，只在失败时记事件）。
- 明细长期保留策略（仍沿用现 14 天短保留 + 滚动清理，不改）。
- 玩家侧错误上报（客户端遥测归 FR-092/094，不在此）。

## 3. 设计（怎么做）

### 3.1 模型 + 服务（`client_dist_event.go` / `client_dist_tracking.go`）
- `ClientDistEvent` 加 `ErrCode`；`ClientDistEventInput` 加 `ErrCode`，`Record` 写入（`ErrCode` 空即成功事件，逻辑不变）。
- `ClientDistEventFilter` 加 `Outcome string` + `ErrCode string`；`QueryEvents`：`outcome=="failure"` → `.Where("status >= ?", 400)`；`outcome=="success"` → `.Where("status > 0 AND status < ?", 400)`；`ErrCode!=""` → `.Where("err_code = ?", ...)`。其余不变（created_at DESC、limit 上限）。

### 3.2 记录点重构（`internal/controlplane/router/client_version.go`，本 FR 独占此文件）
- `GetManifest`：顺序改为 `start/mid/var errCode/manifestVersion := 0` → **先注册 defer**（记 `Status:c.Writer.Status(), ErrCode:errCode`，其余字段同现）→ 再 `authChannelKey`（失败设 `errCode` 并 return，defer 仍执行记 401 事件）→ machine record → BuildManifest（失败 `errCode=...` return）→ 成功路径。
- `GetArtifact`：同构（先 defer，后 `authAnyKey`；注意频道来自密钥归属，鉴权失败时频道未知，`ChannelID` 记空可接受）。
- `authChannelKey`/`authAnyKey`/`respondKeyAuthErr`/`respondConsumerErr` 改为**返回错误码字符串**（与其写入 JSON `error` 字段的码一致），供 handler 捕获到 `errCode`。保持它们「已写响应」的既有职责。
- `ListEvents`：读 `outcome`、`errCode` query 填入 filter。

### 3.3 前端（新视图）
- `web/src/api/clientDistEvents.ts`：`useClientDistEvents(filter)`（平台管理员 `enabled` 门控）+ 类型（含 `errCode`）。
- `ClientDistMonitoringPage.tsx`：加「分发事件（明细）」Tab/区块——筛选控件 + 明细表 + 结果徽章（成功绿/失败红）+ 失败行错误码。复用站内 Table/徽章范式（FR-195）。非管理员整页已降级。
- `mocks/handlers/domains/client.ts`：`/client-dist/events` MSW handler 支持 outcome/errCode 过滤 + 造几条成功/失败样本。

## 4. 任务拆分
- [ ] `client_dist_tracking_test.go`：Record 带 ErrCode、QueryEvents outcome（success/failure）+ errCode 过滤（红→绿）
- [ ] 模型加 `ErrCode` + 输入/过滤扩展 + `QueryEvents` outcome 逻辑
- [ ] `client_version.go` 记录点重构（defer 前置 + 鉴权失败记事件 + 各分支错误码）+ router 测试（manifest/artifact 失败与 401 均记录、带 errCode；ListEvents outcome 参数）
- [ ] 前端「分发事件」视图 + `useClientDistEvents` + MSW + dom 测试
- [ ] i18n zh/en；暗亮主题
- [ ] doc-sync：`docs/API.md`（`GET /client-dist/events` 加 outcome/errCode 参数 + 响应加 errCode）、`docs/ARCHITECTURE.md`（`client_dist_events` ER 加 `err_code`）、PRD FR-249「计划」→「开发中」（只改本行）、CHANGELOG `[Unreleased]` 末尾追加
- [ ] 中文 commit（feat(control-plane) 后端、feat(web) 前端拆 commit）

## 5. 验收标准
- 后端 `go build ./...` + `go test ./internal/controlplane/...` 绿；前端 tsc/eslint/build + vitest 绿。
- 拉取失败（404 无版本/无制品、503 未配签名、**401 密钥无效**）均记 `ClientDistEvent` 且带正确 `errCode`——router 测试断言（尤其 401，此前漏记）。
- `GET /client-dist/events?outcome=failure` 只返失败事件；`outcome=success` 只返成功；`errCode=` 精确筛生效。
- 成功拉取事件 `errCode` 为空、行为与现状一致（不回归 FR-093）。
- 前端事件视图可按结果筛选、失败行见错误码；zh/en + 暗亮正常。
- **【需真机，用户确认】** 面板打开分发事件视图：制造一次错误拉取（如错误密钥/未发布频道）→ 视图按「失败」筛出该事件、显示错误码。（真机为硬闸。）

## 6. 风险 / 待定
- **闭包变量捕获时序**：`defer` 前置 + 闭包读 `errCode`/`manifestVersion` 最终值（Go defer 闭包按引用读变量），需确保各 return 分支都已赋值；单测覆盖每条失败路径。
- **鉴权失败频道未知**：`GetArtifact` 鉴权失败时频道来自密钥、此时无，`ChannelID` 记空——可接受（IP/errCode 仍可追踪）；文档注明。
- **明细量**：失败事件同样进 14 天短保留 + 滚动清理，量级与现成功事件同数量级，不新增治理负担。
