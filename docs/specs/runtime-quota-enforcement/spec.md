# 运行期配额强制（FR-467）

> 状态：🟡 **代码与单测已交付，真机验收待做**（FR-467 端到端已实现，单测/构建覆盖通过；§4 中标「真机」的验收项尚未在真机验证）　·　关联 PRD：FR-467　·　依赖：FR-078、FR-079、FR-317、FR-399、FR-401　·　关联 ADR：ADR-019

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
1. 周期（默认 **60s**，可配 `quota.enforce_interval`）取运行中实例的最新样本；
2. 计算 `usage/limit` 比值；连续 K 次（默认 **5**，抗抖动）超阈值即触发处置；
3. 按 `EnforceMode` 执行（§2.5）；
4. 回落恢复：实例回落正常区间连续 K 次则解除强制，并**真正 Resolve 活跃告警事件**
   （`AlertDispatcher.Resolve`，dedup key 与 Fire 一致 `quota:<instanceId>:<kind>`）——
   仅写审计不 Resolve 会让告警永久停留活跃，且解除后再超限也不再告警。

**判定窗口（M-2 修正）**：`interval × K` 是真正决定「抗抖动」能力的量。原默认 30s×3=90s
短于 MC 的正常长尾（世界加载/区块生成的 RSS 在限额附近可持续数分钟，GC 前堆峰值同理），
故默认调整为 **60s×5 ≈ 5 分钟**，跨过长尾再处置。需要更快响应的部署可下调
`quota.enforce_streak`（设置项即时生效）；`alert` 档只告警，不受该窗口的破坏性影响。

**巡检周期下限的换算关系（配置时必须核对）**：一轮巡检的最坏耗时由**采样并发度**决定，
而每实例的按需采样受 CP 侧 `quotaDiskTimeout = 15s`（`quota_metric_source.go`）约束，故

```
一轮最坏耗时 ≈ ceil(限额实例数 / quotaSampleConcurrency) × quotaDiskTimeout
周期预算     : quota.enforce_interval 必须 ≥ 上式
```

**推荐的 `quota.enforce_interval` 下限 = `ceil(N / 8) × 15s`**（N = 有配额约束的运行中实例数；
`quotaSampleConcurrency = 8`）——即 8 服 15s、64 服 **120s**、128 服 240s。
默认 60s 因此**只对 ≤32 个限额实例成立**；64 服规模下若仍配 60s，一轮最坏耗时（120s）
会超过周期。此时代价是**拍间隔被拖长**而非并发重叠：`evaluate()` 在 ticker 循环里是
**同步**执行的，单轮未结束时不会有第二轮进来（Go ticker 的 tick 被合并丢弃，不会排队重入），
故最坏情况是实际节拍变成 `max(interval, 一轮耗时)`，而不是两轮同时在跑。

判定影响：连续 K 次计数按**实际拍**推进，`interval × K` 于是**不再是真实判定窗口**
（64 服 + interval=60s 时真实窗口约 120s×5=10 分钟），`stop` 档的实际反应时间会
晚于名义值。运行期强制因此仍**正确但不及时**——不会丢样本、不会误判超限与否。
判定：按规模取 `max(60s, ceil(N/8) × 15s)`。需要更密的巡检就同时**调大并发度**
（`quotaSampleConcurrency`，需改码）或**调小单实例采样超时**，而不是只压 interval。
`alert` 档不受该拉长影响（无破坏性动作），故默认值对 alert 档安全。

**内存维度的采样来源（M-2 修正）**：内存判定**不使用**心跳上报的进程 TOPN 样本。
后者被裁为「每实例最多 10 个进程、按 CPU 排序」（`worker/heartbeat/process_metrics.go`），
存在双向系统性偏差：主进程 CPU 低时会被挤出样本 → SUM 低估全树 RSS（**漏判**）；
主进程独占 CPU 时又恰好准确 → 行为在两种形态间摇摆。故内存/CPU/磁盘统一改为走
`GetInstanceResourceSnapshot`（FR-399 通道）：Worker 侧遍历**完整**进程树求和，不受 TOPN 截断。
该调用一次取回 RSS/CPU/磁盘三项（原先为进程与磁盘各发一次 RPC），并有界并发
（`quotaSampleConcurrency = 8`）——64 服规模下串行同步 RPC 会把巡检周期拖长。
心跳 TOPN 样本退居**兜底**：节点不可达时才用于 CPU 判定，且采样不可用时不参与连续计数
（绝不用「无数据」冒充「未超限」）。

