# FR-473～484 日志平台真机验收记录（受控）

> 本文件是**持久**验收记录：`runbook-checklist.md` 的证据指针与 `.tmp/fr433-experiments/**` 产物均被 `.gitignore` 排除（第 45/59 行），清理临时目录即丢失。此处固化「跑了什么、看到什么、结论如何」，使发布决策不依赖临时文件。
>
> 原始产物（JSON/CSV/日志/截图文本）仍在 `.tmp/fr433-experiments/<A..H,perf>/`；本记录是它们的持久摘要。
> 复现脚本同样在临时目录，故每个场景都记录**环境与命令**，必要时可按此重建。

## 0. 环境与资产基线

| 项 | 值 |
|---|---|
| 主机 | node-main（生产主机，IP 已脱敏），与生产 CP/Worker 同机但**使用独立隔离栈**，不触碰 prod |
| 受管 VL | v1.52.0，`build_id=20260716-022147-tags-v1.52.0-0-g46a54c9`，包 SHA `d14f5851…`、解包可执行 SHA `26941a2f…`（见 `asset-inventory.md`） |
| 隔离 CP | `runbook CP`（http 8080 / grpc 8081，SQLite）或 `env-a`（30100/30101） |
| Worker | 本仓库 HEAD 构建（`go build -o … ./apps/worker`） |
| 对象存储 | RustFS S3 自建端点（已脱敏），bucket `dev`，ak/sk 已脱敏 |

## 1. Runbook A — WAL / 恢复责任（`.tmp/fr433-experiments/A-wal/`）

- **做法**：FILE_PRIMARY 源（`rb:node/g1`）写入事件 → `kill -9` Worker（并杀受管 VL 模拟整机重启）→ 重启 Worker。
- **观测**：重启后 Catalog journal 恢复 `entries=2`；账本从持久态重建；采集运行时重启成功；VL 中事件带 canonical 投影字段（`event_id`/`canonical_content_hash`/`projection_generation`/`record_start,end`）；追写后 `durable 727→769`，`reclaim_position` 0→62。
- **结论**：强杀后可定位账本与新投影，投影承担责任后 reclaim 推进。
- **附带发现并修复**：受管 VL 启动异步，`ingest.New` 构造期的恢复投影写与 VL 监听竞态 → 重启后**整个采集运行时创建失败**（首启不触发）。修复 `vlsup.Supervisor.WaitHealthy`（接线前等 HOT 就绪，上限 15s）。

## 2. Runbook B — Catalog / 迁移（`.tmp/fr433-experiments/B-catalog/`）

- **做法**：`POST /api/v1/nodes/1/log-runtime/migrate {storageNamespace, utcDay}` 触发真机 HOT→COLD。
- **观测**：返回 `200 migrated`；journal 完整走完 `ROUTING_FROZEN → DRAINING → SNAPSHOTTING → STAGING_VERIFY → ATTACHED_STAGING → OWNER_SWITCHED → QUERY_LEASE_DRAINING → DETACHED → CLEANED`；迁移后 **COLD 持有数据、HOT 为空**；`ATTACHED_STAGING` 期间 `cold_queryable=false`，`DETACHED` 仍 `recovery_required=true` + `RESIDUAL_DIRS`。
- **各状态崩溃恢复**：`results.ndjson` 记录逐状态恢复视图（owner/query_dir/partial_reasons）。
- **结论**：状态机按契约执行、权威切换与物理迁移生效；staging 不入查询、detach≠完成。

## 3. Runbook C — Supervisor / 鉴权 / 预算（`.tmp/fr433-experiments/C-budget/`）

