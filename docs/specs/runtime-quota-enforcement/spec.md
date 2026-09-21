# 运行期配额强制（FR-467）

> 状态：📋 计划　·　关联 PRD：FR-467　·　依赖：FR-078、FR-079、FR-317、FR-399、FR-401　·　关联 ADR：ADR-019

## 1. 背景与目标

配额与资源限制已经是平台能力，但**全部只在「创建时」或「启动时」生效，运行期完全不强制**：

| 能力 | 实现位置 | 生效时机 | 运行期行为 |
|---|---|---|---|
| 组配额（实例数 / Bot 数 / 存储） | `model/group.go` `GroupQuota`；`service/instance.go:319-363` | 仅 `Create` 事务内 | **不检查**：组内实例运行后超额增长无任何反应 |
| Docker 资源限额（CPU/内存/磁盘） | `model/instance.go` `CPULimit/MemLimitMB/DiskLimitMB`；`service/instance.go:104-109` 注释「仅 docker 模式」 | 仅 `processType=docker` 注入 cgroup（`process/docker.go:163` `applyResourceLimits`） | **直连/守护进程模式无任何限额** |
| 内存水位守卫 | FR-317 `internal/platform/memguard` + `worker/process/memguard.go preflightMemory` | 仅**启动前**一次 | 运行中内存暴涨不拦（只拦下一次启动） |

真机痛点：一个 `processType=daemon` 的 Paper 实例（无 cgroup 限制）持续吃内存/CPU 直到把节点拖垮——`memguard` 只在启动那一刻挡；组配额 `maxStorageMB` 靠备份大小估算（`instance.go:349` 有 `TODO(FR-003)`），运行期磁盘写入失控无人管。**要求的不是「再拦一次启动」，而是运行期的持续强制与超限处置。**

**范围内**
- 运行期采集实例 CPU / 内存(RSS) / 磁盘占用，与「该实例/所属组的配额」比对
- 超限处置策略：**告警 / 限流 / 停止**（三档，可配置）
- 非 docker 模式（direct / daemon）亦生效
- 「配额」来源：组配额（`GroupQuota`）+ 实例级资源上限（`CPULimit/MemLimitMB/DiskLimitMB`，扩展到全模式）

**不做（范围外）**
- 不引入 cgroup v2 之外的通用容器运行时；非 docker 模式的强制手段见 §2.4（首版以「观测 + 处置」为主，**不做内核级 CPU 节流**）
- 不改动 FR-317 的启动前守卫语义（运行期强制是它的补充，不是替代）
- 不做跨节点的分布式配额账本（按组归属在 CP 侧统一核算）

## 2. 设计

### 2.1 运行期指标来源（决策：复用既有采集，不新开通道）

- **实例 CPU / RSS**：Worker 侧已有两条现成路径——
  - `worker/heartbeat/process_metrics.go` 的 `collectProcessMetrics`（随心跳上报 `ProcessMetricSample`，含 CPU%/RSS/进程树）
  - `worker/grpc/instance_resource_snapshot.go` 的 `GetInstanceResourceSnapshot`（按需查根进程 + 完整子进程树，FR-399）
- **磁盘占用**：Worker 工作目录大小（新增：`GetInstanceResourceSnapshotResponse` 增 `WorkDirBytes`，或沿用节点 `DiskUsedMB` 的采集器 `worker/metrics/collector.go` 思路按实例聚合）

**决策**：运行期强制的判定放在 **CP 侧**，消费既有心跳/快照通道，Worker 不做配额裁决。理由：配额真源（`GroupQuota`、实例限额）在 CP 数据库（架构不变量：Worker 不碰 DB）；Worker 只做「执行处置动作」（停/限），裁决与审计留在 CP 一处，避免双真源。

### 2.2 配额模型统一（决策：新增 `RuntimeQuota` 视图，不改变 `GroupQuota` 语义）

运行期强制需要「实例级」的配额基准，而 `GroupQuota` 是「组级」。新增解析函数把两个来源归一为「实例可用的限额」：

```go
// service/quota.go（新增）
type EffectiveQuota struct {
    CPUCores   float64 // 实例 CPU 上限（核）；0=不限（继承组/无）
    MemLimitMB int64   // 实例内存上限；0=不限
    DiskLimitMB int64  // 实例磁盘上限；0=不限
    EnforceMode EnforceMode // alert | throttle | stop
}
```

来源优先级：**实例级字段（`instance.CPULimit/MemLimitMB/DiskLimitMB`）> 组配额派生 > 不限**。组配额目前只有「存储总量」，故磁盘可按「组 MaxStorageMB − 组内已用」推实例可用余量（精确度有限，作软限）。

### 2.3 运行期强制循环（CP 后台）

`QuotaEnforcer`（`service/quota_enforce.go`，参照 `alert_evaluator.go` 的 `Start/Stop/evaluate` 巡检结构）：
1. 周期（默认 30s，可配 `quota.enforce_interval`）取运行中实例的最新样本；
2. 计算 `usage/limit` 比值；连续 K 次（默认 3，抗抖动）超阈值即触发处置；
3. 按 `EnforceMode` 执行（§2.5）；
4. 重启后自动恢复：实例回落正常区间连续 K 次则解除限流状态。

