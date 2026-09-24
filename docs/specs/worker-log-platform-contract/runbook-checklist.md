# FR-472+ Runbook 真机验收清单（T12）

> 本地自动化证据见 `internal/worker/logs/runbook/`（**不替代**真机）。
> 远程主机：`.env.dev` 中 SSH/面板信息（若存在）。
> **持久验收记录见 [`acceptance-record.md`](acceptance-record.md)**——下列 `.tmp/fr433-experiments/**` 指针均被 `.gitignore` 排除，清理即丢失；结论已固化到该受控文档。
> 本清单全部通过前，FR **不得**标已交付。

## 准备

- [x] CP / Worker 使用含生产装配的构建（LogCoord + Worker Log RPC + ingest/archive/runtime）
- [x] Worker 日志：受管 VL 配置时 `GetLogCapabilities` supported=true；未配置时明确 `LOG_UNSUPPORTED`
- [x] VL v1.52.0 双平台 hash 与许可已审批；Linux 资产在 Worker 本地 hash 校验后由 supervisor 起停

## Runbook A — WAL / 恢复责任（真 Worker 进程）

- [x] 写入日志后强杀 Worker → systemd 重启后账本/新 projection 可定位
- [x] 模拟 VL 响应丢失 → `delivery_state=UNKNOWN`，reclaim 保持原水位
- [x] 重启后 projection 对账为 `REPLAY_REQUIRED`，责任转移后 reclaim 推进；非法 reason / hold / self-ref 有单测保护
- [ ] 磁盘满暂停、缺口和人工恢复动作的真实 Worker 证据（**用户明确要求不做真实填充以免影响共享生产盘**；纯逻辑单测覆盖磁盘 80/90 → DEGRADED/PAUSED + 必记缺口）
- [x] 证据：`.tmp/fr433-experiments/A-wal/`（隔离栈 env-a CP + rb-worker + 受管 VL；强杀 `-9` 重启后 Catalog/账本恢复、事件入 VL 带 canonical 投影、reclaim 推进；**并发现并修复重启时序缺陷**：VL 未就绪即接线 ingest）

## Runbook B — Catalog / 迁移

- [x] 各迁移状态强杀 Worker → Recover 后 owner/查询范围符合契约（先前真机逐状态视图 `.tmp/fr433-experiments/B-catalog/results.ndjson` + `catalog` Recover 单测）
- [x] VL detach/restart/re-attach 后 QueryPlanner 不纳入旧 owner（真机迁移链含 STAGING/OWNER_SWITCHED/DETACHED/CLEANED；`runbook_local_test` 持久 journal 过滤 + `catalog` 单测）
- [x] 迟到日志按 Catalog 路由，不重新生成已迁走 HOT 权威（`catalog.RouteWrite` 单测；真机迁移后 HOT 空、COLD 持有数据）
- [x] 证据：`.tmp/fr433-experiments/B-catalog/`（真机 HOT→COLD 迁移链 full+`live-migrate-20260923.md`；各状态恢复视图）

## Runbook C — Supervisor / 鉴权 / 防递归

- [x] HOT/COLD/Rehydrate 三实例 localhost 启停
- [x] Basic Auth：未授权 401、授权 200、COLD 重启后仍 200
- [x] VL 失败日志使用独立 sink，不递归写回数据面（实现/单测 + 远程进程日志）
- [x] 预算降级与 RSS/磁盘阈值的真实采样（真机对 VL 采样；**并修复 RSS 超限**：注入 `GOMEMLIMIT` 后 VL RSS ≈1.1GiB→≈131MiB，数据面合计 ≈441MiB < 1GiB）
- [x] 证据：`.tmp/fr433-experiments/C-budget/`（budget-sample.txt、three-instance-auth.md、result.json）

## 联邦端到端

- [x] CP `GET /api/v1/logs/federation/search` 命中已装配 LogCoord（真机 `complete=true`、目标 `node:1` success、返回事件）
- [x] Worker Log RPC 经反向隧道返回 coverage；Worker offline 时 UI 展示 incomplete/不可导出
- [x] 正常查询 Search/Stats/Facets/Export 与同一 view 一致；partial 不显示为完整零结果（真浏览器验收修 Stats `agg.count` 读法）
- [x] cutover 默认关；已有全集 Worker 水位就绪校验
- [x] Catalog 迁移崩溃/旧 owner re-attach/迟到路由的真实 Worker 集成证据（真机迁移链 + query 面）
- [x] 证据：`.tmp/fr433-experiments/F-federation/`

