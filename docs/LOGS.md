# 日志系统说明书 — JianManager

> 本文是**日志系统的唯一入口**。要说清“日志怎么产生、存哪、怎么查、出问题怎么看”，先看这里。
> 按 `doc-evolution.md`：本文**原地更新**，反映当前真貌；历史设计取舍在 ADR（只追加不删）。

**先分清三件完全不同的事**（这是最容易混淆的地方）：

| | 管什么 | 实现 | 保留策略 |
|---|---|---|---|
| **① 进程自身日志** | CP/Worker 进程自己的运行日志 | `internal/platform/rollog`（进程内） | 按大小 + 份数 + 天数，异步 gzip |
| **② 实例日志（数据面）** | 8 个游戏服进程的 stdout/stderr/文件 | `internal/worker/logs/logtail` + `tailer` → VictoriaLogs | VL 的 `retention_period` + 磁盘上限 |
| **③ 平台审计日志** | 谁在面板上做了什么 | CP 数据库 `audit_logs` 表 | 数据库保留 |

三者不共享任何代码路径，也不互相同步。**改其中一个不影响另外两个**——历史上多次在这点上误判。

---

## ① 进程自身日志

### 位置与命名

```
<dataRoot>/var/log/control-plane.log      # CP
<dataRoot>/var/log/worker.log             # Worker
<dataRoot>/var/log/<name>-<ts>.log        # 轮转后（rename 得来，零复制）
<dataRoot>/var/log/<name>-<ts>.log.gz     # 后台异步压缩后
```

`<dataRoot>` 由 `JIANMANAGER_DATA_DIR` 或默认 `./data` 决定。生产上通常是 `<install>/data`。

### 轮转机制

由 `internal/platform/rollog` 实现，**进程自己持有文件 fd 并自己 rename**：

- 轮转是一次同目录 `rename`（O(1)、零字节复制）。这是相对 `logrotate copytruncate` 的根本改进——后者每次要读写整个文件，2026-10-27 事故里 35GB 文件轮转一次要几十秒。
- 压缩在独立 goroutine 里异步做，**不阻塞任何日志调用**。
- 崩溃可恢复：rename 之后、压缩之前崩溃只会留下未压缩 `.log`，下次启动补压，**不丢已写入内容**。
- 外部删掉活文件会自动重建（`os.Stat` 按 `statInterval` 采样，默认 1s），并打诊断日志。

### 配置（四项均可省略，零值回退默认）

CP 的 `control-plane.yml` 与 Worker 的 `worker.yml` 用**同一个 schema**：

```yaml
log:
  level: info
  format: text
  max_size_mb: 1024      # 单文件触发轮转的大小（MB）；0 或省略 = 1024
  max_backups: 5         # 保留份数（未压缩与已压缩一起计）；0 或省略 = 5；负值 = 不限
  max_age_days: 30       # 保留天数（按档名里的轮转时间算，一天 = 24h）；0 或省略 = 30；负值 = 关闭按天保留
  compress: true         # 轮转档是否 gzip；**显式写 false 才关**（省略 = 开）
```

| 键 | 默认 | 零值语义 | 负值语义 |
|---|---|---|---|
| `max_size_mb` | 1024 | 回退默认 | — |
| `max_backups` | 5 | 回退默认 | 不限（不推荐，会无界增长） |
| `max_age_days` | 30 | 回退默认 | **关闭**按天保留 |
| `compress` | true | 回退默认（开） | — |

> **零值 = 用默认，不是 = 0 份/0 天**。想表达“不保留任何份”目前做不到（需要负值配合语义调整）。
> `compress` 是 `*bool` 三态：`nil`（没写，默认开）与显式 `false` 必须能区分——写成 `bool` 会静默变质。

`max_backups` 与 `max_age_days` 是**两个独立上限，任一命中即删**（与 lumberjack 同口径）。

### 三条必须知道的实现约束

1. **保留清理不依赖压缩**。曾经的缺陷：清理搭在压缩协程里顺带做，于是 `compress: false` 时 `max_backups` 静默失效。现在压缩与清理进同一条维护队列，清理无条件执行。
2. **天数判据取文件名里的时间戳，不取 mtime**。mtime 会被解压、备份、rsync 改掉，一旦被改晚，早该删的日志就能无限留存。
3. **不删自己认不得的文件**。日志目录里人工拷进来的取证快照、改名的旧日志，glob 匹配到但时间戳解析不出的一律跳过——误删证据不可逆。

### 与外部 logrotate 的关系