### 2.4 非 docker 模式如何「生效」（核心决策）

非 docker 模式**没有 cgroup**，无法内核级限 CPU。首版分层手段：**观测 + 告警**（全部模式，超限即触发 `AlertTriggerMetric`，复用 `model/alert.go` 指标阈值规则）；**内存超限 → 停止**（全部模式，与 FR-317 同口径，运行期 RSS 持续超 `MemLimitMB` 则停止实例）；**docker 模式**（既有 cgroup 限额已生效，运行期强制**追加**告警/停止，并感知 cgroup OOM kill 标记原因）；**CPU 软限流**仅 docker 有效，非 docker **降级为告警**（本版不做 nice/cpulimit 注入，避免依赖宿主工具）。

**明确决策**：非 docker 的「CPU 限流」本版**不做内核级节流**，只告警；真正硬限仍建议 `processType=docker`。这是诚实的能力边界。内存与磁盘则三档全支持。

### 2.5 超限处置（三档）与告警对接

```
alert     : 触发告警（含实例/指标/当前值/限额），不干预进程
throttle  : 允许时（docker）落「待收紧限额」并在下次启动生效；非 docker 降级为 alert + 标记「无法限流」
stop      : 优雅停止实例（复用既有停止路径），置 statusReason「配额超限已停止（内存/CPU/磁盘）」，发站内信
```

**throttle 档的真实语义（M-1 修正）**：docker 的 cgroup 限额只能在**创建容器**时注入，
运行期无法收紧。首版实现只写了一条 `success=true` 的审计并把文案写成「下次启动生效」，
却**没有任何字段承载这个意图**、启动路径也不读取它 → 限流永不发生（审计与事实不符，
是最坏的一类失真）。修正后：
- throttle 触发时把收紧目标**持久化**到实例行 `throttle_cpu_limit` / `throttle_mem_limit_mb`；
- **生效时机是「此后重启」而非「登记即生效」**：登记本身只写库，不改变正在运行的容器。
  实际链条是「持续超限 → 巡检判定达 K 次 → 登记待收紧值 → **之后再重启/重建**该容器」，
  由规格翻译（`toStartSpec`）在构造启动规格时消费，生效限额 =
  `min(运维配置限额, 待收紧限额)`（`effectiveCPULimit` / `effectiveMemLimitMB`）。
  故在重启发生前，运行中容器**仍是旧限额**——审计里如实写「下次启动生效」正是这个意思，
  不是「启动时会自动补登记」（无人为重启时不会自动收紧，这是 docker 的 cgroup 限额只能在
  创建容器时注入所致的固有限制，不能靠巡检自愈）；
- 生效限额 = `min(运维配置限额, 待收紧限额)`——**收紧是单向的**，绝不因待收紧项放宽显式配置；
- 收紧目标 = 生效限额 × 0.9（`quotaThrottleTightenRatio`）压到限额之下留一档余量。
  **不能**按「当前用量 × 余量」计算：用量必然 ≥ 限额（否则不会触发），
  由此算出的目标恒大于限额，永远没有可采纳的收紧值，等于换个写法继续不生效；
- 落库失败则记 `success=false` 并降级为告警，不谎称已收紧；
- 无可进一步收紧的维度（磁盘无 cgroup 手段、或现值已更严）只告警，不虚报变更。