## 发布必需项（FR-477/442）

- [x] Deep Archive/Rehydrate 真机验收（RustFS S3：登记/幂等/manifest/往返/Rehydrate 任务/清理矩阵）→ `.tmp/fr433-experiments/D-deep/`
- [x] FR-481 真浏览器验收（真实登录/列表/Stats·Facets 洞察同一 view 一致/导出门禁）→ `.tmp/fr433-experiments/G-browser/`
- [x] 性能：Search 串行 p95 28.5ms（CP 联邦链路）、VL p95 77–111ms；磁盘 11%；数据面 RSS（GOMEMLIMIT 调优后）→ `.tmp/fr433-experiments/perf/`
- [x] **契约 §6.6 30 分钟持续负载**（同时采集+投影+迁移模拟+查询并发 8）：延迟 p95 39.5ms / p99 98.3ms、磁盘 11% PASS；**发现并修复视图注册表无界增长泄漏**（Worker RSS 2474MiB→峰值 857MiB）→ `.tmp/fr433-experiments/perf/soak30`、`soak-fix`

## 判定

- Runbook A/B/C、联邦、Deep Archive/Rehydrate、CP 资产分发、性能（含 §6.6 全量压测 + 64 服务器密度实测）、真浏览器 UI 均有真机证据 → 可进入 FR 验收讨论与发版门禁。
- 明确排除（用户决定）：磁盘满真实填充（避免影响共享生产盘）。
- **64 服务器密度实测（H-scale64）**：修复 reclaim 停滞 + WAL 不裁剪后，Worker 稳态 RSS ≈674MiB（< 1GiB），reclaim 64/64、内存 WAL 257B。残余：`persistedSource.Events`（VL 数据根重建所必需 canonical 集合，≈143MB）——已实测确认**不可**用内存裁剪收紧（会破坏归档/轮转/跨日重建等价性），后续须走事件体落盘/compact。
- **canonical 事件体落盘 compact 已实施（FR-483，2026-09-24）**：按上述遗留方向改造完成——事件体改为追加式磁盘段（ADR-095），集合语义不变；并加界三处跨轮无界累积结构（`n.events`/`linePos`/`delivered`）。**真机验收通过**（node-main + 真 VL v1.52.0）：`state.json` 77 803KiB → **177.9KiB** 并与事件数解耦；稳态 Worker RSS 64/128/150 源 = **22.5/22.3/20.0MiB**、采集期峰值 **56.0/62.0/61.8MiB**（目标稳态 ≤300MiB）；342 源逐一核对 1 026 000 条事件零丢失零重复。受控证据：`../worker-log-canonical-eventstore/acceptance-real.md`。**注意**：H-scale64 的「≈24MiB/源、1GiB≈40 源」线性拟合已作废；峰值与稳态**都不再随源数线性增长**。证据：`.tmp/fr444-experiments/phase5-attribution.md`（含两次量测读错的更正记录）。
- **验收审计（sdd-accept-phase，`.tmp/acceptance-changes-HEAD-20260924.md`）**：10 项发现全部处置——修 i18n `missing-keys` 门禁红（发布必需阻塞）、实现契约 §6.6 的 25% 预留判据、按实测确定 §6.6 RSS 口径、同步 4 处文档漂移，并补齐 4 项证据缺口（FR-473 权限/截断损坏可见、FR-472 告警回归、Rehydrate/ArchiveStatus 越权、隧道层跨节点 secret 复用）。
- **非阻塞遗留**：Windows VL 运行证据、`.tmp` 证据不持久（已由受控文档 [`acceptance-record.md`](acceptance-record.md) 缓解）。
  - 原列的「canonical 事件体 compact」与「采集期单批瞬时峰值」两项**均已由 FR-483 处置**（见上第 58 行），不再是遗留。
  - **FR-480 联邦查询侧仍为占位**：`service.UnimplementedFederatedQuerier` 是当前默认装配（`log_legacy.go:359`），返回结构化 not-ready 而非空成功。这不是缺陷（占位行为正确），但意味着 cutover 后的 federated 查询路径**未接真实实现**，属 FR-480 的已知缺口。
- **交付确认（Gate-4）**：按 `gate-merge.md`，FR 验收须由用户签字，Agent 不得自行标记已交付。
- **逐条签字台账**：[`signoff-ledger.md`](signoff-ledger.md) 把 FR-472～483 的验收证据与未覆盖缺口并列（当前：可签 7 条 / 有缺口待裁决 4 条 / 未就绪 1 条），供逐条签字或打回。
