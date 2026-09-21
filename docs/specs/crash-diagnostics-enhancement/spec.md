# 崩溃诊断增强（FR-470）

> 状态：📋 计划　·　关联 PRD：FR-470　·　依赖：FR-313、FR-407、FR-170、FR-465　·　关联 ADR：无（沿用既有 gRPC 信道与建模模式）

## 1. 背景与目标

FR-313 已落地「崩溃现场留存」：Worker 在进程非正常退出时组装快照（退出码 / 信号 / 时长 + 终端**尾 200 行**），经 `ReportCrashSnapshot` 上报，CP 存 `instance_crash_snapshots` 表（`model/instance_crash_snapshot.go`），每实例滚动保留 **K=5** 条（`grpc/crash_snapshot.go:17`）。前端有「崩溃诊断」卡片（`service/crash_snapshot.go` + `router/crash_snapshot.go`）。

**但快照只是原始字节，没有「理解」**：

- **无堆栈自动解析 / 根因归类**：运维看到的是 200 行日志，得自己从中找 `OutOfMemoryError` / `Exception in thread` / `Address already in use`。机器能做的归类让人做。
- **无崩溃趋势 / 同类聚合**：每实例只有最近 5 条**离散**记录（`pruneCrashSnapshots` 把更早的删掉了），无法回答「这个实例最近一周崩了几次、同一种原因崩了多少次」。K=5 的滚动保留天然抹掉了趋势。
- **无 OOM / GC 关联**：平台已有进程 RSS 快照（FR-170 `ProcessMetricSnapshot`，`model/metric.go:93`）与受管进程诊断（FR-407），但崩溃时刻**没有**和「崩前 RSS 是不是顶到上限」「崩前 GC 是否异常」做关联——而 OOM 崩溃恰恰是最常见、最需要证据链的一类。

**范围内**
- 崩溃快照的**堆栈自动解析与根因归类**（OOM / 端口占用 / 类找不到 / JVM 参数 / 权限 / 段错误 / 未知）
- **崩溃趋势统计与同类聚合**（时间窗内次数、Top 根因、按实例/组维度）
- **崩溃与资源关联**：崩溃时刻附近的 RSS / CPU / （FR-465 落地后的）GC 指标关联，为 OOM 类提供证据

**不做（范围外）**
- 不做 JVM heap dump / thread dump / 火焰图 / attach 级深度诊断（与 FR-407 边界一致，见其 spec §2.2）
- 不做崩溃自动修复 / 自动重启策略（本 FR 只诊断不改行为）
- 不改 FR-313 的上报通道与 K=5 留存语义（趋势数据走**独立汇总表**，见 §2.3）
- 不引入外部 APM / 日志平台依赖

## 2. 设计

### 2.1 堆栈解析与根因归类（CP 侧，纯函数 pipeline）

新增 `internal/controlplane/service/crash_classify.go`：对 `InstanceCrashSnapshot.TailOutput` 做规则分类，产出结构化 `CrashClassification`：

根因枚举：`oom`（OutOfMemoryError / signal 9 OOM killer）、`port_in_use`（Address already in use / BindException）、`class_not_found`（NoClassDefFoundError）、`jvm_args`（Unrecognized VM option）、`permission`（Permission denied）、`segfault`（SIGSEGV / hs_err_pid）、`corrupt_data`（Failed to load / Corrupted）、`unknown`。

```go
type CrashClassification struct {
    SnapshotID uint
    RootCause  string    // 上述枚举
    Signature  string    // 归一化「同类指纹」（Exception 类名 + 首行），用于同类聚合
    Evidence   []string  // 命中规则的原文行，前端「证据」区展示
    Confidence float64   // 0~1；多规则命中取最高并记多标签
}
```

**决策**：分类放在 **CP 侧**（快照已落库，`TailOutput` 是完整文本），不放 Worker。理由：① 规则需迭代，CP 侧改规则可回溯重跑（对历史快照重分类），Worker 侧改了无法回补；② 避免多种 Worker 版本分类逻辑不一致；③ CP 已持有 FR-313 的全部快照文本。

规则顺序与退出码/信号先验结合（如 `ExitCode != 0 && Signal == "killed"` 优先判 OOM）。分类在快照入库时同步执行并写回分类列（§2.2），同时保留「对历史快照重分类」的批处理入口。

### 2.2 数据模型扩展（决策：扩展 `InstanceCrashSnapshot` + 新增汇总表）

扩展既有表（`model/instance_crash_snapshot.go`），加列（向后兼容，旧行为空）：

```go
RootCause  string `gorm:"type:varchar(32);index"`   // 归类结果
Signature  string `gorm:"type:varchar(255);index"`  // 同类指纹
Evidence   string `gorm:"type:text"`                // 命中原文行（JSON 数组）
Confidence float64
```

新增**崩溃汇总表**（承载趋势，独立于 K=5 留存）：

```go
// model/instance_crash_stat.go
type InstanceCrashStat struct {
    ID          uint      `gorm:"primaryKey"`
    InstanceID  uint      `gorm:"not null;index:idx_crash_stat"`
    // BucketDay 按天聚合（日粒度足够趋势分析，且行数有界）。
    BucketDay   string    `gorm:"type:char(10);index:idx_crash_stat"`
    RootCause   string    `gorm:"type:varchar(32)"`
    Signature   string    `gorm:"type:varchar(255)"`
    Count       int       `gorm:"not null"`
    UpdatedAt   time.Time
}
```

**关键决策**：趋势数据**不复用** `InstanceCrashSnapshot`（K=5 会被裁剪）。每次快照入库时**同时 upsert** 一条 (实例, 天, 根因, 指纹) 计数；这样即使快照被裁剪，趋势仍然完整。日粒度使行数有界（实例数 × 天 × 根因数）。