**没有关系**。项目不随包分发任何 logrotate 片段（2026-11 已删）。理由：`copytruncate` 的成本随文件大小线性增长；且用两个轮转器管同一个文件必然互相抢先。

### 部署形态的坑

**绝不要给 systemd 单元加 `StandardOutput=append:<path>`**。进程的 slog 同时写 stdout 和 rollog 文件，加了重定向就会产生**第二份日志**——它不受 rollog 管，永不轮转。生产上曾因此产生 221MB `cp.out` + 1.5GB `worker.out`。正确写法：

```ini
StandardOutput=null
StandardError=null
```

### 优雅关闭

CP 与 Worker 都注册 `SIGINT`/`SIGTERM`，收到后先 `http.Server.Shutdown`（CP）或停各服务（Worker），**最后**调 `closePlatformLogger()` ——它等后台 gzip 收尾，避免刚 rename 的轮转档卡在未压缩状态。

---

## ② 实例日志（数据面）

### 数据流

```
游戏进程 stdout/stderr/日志文件
   ↓  tailer（路径发现 + registry 位点）
   ↓  logtail（按行解析、多行归并、时间/级别归一）
   ↓  sink（拼 NDJSON payload）
   ↓  HTTP InsertJSONLines
VictoriaLogs（Worker 本地受管进程）
   ↓  LogsQL
Worker 查询面（Search/Stats/Fields/Facets/Tail）
   ↓  gRPC over 反向隧道
CP logcoord（联邦、合并、授权收敛、导出）
   ↓  HTTP
前端日志中心
```

**VictoriaLogs 是唯一事实来源**。本地不存事件副本——曾经有过 canonical 事件体（tmpfs 段 + 重建链），2026-11-03 整体删除（FR-484 退废）。

### 日志中心界面与导出

日志中心按平台、节点、实例、全部四个页签展示，四类视图都在页头右上角使用同一个名称「导出」。平台和节点导出当前筛选后可见的文本窗口；全部日志导出当前融合窗口；实例保留当前页、全部匹配与指定时间段的原服务端导出入口及权限、覆盖、质量门禁。

事件总量卡完整展示主数值和当前页、级别、流、时间桶指标，卡内不滚动。重复的计数解释、文件尾部与截断说明不再常驻占用正文；统计状态可通过状态提示查看，读取范围说明归入帮助，全部日志的逐来源状态可按需展开。读取失败、未选节点等操作状态仍显示；下载的 NDJSON 窗口元信息继续包含读取限制、捕获时间、截断和非完整历史声明。

### 核心包职责

| 包 | 职责 |
|---|---|
| `internal/worker/logs/tailer` | 路径发现（含 `*.log`、`proxy.log.*` 等 glob）、registry 持久化读取位点 |
| `internal/worker/logs/logtail` | 逐行读取、多行归并、字符集/时间/级别归一、NDJSON sink |
| `internal/worker/logs/logtypes` | 共享类型与契约 |
| `internal/worker/logs/query` | Worker 本地查询面（固定 view 快照、预算、租约） |
| `internal/worker/logs/vlsup` | 受管 VictoriaLogs 进程的资产分发、启动、健康与保留 |
| `internal/worker/logs/catalog` | 分区（namespace + UTC 日）唯一 owner 与可查询性判定 |
| `internal/worker/logutil` | 运行期调级 `/debug/log-level` 与 TTL 解析（2026-11-10 从已删包中抽出的存活部分） |
| `internal/controlplane/logcoord` | CP 侧联邦协调：扇出、合并、覆盖与质量门禁、导出预算 |
| `internal/controlplane/router/log_federation.go` | 联邦 HTTP 端点 |

> **已删除的包**（2026-11-10，净删 18556 行）：`logs/archive`、`logs/retention`、`logs/lifecycle` 及 `apps/worker/` 下 10 个装配文件。
> 删除依据是真机实证——`retention.NewDriver` 的 Mover 参数恒为 nil（冷层搬运从未执行）、
> 指标 `jm_log_retention_driver_enabled` 为 0、`worker.yml` 根本没有 `log_retention`/`log_archive` 段。
> 它们曾经负责的「实例日志保留」现在完全由 VL 自身的 `retention_period` + 磁盘上限承担（见下文）。

### 路径发现与 `read_from_head`（一个反直觉的点）

`DefaultPathsFor` 里的 `proxy.log.*` glob **能发现** BungeeCord 式的纯文本轮转档（`proxy.log.1`、`proxy.log.2`）。

但**发现不等于采集到内容**：`read_from_head` 决定新发现的文件从哪开始读。

