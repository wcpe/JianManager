# ADR-048: 统一通知模型（站内信 + 告警合并为一条通知流，视图聚合既有两表）

- **日期**: 2026-06-29
- **状态**: accepted
- **关联**: FR-216（通知中心）；FR-215（观测导航重构，留下告警过渡位与页眉铃铛交接）；[ADR-040](040-global-task-center.md)（站内信 `notifications` 源）；FR-011/FR-085（告警 `alert_events` 源）

## 背景

平台此前有两套独立的「给人看的通知」：

1. **站内信（`notifications`，ADR-040 / FR-183）**：投递给**某个用户**的**定向消息**（任务完成/失败、节点上线等），按 `user_id` 归属隔离，字段 `level(info/success/warning/error) + title + body + task_id + read_at`。页眉有独立「收件箱」入口（`NotificationInbox`，挂 FR-183 留位）。
2. **告警事件（`alert_events`，FR-011/FR-085）**：由告警引擎按规则触发的**系统警报**（指标越限、实例崩溃、节点离线、日志关键字…），**面向全体运维**（非定向到某用户），字段 `level(info/warn/critical) + trigger_type + message + fired_at + count + resolved + acknowledged + read`。页眉有独立「告警铃铛」入口（`AlertBell`，链 `/alerts`）。

两者在用户心智里都是「我需要被通知的事」，但页眉**两个铃铛**、入口分散；FR-215 把告警留作观测域「过渡位」，明确**由 FR-216 接手**合并为统一「通知中心」（页眉单铃铛 + 独立页 + 侧栏「系统/账户与审计」一个链接）。

需要一个决策：两套数据源**如何合并**为一条「通知流」，且**不丢各自语义**（定向消息 vs 系统警报、各自的级别枚举、各自的已读/确认口径）。

## 决策

### 1. 合并形态：视图聚合既有两表，**不新建统一表**

在 Control Plane 新增一个**只读聚合服务** `NotificationFeedService`，在**查询时**把 `notifications` 与 `alert_events` 两表合并为统一 DTO `NotificationItem` 列表，**不引入新的物理表、不做双写、不做存量回填**。

理由（按改动面权衡）：

- **写入源已成熟且语义各异**：站内信由 `TaskService` 终态副作用写、按 `user_id` 投递；告警由告警引擎按 `dedup_key` 聚合 upsert、面向全体。强行并到一张表需要双写或触发器同步，徒增一致性风险与迁移成本，**收益仅为「读时少一次 union」**。
- **YAGNI**：通知中心是**读 + 标记已读**的消费视图，不需要对「通知」做跨源的二次写入/编辑。聚合视图足以满足页眉预览、独立页筛选/查询/分页/已读。
- **可演进**：若未来出现「第三类通知源」或需要跨源持久化标记，可在本 ADR 之上新增 ADR 升级为物化表，届时聚合服务的 DTO 契约即现成的迁移目标。

> 决策即：**统一在读侧（聚合服务 + 统一端点），不统一在写侧/存储侧**。架构不变量不破（仍 CP 读写 DB，Worker 不直连）。

### 2. 统一 DTO 与语义对齐（不丢各自语义）

统一条目 `NotificationItem`（API 出参）字段及两源映射：

| 统一字段 | 含义 | 站内信（message）映射 | 告警（alert）映射 |
|---|---|---|---|
| `source` | **来源判别**（核心区分位） | `"message"` | `"alert"` |
| `id` | 源表内主键（同 source 内唯一） | `notifications.id` | `alert_events.id` |
| `level` | 级别（**对齐到统一枚举**，见下） | 原值 info/success/warning/error | warn→warning、critical→error、info→info |
| `title` | 标题 | `title` | 规则名（`rule.name`，缺省 `#<ruleId>`） |
| `body` | 正文 | `body` | `message` |
| `read` | 是否已读 | `read_at != NULL` | `read` 字段 |
| `createdAt` | 发生时间（统一排序键） | `created_at` | `fired_at` |
| `taskId` | 关联任务（仅 message 有） | `task_id` | 空 |
| `triggerType` | 触发类型（仅 alert 有） | 空 | `trigger_type` |
| `acknowledged` | 是否已确认（仅 alert 有） | 否（消息无确认概念） | `acknowledged` |
| `resolved` | 是否已恢复（仅 alert 有） | 否 | `resolved` |