**恢复即清零（R7 修正）**：待收紧限额的写入是**双向**的——超限解除时必须把该维度的待收紧值
**清零并落库**（`clearThrottleOnRecover`）。原实现只有收紧写入、全仓没有清零路径，于是实例
一旦超限过一次就被**永久钉在 90% 硬顶**：`effectiveCPULimit` / `effectiveMemLimitMB` 在每次
启动都取 `min(配置限额, 待收紧限额)`，运维扩容、清理工作目录等任何使超限解除的动作都不再有用；
配额视图还会继续回显这个旧值，运维看到「无超限」+「有限额」自相矛盾。语义约定：
- **按维度独立清零**：CPU 恢复只清 `throttle_cpu_limit`，内存恢复只清 `throttle_mem_limit_mb`。
  部分恢复（CPU 已恢复但内存仍超限）时保留仍在生效的那一维，避免「修复」误撤另一维防护；
- 清零**只写待收紧列**，不触碰 `CPULimit` / `MemLimitMB`（运维显式配置）；生效限额由此自然回落到
  配置值，无需修改 `effective*` 逻辑；
- 清零落库失败则恢复审计记 `success=false` 并写明原因（「不再被限流」这半句没兑现，
  不能让运维据成功审计误判实例已回到配置限额）。

处置全部写审计（`service/audit.go`）：`instance.quota_exceeded` / `instance.quota_stopped` / `instance.quota_throttled` / `instance.quota_recovered`。**审计的 success 必须反映实际动作**：未落库、未收紧、能力不支持一律 `success=false` 并写明原因。`alert` 档复用 `AlertDispatcher.Fire`（`alert_dispatcher.go`），新增触发类型 `AlertTriggerQuotaExceeded`（`model/alert.go`），使「配额超限」可独立配置通知渠道与静默窗口，不与被观测的常规指标告警混淆。

### 2.6 API / MCP

- REST：`GET /instances/:id/quota`（限额 + 实时用量 + 强制状态）、`PUT /groups/:id/quota` 扩展 `enforceMode`。实例级限额沿用既有 `UpdateConfig`（`router/instance.go:310-312` 已支持 `CPULimit/MemLimitMB/DiskLimitMB`）。
- MCP（`mcp/tools_instance.go`）：`instance_quota_status`（读）。

## 3. 任务拆分

- [x] `service/quota.go`：`EffectiveQuota` 解析（实例级 > 组 > 不限）+ 单测（优先级、边界）
- [x] `service/quota_enforce.go`：`QuotaEnforcer` 巡检循环（采样 → 连续 K 次判定 → 处置）+ 单测（抗抖动、恢复、三档处置）
- [x] 非 docker 内存/磁盘超限停止路径 + 全树 RSS 采集（M-2：不再依赖心跳 TOPN 10 进程样本）+ 测试
- [x] **throttle 档真收紧（M-1）**：`Instance.throttle_cpu_limit` / `throttle_mem_limit_mb` 持久化待收紧限额
      + 启动规格翻译取 `min(配置, 待收紧)` + 测试
- [x] `model/alert.go` 新增 `AlertTriggerQuotaExceeded`；`alert_dispatcher` 接入；单测
- [x] **恢复真正 Resolve 活跃告警（s-3）**：`resolveQuotaAlert` + 测试（超限→恢复→再超限可再告警）
- [x] **恢复即清零待收紧限额（R7）**：`clearThrottleOnRecover` 按维度独立清零并落库 + 测试
      （超限→收紧→解除→清零、部分恢复只清对应维度）
- [x] 实例工作目录大小上报：`GetInstanceResourceSnapshotResponse` 增 `WorkDirBytes`（Worker 侧）+ proto 重新生成
- [x] 磁盘遍历可中断 + 巡检采样并发化（s-5：`filepath.WalkDir` + ctx 检查、`quotaSampleConcurrency`）
- [x] 设置键 `quota.enforce_interval`（默认 60s）/ `quota.enforce_streak`（默认 5）/ `quota.enforce_mode`（默认 alert）+ `SettingsReader` 接线
- [x] 审计动作 + 站内信；`router/instance.go` / `router/instance_group.go` 配额端点；MCP `instance_quota_status`
- [x] 前端实例详情「配额」区（当前值/限额/强制状态 + 待收紧限额 + `enforceStateScope`）+ i18n
- [x] 文档同步：ARCHITECTURE、API.md、PRD 状态、CHANGELOG；必要时补 ADR（运行期强制边界）

