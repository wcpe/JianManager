# 实例整机快照与一键回滚（FR-466）

> 状态：🟡 **代码与单测已交付，真机验收待做**（FR-466 端到端已实现，单测/构建覆盖通过；§4 中标「真机」的验收项尚未在真机验证）　·　关联 PRD：FR-466　·　依赖：FR-013、FR-056、FR-051、FR-031、FR-182、FR-323　·　关联 ADR：ADR-019、ADR-042

## 1. 背景与目标

平台已有四套各自独立的「回滚」，但没有一个「整机时间点」的概念：

| 现有能力 | 实现位置 | 粒度 | 痛点 |
|---|---|---|---|
| 备份 / 增量备份链 | `service/backup.go`（`CreateWithOptions` / `Restore` / `resolveChain`）、`model/backup.go` | 工作目录文件树 | 恢复**必须先停服**（`ErrInstanceNotStopped`）；只能回到某条备份链末端，链式回放耗时长 |
| 通用文件版本回滚 | FR-051 `model/file_version.go` | 单文件 | 只覆盖被「改前快照」命中的文件，非工作目录全量 |
| 配置版本回滚 | FR-031 `model/config_version.go` | 单配置文件 | 仅配置文本 |
| 自更新二进制回滚 | FR-182 `service/selfupdate_rollback.go` | CP/Worker 二进制 | 与本机业务数据无关 |

运维真实诉求是「**把实例一键恢复到昨天某时刻的整机状态，并在此过程中留痕、可再回滚**」。现状下这需要人工组合：停服 → 找备份 → 恢复 → 判断是否要重建二进制。缺少「快照」这一层的统一入口，也缺少「回滚前自动建快照」（真机上最容易犯的错：回滚后才发现当前状态没留底，无法退回回滚前）。

**范围内**
- 新增「实例快照」概念：一次快照 = 时间点语义的整机可回滚点（含工作目录 + 元数据）
- 一键回滚：选择某快照 → 回滚到该时间点
- **回滚前自动建快照**（强制，不可跳过），保证「回滚仍是可回滚的」
- 全过程走 `TaskService.RunAsync`，阶段进度 + 终态站内信，可追溯

**不做（范围外）**
- 不替代 FR-013/056 备份体系（快照复用其存储与回放通道，不重建一套归档管线）
- 不做跨实例/跨节点的时间点一致性快照（单实例语义）
- 不做热快照（不实现文件系统级 COW / 内存快照）；运行态实例的快照走「停服或一致性降级」策略，见 §3.4
- 不改动 FR-182 的 CP/Worker 自更新回滚语义

## 2. 设计

### 2.1 数据模型（决策：新增 `InstanceSnapshot`，不粗暴复用 `Backup`）

```go
// model/instance_snapshot.go（新增）
type SnapshotKind string // "manual" | "pre_rollback" | "scheduled"

type InstanceSnapshot struct {
    ID          uint   `gorm:"primaryKey"`
    UUID        string `gorm:"type:char(36);uniqueIndex"`
    InstanceID  uint   `gorm:"not null;index"`
    Name        string `gorm:"type:varchar(128)"`
    Kind        SnapshotKind `gorm:"type:varchar(24)"`
    // RootBackupID 指向本次快照底层落地的 Backup 记录（复用其归档与存储字段）。
    RootBackupID uint `gorm:"not null;index"`
    // BinaryRef 快照时的二进制指纹（FR-468 §），保证「数据 + 版本」成对恢复。
    BinaryName   string `gorm:"type:varchar(255)"`
    BinarySHA256 string `gorm:"type:char(64)"`
    // PrevConfigHash/EnvHash 记录快照时刻的关键元数据，用于回滚差异提示。
    ConfigHash string `gorm:"type:char(64)"`
    TriggeredBy uint  `gorm:"index"` // 操作人
    CreatedAt  time.Time
}
```

**理由**：`Backup` 表已承载全量/增量/远程存储/校验和等归档语义（`model/backup.go`）；快照是「在备份之上加一层时间点 + 版本 + 元数据」的语义，直接给 `Backup` 加 `Kind` 会把归档结构与运维概念混在一张表，且 `Backup` 的 `ParentID` 链语义与快照的「独立时间点」语义冲突。新增薄表、`RootBackupID` 外键指向底层 `Backup`，归档与回放全部复用既有实现。