- **做法**：受管 HOT/COLD/Rehydrate 三实例；Basic Auth 探测；`vlsup` 采样真实 VL 进程 RSS 与数据盘。
- **观测**：三实例绑定 `127.0.0.1:19451/19452/19453`；`/select/logsql/query` 正确凭据 200、无凭据 401、错密码 401；RSS 采样 ≈1.7MiB（idle）、磁盘 10.2%；1GiB 门禁判定 OK、超限 DEGRADED、磁盘压阈 PAUSED。
- **RSS 调优**：契约 §6.6 的 1GiB 门禁曾被突破（VL anon RSS ≈1.09GiB，`-memory.allowedBytes` 只约束 cache 不约束 Go heap）。注入 `GOMEMLIMIT=512MiB` 后 **VL RSS 1.1GiB → ≈131MiB**，延迟无回归。
- **结论**：三实例 localhost + 本地鉴权 + 预算采样/降级评估达标。

## 4. Deep Archive / Rehydrate（`.tmp/fr433-experiments/D-deep/`）

- **做法**：真实 RustFS S3 上执行受管 Raw 登记 → Seal manifest → 对象往返 → Rehydrate 任务。
- **观测**：`object_id`/`content_sha256` = payload sha256；重复登记 `AlreadyRegistered=true, copied=0`；manifest `schema=archive-manifest/v1`、`engine=victorialogs/<build_id>`（不再是占位）、`coverageComplete=true`；`provider.Get` 往返一致；Rehydrate 同 `mergeKey` **复用同一任务**（`waiters=2`）、磁盘预留不足返回 `DISK_RESERVE_UNMET`、`Complete` → `LOG_TASK_SUCCEEDED`。
- **清理矩阵**：无 hold → 可清理；Query View 租约持有 → 不可清理；释放后恢复；在途 Rehydrate → 不可清理；任务完成 → 可清理。
- **结论**：登记/幂等/manifest/往返/任务化/清理保护全部达标。

## 5. CP 受管 VL 资产分发闭环（`.tmp/fr433-experiments/E-asset/`）

- **做法**：`POST /api/v1/log-runtime/assets/linux/amd64`（multipart `package`）上传审批包 → `GET …/assets` 查缓存 → `POST /api/v1/nodes/1/log-runtime/install`。
- **观测**：上传 **201**，返回包/可执行 SHA 与 `approved.go` 一致；状态 `cached=true`；安装 **200** `{"state":"installed","tag":"v1.52.0"}`（CP 生成签名 URL，Worker `InstallApprovedURL` 从 CP 下载并**双重 SHA 校验**后安装）。
- **结论**：上传缓存→签名下载→Worker 安装的管理面闭环真机通过。

## 6. CP 联邦端到端（`.tmp/fr433-experiments/F-federation/`）

- **做法**：源 `storage_namespace=node:1` 与 CP 节点目标对齐后 `GET /api/v1/logs/federation/search?limit=3`。
- **观测**：`coverage.complete=true`，目标 `node:1` `state=success`，返回 3 条事件，`view.order_version` 为契约复合排序。
- **结论**：CP→反向隧道→Worker Catalog/VL 联邦链路贯通。

## 7. 真浏览器 UI 验收（`.tmp/fr433-experiments/G-browser/`）

- **做法**：`vite dev`（代理 /api → 隔离 CP）+ Playwright chromium 真实浏览器，真实登录 admin → `/logs`。
- **观测**：列表渲染真实联邦事件（3 条）与洞察条「当前视图 3 个事件」**一致**；覆盖完整 → 无失败横幅；导出按钮可用。
- **附带发现并修复**：洞察事件数恒为 0 —— 后端 `StatsPoint` 的 wire 真源是 `points[].agg.count`，前端只读顶层 `count`。修复后真机复测 0 → 3。

## 8. 性能与容量（`.tmp/fr433-experiments/perf/`、`H-scale64/`）

