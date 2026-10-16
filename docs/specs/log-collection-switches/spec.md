# 功能规格：日志采集开关组与 gz 导入缺陷修复（FR-504）

> 状态：**计划**（尚未实现）
> 关联 PRD：**FR-504**　·　依赖：FR-503（配置下发链路）、FR-502（规则引擎）
> 关联：FR-474（采集底座）、FR-501（日志成本治理——本项为其「gz 文件吸存未做」缺口的收口）
> 动因：2026-10-15 需求（开关可配）+ 同日实测发现的生产缺陷（814 个 gz 零导入）

本规格不得重新定义 FR-473 Shared Contracts；字段、状态与覆盖语义以
`docs/specs/worker-log-platform-contract/spec.md` 为准。

---

## 1. 背景与目标

本项含**两部分**：① 新增两个采集开关（gz 导入 / 实时采集）；② 修复 gz 零导入缺陷。

### 1.1 开关需求

- **gz 导入**：手动触发 + 定时扫描（当前 `log_ingest.archive_scan_interval` 为空 = 关闭）
- **实时采集**：应急开关——可临时停采而不停服（排障/降载），恢复后从断点续采不丢位置

### 1.2 「gz 零导入」缺陷：调查结论（2026-10-15 实测，**原判据不成立**）

**原始记载**（worker.yml 注释；写本 spec 时的依据）：生产 164 个源中 151 个含 814 个 gz 待导入，
而 catalog 中 gzip 段记录为 0 条（从未导入）；疑似根因是 `ImportArchivesNow` 用无界 `cycleMu.Lock()`
在采集饱和期被饿死。

**实测结论（逐条推翻/修正）**：

| 原判据 | 实测（2026-10-15，生产环境） | 证据 |
|---|---|---|
| gz 零导入（catalog 中 gzip 段 0 条） | **不成立**：全节点 **809 个 gzip 段中 808 个已导入** | Worker 采集索引 `ingest.index.db` 的 12 个源 payload → `ledger_core.segments` 统计 |
| 疑似根因 = 无界锁饿死 | **已修复**（2026-10-10），且既有可转红回归在位 | `archive_scan_bounded_test.go`（含变异记录：改回无界锁即转红） |
| —— | **真实缺陷 A**：bungee 规则的归档 glob `proxy.log.[0-9]*` 会匹配 `proxy.log.0.lck`（0 字节锁文件）与 `proxy.log.0`（**当前活跃日志**） | 全节点唯一一条导入错误正是 `proxy.log.0.lck` 的 `ARCHIVE_GZIP_CORRUPT: EOF`；而把当前活跃日志当归档导入等于重复摄取当前内容 |
| —— | **真实缺陷 B**：手动导入入口**运维不可达**——只有 Worker gRPC，CP 与前端都没有入口 | `grep -rn LogImportArchives internal/controlplane/` 无源码匹配（仅内嵌二进制命中）；前端亦无 |

**「零导入」判据为何失真（如实登记）**：实测显示 gz 早在 2026-10-10 前后（当时
`archive_scan_interval: 30s` 曾启用，Worker 日志有据）就被导入完毕；此后 worker.yml 把该项关掉、
目录里又持续产生新 gz，于是「待导入」的数量看起来像是「从未导入」。
**教训**：判「从未导入」必须看**账本**（`ledger_core.segments[].imported`），不能只看 catalog 的段记录。

**目标**（据实测重排）：
1. 三个开关可配且**真被消费**（经 FR-503 的配置链路）：`gz_import_enabled` 使能定时扫描、
   `gz_scan_interval` 定周期、`realtime_tail` 应急停采（不停服、不丢位置）
2. **修复真实缺陷 A**（归档 glob 收窄 + 可转红回归）与**缺陷 B**（补 CP 端点 + 前端入口）
3. 手动导入入口**真机端到端验证**（造归档 → 触发 → 扫描 → 导入 → VL 可查）

