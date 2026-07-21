# 功能规格：客户端分发 KPI 口径与共享语义

> 状态：草拟　·　关联 PRD：FR-356　·　分支：feature/fr-356-kpi-semantics  
> 增强：FR-095 / FR-217 / FR-218 / FR-219 / FR-265　·　相关 ADR：ADR-023、ADR-049（只读引用，本 FR 不新开 ADR 除非推翻公式）

## 1. 背景与目标

频道工作台「统计」Tab（`ClientStatsPanel`）与「分发监控」统计区（`ClientDistMonitoringPage`）同时展示活跃客户端、成功率等 KPI，但：

- 标签混用「下载量 / 更新成功率 / 活跃客户端」，运营分不清 **请求成功** vs **更新成功**；
- 活跃数有时来自 `/client-dist/stats.activeMachines`，有时来自 `/client-dist/observability.summary.activeMachines`（含 `activeMachinesExact`），脚注不一致；
- 同一语义在两页可能不同 i18n key / 不同兜底数据源，后续 FR-357/358/361 无法共用字典。

本 FR 目标：**统一 KPI 字典（字段名、公式、精确|近似语义、UI 标签/脚注）**，并让两页共享同一展示语义（可抽共享组件或共享 formatter）。允许为缺字段做**只读聚合补全**，不重做观测底座。

## 2. 需求（要什么）

### 范围内

1. **KPI 字典**（文档 + 代码常量/类型注释一致），至少包含：

| 逻辑名 | 权威来源（优先） | 公式 / 语义 | UI 标签约定（中文） | 备注 |
|---|---|---|---|---|
| `activeClients` | observability.summary.activeMachines | 窗内 machineId 去重；`activeMachinesExact=true` 为精确独立数，`false` 为桶人次求和近似 | 「活跃客户端」+ 脚注「精确去重」/「人次近似」 | 不可信机器码，仅统计 |
| `updateSuccessRate` | observability.summary.successRate | `updateSuccess / updateTotal`（分母 0 → 0） | **「更新成功率」** | 来自遥测 result，**不是** HTTP 下载成功 |
| `updateFailStaticRate` | summary.failStaticRate | `updateFailStatic / updateTotal` | 「fail-static 率」+ 脚注「断网兜底启动」 | |
| `updateRollbackRate` | summary.rollbackRate | `updateRolledBack / updateTotal` | 「回退率」 | |
| `downloadRequests` | stats.downloads[].requests 或 summary.manifestPulls+artifactPulls（页内注明） | 拉取/下载**请求次数** | 「下载请求数」或「拉取次数」 | **禁止**标成「更新成功」 |
| `downloadBytes` | summary.downloadBytes / stats.downloads[].bytes | 响应字节合计 | 「下载流量」 | 绝对数展示留给 FR-357 加厚 |
| `manifestPulls` / `artifactPulls` | summary 同名 | 计数 | 「Manifest 拉取」「制品拉取」 | 监控页已有则对齐标签 |
| `securityBadge`（微标语义占位） | 本 FR 只定义**展示语义占位**（如风险等级文案键），**不实现**安全处置 | — | 与 FR-358 对齐时使用同一 key 名 | 无数据时不显示假绿 |

2. **双页对齐**
   - `ClientStatsPanel` 与 `ClientDistMonitoringPage` 统计区：同一 KPI 同一标签、同一公式、同一精确/近似脚注。
   - 数据源优先级写死：有 observability summary 时以其为准；仅 stats 回退时脚注不得写「精确去重」。
3. **共享实现**
   - 抽 `lib/client-dist-kpi.ts`（或等价）集中：字段类型、`formatRate`、`activeClientsHint(exact)`、标签 i18n key 映射。
   - 可选抽小组件 `ClientDistKpiCards`；若抽组件，两页共用，禁止复制粘贴第二套公式。
4. **只读补全（可选）**
   - 若某页缺绝对数字段而另一侧 API 已有，允许前端统一取 observability；**仅当**两边都缺且产品必需时，才允许后端在现有 stats/observability 响应中补只读字段（不改写时路径、不改表结构优先）。
