# FR-343 · 实例系统级基础指标（无需探针，走系统直取）

> 状态：✅ 已交付@v0.18.0 · 类型：feat（增强 FR-170/142/010）
> 盘问结论：用户「TPS/MSPT mock 假数据」的真相 = 实例大多没连 ServerProbe → 无源显占位；
> 后端时序早已真数据（FR-060/169/170/221）。真正要做 = **没探针也要有基础指标（CPU/内存/运行时长走系统直取）** + **去除概览里写死的 `mock-api` 占位**，不是"改 mock 为真实"。

## 目标

1. 未部署/未连 ServerProbe 的**运行中**实例，实例详情概览也能显示**真实** CPU% / 内存(RSS) / 运行时长——数据由 Worker 侧直接采进程（gopsutil），不依赖探针。
2. 去除实例概览中写死的 `mock-api` 假 TPS/MSPT 火花线，接真实时序；无探针数据时诚实标注「需探针」，不再显示假图。
3. 有探针时行为不变：TPS/MSPT/在线数/堆/线程/世界仍来自 ServerProbe 富指标。

## 设计

### 后端（Worker `GetInstanceMetrics`）——无 proto 变更

`GetInstanceMetricsResponse` 的 `cpu_percent` / `memory_mb` / `uptime_seconds` 字段**已存在**，本 FR 只是在**探针不可用时也填充**它们（从系统直取）：

- 现状：非 docker 分支已采 `memory_mb`（进程 RSS，`GetInstancePID` → gopsutil `MemoryInfo().RSS`）；但 **cpu_percent / uptime_seconds 仅探针填充**，无探针时为 0。
- 改动：非 docker 分支在 RSS 之后补采
  - `cpu_percent` ← gopsutil `proc.CPUPercent()`（累计 CPU%，与 FR-170 进程采样器同源、非阻塞）
  - `uptime_seconds` ← `(now - proc.CreateTime()) / 1000`
- 探针可用时仍照旧覆盖（TPS/在线/堆/MSPT/线程/CPU(系统负载)/uptime/世界）并 `probe_available=true` 返回——探针富指标优先。
- **契约**：实例 RUNNING → cpu/mem/uptime 恒有值（系统直取）；`probe_available` 仅门控 TPS/MSPT/在线/线程/堆/世界这些探针专属指标。
- **非目标**：docker 实例 uptime（需 container inspect，本 FR 不做，保持探针/0）；无探针时的 JVM 堆上限（JVM 内部值，仍探针专属，内存条改按 RSS 展示）。

### 前端（实例概览 `OverviewPanel`）

- **CPU 卡**：读 `metrics.cpuPercent`（后端已系统填充）→ 运行中即真实，无需改数据源。
- **内存卡**：无 `heapMaxMb`（无探针）时不再算出误导的 `0%`——改显示 RSS 内存（MB）为主值、不显假百分比；有探针（heapMaxMb>0）时保持「已用/上限 %」。
- **运行时长**：概览新增运行时长展示（`uptimeSeconds` 人性化格式）。
- **去 mock-api 火花线**：删除写死的 `buildSparkValues(19.8, 35)` 假火花线与 `mock-api` 标签，替换为**真实**时序火花线（`useMetricSeries({scope:'instance', targetId: uuid, metrics:['inst_tps']})` 末段点位）；无数据/无探针时显示「需探针」空态而非假图。
- **TPS/在线**：探针不可用（`tps=-1`）时显示「需探针」而非 `-1`/`0`。

## 验收

- [x] 未连探针的运行中实例，概览显示随负载变化的真实 CPU%、内存(RSS MB)、运行时长（走系统直取，非占位/非 0）
- [x] 概览不再出现 `mock-api` 标签与写死火花线；有探针时火花线接真实时序，无探针时显「需探针」空态
- [x] 有探针时 TPS/MSPT/在线数正常；无探针时这些标「需探针」而非假值/-1
- [x] 非 docker（daemon/direct）实例覆盖；docker uptime 暂不做（记非目标）
- [x] Go build/test 绿、前端 tsc/lint/vitest 绿
- [x] **真机验**：FR-277 主机未连探针实例概览 CPU/内存/运行时长真值随负载变化

## 关联

- 数据源复用 FR-170（Worker gopsutil 进程采样）；时序火花线复用 FR-060/169 的 `/metrics/series`。
- 与 FIX-2（探针未连接更新卡死）同属「探针链路」批次：本 FR 保证「没探针也有基础可观测」。