### 2.2 创建快照

`SnapshotService.Create(instanceID, name)`：
1. 同步段：校验实例存在、登记 `InstanceSnapshot`（Kind=manual）、登记 `TaskKindSnapshotCreate` 任务；
2. 后台段：经 `BackupService.RegisterFull(instanceID, "snapshot-"+uuid)`（`service/backup.go`）登记一条**全量**
   `Backup`（`Origin=snapshot`，Pending），再由快照自己的任务驱动 `ExecuteBackup` 打包；
   完成后回读该记录，把 `ID` 与 `FileSizeMB` 回填 `RootBackupID` / `SizeMB`；
   > 不复用 `CreateWithOptions`：它会额外注册一个备份任务，同一份工作出现两条任务记录，
   > 而快照的阶段文案（停服 → 建回滚前快照 → 回放 → 校验）本就与备份任务不同。归档实现是同一份。
3. 采集二进制指纹：读实例工作目录里的启动二进制（`startCommand` 首 token 指向的文件）名字与 sha256，写 `BinaryName/BinarySHA256`。

> 决策：快照**一律全量**，不吃增量链。理由：快照是「可独立回滚的时间点」，若挂增量链，链上任一环被删除/损坏都会让快照失效，违背「一键回滚」的可靠性预期。存储代价由保留策略兜底（§2.5）。

### 2.3 一键回滚（核心闭环）

`SnapshotService.Rollback(snapshotID, operatorID)`：

```
1. 校验目标快照**可回滚且底链存在**（见 §2.4「底链保护」）+ 实例存在
2. 前置拒绝：同实例已有在途 snapshot_create / snapshot_rollback / binary_upgrade 任务 → 直接拒绝
3. 【强制】自动建 pre_rollback 快照（Kind=pre_rollback）
     —— 记录「回滚前状态」，失败即中止回滚（不留无退路的操作）
4. 运行态处置：若实例 RUNNING/STARTING/STOPPING → 先优雅停止
     （复用既有停止路径，非直接拒绝；与 FR-013 Restore 的「拒绝」不同，此处由快照负责停服编排）
     停服未收敛（超时）→ 中止：回退 STOPPING 残留状态 + 清理本次 pre_rollback（见下）
5. 任务执行体：取实例互斥锁 → 【重验实例已停止】→ 回放 RootBackupID
6. 若 BinarySHA256 与当前工作目录二进制不一致 → 提示「数据已回滚，二进制未动」
     （二进制回滚走 FR-468，不在本 FR 自动执行，避免越权替换可执行文件）
7. 恢复实例状态：回滚前为 RUNNING 的，回滚完成后回到 STOPPED（不自动拉起，留人工确认）
8. **终态**写审计 instance.snapshot_rollback（成功/失败各一次）+ 站内信
```

**并发互斥（M-4）**：回滚与升级都会全量读写实例工作目录，必须落在同一互斥域内，否则两个并发
回滚会各自打包并覆盖式回放，交错破坏目录。两道闸：
- 入队前拒绝「同实例在途破坏性任务」（查 `tasks` 表 pending/running）；
- **任务执行体内再取一次实例互斥锁** `acquireInstanceOperation`（复用启停/删除同一把锁），
  把「同实例串行」从检查瞬间延伸到真正执行期。
入队前的停服检查是 TOCTOU 的：任务排队期间实例可能被其它路径拉起，故**回放前必须重验状态**
（`ensureStoppedBeforeRestore`），运行态直接中止而不是覆盖运行中的数据。
注：任务体内不再自动停服（那会与互斥锁形成等待自身的死锁风险），失败后由用户显式重试。

**中止清理（M-5）**：停服超时或回放前重验失败时，两个副作用都必须收拾干净：
- **回退 STOPPING 残留**：否则实例既不在跑也不再是 STOPPED，需人工 Kill 才能恢复；
  回退为 STOPPED 并在 `statusReason` 写明「回滚中止…若进程仍在请手动 Kill」。
  实例已收敛到其它状态（含 CRASHED）时不改写——CRASHED 是重要事实，不该被中止逻辑抹掉。
