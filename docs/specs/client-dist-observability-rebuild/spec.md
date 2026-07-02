# 功能规格：客户端分发观测四 Tab 重构

> 状态：已落地　·　关联 PRD：FR-265（增强 FR-093/094/095/217/218/219）　·　关联 ADR：无新增（沿用 ADR-049，FR-265 仅修订观测展示/契约边界并清理废弃缓存命中指标）　·　并行约束：与 FR-264 并行开发，禁止触碰 `docs/specs/client-dist-security-firewall/` 及 FR-264 正在改动的防护中心 / security hello / 限流防火墙相关文件；禁止删除、回滚、reset 任何文件。

## 1. 背景与目标

客户端分发目前已有三类数据：

1. **分发请求事件**：`client_dist_events`（FR-093/249），记录 manifest / artifact HTTP 请求；
2. **更新结果遥测**：`client_telemetry`（FR-094），记录 reconcile 完成后的 `success | fail-static | rolled-back | error`；
3. **观测快照**：`client_dist_snapshots`（FR-217/ADR-049），把请求事件与更新遥测混合卷积，供现有“客户端分发监控”页展示。

现有观测页存在三类语义漂移：

- **统计口径混用**：请求量、更新成功率、运行版本、平台分布混在同一页，运营无法区分“HTTP 请求是否健康”和“客户端实际运行状态是否健康”；
- **废弃缓存命中指标残留**：FR-256 已删除客户端本地缓存能力，但 FR-217 快照和前端仍残留对应的命中 / 未命中统计；
- **运行态缺失**：现有 `client_telemetry` 只在更新完成后上报结果，不能表达“客户端当前跑哪个 localVersion / coreVersion”，也不能作为启动心跳使用，否则会污染更新成功率。

目标：用**一个 FR**完成客户端分发观测重构，形成同页面四 Tab：

1. **统计**：只看分发 HTTP 请求历史统计；
2. **监控**：只看分发 HTTP 请求近实时健康度；
3. **日志**：只看分发 HTTP 请求明细与脱敏详情；
4. **客户端**：只看客户端运行态与更新结果。

本 FR 是观测重构，不是安全防火墙。与 FR-264 并行时，本 FR 不实现 IP 封禁、key 状态、防火墙限流、security hello、防护中心；这些归 FR-264。

## 2. 需求（要什么）

### 2.1 范围内

#### A. 四 Tab 信息架构

在现有客户端分发监控页内重构为四个 Tab：

| Tab | 数据源 | 语义 |
|---|---|---|
| 统计 | `client_dist_events` / 请求聚合 | 分发 HTTP 请求历史统计 |
| 监控 | `client_dist_events` 近窗聚合 | 分发 HTTP 请求近实时健康度 |
| 日志 | `client_dist_events` 明细 | 单次 manifest / artifact HTTP 请求排查 |
| 客户端 | 新运行态表 + `client_telemetry` | 客户端最新运行态与更新结果 |

硬规则：**统计 / 监控 / 日志不展示更新成功率、运行版本分布、core 版本分布、平台分布、版本滞后分布**；这些全部归“客户端”Tab。

#### B. 新增客户端运行态表

新增 `client_runtime_states`，按 `channel_id + machine_id` 保存最新运行态。启动心跳只 upsert 这张表，不写 `client_telemetry`。

建议字段：

| 字段 | 说明 |
|---|---|
| id | 主键 |
| channel_id | 频道 slug，索引 |
| machine_id | 客户端机器码，不可信，仅统计 |
| ip | 最近一次心跳来源 IP |
| platform | 操作系统：windows / macos / linux / unknown |
| java_version | Java 版本 |
| launcher | 启动器：HMCL / PCL2 / unknown 等 |
| core_version | updater-core 版本 |
| local_version | 客户端本地已应用 manifest 版本 |
| first_seen_at | 首次心跳时间 |
| last_heartbeat_at | 最近心跳时间 |
| last_update_result | 最近一次更新结果，可由 `client_telemetry` 写入或聚合时补齐 |
| last_update_at | 最近一次更新结果时间 |
| created_at / updated_at | GORM 时间戳 |

唯一约束：`UNIQUE(channel_id, machine_id)`。

#### C. 启动心跳端点

新增面向玩家客户端的启动心跳端点：

