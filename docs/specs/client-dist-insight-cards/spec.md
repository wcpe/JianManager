# 功能规格：分发概览洞察卡（FR-428）

> 状态：✅ 已交付@v0.22.0（随 FR-430 运维总览）　·　关联 PRD：**FR-428**　·　关联：FR-356 / FR-357 / FR-430 / ADR-088
> 落点：`/client-dist-ops` 运维页「总览」Tab（`OpsOverviewTab` + `InsightCards`）

## 1. 背景与目标

运维总览需要「一眼看懂分发是否健康」：更新次数/成功率/活跃机器/下载量等 KPI 带同环比与异常标记，而不是只有安全快照数字。

## 2. 需求

- 总览顶部 **InsightCards**：
  - 更新次数、更新成功率、活跃机器、下载字节、Manifest 相关等核心 KPI；
  - **同环比**（相对上一等长时间窗）；
  - 异常标记（突降 / 激增 / 长时间无更新等启发式）。
- **KPI 口径**严格 FR-356：更新类只信 observability，请求类只信 stats，**禁止**用 HTTP 成功率冒充更新成功率。
- 卡片附口径 tooltip（活跃机器精确 vs 近似等）。
- 时间筛选与页头统一时间窗联动（FR-425）。
- 权限：`dist.ops.read` / `stats.read`（FR-432）。

## 3. 设计

- 组件：`InsightCards.tsx` 消费运维总览聚合 hook；Sparkline/StatCard 复用既有 charts/ui。
- 数据：`clientDistObservability` + `clientStats` + error summary；对比窗口由前端按 from/to 推导。
- 异常标记为 UI 启发式，不替代告警规则。

## 4. 任务

- [x] InsightCards + 总览布局（随 FR-430 OpsOverviewTab 落地）
- [x] 同环比与口径 tooltip
- [ ] 独立后端 compare 字段若未齐，另开增强 FR
- [ ] 发版后 PRD 标 `已交付@vX.Y.Z`

## 5. 验收

1. 总览在选定时间窗展示核心 KPI 与同环比。
2. 更新成功率口径来自 observability，与 stats 请求类指标可区分。
3. 空数据/异常有可读提示。
4. 无读节点用户不可写运维处置。

## 6. 范围外

- 替代告警规则引擎；重写 FR-357 全部统计钻取。