### 2.3 崩溃与资源关联（OOM 证据链）

崩溃时刻 → 查询该实例在 `[occurredAt − Δ, occurredAt]`（Δ=5min）的：
- `ProcessMetricSnapshot`（FR-170）根进程 RSS / CPU 轨迹 → 判断「崩前 RSS 是否逼近 `MemLimitMB` / 系统水位」
- 指标时序 `inst_heap_used` / `inst_heap_max`（ServerProbe，`model/metric.go` `MetricInstHeapUsed`）→ JVM 堆是否顶满
- FR-465 落地后：`serverprobe_gc_*` 系列 → GC 频次/耗时是否异常（当前未采集，标注为**待 FR-465 依赖**）

产出 `CrashCorrelation`：`{nearOOM bool, rssAtCrash int64, memLimitMB int64, heapUsedMax int64, gcNote string}`，作为「证据」随分类一起返回。关联查询**只读**，不写新表（可按需缓存）。

### 2.4 趋势、聚合与前端

- `GET /instances/:id/crash-trend?days=30` → 按天/根因计数序列（来自 `InstanceCrashStat`）；`GET /crash-overview`（平台/组维度）→ Top 根因、Top 实例、时间窗趋势；「同类聚合」= 按 `Signature` 分组计数。
- 前端实例控制台「崩溃诊断」卡片（既有 `router/crash_snapshot.go` 的 List 之上）扩展：每条快照显示**根因标签** + 置信度 + 证据行（可展开）+ 关联资源证据；卡片顶部加**趋势迷你图** + 「同类聚合」列表；平台观测页（FR-406 `/monitor`）可选加「崩溃总览」区块。

### 2.5 API / MCP

- REST（`router/crash_snapshot.go` 扩展）：`GET /instances/:id/crash-trend`、`GET /crash-overview`；既有 List 返回项增 `rootCause/signature/confidence/correlation`。
- 重分类批处理：`POST /crash-snapshots/reclassify`（平台管理员，对历史快照按当前规则重跑，回填分类列与统计）。MCP（`mcp/tools_instance.go`）：`instance_crash_trend`（读）。

## 3. 任务拆分

- [ ] `service/crash_classify.go`：规则引擎（根因枚举 + 指纹归一 + 证据提取 + 退出码/信号先验）+ 单测（每类正例/负例、多规则命中、unknown 兜底）
- [ ] `model/instance_crash_snapshot.go` 加列 + `model/instance_crash_stat.go` + AutoMigrate + 单测
- [ ] `grpc/crash_snapshot.go`：入库时同步分类 + upsert 统计；单测（事务内分类与统计一致）
- [ ] 资源关联：`service/crash_correlation.go`（查 ProcessMetricSnapshot + 指标时序）+ 单测（窗口、无数据降级）
- [ ] `POST /crash-snapshots/reclassify` 批处理 + 审计 + 单测
- [ ] `service/crash_snapshot.go`：趋势查询 + 同类聚合；`router/crash_snapshot.go` 新端点 + 权限；MCP `instance_crash_trend`
- [ ] 前端卡片：根因标签 / 证据 / 证据链 / 趋势图 / 同类聚合 + i18n 中英 + 双主题
- [ ] 文档同步：ARCHITECTURE（列 + 统计表）、API.md、PRD 状态、CHANGELOG

## 4. 验收标准

- 单测全绿：分类规则（OOM/端口/类找不到/参数/权限/段错误/数据损坏/未知）、指纹归一稳定、证据提取、退出码+信号先验、统计 upsert 幂等、资源关联窗口；前端 vitest：根因标签、证据展开、趋势图、同类聚合、空态
- **真机（要真机过）**：① **OOM 崩溃**（低 `-Xmx` 后灌数据）→ 根因标 `oom`、证据含 `OutOfMemoryError`、关联显示崩前 RSS/堆顶满；② **端口占用**（两实例抢同端口）→ 根因 `port_in_use`、证据含 `Address already in use`；③ **类找不到 / JVM 参数错误** → 对应根因；④ **趋势不受 K=5 影响**：连崩 >5 次后列表只剩 5 条但趋势计数完整（>5）；⑤ **同类聚合**：同原因崩 N 次归为同一 Signature、计数 N；⑥ 历史快照重分类后根因/统计正确回填
- 横切：FR-313 上报通道与 K=5 语义不回归；老 Worker 快照（无新字段）分类降级为 `unknown` 不报错

## 5. 风险 / 待定

- **分类规则覆盖率与误报**：规则是启发式，可能误判。缓解：返回置信度 + 证据原文（人可复核），多规则命中记多标签；规则可持续迭代 + 重分类回补。
- **FR-465 依赖（GC 关联）**：GC 指标尚未采集（`serverprobe_gc_*` 探针已暴露但未入库）。GC 关联部分**待 FR-465 落地后启用**，本 FR 先做 RSS/堆关联。
- **统计表膨胀**：日粒度 + 根因/指纹维度，行数 = 实例 × 天 × 根因数。缓解：日粒度 + 保留窗口（如 90 天，复用设置键模式 `crash.stat_retention_days`）。
- **多语言堆栈**：非 JVM 进程（Go/Rust 二进制，FR-441）崩溃日志形态不同（如 Go panic）。首版规则以 JVM + 通用 OS 错误为主，非 JVM panic 先归 `unknown`。
- **与 FR-407 边界**：FR-407 做「受管进程运行期诊断」，FR-470 做「崩溃时点归类」，二者共用 `ProcessMetricSnapshot` 但用途不同，文档明确不重复。
