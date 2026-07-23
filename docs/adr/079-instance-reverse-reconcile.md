# ADR-079: CP↔Worker 实例反向对账

- **日期**: 2026-07-23
- **状态**: accepted
- **上下文**: 正向对账已存在——CP 以 Worker 心跳 `instances` 为真源，DB 认为在跑但本拍未上报的实例置 STOPPED。反向缺失：Worker 仍在管/接管的实例若 CP 库已无记录（误删、脏恢复、手工清库等），进程成为永久无主孤儿，占端口与资源。FR-310/325 覆盖删除与 reconnect 孤儿，不覆盖「CP 记录已消失但 Worker 仍持有」。
- **决策**:
  1. **复用心跳 `instances` 清单**作反向输入（加性可选 `pid`），不另开附属 RPC 上报；老 Worker 零字段兼容。
  2. CP 侧 `OrphanRuntimeTracker`：比对 Worker 清单与 DB `instances`（软删视为无）→ `orphan_runtimes` 状态机 `pending → confirmed | cancelled → disposed`。
  3. **护栏优先于自动干净**：宽限期默认 10m（`instance_reverse_reconcile.grace_period`）；`auto_dispose` 默认 **false**（只告警/列表）；管理员 `POST /api/v1/orphan-runtimes/:uuid/dispose` 手动确认。
  4. 处置 RPC `DisposeOrphanRuntime`：Worker Kill+Remove 注册 + `ReapDaemonForDelete`（对齐 FR-310 运行态清理），**不删工作目录、不重建 CP 实例记录**。
  5. 手动/自动处置均写审计；平台日志中文。
- **理由**:
  - 复用已有心跳真源，避免双清单漂移；正向语义只读扩展、不改写。
  - 默认不自动杀防止误杀（宽限内 CP 又出现记录则 cancelled）。
  - 专用 Dispose 与业务 Stop/RemoveInstance 分离：Remove 会删目录且有运行态守卫，无主场景只需清运行态。
- **后果**:
  - 新增表 `orphan_runtimes`、设置白名单两键、管理员 HTTP 列表/处置、Worker 新 RPC。
  - 老 Worker/老 CP 互通：缺字段或 Unimplemented 时反向路径关闭或处置失败记日志，不崩。
  - 前端无主列表可后置（本 ADR 后端闭环优先）。
- **替代方案**:
  - 心跳响应下发「待处置 UUID 列表」由 Worker 自处置——CP 难审计、难人工确认。
  - 自动重建 CP 实例记录——禁止（spec：不 invent 业务实例）。
- **参考**: FR-326、`docs/specs/instance-reverse-reconcile/spec.md`、ADR-003、FR-310/325