5. **文案**
   - 明确区分：「请求成功/失败」（dist events）vs「更新成功/失败」（telemetry result）。
   - zh/en i18n 同步关键标签与脚注。

### 不做（范围外）

- 错误码 TopN、日志列加厚、分布点击钻取 → **FR-357**
- 安全画像处置、频道安全摘要条 → **FR-358**
- 跨页 query 深链 → **FR-359**
- 遥测 schema 新字段 / 隐私 opt-out 改契约 → **FR-360**
- CSV 导出 → **FR-361**
- 告警联动、重写 ADR-049 小时桶机制

## 3. 设计（怎么做）

### 3.1 模块

| 层 | 改动 |
|---|---|
| 前端 | `apps/control-plane-web/src/lib/client-dist-kpi.ts`（新建）；`ClientStatsPanel.tsx`；`ClientDistMonitoringPage.tsx`（统计 KPI 区）；相关 i18n；dom 测试对齐标签/脚注 |
| 后端 | **默认不改**；仅当缺字段无法前端补齐时，最小扩展 `GET /client-dist/stats` 或 observability summary 只读字段，并更新 `docs/API.md` |
| 文档 | 本 spec；`docs/API.md` 增加「KPI 语义」短表或交叉引用；CHANGELOG 一条；PRD FR-356 状态 |

### 3.2 数据源优先级（前端硬约定）

```
activeClients / *Rate  →  obs.summary 优先，stats 回退（回退时 exact 视为 unknown，不展示「精确去重」）
download trend         →  stats.downloads（按日）与监控页 series 并存，标签均用「下载请求」语义
```

### 3.3 与 FR-360 边界

- 本 FR **不**修改 `client_telemetry` 表、不上报新字段。
- 展示层预留 `coreVersion` 等列位可留给 360；356 只锁 KPI 率/活跃/下载语义。

### 3.4 ADR

- 不新开 ADR；若发现必须推翻 ADR-049 去重口径 → **停工报告**，不在本 FR 静默改公式。

## 4. 任务拆分

- [ ] 落地 KPI 字典（`lib/client-dist-kpi.ts` + 注释/类型与本 spec 表一致）
- [ ] 统一 i18n 标签/脚注（zh + en）
- [ ] 改造 `ClientStatsPanel` 使用共享语义
- [ ] 改造 `ClientDistMonitoringPage` 统计 KPI 使用同一语义
- [ ] 单测/DOM 测：关键标签字符串、精确/近似脚注、分母为 0 的率
- [ ] （可选）后端只读字段补全 + API 文档
- [ ] 文档同步：PRD 状态、ARCHITECTURE 一句（若有共享模块描述）、API KPI 表、CHANGELOG 末尾追加

## 5. 验收标准

- [ ] 存在可检索的 KPI 字典（代码模块 + API/本 spec 一致）：至少覆盖 `activeClients`、`updateSuccessRate`、`updateFailStaticRate`、`updateRollbackRate`、下载请求/流量语义。
- [ ] 同频道、同时间窗语义下：两页「更新成功率」「活跃客户端」数值与脚注规则一致（允许因 range 枚举映射差异导致窗边界不同——须在 UI 或注释标明窗来源；**同一 range 映射下数字必须一致**）。
- [ ] 「更新成功率」文案不得用于下载/manifest 请求成功率；下载侧标签不得写成「更新成功」。
- [ ] `activeMachinesExact=true` 显示精确脚注；`false` 显示近似；回退 stats 无 exact 时不谎报精确。
- [ ] 自动化：相关 vitest/dom 测试红→绿；`npx tsc -b` 通过（在 control-plane-web 惯例路径）。
- [ ] 真浏览器（mock）：两页截图或 Playwright 断言关键 KPI 标签可见且一致（至少 1 条跨页对照）。

## 6. 风险 / 待定

- `/client-dist/stats` 的 `successRate` 与 observability 的 `successRate` 历史实现若分叉，以 **observability + 本字典** 为准，并在 stats 回退路径加脚注或逐步废弃 stats 率字段展示。
- 监控页 `toApiRange` 将 1h/6h 映射为 24h，与频道 Tab 的 7/30/90 不完全同一选择器——本 FR 对齐**语义**，不强行统一选择器控件（控件统一可归 FR-359）。