**非目标**（范围外）：
- 不改动 gz 解压算法本身（`acquire.NewArchiveImporter` / `ImportGzip`）
- 不做分级验证/吸存策略（属 FR-501 的「gz 文件吸存」范围，本项只保证**能导入**）

---

## 2. 需求（要什么）

### 2.1 开关

| 开关 | 行为 | 默认 |
|---|---|---|
| `gz_import_enabled` | gz 归档导入**总开关**（关 = 不启用定时归档扫描；**不停**采集轮的自动发现，手动入口也不受影响——见下方「语义要点」） | 见语义要点（**默认不扫描**由「周期空 ⇒ 基线周期 0」保证，与开关取值无关） |
| `gz_scan_interval` | 定时扫描周期（**空/0 = 未配置 → 回落 `worker.yml` 基线 `log_ingest.archive_scan_interval`**；要停扫描请关总开关） | 空 |
| `realtime_tail` | 实时采集（关 = 停 tail 不采集新日志，服务照常运行） | `true` |

**语义要点**：
- `gz_import_enabled=false` 时**手动导入入口仍可用**（应急补历史）；停的是**定时归档扫描**，
  而**采集轮内的自动发现不停**（FR-474 的既有承诺：新接管源先导入历史再给当前段分配位置，
  停掉会造成静默的历史缺失）——见 §3.1 的落点修正
- `gz_scan_interval` **空/0 = 未配置**（不是「关闭」）：Worker 折算时回落到 `worker.yml` 基线
  `log_ingest.archive_scan_interval`。「关闭」只由总开关表达（写入侧禁止配 0/负数）
- `realtime_tail=false` 是**应急停采**：不影响实例进程、不影响已存数据、恢复后从断点续采
- 两个开关都经 FR-503 的配置链路，支持平台默认 + Worker 覆盖

### 2.2 缺陷修复（据实测重排）

**缺陷 A：归档 glob 指向不可导入的形态**（真实、已修；修法经三次收敛，过程如实记录）
- 收敛过程：① 先收窄为 `proxy.log.[1-9]*`（排除 0 字节锁文件 `proxy.log.0.lck` 与**当前活跃
  日志** `proxy.log.0`）；② 复查发现更根本的问题——BungeeCord 的轮转产物是**纯文本**（非 gzip），
  而当时归档导入器只支持 gzip ⇒ 任何匹配到的文件都必然失败，且该失败原因**不在自动消解白名单**，
  源一旦被暂停将**永久停采**，故改为空 glob；③ **用户要求补齐该能力后，本项实现了纯文本轮转
  归档导入**，glob 恢复为 `proxy.log.[1-9]*`。
- **纯文本轮转归档导入（本项新增能力）**：账本新增归档类段形态 `rotated_text`，与 `gzip` 同属
  「归档类」并由 `ledger.IsArchiveSegmentKind` 作**单一真源**判定（避免统计与「已导入」判定
  失真）；形态判定**内容优先、名字只承担既有契约**（`.gz` 名一律走 gzip、内容非 gzip 时仍报
  `ARCHIVE_GZIP_CORRUPT`，即既有「坏归档必须可见」的行为逐字不变；其余按文件头魔数判定，
  故无扩展名的 `proxy.log.N` 由内容决定）；导入器与 gzip **共用同一实现**（归属闸、断点续读、
  WAL 余量预算下的有界分批、行口径与 canonical hash 全部逐字共用）。**未引入任何新的失败
  原因码** ⇒ 「不可自愈缺口会让源永久停采」这条红线在结构上不可能被新形态触发。
- 可转红回归：`TestBungeeRuleImportsPlainTextRotations`、`TestRegisterInstanceBungeeRuleImportsPlainTextRotations`
  （端到端：正文进事件段 + `kind=rotated_text` + 零缺口）、
  `TestBungeeTextRotationIsNotRediscoveredAsPending`（已导入不得被反复判为待导入）、
  `TestImportTextProducesSameEventsAsGzip`（事件级口径与 gzip 一致）、
  `TestImportArchiveTextIsBoundedAcrossRounds`（有界分批 + 跨轮续读不重读前缀）；
  变异验证：形态分流回退、新形态不参与归档判定，各自确定性转红（实测）。
