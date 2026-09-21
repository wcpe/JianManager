# 集群级滚动/分批/灰度编排与配置批量下发收敛（FR-457 / FR-458）

> 状态：📋 计划　·　关联 PRD：FR-457、FR-458　·　依赖：FR-058（实例批量操作）、FR-031（配置引擎）　·　关联 ADR：ADR-050（Worker 进程管理器）

## 1. 背景与目标

玩家数增长到 60+ 台实例后，运维的两类批处理能力都不足：

1. **批量操作是一次性并发扇出，无法安全滚动**。`internal/controlplane/service/instance_batch.go` 的 `Batch()` 固定 `instanceBatchConcurrency = 16`，用 `sync.WaitGroup` + 信号量一次性扇出全部目标，**无分批、无批间隔、无失败即停、无暂停/继续、无按比例灰度**，也不预留进度与取消的落点（`InstanceBatchResult` 只有计数）。对 60+ 台执行「重启」会同时打满 16 并发，无灰度与熔断，一次失败无可挽回——这在生产上是不可接受的。
2. **配置链路只到单实例，跨实例只有只读检查**。`internal/controlplane/service/config.go` 的 `Write/WriteFields/Diff/Rollback` 均以 `instanceID` 为单位；跨实例能力仅 `internal/controlplane/service/schema/crosscheck.go` 的 `CheckPortConflicts/CheckProxyConsistency/CheckForwardingSecret`（`CheckAll` 汇总）——这是**只读一致性告警**，没有「把一份基线推到 N 台」「检出漂移」「一键收敛」。

**目标**：给批量操作加滚动/分批/灰度语义（批大小、批间隔、失败即停、暂停继续、按比例），带进度与取消；给配置链路加「配置模板推送到 N 台」「跨实例漂移检测」「一键收敛到基线」。

**范围内**：CP 侧编排语义 + 配置批量与收敛；复用既有 per-instance RPC 与配置版本机制。**不做**：不改 Worker 侧单实例执行语义；不引入外部编排引擎（Temporal/K8s）；不做配置的跨类型自动改写（模板以整份/字段补丁为准）。

## 2. 设计

### 2.1 FR-457：编排会话与分批语义

- 新增编排实体 `InstanceRollingOp`（model + `internal/controlplane/service/instance_rolling.go`），持久化进度以便「暂停/继续/取消」在 CP 重启后仍可恢复（镜像 `bot_load` 的 run 会话思路）：
  ```go
  type RollingPolicy struct {
      BatchSize    int           // 每批台数（0=不分批，退化为现并发语义）
      BatchInterval time.Duration // 批间隔
      FailFast     bool          // 失败即停
      Ratio        float64       // 灰度比例（0<ratio<1 时按目标比例抽样，首批即灰度）
  }
  type RollingOp struct {
      ID uint; Action InstanceBatchAction; Policy RollingPolicy
      Targets []uint; Cursor int         // 已执行到第几批
      State string                       // pending|running|paused|done|canceled
      Result InstanceBatchResult         // 累计计数 + 失败明细
  }
  ```
- **执行器**：`Batch()` 保留为「一次性」快路径（`BatchSize=0` 时语义不变，向后兼容）；新增 `RunRolling(op *RollingOp)` 异步 goroutine，按批推进：一批内仍用 `instanceBatchConcurrency` 有界并发，批间 `BatchInterval` 等待；每批结束检查 `State`（暂停即阻塞在批边界、取消即停止后续批）；`FailFast` 且批内出现失败 → 停止并置 `done`。
- **目标与鉴权**：目标解析复用 `resolveBatchTargets` + `applyInstanceBatchFilter`（含 scope 收敛）；`Ratio` 仅在 filter 模式下有意义（按稳定序抽样）。
- **灰度**：`Ratio<1` 时首批只取按比例抽样的子集；运维可观察首批无异常后再以 `Ratio=1` 续跑剩余（或新增「晋升」动作），实现两段式灰度。
- **单实例委托不变**：逐台仍经 `delegateBatchOne` 复用既有 per-instance RPC 与状态回写，不新增 Worker 侧语义。
- **进度与取消**：新增查询/暂停/继续/取消端点（只增不改旧端点），编排动作落审计。

