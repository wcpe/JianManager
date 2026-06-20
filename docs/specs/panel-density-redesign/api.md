# API Spec — FR-061 面板信息密度与视觉改造

> 关联 FR: FR-061 | 优先级: P1 | 状态: 📋 todo | 关联 ADR: ADR-009（运维控制台 Shell，本 FR 在其内演进侧栏 IA）

## 概述

本 FR 为**纯前端重构**，参考 baota 把面板做成高密度运维界面：**不新增后端 API**，复用既有 REST/WS + FR-060 的 `/metrics`。本文档定义前端「设计系统契约」——设计 token、组件契约、信息架构（IA）——作为各页面套用的统一接口。

约束（架构不变量）：仍基于 shadcn/ui + Tailwind + OKLCH，仅扩展 token + 新增高密度组件变体，**不引入新 UI 框架，不改后端行为**。

## 设计 token（扩展 `web/src/index.css` OKLCH 变量）

### 密度档位
- 间距/行高/字号整体下调一档：表格行高 ~32px、卡片 padding ~12–16px、正文 13px、次要 12px、标签 11px（**不低于 11px**）。
- 统一圆角与 0.5px 边框。

### 状态色系（新增，替代纯灰阶）
- `--status-success / --status-warning / --status-danger / --status-info`（亮/暗双值）。
- **阈值规则**——资源类：`<50%` success、`50–80%` warning、`>80%` danger；TPS：`≥18` success、`15–18` warning、`<15` danger。

### 主色
- MC 绿作为 `--primary`（导航高亮、主按钮、logo）；状态色独立于主色，不复用。

## 组件契约（`web/src/components/ui` + `console` + `charts`）

| 组件 | 契约 |
|---|---|
| 多级侧栏 `ConsoleSidebar` | 常驻、分组可展开、激活态高亮；**保留实例树快速访问 + 节点切换器**；用户/组/审计收入菜单 |
| 环形仪表盘 `ResourceGauge` | props: `label/value/max/unit/threshold`；按阈值变色；用于节点/总览 |
| 分区面板 `Panel` | 标题栏（标题 + 头部操作）+ 内容区；统一边框/圆角 |
| 密集表格 `DataTable`（dense 变体） | 紧凑行高 + 行内「操作」链接 + 状态徽章 + 迷你资源条 |
| 迷你资源条 `MiniBar` | `value/threshold` → 阈值变色细条 |
| 状态徽章 `StatusBadge` | 运行/停止/异常… → 状态色 |
| 时间区间选择器 `RangePicker` | `1h/6h/24h/7d/30d/90d`；供 FR-060 图表 + 监控页复用 |
| 历史曲线 `TimeSeriesChart` | recharts 封装；多序列、null 断点、tooltip(avg/min/max)；FR-060 复用 |

> `RangePicker` 与 `TimeSeriesChart` 是 FR-060 前端的依赖——本 FR Phase 2 产出，FR-060 消费。

## 信息架构（IA）

多级侧栏分组（替换三段式，能力不丢）：
- 总览 / 节点 / 实例（展开：全部实例、群组与代理）/ 监控（资源、告警）/ Bot / 文件 / 计划任务 / 备份 / 制品库 / 设置（展开：用户、用户组、审计、系统设置）。
- **实例树**：作为「实例」组的可展开子区或常驻面板，保留状态点 + bot 聚合徽标。
- **节点切换器**：保留于实例树顶部。

## 复用的既有 API（无新增）
- 实例/节点/Bot/告警/备份/审计… 既有 REST。
- 终端 WS、实例事件 SSE。
- FR-060：`GET /metrics/series`、`GET /metrics/overview`。

## 一致性
- 与 ADR-009（运维控制台为主 Shell）一致——本 FR 在其内演进侧栏 IA，不推翻 Shell 决策。
- 与 FR-037（控制台布局）、FR-026（shadcn/ui 标准化）、FR-016（i18n）、FR-060（仪表盘）协同。
- 架构不变量：纯前端，不改通信边界与后端行为。