```text
POST /api/v1/client-channels/:id/telemetry/heartbeat
```

鉴权：`X-Client-Key` 必须属于该频道，不能用 `VerifyAnyKey` 跨频道放行。

请求体：

```json
{
  "platform": "windows",
  "javaVersion": "17.0.10",
  "launcher": "HMCL",
  "coreVersion": "3",
  "localVersion": 15
}
```

机器码仍从 `X-Machine-Id` 取，IP 由服务端取 `ClientIP()`。

响应：`202 Accepted`。心跳是 best-effort：客户端发送失败不得影响更新与启动。

#### D. 客户端运行态聚合查询

新增平台管理员查询端点：

```text
GET /api/v1/client-dist/clients?channelId=&range=7d
```

响应包含：

- 近 5 分钟启动客户端数；
- 今日启动客户端数；
- 更新成功率 / 更新失败率（来自 `client_telemetry`，不是心跳）；
- 运行版本分布 `localVersion`；
- updater-core 版本分布 `coreVersion`；
- 平台分布；
- 启动器分布；
- 版本滞后分布：频道 latestVersion - localVersion；
- 更新结果趋势：success / fail-static / rolled-back / error。

文案约束：不展示“在线客户端”。MVP 只展示“近 5 分钟启动客户端 / 今日启动客户端”。

#### E. 分发请求实时聚合

新增平台管理员端点：

```text
GET /api/v1/client-dist/realtime?channelId=
```

响应包含：

- 近 1h 清单请求数；
- 近 1h 制品请求数；
- 近 1h 错误请求数；
- 近 1h 活跃机器码；
- 最近 24h 请求速率：manifest / artifact / error；
- 最近错误事件；
- 近 1h TOP IP。

用途：服务“监控”Tab，自动刷新 30s。

#### F. 分发请求日志扩展与详情

扩展 `client_dist_events`，只保存排障必需的脱敏白名单字段：

- `method`：GET / POST；
- `path`：不含 query 中敏感值；
- `request_headers_json`：请求头白名单 JSON；
- `response_headers_json`：响应头白名单 JSON；
- `etag`：便于列表快速显示，可选；
- `err_reason`：可读错误原因，可选。

请求头白名单：

- `User-Agent`
- `If-None-Match`
- `Range`
- `X-Machine-Id`
- `X-Client-Core-Version`
- `X-Client-Key` 只保存 `present` 或脱敏标记，绝不保存明文

响应头白名单：

- `ETag`
- `Cache-Control`
- `Content-Length`
- `Content-Range`

新增详情端点：

```text
GET /api/v1/client-dist/events/:id
```

仅平台管理员可访问，记录审计 `client_dist_event.detail`。

#### G. 日志列表筛选增强

`GET /api/v1/client-dist/events` 增加筛选：

- `artifactSha`
- `runtimeVersion`
- `coreVersion`
- `platform`
- `lag`
- `page`
- `pageSize`

运行态维度筛选由后端 join / 子查询 `client_runtime_states` 得到 machineId 集合，再过滤 `client_dist_events`。前端不得一次性传大量 machineId。

响应格式建议升级为分页对象：

```json
{
  "items": [],
  "page": 1,
  "pageSize": 100,
  "total": 1234
}
```

兼容策略：若担心破坏现有调用，可保留旧 `/client-dist/events` 数组响应，新增 `/client-dist/events/search` 分页端点。实现前按现有调用点最终确认，默认推荐新增分页搜索端点以降低回归风险。

#### H. 前端四 Tab 与联动

页面标题统一为“客户端分发观测”。

Tab 内容：

1. **统计**：请求历史 KPI、趋势、状态码/错误码/TOP IP/目标版本分布；
2. **监控**：近实时 KPI、最近错误、TOP IP，30s 自动刷新；
3. **日志**：明细列表、筛选、分页、行详情；
4. **客户端**：启动客户端 KPI、运行版本/core 版本/平台/启动器/滞后分布、更新结果趋势。

联动规则：

