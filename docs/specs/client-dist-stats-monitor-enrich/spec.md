# 功能规格：分发统计与监控信息加厚 + 分布钻取

> 状态：已审　·　关联 PRD：FR-357　·　分支：feature/fr-357-stats-monitor-enrich  
> 增强：FR-095 / FR-218 / FR-219 / FR-249 / FR-265　·　依赖：**FR-356**（KPI 口径）　·　字段展示可渐进接入 FR-360

## 1. 背景与目标

频道统计 Tab 与分发监控页（统计/监控/日志/客户端）已有 KPI 与四 Tab 骨架，但运营日常排障仍缺：

- KPI **绝对数**（更新总次数、成功/失败计数）与下载 **bytes 趋势**；
- **错误码 TopN**、失败样例；
- 日志列过瘦（缺 player/machine/core/bytes/耗时/可读 err）；
- 分布条**不能点击过滤**日志/客户端；
- 空态混淆「无流量 / 未开遥测 / 时间窗外」。

本 FR 在 **不推翻 FR-265 四 Tab 边界**、**不改 FR-356 KPI 公式** 前提下加厚信息与钻取。

## 2. 需求（要什么）

### 2.1 范围内

1. **KPI 加厚（遵守 FR-356 字典）**
   - 请求侧：manifest/artifact 拉取数、下载 bytes（绝对值 + 趋势序列）。
   - 更新侧（客户端 Tab 或频道统计观测区）：`updateTotal` / `updateSuccess` / fail-static / rolled-back / error **绝对数**与率并列展示。
   - 标签继续区分「请求成功率」vs「更新成功率」。

2. **错误码 TopN + 失败样例**
   - 基于 `client_dist_events`（失败 `status>=400` 或 `errCode` 非空）聚合 TopN（默认 10）。
   - 失败样例：最近 K 条（默认 20）失败事件摘要（时间/频道/kind/errCode/ip/machine 脱敏）。
   - 允许只读 API：扩展现有 stats/realtime/observability **或** 新 `GET /client-dist/error-summary`（平台管理员）。

3. **日志列与详情加厚**
   - 列表列：playerName（脱敏）、machineId（脱敏）、coreVersion（若可关联运行态/遥测）、bytes、durationMs、可读 errReason/errCode。
   - 详情结构化展示（非纯 JSON dump）；敏感 header 仍白名单脱敏（FR-265）。

4. **分布钻取**
   - 版本/平台/IP/错误码/lag 等分布条或榜单击 → 跳转日志或客户端 Tab，并带上筛选（query 键与 FR-359 对齐：`channelId`/`errCode`/`version`/`machineId`/`ip`/`tab` 等）。
   - 本 FR 至少完成 **页内 Tab 联动**；跨页深链完整互通可依赖 FR-359 收口。

5. **空态三类**
   - 无流量：窗口内 0 请求且 0 遥测。
   - 未开遥测：有请求但更新侧全空（可文案提示 `telemetry:false` 或无客户端上报）。
   - 窗外：请求超出明细保留窗导致活跃/精确去重不可用（沿用 `activeMachinesExact=false` 脚注，文案明确）。

### 2.2 不做

- 告警规则引擎 / 站内信联动  
- 重写 ADR-049 小时桶或 FR-265 四 Tab 数据源边界  
- CSV 导出 → **FR-361**  
- 安全一键封禁 / 频道安全摘要 → **FR-358**  
- 完整跨页 URL 状态机（可预留 query 键）→ **FR-359**  

## 3. 设计（怎么做）

### 3.1 后端

| 能力 | 建议落点 |
|---|---|
| 错误码 TopN | `ClientDistStatsService` 或 observability/realtime 扩展；或独立 `error-summary` 只读端点 |
| 失败样例 | 复用 events search（`outcome=failure`，limit/page） |
| 日志列字段 | events list/detail DTO 已有则补；player/core 可 left join 运行态/遥测 **best-effort**（不可信字段角标） |
| 权限 | 平台管理员 + 审计（观测类访问留痕，沿 FR-217） |

### 3.2 前端

- `ClientStatsPanel`、`ClientDistMonitoringPage` 四 Tab：加厚 KPI 卡、bytes 趋势、错误码面板、分布 onClick。
- 复用 `lib/client-dist-kpi`（FR-356）与 `lib/privacy-mask`（FR-360）。
- i18n zh/en；空态三类文案键。

### 3.3 依赖边界

- **硬依赖 FR-356** 标签/公式。  
- FR-360 字段：有则展示 core/locale 等；无则「—」，不阻塞本 FR。

## 4. 任务拆分

- [ ] 错误码 TopN + 失败样例 API/服务层 + 测试  
- [ ] 前端 KPI 绝对数 + bytes 趋势  
- [ ] 日志列加厚 + 详情结构化  
- [ ] 分布点击 → 日志/客户端筛选联动  
- [ ] 空态三类  
- [ ] vitest/dom + 可选 Playwright  
- [ ] 文档：API、ARCHITECTURE 一句、CHANGELOG、PRD 状态  

## 5. 验收标准

- [ ] 同窗同频道：绝对数与率自洽（分母 0 率=0，标签符合 FR-356）。  
- [ ] 有失败流量时错误码 TopN 非空且可点过滤日志。  
- [ ] 日志可见脱敏 machine/player、bytes、耗时、errCode/errReason。  
- [ ] 三类空态文案可区分。  
- [ ] 相关 Go/前端测试红→绿；tsc 通过。  
- [ ] mock 真浏览器：至少 1 条「点错误码 → 日志过滤」路径。  

## 6. 风险 / 待定

- 明细 14d 保留窗外错误码仅能来自仍保留的聚合/快照——窗外 TopN 可为「不可用」而非假 0。  
- player/core 关联是 best-effort，不可信，须角标。  
