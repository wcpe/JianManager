# 采集索引迁移至本地 SQLite（FR-496）

> 状态：📋 计划　·　关联 PRD：FR-496　·　关联 ADR：ADR-094（Worker 日志本地数据面）、ADR-095（磁盘权威事件存储）、ADR-093（进程生命周期韧性）
> 依赖：无（可先行）　·　被依赖：FR-497（增量对账依赖索引可精确记账）

## 1. 背景与目标

现采集侧索引为**单一 JSON 文件**（`data/var/log/ingest.state.json`），由 `ingest.Manager.persist` **整本重写**：

- 生产实测：**307MB / 13 个源**（2026-09-30）；单实例日志量大时增长更快（历史事故：索引 ~1.2GB 时单次持久化 >5s，`vlsup` 5s 超时 → 采集静默停摆）；
- 目标形态为**单节点 60 实例**：按 24MB/源外推索引将达 **1.5–3GB** → 每次交付批次都整本重写 → 秒级到数十秒级阻塞，采集吞吐被索引写入拖死。

**目标**：索引改为**本地嵌入数据库（SQLite）**，写入成本从 O(全量) 降到 O(变更行)；保持「不依赖 CP/网络」的自治性（CP 挂掉采集照常记账）；崩溃不丢索引。

**范围内**：索引的存储格式与读写路径、老 state 一次性迁移、配套回归与真机验证。
**不做**：不改交付/投递语义（仍为 FR-497 的条数级对账口径）；不引入远程数据库（违反 HA 要求）；不改账本事件体（`events/`）与 VL 数据面。

## 2. 设计

### 2.1 选型

- **`modernc.org/sqlite`（纯 Go，无 cgo）**：生产构建 `CGO_ENABLED=0` 可直接编译；不引入系统依赖。
- 库文件：`data/var/log/ingest.index.db`（与现有 `var/log` 同级目录，随 Worker 数据目录迁移）。
- 连接模式：**单写者**（Worker 进程内单连接 + `journal_mode=WAL` + `synchronous=NORMAL`）；读路径仅本进程（无跨进程共享）。

### 2.2 表结构（首版）

```sql
-- 每个日志源一行：身份与配置摘要
CREATE TABLE source (
  key            TEXT PRIMARY KEY,   -- LogSourceID/SourceGeneration
  log_source_id  TEXT NOT NULL,
  source_generation TEXT NOT NULL,
  storage_namespace TEXT NOT NULL,
  updated_at     INTEGER NOT NULL
);
-- 采集游标（可恢复到文件内偏移）
CREATE TABLE position (
  key        TEXT PRIMARY KEY REFERENCES source(key),
  read_pos   INTEGER NOT NULL,
  durable_pos INTEGER NOT NULL,
  reclaim_pos INTEGER NOT NULL,
  acquire_paused INTEGER NOT NULL DEFAULT 0,
  pause_reason   TEXT
);
-- 未解缺口（逐条可查、可人工解算）
-- 实现要点（2026-10-01 加固）：WITHOUT ROWID + 复合主键 (key,id)（id 为源内序号，
-- 保证重启后行身份可稳定推导，支撑「只写变更行」）；不设冗余二级索引——实测
-- rowid 表 + idx_gap_key_resolved 会把 85B 源键存 3 份，db 达 450.9MB（原 JSON 306.5MB）；
-- 改造后 276.7MB。
CREATE TABLE gap (
  id         INTEGER NOT NULL,
  key        TEXT NOT NULL,
  start_pos  INTEGER NOT NULL,
  end_pos    INTEGER NOT NULL,
  reason     TEXT NOT NULL,
  detail     TEXT,
  resolved   INTEGER NOT NULL DEFAULT 0,
  resolution TEXT,
  PRIMARY KEY (key, id)
) WITHOUT ROWID;
-- 投影发布状态（重启增量对账的记账面）
CREATE TABLE projection (
  key        TEXT PRIMARY KEY REFERENCES source(key),
  generation TEXT NOT NULL,
  events_stored_through INTEGER NOT NULL DEFAULT 0,
  pending    INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
-- 实例绑定（stdout/stderr 采集所依赖）
CREATE TABLE instance_binding (
  uuid       TEXT PRIMARY KEY,
  namespace  TEXT NOT NULL,
  generation TEXT NOT NULL,
  mode       TEXT NOT NULL,
  work_dir   TEXT NOT NULL
);
```