## 4. 验收标准

- 单测全绿：`EffectiveQuota` 优先级、连续 K 次判定与恢复、三档处置分发、告警触发、恢复 Resolve、
  **throttle 待收紧限额落库与单向收紧**、全树 RSS 采样路径
- **真机（待真机验收）**：① **非 docker 内存超限**：`daemon` 实例设低 `MemLimitMB` → RSS 超限 → 按 `enforceMode` 触发告警/停止，`statusReason` 正确；② **CPU 超限告警**：非 docker 实例 CPU 持续超限 → 触发 `quota_exceeded` 告警（不误停）；③ **docker 模式**：cgroup 限额生效，超限 OOM 被 CP 感知并标记原因；④ 处置后回落正常连续 K 次 → 强制状态解除且活跃告警被 Resolve；⑤ 全过程审计可查、站内信可达；⑥ throttle 档登记的待收紧限额在实例重启后确实体现在容器 cgroup 上
- 横切：既有 `Create` 时配额校验不回归（`instance.go:319-363`）；不改 FR-317 启动守卫行为

## 5. 风险 / 待定

- **非 docker CPU 无法硬限**：诚实边界。待定：v2 是否引入 `cpulimit` / `systemd` 资源控制（本版不做）。
- **组配额存储精确度**：`MaxStorageMB` 现按备份大小估算（`instance.go:349` 的 `TODO(FR-003)`），运行期若改用工作目录实际大小，语义变化需产品确认。
- **误杀风险**：内存瞬时峰值（GC 前）可能触发停止。缓解：连续 K 次 + 阈值留余量（限额 × 1.1）+ 判定窗口 60s×5 ≈ 5 分钟（跨过世界加载/GC 长尾）。待定：是否提供「宽限期」配置。
- **采样频率与开销**：`collectProcessMetrics` 有 3s 超时与进程树上限（`processTreeLimit=128`），高频巡检需评估 Worker 压力；默认 60s 折中。
- **周期与规模的换算关系（必须按规模核对，见 §2.3）**：一轮最坏耗时 ≈ `ceil(限额实例数 / quotaSampleConcurrency) × quotaDiskTimeout(15s)`，
  故推荐 `quota.enforce_interval ≥ max(60s, ceil(N/8) × 15s)`——64 服时应为 **120s**，默认 60s 只对 ≤32 个限额实例成立。
  周期偏小时 `evaluate()` 同步执行使实际节拍被拖长为「一轮耗时」（不丢样本、不误判），
  但 `interval × K` 不再是真实判定窗口，`stop` 档反应会晚于名义值。`alert` 档不受影响。
- **「10 进程截断」这一系统性偏差（M-2 记录）**：心跳的进程样本被裁为每实例最多 10 条且按 CPU 排序
  （`worker/heartbeat/process_metrics.go` 的 `processMetricLimit`），故其 SUM 既可能低估全树 RSS（漏判）
  也可能恰好准确（取决于 CPU 落在哪个进程），行为在两种形态间摇摆。**处理**：内存/CPU 判定不再依赖该样本，
  改走 `GetInstanceResourceSnapshot` 的全树采集；心跳样本仅作节点不可达时的 CPU 兜底。
  该限制对**其它**（展示类）消费方仍然存在，不应据心跳 SUM 做容量判定。
- **磁盘遍历成本（s-5）**：工作目录可能数十 GB，遍历单独计预算（`instanceWorkDirSizeTimeout = 10s`）且
  改用可中断的 `filepath.WalkDir`（每项前检查 ctx），避免超时后仍占住 goroutine；巡检侧采样并发化
  （`quotaSampleConcurrency = 8`）以免 64 服规模下超过巡检周期。
- **强制状态的作用域（s-2）**：`QuotaEnforcer.states` 是**本次进程内**内存态，重启后重新累积。
  `GET /instances/:id/quota` 以 `enforceStateScope = "in_process"` 显式标注，历史事实应查审计。