- **边界（如实登记）**：`.gz` 名但内容是纯文本仍按坏归档隔离（既有契约，刻意保留）；
  `proxy.log.N.lck`（N≥1、0 字节）会被 glob 匹配但按**空归档**无害消化（0 事件、无缺口、记已导入）；
  单行超长不做额外限制（有界性由「单轮事件数预算 + 跨轮续读」保证，与 gzip 路径同源）。

**缺陷 B：手动入口运维不可达**（真实、已修）
- CP 新增 `POST /api/v1/nodes/:id/log-runtime/ingest/import-archives`，经反向隧道调 Worker
  既有 gRPC `LogImportArchives`；纪律同 clear-identity-conflicts：操作人取自认证上下文、
  理由必填（进审计）、不指名命名空间（全 Worker 范围）仅平台管理员
- **命名空间语义**：值为**实例存储命名空间**（`inst:<id>`），与 Worker 侧 `ImportArchivesNow`
  的过滤口径一致。实测教训：传**完整源键**会得到 `scanned=0`——看着像「执行成功但没东西可导」，
  实际一个源都没匹配上；故 CP 侧按实例命名空间收口并做实例级对象授权
- 前端：日志页覆盖区新增「手动导入归档」入口（`ImportArchivesDialog`；理由必填、明确
  「已触发」而非「已完成」、并把核对口径指向 Worker 日志）
- 回归：`log_import_archives_test.go` 5 例（转发操作人/理由、缺操作人拒、缺理由拒、
  全量范围仅平台管理员、Worker 报错不伪装成功）；变异验证：去掉越权判据即转红（实测）

**锁问题**：经复核**不复现**——`ImportArchivesNow` 已于 2026-10-10 改为有界获取
（`archiveScanCycleMuWait` 预算），既有回归 `archive_scan_bounded_test.go` 含变异记录。
本项不动它，并把「根因是锁」的旧结论标注为**已过时**（见 §1.2 对照表）。

### 3.1 开关接线

**gz 导入开关**：
- 现有 `RunArchiveScan(interval)`（`ingest/runtime.go:3330`）：`interval <= 0` 即关闭 ⇒ 已具备关闭语义
- ~~新增：`gz_import_enabled` 控制**常驻采集循环内的自动发现**（spec 第 60 行所述行为）~~
  ——**该落点已作废**（见 §4「落点修正」）：`gz_import_enabled` 的实际落点是**折算定时扫描周期**
  （关 ⇒ `ArchiveScanInterval()` 返回 0），采集轮的 `discoverSourceArchives` 不受它影响
- `gz_scan_interval` 与 `worker.yml` 基线的关系：**策略周期 > 0 用策略；空/0 = 未配置 ⇒ 回落基线**
- 手动入口 `ImportArchivesNow(storageNamespace, operator)`（`runtime.go:3230`）**不受开关影响**（应急可用）

**实时采集开关**：
- 在采集轮（cycle）入口加判据：关闭时**跳过 Poll/tail**，但保持
  - 账本与 WAL 状态不变（可恢复）
  - 实例日志文件不被触碰（只停读，不移动/不截断）
  - 心跳与实例管理照常
- 恢复时从 `cursor` 断点续采（`FileTailer` 已维护 cursor，无需额外逻辑）

### 3.2 gz 缺陷修复

**调查步骤**（先证后修）：
1. 定位 `ImportArchivesNow`（`runtime.go:3230`）的锁获取路径
2. 对比 `ResolveCoveredGapsForSource` 的 `acquireCycleMuBounded` 实现
3. 用夹具复现：制造采集吞吐饱和，观察 `ImportArchivesNow` 是否永不进入
4. 确认后改为有界获取（超时返回明确错误，而非无限等待）