- **删除本次 pre_rollback**：它不受条数裁剪（§2.4），每次失败留一份全量归档会让磁盘只增不减。

任务类型：新增 `TaskKindSnapshotRollback = "snapshot_rollback"`（`model/task.go`）。阶段：`停服 → 建回滚前快照 → 回放数据 → 校验 → 完成`。

**关键决策：自动建 pre_rollback 快照为强制且不可配置关闭**。理由：「一键回滚」的最大风险是把当前可用状态永久覆盖；若允许关闭，真机事故（误点回滚）将不可逆。这一步代价是一次全量备份，用存储换安全，符合运维预期。

### 2.4 可追溯与保留

- 每条 `InstanceSnapshot` 记 `TriggeredBy`；`pre_rollback` 与其触发的回滚互相关联（新增 `TriggeredByRollbackID`），形成「回滚 → 退回」可视链。
- 审计动作（`service/audit.go`）：`instance.snapshot_create` / `instance.snapshot_rollback` / `instance.snapshot_rollback_pre`。
- 保留：平台设置键 `snapshot.retention_count`（默认 10）/ `snapshot.retention_days`（默认 30），复用 `service/settings.go` 的 `SettingsReader.EffectiveValue`（同 `backup.retention_days`）；裁剪循环参照 `backup.go` 的 `runRetentionLoop` / `pruneExpiredOnce`。**`pre_rollback` 快照不受条数裁剪**（至少留最近 M 条），避免「回滚把回滚点挤掉」。

#### 底链保护（B-1）

快照必须**自包含**，其生命周期只能由 `snapshot.retention_days` 决定。若底链（`RootBackupID` 指向的
`Backup` 行）交给普通备份保留策略管，`backup.retention_days`（默认 **14**）会先于
`snapshot.retention_days`（默认 **30**）把它裁掉——第 15 天起所有快照都变成指向软删行的**死链**，
而列表仍显示 completed/可回滚，直到用户点下去才报 `record not found`。三层修复：

1. **来源标记**：`Backup` 新增 `Origin`（`BackupOriginSnapshot` / 空=普通备份），`RegisterFull` 固定登记为
   `snapshot`；`BackupService.pruneExpiredOnce` 的查询**排除** `origin = snapshot`——
   只豁免快照底链，普通备份照常到期清理（不做一刀切关停策略）。
2. **读侧前置校验**：列表与回滚入口现算底链可用性（**读时派生，不落库**——底链会被人工删除/并发清理
   改变，持久化该字段只会得到陈旧值，而陈旧值正是本缺陷的成因）。不可回滚时返回
   `rootBackupState = "missing"` + `notRollableReason`，回滚入口返回 `ErrSnapshotNotRollable` 并点明
   「底层备份 #N 已不存在」；前端据此禁用按钮并展示原因。**绝不静默显示为可回滚**。
3. **回归测试**：同时跑两套保留策略（快照 30 天 + 备份 14 天），断言超期快照底链存活、
   快照仍可回滚、且普通超期备份确实被裁。

#### 创建上限保护（m-2）

设置键 `snapshot.max_per_instance`（默认 20，0=不限）与 `snapshot.max_total_mb`（默认 0=不限）。
两键都在设置写入白名单内、由设置页可改（值语义：**0 与正数接受、负数拒绝**——0=不限是有效语义，
与读侧 `intSetting` 只对 `n<0` 回落缺省严格对齐；若按同族保留策略的 `≥1` 校验，运维就无法表达
「不限」）。创建前检查该实例的已完成快照条数与总占用，超限直接拒绝（`ErrSnapshotQuotaExceeded`）。
目标不是精确预测归档大小（无法预测），而是拦住「保留策略尚未生效前就把节点磁盘写满」这一类
可预见的自伤——快照是全量归档，反复创建会成倍占用磁盘。`pre_rollback` 不受数量上限约束
（它是回滚退回点，被挤掉等于回滚不可逆）。

### 2.5 复用与接口

