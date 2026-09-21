# 实例整机快照与一键回滚（FR-466）

> 状态：📋 计划　·　关联 PRD：FR-466　·　依赖：FR-013、FR-056、FR-051、FR-031、FR-182、FR-323　·　关联 ADR：ADR-019、ADR-042

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
2. 后台段：调用既有 `BackupService.CreateWithOptions(instanceID, "snapshot-"+uuid, CreateOptions{})` 建**全量**备份（快照必须是自包含的，不做增量），其 `ID` 回填 `RootBackupID`；
3. 采集二进制指纹：读实例工作目录里的启动二进制（`startCommand` 首 token 指向的文件）名字与 sha256，写 `BinaryName/BinarySHA256`。

> 决策：快照**一律全量**，不吃增量链。理由：快照是「可独立回滚的时间点」，若挂增量链，链上任一环被删除/损坏都会让快照失效，违背「一键回滚」的可靠性预期。存储代价由保留策略兜底（§2.5）。

### 2.3 一键回滚（核心闭环）

`SnapshotService.Rollback(snapshotID, operatorID)`：

```
1. 校验目标快照 completed 且实例存在
2. 【强制】自动建 pre_rollback 快照（Kind=pre_rollback）
     —— 记录「回滚前状态」，失败即中止回滚（不留无退路的操作）
3. 运行态处置：若实例 RUNNING/STARTING/STOPPING → 先优雅停止
     （复用既有停止路径，非直接拒绝；与 FR-013 Restore 的「拒绝」不同，此处由快照负责停服编排）
4. 回放目标快照的 RootBackupID（委托 BackupService.executeRestore，复用 RestoreBackup RPC）
5. 若 BinarySHA256 与当前工作目录二进制不一致 → 提示「数据已回滚，二进制未动」
     （二进制回滚走 FR-468，不在本 FR 自动执行，避免越权替换可执行文件）
6. 恢复实例状态：回滚前为 RUNNING 的，回滚完成后回到 STOPPED（不自动拉起，留人工确认）
7. 终态站内信 + 审计动作 instance.snapshot_rollback
```

任务类型：新增 `TaskKindSnapshotRollback = "snapshot_rollback"`（`model/task.go`）。阶段：`停服 → 建回滚前快照 → 回放数据 → 校验 → 完成`。

**关键决策：自动建 pre_rollback 快照为强制且不可配置关闭**。理由：「一键回滚」的最大风险是把当前可用状态永久覆盖；若允许关闭，真机事故（误点回滚）将不可逆。这一步代价是一次全量备份，用存储换安全，符合运维预期。

### 2.4 可追溯与保留

- 每条 `InstanceSnapshot` 记 `TriggeredBy`；`pre_rollback` 与其触发的回滚互相关联（新增 `TriggeredByRollbackID`），形成「回滚 → 退回」可视链。
- 审计动作（`service/audit.go`）：`instance.snapshot_create` / `instance.snapshot_rollback` / `instance.snapshot_rollback_pre`。
- 保留：平台设置键 `snapshot.retention_count`（默认 10）/ `snapshot.retention_days`（默认 30），复用 `service/settings.go` 的 `SettingsReader.EffectiveValue`（同 `backup.retention_days`）；裁剪循环参照 `backup.go` 的 `runRetentionLoop` / `pruneExpiredOnce`。**`pre_rollback` 快照不受条数裁剪**（至少留最近 M 条），避免「回滚把回滚点挤掉」。

### 2.5 复用与接口

归档/回放复用 `BackupService.CreateWithOptions` + `executeRestore`（`RestoreBackup` RPC）；存储复用 FR-057 `BackupStorageService` + `StorageBackendSpec`；进度复用 `TaskService.RunAsync`（FR-323）；停服走既有 graceful stop（`graceful_stop.timeout`）。

REST（`router/snapshot.go` 新增）：`GET /instances/:id/snapshots`、`POST /instances/:id/snapshots`、`POST /snapshots/:sid/rollback`、`DELETE /snapshots/:sid`，权限对齐实例读/写。

## 3. 任务拆分

- [ ] `model/instance_snapshot.go` + AutoMigrate 注册（`database.go`）+ 单测（UUID、索引）
- [ ] `service/snapshot.go`：Create（全量备份 + 二进制指纹）+ 单测（依赖 mock BackupService）
- [ ] `service/snapshot.go`：Rollback 闭环（强制 pre_rollback、运行态停服、回放、终态）+ 单测（顺序、失败中止、pre_rollback 强制）
- [ ] `model/task.go` 新增 `TaskKindSnapshotCreate` / `TaskKindSnapshotRollback`；`RunAsync` 阶段文案
- [ ] 保留策略：设置键 + 裁剪循环（参照 `backup.go`）+ 单测（pre_rollback 不被挤掉）
- [ ] `router/snapshot.go` + 权限 + 审计动作；router 注册；MCP `instance_snapshot_list` / `_create` / `_rollback`
- [ ] 前端实例控制台「快照」面板：列表 / 创建 / 一键回滚（二次确认）/ 进度；i18n 中英 + 双主题
- [ ] 文档同步：ARCHITECTURE、API.md、PRD 状态、CHANGELOG

## 4. 验收标准

- 单测全绿：快照创建（全量 + 指纹）、回滚顺序、**回滚前必建 pre_rollback**、pre_rollback 不被裁剪挤掉、链式回放参数组装；前端 vitest：列表、二次确认、空态、进度
- **真机（要真机过）**：① 对运行中实例打快照 → 停服 → 改坏工作目录 → 一键回滚 → 数据恢复且留痕；② 回滚后 `pre_rollback` 快照存在，可再从它退回回滚前状态（闭环可逆）；③ 任务中心可见阶段、终态发站内信；④ 目标快照二进制与当前不一致时明确提示但不越权替换
- 横切：备份链既有功能不回归；中英/双主题正常

## 5. 风险 / 待定

- **占用与耗时**：全量快照对大工作目录（数十 GB）耗时与占空间显著。缓解：全量 + 保留策略。待定：是否允许选择压缩级别。
- **运行态快照一致性**：本版要求回滚编排先停服；创建快照时若非停服状态，MC 世界文件可能非一致。首版倾向**允许创建但标注**，回滚源快照非停服态时给出警告。
- **二进制与数据版本耦合**：快照记录 `BinarySHA256` 但本 FR 不自动回滚二进制，需与 FR-468 协同。首版只提示不处理。
- **与 FR-051/031 边界**：快照回滚整体覆盖工作目录；回滚后**不自动**改写 `file_version` / `config_version` 历史，仅 UI 提示。
