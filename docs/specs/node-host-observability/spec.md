# 功能规格：节点主机级观测采集补全

> 状态：草拟（待审核）　·　关联 PRD：FR-513（P2）　·　分支：feature/web-overhaul
>
> 本 spec 补充并订正 `docs/specs/inflight-backlog/spec.md` §FR-513。**订正点见 §6 待定 1**。

## 1. 背景与目标

首页三个资源球（CPU / 负载 / 内存）的悬浮卡当前只能回答「哪个节点、哪些**受管**实例、哪些**受管**进程在吃资源」——因为 Worker 只采了这些。运维真正要判断的「这台主机本身怎么回事」缺数据：

- 负载只有 `load_avg1` 单值，看不出是瞬时尖峰还是持续爬升；
- 只有**受管**进程 top，看不到整机上谁在吃 CPU（例如系统进程、别人的进程）；
- 磁盘只有一个根分区使用率，`/var` 写满而 `/` 还宽松时完全无感；
- CPU 只有总使用率，分不清 user / system / iowait——**IO 瓶颈会被误读成「CPU 不满、机器很闲」**。

目标：把上述主机级数据补齐到「Worker 采集 → CP 落库 → API 暴露 → 首页悬浮卡展示」的完整链路，让悬浮卡能支撑「这台机器为什么慢」的判断。

属于阶段：**P2**（增强，非阻塞项）。

## 2. 需求（要什么）

### 范围内

1. **负载三档**：`load1` / `load5` / `load15` 原始值（保持与现有 `load_avg1` 同口径，不在此层归一化——归一在 CP 侧按核数做）。
2. **进程计数**：活动进程数（runnable）与总进程/线程数。
3. **CPU 时间构成**：`user` / `nice` / `system` / `idle` / `iowait` / `irq` / `softirq` / `steal`（按窗口增量归一的占比，0..1）。`guest` / `guest_nice` 为范围内可选，见 §6 待定 2。
4. **整机 top-N 进程**：按 CPU 或 RSS 排序取前 N（N ≤ 5），**只上报 `pid` / `comm` / `cpuPct` / `rssBytes`**。
5. **分挂载点磁盘**：按挂载点分别采集总容量 / 已用 / 使用率，覆盖 `/` 与数据根所在分区，另含常见独立挂载（若存在）。
6. 上述数据**随心跳上行**并落 CP 时序库，经 `GET /metrics/resource-attribution` 暴露给首页悬浮卡。
7. 前端悬浮卡补上对应展示位（**订正：版式并未就位，见 §6 待定 1**）。

### 不做（范围外）

- **不上报完整 `cmdline`**（隐私边界，沿用 spec 原文约束）：只有 `comm`。
- 不改动现有 `topProcesses`（受管进程树）的语义与渲染——整机 top-N 是**新增并列块**，不是替换。
- 不做告警规则扩展（iowait 告警已在 FR-485 覆盖；本 FR 不加新告警类型）。
- 不引入新的采集守护进程/agent：复用现有心跳与 `CPUSampler`。
- 不做指标保留策略变更（沿用 raw 48h / 5m 30d / 1h 400d）。

## 3. 设计（怎么做）

### 3.1 proto（`proto/worker.proto`）

`HeartbeatRequest` 现有字段最大编号 **17**（`iowait`），新字段从 **18** 起。全部为**新增 optional 语义字段**：旧 CP 忽略未知字段、旧 Worker 不发新字段，两侧都不改变既有语义（与 FR-485 `iowait` 同款兼容策略）。

新增字段（编号 18 起，具体分配在实现时按序落地）：

- 负载：`load5`、`load15`（`load1` 已由 `load_avg1=11` 承载）
- 进程计数：`procs_running`、`procs_total`
- CPU 构成：`cpu_user`、`cpu_nice`、`cpu_system`、`cpu_idle`、`cpu_iowait`、`cpu_irq`、`cpu_softirq`、`cpu_steal`（均为 0..1 窗口占比）
- 整机 top-N：新增消息 `HostProcessSample { uint32 pid; string comm; float cpu_pct; uint64 rss_bytes; }`，`repeated HostProcessSample host_top_processes`
- 分挂载点磁盘：新增消息 `HostMountUsage { string mountpoint; uint64 total_bytes; uint64 used_bytes; float used_pct; }`，`repeated HostMountUsage host_mounts`

> **不复用 `process_count`**：`GetInstanceResourceSnapshotResponse.process_count`（`worker.proto:1161`）是**实例内**进程数，与主机总进程数语义不同，混用会造成同一名字两种含义。

### 3.2 Worker 采集（`internal/worker/register/register.go`）

`CollectHeartbeatData` 内扩展，复用现有依赖、不自写 `/proc` 解析：