归档/回放复用 `BackupService.CreateWithOptions` + `executeRestore`（`RestoreBackup` RPC）；存储复用 FR-057 `BackupStorageService` + `StorageBackendSpec`；进度复用 `TaskService.RunAsync`（FR-323）；停服走既有 graceful stop（`graceful_stop.timeout`）。

REST（`router/snapshot.go` 新增）：`GET /instances/:id/snapshots`、`POST /instances/:id/snapshots`、
`POST /snapshots/:sid/rollback`、`DELETE /snapshots/:sid`。

**权限（两层，缺一不可）**：
- **权限树节点**：读 `instance.read`；创建/回滚 `requireNodes(c, "instance.write")`；删除
  `requireNodes(c, "instance.delete")`（销毁归档数据与实例删除同权，`instance.write` 不足）。
- **组归属**：`canAccessInstance` / `canManageInstance`（实例须在调用方可访问的组内）。

只做第 2 层是不够的：`group_viewer` 只读角色只有 `instance.read` 节点，但其 `CanAccessGroup`
为真 → 仅凭 `canManageInstance` 会放行只读角色覆盖实例目录（越权回滚 / 替换可执行文件）。
二进制升级/回滚（`router/binary_version.go`）同口径要求 `instance.write` 节点。

**破坏性操作确认要素（m-1）**：回滚要求请求体携带 `confirmSnapshotId`（须等于目标快照 ID）
或 `confirmName`（须等于目标快照名），二者至少一项且必须匹配，否则 400 `CONFIRM_REQUIRED`。
MCP 工具 `instance_snapshot_rollback` 同口径。目的是拦住「点错按钮 / 脚本循环变量写错」
直接覆盖生产数据。

## 3. 任务拆分

- [x] `model/instance_snapshot.go` + AutoMigrate 注册（`database.go`）+ 单测（UUID、索引）
- [x] `service/snapshot.go`：Create（全量备份 + 二进制指纹）+ 单测（依赖 mock BackupService）
- [x] `service/snapshot.go`：Rollback 闭环（强制 pre_rollback、运行态停服、回放、终态）+ 单测（顺序、失败中止、pre_rollback 强制）
- [x] `model/task.go` 新增 `TaskKindSnapshotCreate` / `TaskKindSnapshotRollback`；`RunAsync` 阶段文案
- [x] 保留策略：设置键 + 裁剪循环（参照 `backup.go`）+ 单测（pre_rollback 不被挤掉）
- [x] **底链保护（B-1）**：`Backup.Origin` 标记 + `pruneExpiredOnce` 豁免 + 列表/回滚前置校验 + 双保留策略回归测试
- [x] **并发互斥（M-4）**：入队前拒在途任务 + 任务体内实例互斥锁 + 回放前状态重验 + 并发测试
- [x] **中止清理（M-5）**：回退 STOPPING 残留 + 清理失败产生的 pre_rollback + 测试
- [x] **终态审计（M-6）**：成功/失败分别写审计（不再入队即记成功）+ 失败审计测试
- [x] **创建上限（m-2）**：`snapshot.max_per_instance` / `snapshot.max_total_mb` 创建前保护 + 测试
- [x] **pre_rollback_keep 最小值收敛（M4）**：读路径 `preRollbackKeep()` 收敛最小值 1（`=0` 会让天数维清空 pre_rollback，破「回滚把回滚点挤掉」的不变量；条数维本已豁免），`keepNewestPreRollbackIDs` 再兜一层 + 不变量回归测试（`keep=0` 仍保留最近 1 条）
- [x] **中断残留清扫（m6）**：`Start()` 内一次性把残留 `pending`/`running` 快照标记 `failed` 并写明重启中断原因 + 测试
- [x] `router/snapshot.go` + 权限（`instance.write` / `instance.delete` 节点）+ 审计动作；router 注册；MCP `instance_snapshot_list` / `_create` / `_rollback`（含确认要素）
- [x] 前端实例控制台「快照」面板：列表 / 创建 / 一键回滚（二次确认）/ 进度；i18n 中英 + 双主题
- [x] 文档同步：ARCHITECTURE、API.md、PRD 状态、CHANGELOG

## 4. 验收标准