### 2.4 非 docker 模式如何「生效」（核心决策）

非 docker 模式**没有 cgroup**，无法内核级限 CPU。首版分层手段：**观测 + 告警**（全部模式，超限即触发 `AlertTriggerMetric`，复用 `model/alert.go` 指标阈值规则）；**内存超限 → 停止**（全部模式，与 FR-317 同口径，运行期 RSS 持续超 `MemLimitMB` 则停止实例）；**docker 模式**（既有 cgroup 限额已生效，运行期强制**追加**告警/停止，并感知 cgroup OOM kill 标记原因）；**CPU 软限流**仅 docker 有效，非 docker **降级为告警**（本版不做 nice/cpulimit 注入，避免依赖宿主工具）。

**明确决策**：非 docker 的「CPU 限流」本版**不做内核级节流**，只告警；真正硬限仍建议 `processType=docker`。这是诚实的能力边界。内存与磁盘则三档全支持。

### 2.5 超限处置（三档）与告警对接

```
alert     : 触发告警（含实例/指标/当前值/限额），不干预进程
throttle  : 允许时（docker）收紧 cgroup；非 docker 降级为 alert + 标记「无法限流」
stop      : 优雅停止实例（复用既有停止路径），置 statusReason「配额超限已停止（内存/CPU/磁盘）」，发站内信
```

处置全部写审计（`service/audit.go`）：`instance.quota_exceeded` / `instance.quota_stopped` / `instance.quota_throttled`。`alert` 档复用 `AlertDispatcher.Fire`（`alert_dispatcher.go`），新增触发类型 `AlertTriggerQuotaExceeded`（`model/alert.go`），使「配额超限」可独立配置通知渠道与静默窗口，不与被观测的常规指标告警混淆。

### 2.6 API / MCP

- REST：`GET /instances/:id/quota`（限额 + 实时用量 + 强制状态）、`PUT /groups/:id/quota` 扩展 `enforceMode`。实例级限额沿用既有 `UpdateConfig`（`router/instance.go:310-312` 已支持 `CPULimit/MemLimitMB/DiskLimitMB`）。
- MCP（`mcp/tools_instance.go`）：`instance_quota_status`（读）。

## 3. 任务拆分

- [ ] `service/quota.go`：`EffectiveQuota` 解析（实例级 > 组 > 不限）+ 单测（优先级、边界）
- [ ] `service/quota_enforce.go`：`QuotaEnforcer` 巡检循环（采样 → 连续 K 次判定 → 处置）+ 单测（抗抖动、恢复、三档处置）
- [ ] 非 docker 内存/磁盘超限停止路径 + docker cgroup 超限感知（OOM 归因）
- [ ] `model/alert.go` 新增 `AlertTriggerQuotaExceeded`；`alert_dispatcher` 接入；单测
- [ ] 实例工作目录大小上报：`GetInstanceResourceSnapshotResponse` 增 `WorkDirBytes`（Worker 侧）+ proto 重新生成
- [ ] 设置键 `quota.enforce_interval` / `quota.enforce_mode`（默认 alert）+ `SettingsReader` 接线
- [ ] 审计动作 + 站内信；`router/instance.go` / `router/instance_group.go` 配额端点；MCP `instance_quota_status`
- [ ] 前端实例详情「配额」区（当前值/限额/强制状态）+ i18n
- [ ] 文档同步：ARCHITECTURE、API.md、PRD 状态、CHANGELOG；必要时补 ADR（运行期强制边界）

## 4. 验收标准

- 单测全绿：`EffectiveQuota` 优先级、连续 K 次判定与恢复、三档处置分发、告警触发
- **真机（要真机过）**：① **非 docker 内存超限**：`daemon` 实例设低 `MemLimitMB` → RSS 超限 → 按 `enforceMode` 触发告警/停止，`statusReason` 正确；② **CPU 超限告警**：非 docker 实例 CPU 持续超限 → 触发 `quota_exceeded` 告警（不误停）；③ **docker 模式**：cgroup 限额生效，超限 OOM 被 CP 感知并标记原因；④ 处置后回落正常连续 K 次 → 强制状态解除；⑤ 全过程审计可查、站内信可达
- 横切：既有 `Create` 时配额校验不回归（`instance.go:319-363`）；不改 FR-317 启动守卫行为

## 5. 风险 / 待定

- **非 docker CPU 无法硬限**：诚实边界。待定：v2 是否引入 `cpulimit` / `systemd` 资源控制（本版不做）。
- **组配额存储精确度**：`MaxStorageMB` 现按备份大小估算（`instance.go:349` 的 `TODO(FR-003)`），运行期若改用工作目录实际大小，语义变化需产品确认。
- **误杀风险**：内存瞬时峰值（GC 前）可能触发停止。缓解：连续 K 次 + 阈值留余量（限额 × 1.1）。待定：是否提供「宽限期」配置。
- **采样频率与开销**：`collectProcessMetrics` 有 3s 超时与进程树上限（`processTreeLimit=128`），高频巡检需评估 Worker 压力；默认 30s 折中。