| `read_from_head` | 行为 |
|---|---|
| `true`（**当前默认**） | 从头读，能采到轮转档的历史内容 |
| `false` | 直接把位点设到 EOF，历史内容全部跳过 |

真机实测（2026-11）：活文件新行能进 VL，`proxy.log.1` 的 6MB 历史查不到——当时 `read_from_head` 未配置走了旧默认。

### 保留（VL 侧）

两个旋钮，**同时生效，谁先到谁触发**：

```yaml
log_vl:
  retention_period: 180d              # 唯一的保留期：既管可查窗口也管长期留存
  max_disk_space_bytes: 107374182400  # 100 GiB；0/不写 = 不限制
  # max_disk_usage_percent: 85        # 二选一，与上一行同时配会启动报错
```

**`retention_period` 必须不短于 `max(hot_retention, cold_retention)`**，否则 `Load` 在启动受管进程**之前**就拒绝启动（不是告警）。

两条 VL 官方限制决定了「上限 != 沙盘」：
1. VL **至少保留最近 2 天**：最近 2 天超过上限时占用必然超阈值；
2. 占用检查是**周期性**的，两次之间可以涨穿；而磁盘写满时 VL 进只读模式、反而无法执行丢弃（丢弃也要写元数据）。

⇒ 上限必须**远低于**可用空间（建议 ≤ 可用量 40%），并配 `disk_full` 告警。

### 查询与权限

- **授权收敛在 CP**：非平台管理员强制收敛到 `AccessibleInstanceIDs`；显式 `instanceId` 必须落在授权集合内；`nodeId` 对非管理员直接 403（越权侧信道）。**没有回落到 `*` 的分支**。
- **Worker 纵深防御**：`authorizeScope` 拒绝「空授权 + 无 View」；复用路径再按本次授权收窄。
- **LogsQL 作用域隔离**：历史检索把用户过滤放在 `extra_filters`，与授权 selector 分开；原生 tail 的用户表达式在 `query`，授权来源与时间下界独立放在 `extra_filters`，并逐行复核来源。两条路径都不让用户表达式扩大授权 selector；namespace 使用精确 ID 与 `/` 边界，避免 `inst:3` 命中 `inst:31`。

### 日志工作台

- **实例日志**使用 VL 的历史检索与分析。列表、时间桶、字段、常见值、流和导出共用授权目标、时间窗、过滤表达式与 Query View；字段分析只在展开并选择字段后发起。数值 count 仅计有效数值样本，缺失与非数值不进入 avg 分母。
- **默认查询与筛选布局**：首屏查询最近 1 小时，仍可显式选择 15 分钟或更长窗口；当前窗口没有日志不代表历史数据丢失。筛选区按查询与跟随、来源与级别、字段与流分行排列，耗时、返回行数与执行预算统一放在筛选区；未应用草稿、查询中或取消后不把旧执行信息当作新结果。总量卡保留当前页、级别、流与时间桶指标。
- **四类视图与汇总窗口**：平台日志、节点日志、实例日志、全部日志各有独立页签。全部日志按时间合并 CP 文本尾部、最多 4 个在线节点的文本尾部及实例 VL 当前页，逐行保留来源；离线、超限、读取失败和截断状态按来源显示。汇总搜索只作用于已加载行；平台和节点可导出当前筛选后的文本窗口，汇总可导出当前可见行，NDJSON 首行标明窗口范围和非完整历史，不能当作全历史或精确总量。
- **节点与平台日志**读取各自进程的普通文件尾部，窗口为 50/100/200 行，另受 64 KiB 限制。「节点日志」须显式选择具体 Worker；`/logs?view=node&nodeId=<ID>` 可直接定位，旧 `source=worker` 链接仍兼容。窗口内搜索与级别筛选、刷新、3 秒文件快照跟随均不表示全历史查询；不提供 VL 字段分析、全量总数或历史分页。实例 VL 的时间范围和历史导出不适用于文本窗口。
- **高级过滤**通过 `advanced=true&logsQL=…` 显式应用草稿，使用字段、布尔与正则表达式；普通 keyword 同样受到 8 KiB 和 filter-only 词法边界约束。未引用的管道、注释与控制字符拒绝，授权 selector、HTTP 时间与用户过滤独立下发。高级历史查询最多 1000 行 / 32 MiB / 30 秒 / 64 Worker，请求只能收紧；`query_meta` 来自服务端实际执行。
- **实例实时跟随**通过兼容 RPC `LogTailLive` 读取 VL 原生 tail，再经 CP Bearer SSE 到浏览器；浏览器不直连 VL 或 Worker，JWT 不进入 URL。会话最多 45 秒 / 10000 行 / 8 MiB / 8 Worker，且受 token 到期和更小请求预算约束；每 2 秒复核权限。旧能力显式回退轮询，权限、慢消费者和资源超限明确停止。
- **交互与完整性**：显示最多 100 行实时窗口，原生拖选和复制不接管 Ctrl/Cmd+A/C；回看与选区暂停连接并保留正文，显式恢复回到顶部并清除本窗口选区。重连只尝试最近约 10 秒回放，可能缺行，不提供持久游标或无漏承诺。时间图只使用全窗 Stats 桶；只有完整统计可补零，未知空隙保留未知，最多 256 柱。