- 统计点击错误码 → 日志 `errCode=...`；
- 统计点击 TOP IP → 日志 `ip=...`；
- 统计点击 manifest 版本 → 日志 `kind=manifest&version=...`；
- 监控点击错误请求数 → 日志 `outcome=failure`；
- 监控点击最近错误行 → 日志 `channelId + errCode`；
- 客户端点击运行版本 → 日志 `runtimeVersion=...`；
- 客户端点击 core 版本 → 日志 `coreVersion=...`；
- 客户端点击平台 → 日志 `platform=...`；
- 客户端点击滞后档 → 日志 `lag=...`。

### 2.2 不做（范围外）

- 不做真实“在线客户端”：不做周期心跳、不做退出探测、不承诺游戏进程仍在线；
- 不做 FR-264 的防火墙能力：IP 临时封禁、key 暂停/限速、频道保护模式、security hello、防护中心不在本 FR；
- 不长期保存完整请求 / 响应 Header；
- 不保存 `X-Client-Key` 明文；
- 不恢复废弃缓存命中指标；
- 不引入外部 TSDB / ClickHouse / Prometheus；
- 不改 Worker / gRPC / Bot Worker。

## 3. 设计（怎么做）

### 3.1 并行开发边界

本 FR 与 FR-264 并行开发，必须遵守：

- 不删除、回滚、reset 当前工作区任何文件；
- 不修改 `docs/specs/client-dist-security-firewall/`；
- 不修改 FR-264 新增的 `client_security`、`client_dist_security`、`SecurityHello`、`ProtectionCenterPage` 等安全防火墙文件，除非后续集成时用户明确要求；
- 本 FR 的运行态心跳端点命名为 `/telemetry/heartbeat`，不占用 FR-264 的 security hello 语义；
- 如果同一文件不可避免冲突（例如 `router.go` 注册路由、`database.go` AutoMigrate），实现时只追加本 FR 必要行，保留 FR-264 已有改动，不重排、不格式化无关段落。

### 3.2 数据模型

新增文件建议：

```text
internal/controlplane/model/client_runtime_state.go
internal/controlplane/service/client_runtime_state.go
internal/controlplane/router/client_runtime_state.go
```

避免与 FR-264 的 `client_security.go` / `client_dist_security.go` 混用。

`ClientRuntimeState` 仅保存最新状态，不保存心跳明细，避免数据膨胀。若后续需要历史启动轨迹，再另开 FR。

`client_telemetry` 仍只保存更新结果，不新增 heartbeat result。

### 3.3 updater-core 心跳

在 `Core.run` 中完成基础 ctx 校验、machineId 生成、transport 构造后，读取本地状态 `StateStore.load(stateDir).lastSeenVersion()` 作为 `localVersion`，best-effort 调用 `transport.postHeartbeat(...)`。

要求：

- 心跳超时短于 telemetry，上限建议 2s；
- 异常吞掉，不影响更新；
- `telemetry=false` 时是否关闭 heartbeat：推荐关闭，因为运行态仍属客户端遥测；
- 心跳不写 `client_telemetry`。

`HttpTransport` 新增 `postHeartbeat`，路径：

```text
/client-channels/{channel}/telemetry/heartbeat
```

### 3.4 后端聚合口径

#### 客户端 Tab

- `recentStarted`：`last_heartbeat_at >= now - 5min` 的去重行数；
- `todayStarted`：当天 `last_heartbeat_at` 或 `first_seen_at` 命中口径需实现前固定。推荐：今日有心跳，即 `last_heartbeat_at >= todayStart`；
- `runtimeVersionDist`：按 `local_version` group；
- `coreVersionDist`：按 `core_version` group；
- `platformDist`：按 `platform` group；
- `launcherDist`：按 `launcher` group；
- `lagDist`：按频道 latestVersion - localVersion，低于 0 归 0；
- 更新结果趋势：仍查 `client_telemetry`，按 range 聚合。

#### 统计 Tab

统计 Tab 可复用现有 `/client-dist/observability`，但必须在前端和服务层停止展示废弃缓存命中指标，并把 successRate 语义改为请求成功率。若现有端点无法表达请求失败率，则在本 FR 中扩展或新增请求统计端点。

推荐实现：新增轻量查询方法，直接基于 `client_dist_events` / 快照返回请求口径：

- `manifestPulls`
- `artifactPulls`
- `downloadBytes`
- `activeMachines`
- `requestSuccessRate`
- `requestFailureRate`
- `statusDist`
- `errCodeDist`
- `targetVersionDist`
- `topIps`