- **CPU 构成**：`internal/worker/metrics/cpu_sampler.go` 现有的 `cpu.TimesWithContext` 增量快照已含全部十列，只需把 `TimesStat` 的各列按同一 `cpuTotal(delta)` 归一后发布（`IOWaitRatio` 已在 `collector.go:57` 提供单列范本，按其形式扩展为结构体）。
- **负载三档**：`load.Avg()` 已返回 `Load1/Load5/Load15`，当前只取了 `Load1`（`register.go:259-261`），补两个字段即可。
- **进程计数**：从 `/proc/stat` 的 `procs_running` / `procs_blocked` 与进程总数派生。**用 gopsutil `process.Pids()` 长度取总数会全表扫描**，与「采集开销受控」冲突 → 优先读 `/proc/stat` 与 `/proc/loadavg` 末两列（运行/总线程数），见 §6 待定 3。
- **整机 top-N**：`process.Processes()` + 逐个 `CPUPercent`/`MemoryInfo` 是 O(全进程) 且有显著开销 → 受 **≤1 次/10s 节流**（spec 原文要求），且与心跳 30s 周期对齐（即每心跳最多采一次）。
- **分挂载点磁盘**：`disk.Partitions(false)` 取物理分区 → 对每个挂载点 `disk.Usage(mp)`。**必须跳过**伪文件系统与容器 overlay，避免噪声与错误容量。

新增配置（`internal/worker/config.go`，`log_*` 之外新命名空间 `host_observe.*`）：
- `host_observe.top_n`（默认 5，上限 10）
- `host_observe.top_interval`（默认 10s，最小值 10s，防止高频全表扫描）
- `host_observe.mounts`（显式挂载点白名单；空=自动发现物理分区）

### 3.3 CP 落库

两条路径分开，**不混**（沿用 iowait 的既定形态）：

- **时序**（权威，供图表与趋势）：`MetricService.ingestHeartbeatAt` 新增 metric_key，写 `MetricSampleRaw`。新增 key 常量集中定义在 `internal/controlplane/model/metric.go`（如 `node_load5` / `node_load15` / `node_procs_running` / `node_procs_total` / `node_cpu_user` / … / `node_mount_used_pct`）。
  - **多挂载点是多值维度**，`MetricSampleRaw` 是单值表 → 采用 `metric_key` 后缀编码挂载点（如 `node_mount_used_pct{mp=/var}` 编码为 `node_mount_used_pct@/var`），维度表 `MetricSeries` 记录完整标签。见 §6 待定 4。
- **节点当前值**（供节点列表/悬浮卡即时读）：`HandleHeartbeat` 的 `updates` map 增补标量列（`load_avg5`、`load_avg15`、`procs_running`、`procs_total`、CPU 构成各列）。**分挂载点与 top-N 不进 `nodes` 表**（多值，不适合宽表），改由独立快照表承载：
  - `HostMountSnapshot`（node_uuid、mountpoint、total/used、sampled_at，TTL 与 raw 一致 48h）
  - `HostProcessSnapshot`（node_uuid、pid、comm、cpu_pct、rss_bytes、sampled_at，TTL 48h）
  - 两张表随心跳写入；旧记录按 `sampled_at` 清理，复用现有 `metric.go` 的清理巡检。

### 3.4 API

`GET /metrics/resource-attribution`（`ResourceAttributionAt`）扩展：

- `ResourceAttributionNode` 新增：`load5Pct` / `load15Pct`（已归一）、`procsRunning` / `procsTotal`、`cpuBreakdown { user, nice, system, idle, iowait, irq, softirq, steal }`、`mounts []{ mountpoint, totalBytes, usedBytes, usedPct }`。
- `ResourceAttributionResult` 新增：`hostTopProcesses []{ nodeId, nodeName, pid, comm, cpuPct, rssBytes }`。
- **向上兼容**：所有新字段为 nullable；旧 Worker 无数据时返回 `null`/空数组，前端显示 `--`。这是 PRD 验收①的落点。

### 3.5 前端（`OverviewPageView.tsx`）

**订正：spec 原文称「版式已就位、无需再改版式」，与代码不符**——`GaugeAttributionTooltip`（`:320-473`）当前只有「按节点 / Top 实例 / Top 受管进程」三块，没有任何新增字段的渲染位。因此本 FR 必须同时补版式：

- 负载球悬浮卡：结论徽标下方增加三档负载一行（`1m / 5m / 15m`）。
- CPU 球悬浮卡：新增「CPU 构成」分段（`user/system/iowait/...`，只展示 Top 3-4 项 + 其余归并为「其他」，避免 10 行噪声）。
- 内存/磁盘球悬浮卡：新增「分挂载点」分段（挂载点 + 使用率，`≥90%` 高亮）。
- 新增「整机 Top 进程」分段，与既有「Top 受管进程」**并列**，标题需明确区分（前者「整机」、后者「受管」），否则会被误读为同一份数据。
- i18n：沿用 `dashboard.attribution*` 命名空间新增键，中英齐备。

