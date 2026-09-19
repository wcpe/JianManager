# 功能规格：分发机器级更新清单与钻取（FR-426）

> 状态：✅ 已交付@v0.22.0（随 FR-430 运维页）　·　关联 PRD：**FR-426**　·　关联：FR-217 / FR-265 / FR-430 / ADR-088
> 落点：`/client-dist-ops` 运维页「机器 · 客户端」Tab（`OpsClientsTab` + `MachineListPanel` + `ObsOverviewSection`）

## 1. 背景与目标

运营需要按时间段看到「哪些机器在更新、更新了几次、版本是否滞后」，并钻取单机更新时间线；此前机器维度信息分散在监控/客户端页，无统一清单。

## 2. 需求

- 运维页「机器 · 客户端」Tab：按当前时间窗列出机器（machineId / 更新次数 / 最近更新 / 版本滞后等）。
- 支持排序与翻页（服务端窗口：明细约 14 天内精确，超窗小时桶近似时 UI 明示）。
- 点选机器 → 该机器更新事件时间线（时间 / 版本 / 结果），并可沿 FR-359 query 深链。
- machineId 展示脱敏（对齐 FR-265）。
- 可选 CSV 导出沿用 FR-361 体系。
- 路由与权限：随 `/client-dist-ops`，`dist.ops.read`（FR-432）。

## 3. 设计

- 数据：observability / machines 相关 hook（`useClientDistObservability`、runtime overview 等）+ 时间窗 `from/to`。
- UI：`MachineListPanel` 清单 + 下钻详情；与 `ObsOverviewSection` 共用时间筛选。
- 不改后端 schema；复用 FR-217 时序与聚合端点。

## 4. 任务

- [x] 机器清单与时间窗联动（随 FR-430 Tab 落地）
- [x] 下钻时间线 / 深链
- [ ] 独立后端 machine 明细端点增强（若与现网聚合口径不一致，另开 FR）
- [ ] 发版后 PRD 标 `已交付@vX.Y.Z`

## 5. 验收

1. 运维页机器 Tab 在给定时间窗内可列出机器并排序。
2. 点选机器可看到更新事件时间线。
3. 超窗近似有 UI 提示；无数据空态可读。
4. 无 `dist.ops.read` 的用户 API/UI 不可写运维处置（读节点门禁见 FR-432）。

## 6. 范围外

- CDN/多源站机器画像重建；替代 FR-265 客户端运行态心跳。