#### 监控 Tab

`/client-dist/realtime` 直接查近窗明细即可，范围固定近 1h / 24h，数据量受 14 天保留约束，查询可控。

### 3.5 API 草案

#### POST /client-channels/:id/telemetry/heartbeat

- 关联 FR：FR-265；
- 鉴权：拉取密钥绑定频道；
- 成功：202；
- 错误：401 `INVALID_CLIENT_KEY`，404 `CHANNEL_NOT_FOUND`，500 `INTERNAL_ERROR`；
- 不记录审计；
- 可被 FR-264 防护入口包裹，但本 FR 不实现防护策略。

#### GET /client-dist/clients

- 关联 FR：FR-265；
- 鉴权：JWT 平台管理员；
- 审计：`client_dist_clients.query`；
- query：`channelId?`、`range=24h|7d|30d|90d`；
- 返回客户端运行态分布 + 更新结果趋势。

#### GET /client-dist/realtime

- 关联 FR：FR-265；
- 鉴权：JWT 平台管理员；
- 审计：不强制每 30s 记录，否则刷屏。推荐只对详情查询审计；
- query：`channelId?`；
- 返回近实时请求健康度。

#### GET /client-dist/events/:id

- 关联 FR：FR-265；
- 鉴权：JWT 平台管理员；
- 审计：`client_dist_event.detail`；
- 返回单条请求事件详情 + 脱敏白名单 Header。

#### GET /client-dist/events/search（推荐）

- 关联 FR：FR-265；
- 鉴权：JWT 平台管理员；
- query：继承 `/client-dist/events` 现有筛选并增加 runtime 维度与分页；
- 返回分页对象。

### 3.6 前端实现约束

- 页面：继续使用现有 `web/src/pages/ClientDistMonitoringPage.tsx`，但实现时必须先检查 FR-264 是否改过该页；若 FR-264 已改动，需基于最新文件增量编辑，不覆盖；
- API：建议新增 `web/src/api/clientDistObservability.ts` 或扩展现有 clientDist API 文件，避免和 FR-264 的 `clientDistSecurity.ts` 混用；
- i18n：只追加 `clientDistObservability` / `clientDistMonitor` 相关键，不改 FR-264 防护中心键；
- DOM 测试：新增或扩展 `ClientDistMonitoringPage.dom.test.tsx`，覆盖四 Tab、筛选、联动、废弃缓存命中指标不出现。

### 3.7 文档演进

实现完成时同步：

- `docs/API.md`：新增 / 修订端点；
- `docs/ARCHITECTURE.md`：新增 `client_runtime_states` 表与四 Tab 数据边界；
- `docs/PRD.md`：登记 / 更新 FR-265；
- `CHANGELOG.md`：未发布段记录；
- 如修改 ADR-049 决策正文不可直接编辑，应新增 ADR-055（若编号仍可用）取代或补充 ADR-049，并把 ADR-049 状态行标记 superseded。

## 4. 任务拆分

- [x] PRD 登记 FR-265：一条大型增强 FR，指向本 spec，状态已同步为已交付；不改 FR-264。
- [x] 后端测试先行：`ClientRuntimeStateService` upsert / 聚合查询 / range 口径；`ClientDistTrackingService` 日志详情白名单 / runtime 维度筛选 / 分页。
- [x] 新增 `ClientRuntimeState` 模型 + AutoMigrate；在 `database.go` 追加模型，不删除或重排 FR-264 模型。
- [x] 新增心跳 router/service：`POST /client-channels/:id/telemetry/heartbeat`，频道绑定拉取密钥鉴权，202 best-effort。
- [x] updater-core 新增 `postHeartbeat`：启动 early heartbeat，短超时，`telemetry=false` 关闭，失败不影响更新。
- [x] 新增 `GET /client-dist/clients` 聚合端点 + router 测试。
- [x] 扩展请求事件记录白名单详情：method/path/requestHeaders/responseHeaders/etag/errReason，确保 `X-Client-Key` 不明文落库。
- [x] 新增事件详情端点 `GET /client-dist/events/:id` + 测试审计和权限。
- [x] 新增 realtime 查询端点 `GET /client-dist/realtime` + 测试近 1h KPI / 24h 速率 / 最近错误 / TOP IP。
- [x] 新增分页搜索端点 `GET /client-dist/events/search` + runtimeVersion/coreVersion/platform/lag 筛选。
- [x] 前端四 Tab 改版：统计 / 监控 / 日志 / 客户端；清理废弃缓存命中指标展示；补齐联动。
- [x] MSW mock 补新端点；DOM 测试覆盖四 Tab、筛选、联动、废弃缓存命中指标不出现、非管理员门控。
- [x] 文档同步：API / ARCHITECTURE / PRD / CHANGELOG / spec 已同步。
- [x] 验证：Go 后端相关测试、updater-core Gradle 测试、前端 lint / vitest / DOM 测试已通过；真机验收待用户在本地环境执行。