### 2.2 FR-458：配置模板下发、漂移检测、一键收敛

- **配置基线（模板）**：新增 `ConfigBaseline`（model），键为 `(scopeKey, filePath)`，值为基线内容 + 内容哈希（sha256，与 `config.go` 落 `InstanceConfigVersion.ContentHash` 同口径）。scopeKey 可为分组/网络/标签集合，限定「哪些实例应共享该基线」。
- **推送到 N 台**：`Converge` 服务方法接收目标实例集合与基线，逐台调用既有 `ConfigService.Write(instanceID, filePath, content, ...)`——**复用**其版本落库、Worker 侧 `WriteConfig` 校验与字段补丁（`WriteFields`），不另建写入通道。批量结果复用 2.1 的编排会话（可分批/失败即停/进度）。
- **漂移检测**：`DetectDrift(baselineID)` 对 scope 内每台实例取该 `filePath` 的**当前内容哈希**（优先读最新 `InstanceConfigVersion`，缺失则现场 `ConfigService.Read` + 计算哈希），与基线哈希比对；不一致即 drift。输出逐台 `{instanceID, drift: bool, currentHash, baselineHash}`。只读、零副作用。
- **一键收敛**：`Converge` = 对所有 drift 实例推送基线；收敛后再跑一次 `DetectDrift` 复核并将残余漂移回报。
- **与既有跨实例检查协同**：推送前可先跑 `schema.CheckAll`（端口冲突/代理一致性/转发密钥）作前置告警，避免把冲突配置推满集群；不强制阻断（告警档）。

## 3. 任务拆分

- [ ] T1 `InstanceRollingOp` model + `instance_rolling.go`：策略结构、分批执行器、暂停/继续/取消（依赖无）
- [ ] T2 `Batch()` 向后兼容改造（`BatchSize=0` 走旧路径）+ 编排端点与权限校验（依赖 T1）
- [ ] T3 `ConfigBaseline` model + `DetectDrift`（只读）（依赖无）
- [ ] T4 `Converge`（推送基线到 N 台，复用 `ConfigService.Write` 与编排会话）+ 收敛后复核（依赖 T1、T3）
- [ ] T5 前端入口（批量面板加策略、配置页加基线与漂移视图）（依赖 T1~T4）
- [ ] T6 编排/漂移/收敛单测 + 集成（依赖 T1~T4）

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | 对 60+ 台执行「每批 5 台、批间隔 N 秒」的滚动重启，逐批推进、服务器逐批恢复 | 真机 |
| 2 | `FailFast` 开启时某批失败即停止后续批，结果含失败明细 | 真机 + 单测 |
| 3 | 编排可暂停/继续/取消，且 CP 重启后进度可恢复 | 真机 + 单测 |
| 4 | `Ratio<1` 灰度首批仅执行按比例子集 | 单测 |
| 5 | `BatchSize=0` 时行为与现 `Batch()` 一致（向后兼容） | 单测 |
| 6 | 把一份 `server.properties` 推送到选定分组，N 台均落新版本 | 真机 |
| 7 | 手工改某台后 `DetectDrift` 检出该台漂移，其余不报 | 真机 |
| 8 | `Converge` 把漂移实例收敛回基线，复核残余漂移为 0 | 真机 |
| 9 | 编排/收敛动作写审计 | 真机 |

## 5. 风险 / 待定

- **批间隔与 Minecraft 启动时长**：滚动重启的批间隔须大于服务器「起来并稳定」的时间，否则连续批会叠加资源压力。待定：批间隔是否支持「等待健康即进入下一批」的自动模式（依赖 FR-459 巡检）。
- **基线 scope 的粒度**：以分组/网络/标签定义共享基线，需避免「无关实例被同一条基线收敛」。首版按分组，标签/网络作为待定扩展。
- **灰度抽样稳定性**：`Ratio` 抽样需稳定序（按 id），避免每次抽样不同导致漏收敛。
- **配置写入的原子性**：逐台 `Write` 非事务；单台失败不回滚已写台。以「收敛后复核 + 失败明细」暴露残差，不追求全局事务。
- **与 FR-052/插件批量部署的关系**：本 spec 只做实例配置（`ConfigService` 覆盖的配置文件），不含制品/jar 批量（归 `plugin-batch-deploy`）。
