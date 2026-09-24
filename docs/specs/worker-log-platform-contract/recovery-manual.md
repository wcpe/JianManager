# 日志平台恢复与故障处置手册（FR-482）

> 受控文档：面向运维的日志平台（FR-472～483）切换、恢复与故障处理手册。
> 语义真源：`docs/specs/worker-log-platform-contract/spec.md`（FR-472 共享契约，已冻结）。
> 验收状态见 `runbook-checklist.md`；资产清单见 `../worker-victorialogs-runtime/asset-inventory.md`。

## 1. 适用范围与真源

- 覆盖 Worker 本地日志数据面（采集 / 归一化 / WAL / HOT VL / COLD Lifecycle / Deep Archive+Rehydrate）
  与 CP 联邦查询、入库切换、Legacy 只读。架构见 `docs/ARCHITECTURE.md`「Worker 日志平台数据面」。
- **CP 是浏览器唯一入口**；浏览器不得直连 Worker/VL。跨 Worker 调用只经 CP 反向 gRPC 隧道（ADR-081/090）。
- 能力不可用时**必须返回结构化 not-ready/unsupported**，禁止把空响应当作完整结果。

## 2. 合法部署组合

只允许以下组合（`spec.md` §6.5/§7）：

| 组合 | 说明 |
|---|---|
| HOT-only | 仅本地 HOT VL |
| HOT+COLD | 本地冷热分层 |
| HOT+DEEP | HOT + 对象存储归档 |
| HOT+COLD+DEEP | 完整三层 |

- `cold` 关闭且 archive 开启时，必须**显式选择** Raw-only 或 rehydrate 策略，不得隐式丢弃历史。
- `HOT cache` 预算（`log_vl.hot_cache_bytes`，模板 512MiB）是缓存上限，**不等于** Worker 日志数据面 RSS 上限。

## 3. 资源预算与阈值

- Worker 日志总预算覆盖 WAL、HOT、COLD、staging、Rehydrate、canonical projection、查询并发、临时导出空间和 RSS。
- 磁盘阈值（`log_capacity`，契约默认）：`degraded_at_percent=80` 进入降级；`pause_at_percent=90` 暂停不可恢复写入。
- 单 Worker 日志数据面 RSS 目标 ≤ 1GiB（不含文件系统 page cache，`spec.md` §6.6）。
- WAL/staging/临时 Export 预留 ≤ 总日志预算的 25%。

## 4. 数据损失边界

- **同盘进程崩溃**（Worker/VL 强杀、重启）：可从持久账本 + WAL + 已登记受管恢复分段恢复，正常重启不丢已 `durable` 事件。
- **整盘 / 整机损失**：本地 WAL+Raw **无法推导零损失**；必须依赖 Deep Archive 归档副本或外部备份作为独立证据。
- HTTP 2xx **不构成**回收依据；未知投递结果的 WAL 前缀在恢复来源未解除前**不得回收或删除**。

## 5. 运维与管理入口

CP HTTP 管理面（默认全部经 `node.manage`/`log.read` 权限；cutover 默认关闭）：

| 用途 | 端点 |
|---|---|
| 联邦查询门面 | `GET /api/v1/logs/federation`（+`/search`、`/stats`、`/fields`、`/facets`、`/tail`、`POST /export`）|
| 切换状态 / 更新 | `GET`/`PUT /api/v1/logs/cutover` |
| Legacy 只读 | `GET /api/v1/logs/legacy` |
| 受管 VL 运行时状态 | `GET /api/v1/nodes/:id/log-runtime` |
| 运行时控制（含 install/启停） | `POST /api/v1/nodes/:id/log-runtime/:namespace/:action` |
| 触发分区迁移 | `POST /api/v1/nodes/:id/log-runtime/migrate` |
| 解决采集缺口 | `POST /api/v1/nodes/:id/log-runtime/ingest/resolve-gaps` |
| 归档状态 / 触发回灌 | `GET /api/v1/nodes/:id/log-archive/status`、`POST /api/v1/nodes/:id/log-archive/rehydrate` |
| 上传审批 VL 资产 / 安装 | `POST /api/v1/log-runtime/assets/:os/:arch`、`POST /api/v1/nodes/:id/log-runtime/install` |