**可转红回归**：构造「采集轮长期持锁 + 调用 ImportArchivesNow」场景，
断言修复前超时失败、修复后成功。

**注意**：修复需保证**不破坏既有语义**——`ImportArchivesNow` 的
「扫描数/导入数」返回值、gRPC 入口的 operator 校验、以及 spec 第 58 行的
「`.gz` 接管前必须登记前段结束位置」约束，全部逐字不变。

### 3.3 与 FR-501 的边界

FR-501 的 PRD 行写「**gz 文件吸存**未做」，本项与之的关系：

| | FR-501 | FR-504（本项） |
|---|---|---|
| 目标 | gz 的**分级验证 / 吸存策略 / 成本治理** | gz**能被导入**（基础能力）+ 开关 |
| 边界 | 保留/分层/搬运策略 | 路径发现 + 开关 + 锁缺陷 |

⇒ 本项**不重复** FR-501 的策略工作，只保证导入链路可用；FR-501 的策略建立在「能导入」之上。

---

## 4. 任务拆分

- [x] 开关：`gz_import_enabled` 接线（**落点修正**见 §3.1 实现定案：它使能定时扫描，而非放宽采集轮判据——后者会违反 acquire 层不变量）
- [x] 开关：`gz_scan_interval` 接通既有 `archive_scan_interval`（策略优先、未配置回落基线、热启停且幂等）
- [x] 开关：`realtime_tail` 应急停采（保持账本/游标/心跳，恢复续采）
- [x] **调查**：`ImportArchivesNow` 锁饿死根因 → **结论：已过时**（2026-10-10 已修且回归在位，见 §1.2）
- [x] **修复 A**：bungee 归档 glob 收窄（可转红回归 + 变异验证）
- [x] **修复 B**：补手动导入的 CP 端点与前端入口（CP 侧 5 例回归 + 变异验证）
- [x] 真机验证：端到端导入（造归档 → 触发 → `scanned=1/imported=1` → VL 可查）
- [x] 三个开关经 FR-503 链路可配（平台默认 + Worker 覆盖）

---

## 5. 验收标准

> 状态说明（2026-10-15）：`[x]` 项已由自动化测试或真机验收覆盖（分别标注）。

- [x] **gz 导入可手动触发并成功导入**（真机过：造一份含标记的归档 → 经新端点触发 → Worker 报 `scanned=1/imported=1` → **VL 查到该标记**）
- [x] **gz 定时扫描按配置周期执行**（接线完成：策略驱动 + 热启停 + 幂等断言；周期执行本身的长期观测并入 FR-501 的吸存策略一并做）
- [x] **实时采集开关可停可恢复**，且**停采期间不丢位置**（`TestRealtimeTailSwitchPausesAndResumes`；变异验证：删掉停采判据即转红）
- [x] **停采不影响实例进程与心跳**（停采只在采集侧整源返回，不触碰进程/心跳；旁证：真机两次 Worker 二进制重启 12 台游戏服 PID 零漂移）
- [x] **gz 缺陷有根因结论**（成文见 §1.2）——**结论是原判据不成立**，真实缺陷是 glob 过宽与手动入口不可达
- [x] **修复后生产真机可导入**（现状 809 个 gzip 段中 808 已导入；另以端到端新增 1 份验证导入链路仍然工作）
- [x] `go test ./internal/worker/logs/...` 全绿（ingest 全量 210s + 规则/开关用例）
- [x] 真实环境验收证据与自动化测试分开记录（见 §7）

---

## 6. 风险 / 待定