详细契约见 [API](API.md)、[高级查询边界 ADR](adr/109-instance-logs-advanced-query-boundary.md) 与 [原生尾流 ADR](adr/110-native-instance-log-tail-stream.md)。验收记录分别归入 [检索分析规格](specs/logs-query-workbench/spec.md)、[高级过滤规格](specs/logs-advanced-query/spec.md) 和 [实时流规格](specs/logs-live-streaming/spec.md)。

### 结构化筛选的组合规则（容易出错）

`level` / `source` / `keyword` 三个参数在 CP 侧组合成一个 LogsQL 过滤表达式下发。三条规则：

1. `level`、`source` **走白名单**（用户输入，直接拼会引入注入面）。
2. `keyword` 必须被包成 `(keyword) AND ...`：LogsQL 里 `AND` 优先级高于 `OR`，否则 `a OR b AND level:WARN` 会让级别约束只作用于 `b`。
3. 包括号的**前提是 keyword 自身括号与引号配对**。不配对就返 400 拒绝，不静默转义——后者会改变用户意图且更难排查。

> 实测依据（2026-11-09）：库里 2872 条全是 INFO，`pinged AND level:WARN` 正确得 0 条；而 `(InitialHandler) OR (pinged) AND level:WARN` 得 **2872 条**——级别筛选彻底失效。

### 分面（Facets）的两个体积闸

- `field_values` 端点**不向 VL 传 limit**（传 limit 会让 hits 全归零），所以 `MergeFacets` 的**本地裁剪是唯一体积闸**。
- `dimensionLimit` 是 `uint32`：直接把用户输入转换时**负值会溢出成 4294967295**，使裁剪与 `Truncated` 标记一并失效。CP 侧钳到 `[0, 1000]`，`MergeFacets` 内部还有一层兜底。

### 已知缺口

**重复投递无防护**：`tailer/registry.go` 遇到坏行 `continue` 跳过 → 该文件被视为「新发现」→ `read_from_head=true` 从头读 → 重复进 VL。投递侧 `logtail/sink.go` 直接 `InsertJSONLines`，**无 event_id 去重**；查询侧只在单次 Search 页内去重，Stats/Facets 走 VL 聚合完全不去重。

触发前提是 registry 行损坏（磁盘满、写一半断电）；后果是静默重复且 VL 里查不出。

---

## ③ 平台审计日志

写 CP 数据库 `audit_logs` 表，由审计中间件与 handler 内显式 `recordAudit` 产生。与上面两套日志**完全无关**（不落文件、不进 VL）。

⚠ 已知缺口：部分端点（如 `POST /logs/federation/export`）的查询参数走 URL query 而审计中间件只捕获 body，**「谁导出了哪个实例、什么时间范围」不会落审计**。

---

## 排障速查

### 日志突然不写了

按顺序查：

1. `ls -l /proc/<pid>/fd | grep <日志名>` —— 是否显示 `(deleted)`（外部删除了文件）。
2. 进程 stderr 有没有 `[rollog]` 开头的行 —— 写失败/重建失败都会打。
3. 磁盘是否满（`df -h`）。rollog 在 `w.file == nil` 时会忽略 statInterval 立即重试重建，但磁盘满时重试也会失败。
4. systemd 单元是否被加了 `StandardOutput=append:` —— 那会产生**另一份**不受管的日志。

### 日志目录越来越大

1. 确认 `max_backups` / `max_age_days` / `compress` 的生效值（注意零值回退默认）。
2. 确认没有第二个写入方（systemd 重定向、外部 `nohup >>`、logrotate）。
3. 确认保留清理在跑：清理无条件执行，但与压缩共用维护队列；队列满时本次维护被放弃（下次启动补做）。

### 轮转档没被压缩