**级别统一枚举**：通知流对外统一用站内信的四档 `info/success/warning/error`（消费端无需识别两套）。告警三档按严重度**就近映射**：`warn→warning`、`critical→error`、`info→info`（无 success）。原始告警级别仍可经 `triggerType` + 详情页（`/alerts`）保留完整语义，聚合视图只做展示级归一。

**「不丢语义」保证**：

- `source` 永远在出参里，前端据此**分组/筛选**（消息 / 告警两类筛选）、决定可执行动作（消息可「标记已读」；告警可「标记已读」，更深的"确认/认领"仍走 `/alerts` 详情）。
- 定向 vs 广播：`message` 经 `user_id` 严格归属隔离（只见自己的）；`alert` 面向全体运维（与既有 `/alerts` 一致，不按用户隔离）。聚合服务**分别**按各自归属规则取数后再合并。

### 3. 已读 / 筛选 / 保留口径

- **已读（读取状态）**：沿用各源既有语义，不引入新存储位。
  - 标记单条已读：`source=message` → `notifications.read_at`（`NotificationService.MarkRead`，按 user 归属）；`source=alert` → `alert_events.read`（`AlertService.MarkRead`）。
  - 标记全部已读：分别调两源的「全部已读」（站内信按当前用户、告警全局），合并返回受影响数。
- **未读计数（页眉角标）**：`当前用户未读站内信数 + 全局未读告警数`，由聚合服务一次返回（`GET /notifications/feed/unread-count`），替代页眉原先**两个**未读计数查询。
- **筛选**：统一端点支持 `source`（`message`/`alert`/空=全部）、`unread`（仅未读）、`keyword`（标题/正文模糊）。各自下推到对应表查询（不在内存全量过滤大表）。
- **分页**：两源各取「页所需上界」后归并排序（按 `createdAt` 倒序）再切页，返回 `items + total`（`total = 两源命中数之和`）。保留窗口沿用各源现状（站内信/告警事件均不在本 FR 引入额外清理）。

## 影响

- **新增**：`NotificationFeedService`（聚合只读）+ 统一端点 `GET /notifications/feed`、`GET /notifications/feed/unread-count`、`POST /notifications/feed/read-all`、`POST /notifications/feed/:source/:id/read`。挂在认证用户组（非管理员只见自己的站内信 + 全局告警，与既有 `/alerts` 可见性一致）。
- **保留不动**：`notifications`、`alert_events` 两表与各自既有端点（`/notifications/*`、`/alerts/*`）与写入路径**全部保留**——`/alerts` 仍是告警规则/通道/事件管理页的后端，`/notifications/*` 既有端点保留（向后兼容，前端站内信旧入口被通知中心取代后可逐步弃用，但端点不删）。
- **前端**：页眉**两个铃铛（`AlertBell` + `NotificationInbox`）合并为一个**「通知」铃铛（消费统一 feed + 未读计数，下拉预览混合流）；新建「通知中心」页（按 source 筛选 + 已读 + 关键字 + 分页）；侧栏告警过渡位收口为「通知中心」入口，置「系统/账户与审计」。
- **无迁移**：不动 schema、不回填。

## 备选与拒绝

- **新建统一 `notifications_unified` 物化表 + 双写/同步**：拒绝。写侧改动大、引一致性风险，收益仅省一次读时 union；违背 YAGNI。可作为未来演进（新 ADR）。
- **把告警直接写一份进 `notifications`**（告警触发即投递站内信给所有人）：拒绝。会按用户数放大告警写入（N 用户 × M 告警）、污染站内信表，且与告警的 dedup/ack 语义割裂。
- **只在前端合并两套既有端点**（不加后端聚合端点）：拒绝。分页/排序/未读合计需要跨源归并，放前端会产生「两个分页游标对不齐」的取数错误与多次往返；聚合在后端一次完成更稳。