### 8.1 契约 §6.6 30 分钟持续负载（`perf/soak30`、`perf/soak-fix`）
- 负载：10k 事件基线 + 持续追加（~100 行/秒）+ Search limit=200 并发 8，持续 30 分钟；每 5s 采样；T+10min 迁移模拟。
- 延迟：**PASS**（conc8 654,272 次查询 p95 **39.5ms**、p99 **98.3ms**；阈值 250/500ms）。磁盘 11%（阈值 80/90）。
- **RSS FAIL → 修复**：Worker RSS 由 158MiB **单调涨到 2474MiB**。根因：CP `logcoord.Coordinator.views` 与 Worker `query.Planner.views` 为**无界视图注册表**（每个不带 `view_id` 的首次查询永久新增、从不回收）。修复：两侧**有界化（上限 4096）**，淘汰最旧、被淘汰视图显式 `VIEW_STALE`。复测峰值 2474 → **857MiB** 且回落、延迟无回归。

### 8.2 64 服务器密度容量（`H-scale64/`）
| 源数 | Worker RSS | VL RSS | state | 内存 WAL | reclaim |
|---|---|---|---|---|---|
| 2 | 106 MiB | 48 MiB | 5.6 MB | 4 B | 2/2 |
| 8 | 247 MiB | 72 MiB | 22 MB | 16 B | 8/8 |
| 16 | 487 MiB | 85 MiB | 45 MB | 32 B | 16/16 |
| 32 | 832 MiB | 84 MiB | 89 MB | 64 B | 32/32 |
| 64 | ≈674 MiB | 76–95 MiB | 143–189 MB | 257 B | 64/64 |

- **初始实测**：Worker RSS ≈2.0GiB、state 285MB、内存 WAL 107MB、**reclaim 0/64**。
- **根因（三处）**：①`pollOnce` **静默吞掉采集错误**（整轮 reclaim 失败零日志）；②真实日志行常无可解析语义时间，`groupEventsByUTCDay` 直接报错 → 投递失败 → 恢复责任无法转移 → **reclaim 永不推进**；③`WAL.entries` 在 `reclaim == durable` 后**从不裁剪**。
- **修复**：①采集错误按源去重上报；②`canonicalEventTime`/`eventUTCDay` 在语义时间缺失时回退 ingest 时间再回退源 `utc_day`（与既有 `normalizeCanonicalEvent` 同契约）；③新增 `WAL.pruneReclaimed`。
- **复测**：reclaim **64/64**、内存 WAL **257B**、Worker RSS **≈674MiB**（< 1GiB）。
- **容量拟合**：RSS ≈ **24 MiB/源**、state ≈ 2.8 MB/源（每源 3000 事件）→ **1 GiB ≈ 40 源**。
  - **⚠ 已作废（2026-09-24，FR-484）**：该拟合是「canonical 事件体常驻内存 + 内联 state」时的结论。事件体改为落盘 compact 并加界三处跨轮累积结构后（ADR-095，**真机验收**见 `../worker-log-canonical-eventstore/acceptance-real.md`），稳态 Worker 降至 64/128/150 源 = **22.5/22.3/20.0MiB**、峰值 **56.0/62.0/61.8MiB**，`state.json` 77 803KiB → 139.5KiB 并与事件数解耦；本节表格与拟合保留为**改造前基线**，不再是当前密度依据。现行约束项见契约 §6.6 修订与 FR-484 规格 §5.3。
- **口径**：§6.6「日志数据面 RSS」= Worker 进程日志部分 + 受管 VL 之和（已写入契约）。

## 9. 审计与遗留

- 验收审计（`sdd-accept-phase`）：`.tmp/acceptance-changes-HEAD-20260924.md`，10 项发现全部处置（含 1 项误判纠正）。
- 非阻塞遗留：Windows VL 运行证据、临时产物不持久（**本文件即为缓解**）。
  - 原列的「canonical 事件体落盘/compact」**已由 FR-484 完成**（见 §8 密度小节与 ADR-095），不再是遗留。
  - **FR-481 联邦查询侧仍为占位**（`service.UnimplementedFederatedQuerier`，`log_legacy.go:359`）：占位本身返回结构化 not-ready 是正确的，但 cutover 后的 federated 路径未接真实实现，属该 FR 的已知缺口。
- **交付确认（Gate-4）**：按 `gate-merge.md`，FR 验收须由用户签字，Agent 不得自行标记已交付。