`.log.gz.tmp` 残留 = 压缩中途进程被杀。下次启动 `sweepOrphanTmp` 会扫目录清掉孤儿 `.tmp` 并补压。若源 `.log` 已不存在，`.tmp` 也会被删除（内容已在归档里或本就无效）。

### 查不到某个实例的日志

1. 先检查联邦响应的 `coverage` 和目标 `reasons`。`BUDGET_EXCEEDED`、查询失败或覆盖待校验不能当成「没有日志」。文件系统达到 **90% 暂停水位**时会停止新增写入、物化和连续/高成本实时工作；有界历史只读查询仍允许读取已有日志，但仍受查询自身的行数、字节数和超时预算限制。后台物化与历史导出在 **80% 降级水位**即可被拒绝。若只读查询因 RSS 等其他资源保护被拒绝，页面应显示保护原因而不是空成功。水位按 Worker 数据根所在文件系统计算，不能只看 VL 目录大小。安全释放空间后，等资源采样刷新并重新查询；不要放宽阈值或删除 VL、实例日志及采集位点。
2. 对照原始文件是否持续写、新行的本地时刻、当前 UTC 与返回的 `event_time_utc`。MC 风格 `[HH:MM:SS]` 没有时区，`log_ingest.time_zone` 空值按 **UTC** 解释；JVM 输出本地时间时必须显式配置实际来源时区。这会影响落库时间和 Catalog 的 UTC 日期，并非只改变界面显示。东八区来源示例：

   ```yaml
   log_ingest:
     time_zone: Asia/Shanghai
   ```

   配置修改需要重启 Worker 才生效。开发环境以 `systemctl --user show <worker-service> -p ExecStart -p WorkingDirectory` 为准，当前配置为 `<dev-root>/worker/worker.yml`。保留原 registry 和 offset；修正仅作用于后续采集，不会自动重写已有错误时间戳。不要靠清空位点重采全文件来修时间。
3. `range` 是否超出 VL `retention_period`（超了 VL 返回 200 但静默丢）。扩大窗口只能用于对照，不能替代资源准入和来源时区排查。
4. 该实例是否真的产生过日志——`target_no_data`（从未产生）与 `engine_not_ready`（引擎没起来）是**不同**的原因，不要混。
5. 若是 BungeeCord 式轮转档，确认 `read_from_head` 与预期一致。

实机验收应同时核对：默认最近一小时有新行、UTC 时间与来源时区换算一致、对应 UTC 日可查询、重启前后 registry 位点继续向前、实例进程身份保持。仅看到采集计数增长或 24 小时窗口有旧记录，不能证明当前时间窗已恢复。

### 「点了只看 WARN 却拿到 INFO」

`keyword` 里含未配对的括号或引号时会改变过滤表达式结构。前端应收到 400 `INVALID_REQUEST`（2026-11-09 后的行为）。

---

## 验收方法

### 进程日志轮转（真机）

用**真实 rollog 包**编一个小二进制（比启动完整 CP 更容易造压）：

1. 临时把 `max_size_mb` 调到 1MB；
2. 多协程写入直到触发轮转；
3. 断言：产生 `.log.gz`、`zcat` 可解压、行数完整、旧档按 `max_backups` 被删；
4. `kill -9` 后重启，断言归档仍可解压、活文件追加不截断。

**不要用 `--help` 探活**：`jianmanager-worker --help` 会真的启动进程并往默认 `./data` 写日志。一律用隔离的 `JIANMANAGER_DATA_DIR`。

### 按天保留（易被假绿）

造「40 天前」「5 天前」两个档名，**把两个文件的 mtime 都伪造成“现在”**。若实现用 mtime 判断，超期档会活下来 → 用例转红。这样才真正锁住“判据取档名时间戳”。

### 配置绑定（易被假绿）

单测直接构造 struct 证明不了 mapstructure tag 写对了。必须真实喂 YAML 给 viper 解析，断言 `max_size_mb: 256 → 268435456`、`compress: false` 被识别为“显式关闭”而非零值。

---

## 相关文档

| 内容 | 位置 |
|---|---|
| 系统架构（跨进程全景） | [ARCHITECTURE.md](ARCHITECTURE.md) |
| HTTP/gRPC 端点逐参数 | [API.md](API.md) |
| 历史设计取舍 | [ADR](adr/)（**只追加不删**） |
| 编码规范与日志纪律 | [CONVENTIONS.md](CONVENTIONS.md) §日志数据面配置与证据 |
| FR 状态与废弃记录 | [PRD.md](PRD.md) FR-473~505 |
| 各项专项设计与验收 | [specs/](specs/)（按 FR 编号索引） |