## 5. 验收标准

1. 页面展示四个 Tab：统计、监控、日志、客户端；仅平台管理员可访问，非管理员不发起相关请求。
2. 统计 Tab 只展示分发请求指标：manifest 请求、artifact 请求、下载字节、请求客户端、请求成功率、请求失败率、状态码/错误码/TOP IP/目标版本分布。
3. 监控 Tab 只展示近实时请求健康度：近 1h 请求 KPI、错误请求数、近 24h 请求速率、最近错误、TOP IP，并 30s 自动刷新。
4. 日志 Tab 可按频道、类型、结果、错误码、version、artifactSha、IP、machineId、runtimeVersion、coreVersion、platform、lag 筛选，并支持分页。
5. 日志详情只展示白名单 Header，`X-Client-Key` 不以明文、密文或可还原形式落库 / 返回。
6. 客户端 Tab 展示近 5 分钟启动客户端、今日启动客户端、更新成功率、更新失败率、运行版本分布、core 版本分布、平台分布、启动器分布、版本滞后分布、更新结果趋势。
7. 启动心跳写入 `client_runtime_states`，同一 `channel_id + machine_id` 重复上报只更新最新态，不增加重复行。
8. 心跳端点要求频道绑定拉取密钥；错误密钥返回 401，不能用其他频道有效 key 上报本频道运行态。
9. updater-core 心跳失败、超时或服务端 5xx 时，更新与游戏启动不受影响。
10. `client_telemetry` 不出现 heartbeat result；更新成功率只由 success / fail-static / rolled-back / error 计算。
11. 页面和 API 不再展示废弃缓存命中率、命中数、未命中数；DOM 测试断言对应文案不出现。
12. 点击统计 / 监控 / 客户端中的可联动项，能切换到日志 Tab 并带入对应筛选。
13. 后端测试覆盖运行态 upsert、客户端聚合、realtime 聚合、日志详情脱敏、runtime 维度筛选、权限控制。
14. 前端测试覆盖四 Tab 渲染、非管理员门控、筛选、联动、空态、错误态。
15. 文档与代码一致：API、ARCHITECTURE、PRD、CHANGELOG 同步；若 ADR-049 被取代，新增 ADR 而不是直接改其决策正文。
16. **真机验收**：使用测试频道启动一次客户端，心跳后“客户端”Tab 能看到该机器的 localVersion/coreVersion/platform；制造一次错误拉取后，“监控”Tab 最近错误出现，点击可跳“日志”Tab 查看脱敏详情。

## 6. 风险 / 待定

- **与 FR-264 并行冲突**：FR-264 正在改 updater-core、router、database、前端导航与防护中心。本 FR 实现时必须基于最新工作区增量编辑，不能覆盖 FR-264 改动；如同一文件冲突，先暂停报告。
- **旧 `/client-dist/events` 兼容**：现有前端 hook 期望数组。推荐新增 `/client-dist/events/search` 分页端点，保留旧端点数组响应。
- **今日启动口径**：推荐按 `last_heartbeat_at >= 今日 00:00 UTC/本地?`。正式实现建议统一 UTC，与其他分发统计一致。
- **运行态表只存最新态**：无法回看历史启动轨迹。当前为 MVP 选择，避免心跳明细膨胀。
- **Header 脱敏**：必须集中写白名单函数并单测覆盖，避免未来新增 Header 时误落密钥。
- **心跳与 `telemetry=false`**：推荐 `telemetry=false` 同时关闭启动心跳，尊重客户端遥测 opt-out；若运营需要强制运行态，需另行设计告知与配置，不在本 FR。