- ~~**锁修复的影响面**~~ → **本项不需要**：锁问题已于 2026-10-10 修复且回归在位，本次只复核不动它（见 §2.2）。
- ~~**814 个 gz 的实际可导入性待验**~~ → **已实测**：809 个 gzip 段中 808 已导入，1 个"待导入"实为误判的 0 字节锁文件（缺陷 A，已修）。
- **导入的性能影响** → 由既有机制覆盖：`pipeline.ImportArchive` 按 WAL 余量预算**逐批**导入、位置落回账本、下一轮从断点续读（`archiveImportDeferred` 可观测）；本项**不需要**另加限速。
- **`realtime_tail=false` 的语义易被误解**：UI 文案须明确「停采期间的新日志不会被采集」；本项的前端入口在**归档导入**侧，停采开关在设置页（`log.acq.realtime_tail`），二者不混用。
- ~~**归档能力缺口**~~ → **已补齐（2026-10-16）**：纯文本轮转归档导入已实现（账本新增归档类形态 `rotated_text`、内容优先的形态判定、与 gzip 共用的有界分批导入器），bungee 的 glob 已恢复为 `proxy.log.[1-9]*`；红线（新失败原因必须可自动消解）通过「不引入新原因码」在结构上守住。

---

## 7. 实现记录（2026-10-15）

| 层 | 文件 | 内容 |
|---|---|---|
| 开关·停采 | `internal/worker/logs/ingest/runtime.go`（`pollSource` 入口） | `realtime_tail=false` 时整源本轮返回：不读、不轮转、不导入，账本/游标/磁盘全不动 |
| 开关·扫描 | `policy.go`（`ArchiveScanInterval` / `SyncArchiveScan` / `StopArchiveScan`）、`runtime.go`（Manager 字段） | 总开关折算 + 周期热同步（幂等，避免每拍重启扫描 goroutine）+ 停止路径 |
| 装配 | `apps/worker/main.go` | 策略加载提前到采集运行时构造处；`effectiveArchiveScanInterval`（策略优先、回落 worker.yml 基线）；心跳 applier 里同步周期 |
| 缺陷 A | `ingest/rules.go`（glob 恢复）+ `ledger` / `acquire` / `pipeline`（纯文本轮转导入） | bungee `ArchiveGlob` 恢复为 `proxy.log.[1-9]*`；新增归档类形态 `rotated_text`、内容优先的形态分流（`.gz` 名保持既有契约）与有界分批导入（与 gzip 共用实现） |
| 缺陷 B·CP | `internal/controlplane/router/log_runtime.go` | `POST .../ingest/import-archives` + `authorizeImportNamespace` + `authorizeImportAllSources` |
| 缺陷 B·前端 | `api/logRuntime.ts`、`components/ImportArchivesDialog.tsx`、`pages/LogsPage.tsx`、`i18n/{zh,en}.json` | `useImportLogArchives` + 日志页入口 + 文案 |

**自动化证据**：ingest 侧 3 例（停采/折算/扫描幂等）+ 规则侧 1 例（glob 双向断言）、CP 侧 5 例；
**3 条变异验证**（停采判据、扫描幂等、glob 收窄、越权判据）确定性转红后逐字还原；
`go test ./internal/worker/logs/...` 与 `./internal/controlplane/router/` 全绿；前端 `tsc -b` + vitest 全绿。

**真机验收（2026-10-15，生产环境）**：

| 时刻 | 动作 | 观测 |
|---|---|---|
| 20:40 | 经新端点传**完整源键**触发导入 | `scanned=0` —— 暴露命名空间语义不匹配（教训已成文，见 §2.2） |
| 20:44 | 改传 `inst:317` 再触发 | 仍 `scanned=0`：查账本确认 81 个 gz **全部已导入**（判据修正的起点） |
| 20:39 | 全节点账本统计 | **809 个 gzip 段 / 808 已导入 / 1 个报错**（`proxy.log.0.lck`，即缺陷 A） |
| 20:50 | 部署含 glob 修正的 Worker；造一份含标记的归档 → 触发导入 | `scanned=1 / imported=1`；**VL 查到该标记**（端到端闭环，用时 1 秒） |

**未完成 / 边界（如实登记）**：定时扫描的**长期**执行观测（本项只验证接线与幂等）；
浏览器人工验收（前端自动化已全绿）；81 个 gz 的历史导入时刻无法回溯（账本只记「已导入」不记时刻）。