- 单测全绿：快照创建（全量 + 指纹）、回滚顺序、**回滚前必建 pre_rollback**、pre_rollback 不被裁剪挤掉、
  **`pre_rollback_keep=0` 时不变量不破（仍留最近 1 条）**、**重启残留 pending/running 被启动清扫标记 failed**、
  链式回放参数组装、**底链豁免备份保留策略**、底链缺失显式降级、并发拒绝、中止清理、失败审计；
  前端 vitest：列表、二次确认、空态、进度、不可回滚原因
- **真机（待真机验收）**：① 对运行中实例打快照 → 停服 → 改坏工作目录 → 一键回滚 → 数据恢复且留痕；② 回滚后 `pre_rollback` 快照存在，可再从它退回回滚前状态（闭环可逆）；③ 任务中心可见阶段、终态发站内信；④ 目标快照二进制与当前不一致时明确提示但不越权替换
- 横切：备份链既有功能不回归；中英/双主题正常

## 5. 风险 / 待定

- **占用与耗时**：全量快照对大工作目录（数十 GB）耗时与占空间显著。缓解：全量 + 保留策略 + 创建上限（m-2）。待定：是否允许选择压缩级别。
- **运行态快照一致性**：本版要求回滚编排先停服；创建快照时若非停服状态，MC 世界文件可能非一致。首版倾向**允许创建但标注**，回滚源快照非停服态时给出警告。
- **二进制与数据版本耦合**：快照记录 `BinarySHA256` 但本 FR 不自动回滚二进制，需与 FR-468 协同。首版只提示不处理。
- **与 FR-051/031 边界**：快照回滚整体覆盖工作目录；回滚后**不自动**改写 `file_version` / `config_version` 历史，仅 UI 提示。
- **底链保护的边界（B-1 记录）**：豁免只覆盖「保留策略自动裁剪」。若运维**手工删除**底链、
  或底层归档文件在存储侧丢失，快照仍会失效——这两条路径由读侧前置校验兜住（显式降级为不可回滚），
  不会静默成功。反之，`Origin=snapshot` 的备份不受时间裁剪，长期留存大量快照会持续占用磁盘，
  由 `snapshot.retention_days` + `snapshot.max_total_mb` 两道策略控制。
- **中断残留快照（m6，已实现启动清扫）**：归档状态 `running`/`pending` 的推进者是 **CP 进程内的
  goroutine**（`runCreate` + 同步 RPC），不存在跨进程续跑。CP 在归档中重启（发布、崩溃、OOM）
  会让该行永久停在 `running`：既不会自行收敛到终态，又被保留策略的条数维刻意豁免
  （`kind <> pre_rollback` 只豁免条数；`pre_rollback` 更被天数维保住），前端于是永久显示
  「归档中」且一直占用 `snapshot.max_per_instance` 配额。现由 `Start()` 内的
  `sweepStartupPendingOrphans` / `sweepStartupOrphans` **一次性**把它们标记 `failed`
  并写明「控制面重启导致归档中断」（复用 `markSnapshotFailed` 的终态语义，不新增状态）。
  选择标记失败而非删除：快照行是回滚留痕，且归档在**末段**中断时底层备份可能已落盘，
  留行让运维看得见并手工处置。**仍存在的限制**：清扫只覆盖 CP 重启这一路径；
  若归档 goroutine 因 panic 而静默死亡（进程未退），该行不会被清扫。
  当前 `runCreate` 无 panic 恢复，且其调用点（任务中心）已有兜底，故未加 recover；
  如后续出现该形态，应改为带超时的在途巡检（按 `updated_at` 老化判定）。
- **无平台/节点级总量上限（m6 记录，需运维自行开启）**：`snapshot.max_per_instance`(默认 20)
  是**单实例**条数闸，`snapshot.max_total_mb` 默认 **0=不限**。64 服规模下若每实例都跑满 20 条
  全量快照，节点磁盘占用没有平台级/节点级总量约束——**该场景需运维显式设置
  `snapshot.max_total_mb`**（按实例生效，需逐实例评估工作目录体积后设定）。节点级总量上限
  不在本 FR 范围内（属节点容量治理），本版只如实记录该缺口而非假装已覆盖。