Worker 侧配置键（`worker.yml`，零配置默认关闭）：`log_sources`、`log_query`、`log_vl`、`log_capacity`、`log_archive`（`secret_key` 只允许环境变量注入）。

## 6. 故障处置流程

### A. 投递结果未知（`delivery_state=UNKNOWN`）
- 触发：HTTP 请求已发出、响应丢失（如 VL 在确认窗口重启）。
- 处置：保留受管恢复分段，按 `event_id`/范围核验；无法确认则重放并记录可能重复。**不得**进入坏记录隔离，**不得**据此推进 `reclaim_position`。

### B. WAL 回收与恢复责任
- `reclaim_position` 仅在受管恢复分段完成持久提交、校验、登记（`WAL_RESPONSIBILITY_TRANSFERRED`）后推进。
- 释放证明（`PROJECTION_BACKED` / `NEXT_COPY_VERIFIED` / `RETENTION_EXPIRED_WITHOUT_HOLDS`）的接收者与理由必须持久记录；证明不完整、有 hold 或预算不足时**保留分段并降级**，后台清理不得自行推断释放理由。
- `/internal/force_flush` 只可用于可查询性实验，**不是**耐久凭证。

### C. Catalog 迁移崩溃
- 迁移链：`ROUTING_FROZEN → DRAINING → SNAPSHOTTING → STAGING_VERIFY → ATTACHED_STAGING → OWNER_SWITCHED → QUERY_LEASE_DRAINING → DETACHED → CLEANED`。
- 启动先恢复 Catalog/journal 再处理 VL 残留目录；每个中断状态按 `Recover` 给出的 `next_action` 续跑。
- `ATTACHED_STAGING` 即使被 VL 自动 attach，也**必须被 QueryPlanner 排除**；`DETACHED` ≠ 完成，残留目录仍由 Catalog 过滤。
- 迟到事件按 Catalog owner/generation 路由，禁止按 `now-7d` 重新生成 HOT 权威。

### D. VL 进程 / 分区
- VL 进程 health、分区恢复完成、查询范围完整可用是**三个独立状态**；不得用目录存在或 HTTP health 代替 Catalog 一致性。
- 受影响范围进入 `RECOVERY_REQUIRED`/`PARTIAL` 并暴露缺口；其余范围继续服务。

### E. 磁盘满
- 磁盘 ≥ 80% 降级、≥ 90% 暂停不可恢复写入；暂停期间暴露缺口与人工动作。
- 人工动作：清理已 `RELEASED`/`CLEANED` 的产物、扩容或迁移 COLD/Deep，然后经 `ingest/resolve-gaps` 收口。

### F. 归档对象损坏 / 上传中断（Deep）
- 对象上传中断、manifest 损坏：按结构化状态重试；重复恢复**幂等**。
- 只有复制、校验、manifest 登记成功的受管 Raw 才算恢复来源；manifest-only 不构成可清理证明。

### G. Rehydrate 失败 / 超时
- 任务化：`task_id` + 租约 + 并发合并键 + 取消 + 超时 + 磁盘预留 + generation 隔离；重复请求复用在途任务。
- 恢复不改原始 `_time`/`event_id`/`ingest_seq`；临时分区清理不得删除仍被 Query View 或恢复任务持有的副本。
- Rehydrate 完成后**不得**悄悄把 DEEP 数据加入旧 view：等待并新建 view，或使旧 Cursor 显式 `VIEW_STALE`。

### H. 入库切换 / Legacy
- 逐 Worker 切换前完成能力确认、watermark、Legacy 保留预算预检；不足则不切换并报阻断原因。
- 切换后实例与 Worker/Node 日志都停止新入 CP `logs`；未到期 Legacy 不受 platform 容量淘汰挤出。
- 旧日志按行、新日志按 Multiline 事件时，未建等价映射前**禁止**拼接精确趋势。

## 7. 变更控制

- 修改 FR-472 共享契约语义必须显式评审并同步依赖模块、兼容规则与测试。
- VL 资产换版必须重跑受影响能力验证并更新 `asset-inventory.md` 与 `internal/platform/logasset/approved.go`。