### 2.3 读写与崩溃语义

- 每次 `persist` 只写**本批次变更的行**（UPSERT），事务提交即持久；WAL 保证崩溃可恢复。
- 启动时一次性迁移：读取旧 `ingest.state.json` → 事务写入 SQLite → 校验行数/关键字段一致 → 旧文件**改名归档**（保留一个版本，不删除）。
- 迁移失败/校验不一致 → 拒绝启动采集并明确报错（不静默降级），保留旧文件供人工处置。
- 旧文件还含 `source_configs` 与 `instances`（实例绑定，stdout/stderr 采集依赖），**一并迁移**并在校验中逐项比对。
- **回滚步骤（写成脚本 `scripts/rollback-log-index.sh`）**：停止 Worker → 归档现有 `ingest.index.db` → 把归档的旧 JSON 改回 `ingest.state.json` → 启动 Worker；因两格式共用同一语义层（游标/缺口/投影），回滚不需要反向导出。
- 保留 JSON 只读导出（`ingest.index.db` → 导出命令）供排障与回滚。

## 3. 验收标准

1. **回退**：迁移后重放 13 源历史窗口，游标/缺口/投影与迁移前逐字段一致（脚本比对）。
2. **续传回归**：重启 Worker 后从上次位置续采（沿用既有 tailer 用例 + 新增索引层用例）。
3. **性能（真机）**：60 源规模下**单次持久化 ≤50ms**（现 307MB 时同口径为数百 ms 起、外推至 GB 级为秒级）；采样打印 P50/P95。
4. **崩溃不丢**：随机 `kill -9` Worker，重启后索引无丢失/无回退超过一个批次（与 FR-497 的允许重复口径一致）。
5. **可运维**：`sqlite3` 直接查询缺口/暂停源；提供「导出 JSON」命令。
6. **零行为变更**：现有 ingest/query 全套测试绿；`gofmt`/`vet` 干净。
7. **二次启动 + 脏环境（纪律，本次事故换来）**：验收必须覆盖「迁移 → 首启 → 停 → **二启** → 三启」，且二启/三启跑在**脏环境**（VL 中仍留有上一轮生命周期的同名代次行、残留 WAL、迁移前归档文件）上；部署后按「部署后复验」**立刻再重启一次**验证通过才算完成。只有单轮首启证据不算通过。规则原文：`.claude/rules/testing-and-quality.md`「状态 / 迁移类变更」、`.claude/rules/gate-merge.md`「部署后复验」。

## 4. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 新依赖（第三方驱动） | 纯 Go、广泛使用；锁版本；构建/运行无 cgo |
| 迁移一次性错误 | 迁移前归档旧文件；校验失败拒绝启动；提供回滚命令 |
| 单文件损坏 | WAL + 定期 `PRAGMA integrity_check` 巡检（随 Worker 健康巡检 FR-459） |
| **投影代次名被复用（2026-10-01 生产事故，已修）** | 代次名派生自本机状态计数器，状态重生（迁移/回滚/重部署）会令计数器回退，与 VL 中上一轮生命周期的行重名——VL 只追加（「取代」仅改查询侧白名单），而校验只按代次名过滤，`(同源, 同名, 校验窗口)` 命中旧行即判 `unexpected or duplicate`，启动整窗重发时**必然**卡死（首启低号侥幸过关、二启爬到旧整窗号段即死）。修法：选名前探测 VL 是否已占用，占用即进位（`nextFreeProjectionGeneration`）。回归：`generation_reuse_test.go`（改回旧行为即红，错误串与生产同类） |

## 5. 验证方式

- 单元/回归：ingest、ledger、acquire 包测试 + 新增索引层用例；
- 真机：与 FR-498（规模压测）同批次执行，出索引持久化 P50/P95 数字。