## 4. 任务拆分

- [ ] proto：`HeartbeatRequest` 新字段（18 起）+ `HostProcessSample` / `HostMountUsage` 消息，`make proto` 重生成
- [ ] Worker：`cpu_sampler` 发布 CPU 十列构成；`CollectHeartbeatData` 补负载三档 / 进程计数 / top-N / 分挂载点；新增 `host_observe.*` 配置与校验
- [ ] CP 落库：`model.Node` 标量列 + `HostMountSnapshot` / `HostProcessSnapshot` 两表与迁移；`HandleHeartbeat` 写入；`ingestHeartbeatAt` 新增 metric_key
- [ ] CP API：`ResourceAttributionNode` / `ResourceAttributionResult` 扩字段 + 取数（含 nullable 兼容）
- [ ] 前端：悬浮卡四处版式补充 + i18n 中英
- [ ] 测试：Worker 采集单测（伪 `/proc` 夹具）、CP 落库与 API 兼容单测（旧 Worker 缺字段 → null）、前端 DOM 用例（含 `--` 兜底）
- [ ] 文档同步：PRD FR-513 状态、ARCHITECTURE（心跳字段表）、API（resource-attribution 响应）、CHANGELOG

## 5. 验收标准

1. `HeartbeatRequest` 新增字段全部生效；**旧 Worker（不发新字段）心跳不被拒绝**，CP 侧对应值落为 null，前端显示 `--`（自动化用例覆盖）。
2. `GET /metrics/resource-attribution` 返回 §3.4 全部新字段，形状与类型符合契约；无数据时为 `null` 而非 0（避免「0 负载」被误读为真实读数为零）。
3. 首页悬浮卡在真实浏览器（Playwright）中显示：负载三档、CPU 构成、分挂载点、整机 Top 进程四类新数据；且「整机 Top 进程」与「Top 受管进程」标题可区分。
4. 采集开销受控：整机进程扫描**≤1 次/10s**（用例断言节流器行为，不靠人眼）；top-N 全表扫描不在每次心跳都发生。
5. 隐私边界：上报内容不含 `cmdline`；用例断言 `HostProcessSample` 无 `cmdline` 字段。
6. 分挂载点采集跳过伪文件系统（`tmpfs`/`overlay`/`proc` 等）——用例给定伪分区夹具，断言不产生条目。
7. 既有测试全绿（尤其 `metric_test.go` 的 resource-attribution 用例与前端 `OverviewPageView` 相关 DOM 用例）。

> 第 3 条属**真实环境维度**：需在真浏览器复验并留截图，单元/e2e 全绿不替代它。

## 6. 风险 / 待定

1. **【需拍板】spec 原文与实际不符**：`inflight-backlog/spec.md` §FR-513 写「前端版式已就位（FR-512 同批），数据到位即可填充，无需再改版式」并称「前端已预留位置」。实测 `GaugeAttributionTooltip` 只有三块既有内容，**无任何新字段渲染位**。本 spec 按「需补版式」编排任务与验收（§3.5）。若你确认另有未合入的版式分支，请指出，我再据此收敛范围。
2. **【需拍板】CPU 构成是否含 `guest` / `guest_nice`**：`/proc/stat` 首行有这两列，但对非虚拟化宿主恒为 0。默认**不含**（减少无效字段与前端噪声）；若生产有 KVM 宿主需要观测，则纳入。
3. **【需拍板】进程总数的取法**：`/proc/loadavg` 末两列是「运行/总线程数」（廉价、无扫描），`process.Pids()` 长度更精确但要遍历 `/proc`。默认取前者（符合「采集开销受控」），代价是与 `ps -e` 计数可能差几十。
4. **【需拍板】多值维度的编码方式**：分挂载点与 top-N 是多值，`MetricSampleRaw` 是单值表。方案 A：`metric_key` 内嵌挂载点（`node_mount_used_pct@/var`）；方案 B：不改时序表，只进 `HostMountSnapshot`，时序仅保留「最大使用率」等聚合值。默认 **A**（可查历史趋势），代价是 `metric_key` 基数随挂载点数增长。
5. **风险**：top-N 进程扫描在进程数极多（数千）的宿主机上单次开销可能达数十毫秒，落在心跳 goroutine 会拖慢心跳。缓解：扫描放独立 goroutine + 结果快照，心跳只读快照（与 `CPUSampler` 同款生产者/消费者形态）。
6. **风险**：`disk.Partitions(false)` 在容器内可能仍返回 overlay 之外的主机分区，导致跨容器泄漏宿主挂载信息。缓解：`host_observe.mounts` 白名单优先于自动发现；文档注明容器部署需显式配置。
