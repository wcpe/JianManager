# 功能规格：Worker 日志 Multiline 与归一化（FR-475）

> 状态：已交付@v0.24.0（Linux 范围；本轮生产 pipeline 逻辑事件有限默认/溢出守恒修复与已覆盖回归完成；既有 Multiline/字符集/时区真机证据见 [验收记录](acceptance-real.md)，Windows 运行时本轮不测、未认证）
> 本次提交/功能修复验收不承诺性能、长稳或全部故障覆盖；已有验收及延期专项统一见 [收口范围与验收表](../worker-log-platform-contract/spec.md#2026-10-25-功能修复收口与延期专项)。　·　关联 PRD：FR-475　·　依赖：FR-473/474

## 2026-10-22 生产逻辑事件边界修复契约

- 单物理行 256KiB、每轮 4MiB 的读取界不能限制多行事件总缓存：连续合法 continuation 仍可在超时前累计成超大事件。生产 pipeline 必须在统一默认入口提供明确有限的 Multiline 字节/行数上限，Manager/Worker 装配消费同一入口，不另设冲突默认。
- 生产唯一默认已落地：`pipeline.DefaultMultilineLimits()` 为 MaxBytes 1MiB / MaxLines 512；`pipeline.New` 只补零字段，保留显式非零限制与原 Timeout。独立 `normalize.New` 库零值“未设限制”语义不改；这不是对所有层统一重定义零值。
- 溢出沿既有 `PARTIAL/TRUNCATED` 等显式状态封片承接，后续 continuation 不裸丢、不静默截正文，不另造事件 ID 算法。必须证明原文行序列/字节及源位置区间并集不变，区分事件数增加与源行丢失。
- 单事件编码不满足 eventstore 约束时，存储层仍须自身在落盘前校验并保持失败状态机可读/可重开；不能只依靠上游默认界掩盖下游损坏。

已取得真实文件反例红证：首轮 Poll 曾缓存约 3.93MB/31 行或 1201 行；补生产默认后 acquire+pipeline 包测退出码 0，核对完整原文重拼、源区间并集与规范 EventID 推导不变。既有 Normalizer 的 TRUNCATED 不丢正文，容量切分后的无头 continuation 尾片显式 PARTIAL，并带 `multiline_limit_continuation=true`。该具名守恒回归与最终完整 ingest/全 Go 回归结合，收口本轮 Linux 范围修复；不由“单行不超限”或整包总数推导全部逻辑事件场景已通过，Windows 运行时仍未认证。

## 1. 背景与目标

将行级来源转换为可追溯事件，不混淆事件数与行数。

本规格不得重新定义 FR-473 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：事件必须保留完整 `record_start`/`record_end`，固定 `parser_version`；重新解析走受控 projection/dataset generation。
- 范围内：按 parser_version 执行异常头、堆栈、Caused by、超时和上限；事件保留原文行序列、event_id、解析状态和时区；半条在 WAL 恢复后续接。
- 继承 FR-473：事件保存完整 `record_start`/`record_end`，源分段绑定固定 `parser_version`；重新解析走 projection，不改变默认逻辑事件集合。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

按 parser_version 执行异常头、堆栈、Caused by、超时和上限；事件保留原文行序列、event_id、解析状态和时区；半条在 WAL 恢复后续接。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-473 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-473 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-483 文档对账。

## 5. 验收标准

- [x] 跨午夜/无时间/损坏编码/超长堆栈可恢复；解析失败保留原文；同一输入版本输出稳定；事件数和行数统计分别正确。（真机 + 单测双覆盖，见 `acceptance-real.md`）
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-473 冻结的实际数值，不自行发明未登记阈值。
- [x] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。（真机证据见 `acceptance-real.md`，与单测分列）

## 6. 风险 / 待定

- FR-473 基线已冻结。既有 Multiline 真实采集与恢复历史记录保留于 [验收记录](acceptance-real.md)，本轮生产缓存默认与溢出守恒修复按已有反例及回归证据收口；本批 Windows 运行时未执行，不标通过。
  - Windows 就绪的部分语义已按字节级夹具覆盖（Windows 日志经 log4j2 `%n` 输出 **CRLF**）：**行终止符归一**——`\r` 视为行终止符而非正文，`normalize.FeedAt` 收口剥除单个行尾 `\r`，故 CRLF 与 LF 的同一逻辑内容产出相同 `message` 与 `canonical`，多行归并一致；回归见 `internal/worker/logs/normalize/crlf_test.go` 与 `internal/worker/logs/pipeline/crlf_line_ending_test.go`（含 record 位置**字节精确**与追加后游标对齐）。修复前 `\r` 会进入正文并改变 canonical（见 CHANGELOG [Unreleased]）。
  - **仍未覆盖**：Windows 上的真机采集/恢复行为（文件锁与改名语义、路径分隔符、服务与编码环境），须在 Windows 主机执行，本机 linux-amd64 无法替代。复验入口已入库：`go run ./scripts/acceptance-normalizer/`（Windows 用 `ACCEPT_LINE_ENDING=crlf` 指定 CRLF 夹具）。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-476 资产审批冻结。

## 3.1 事件边界与统计口径

- Multiline 上限、超时、半条事件、跨午夜和损坏编码都必须有明确状态；解析失败保留原文、源位置范围和 parser_version。
- 事件数与文本行数是两个独立指标：事件数用于 Search/Stats/Facets/Export，行数只用于原文展示、接缝和诊断；Dashboard 与切换前后趋势不得混用。
- 同一源分段绑定固定 parser_version；重新解析是新的 projection/dataset generation，不把新旧投影同时放入默认逻辑集合。

## 3.2 崩溃验收

- 异常头、堆栈和 Caused by 跨 WAL/批次边界强杀后，恢复出的事件保留完整首尾源位置和原文。
- 超过 multiline 上限或超时的半条事件进入显式 `TRUNCATED`/`TIMEOUT` 状态，不静默拼接下一事件。

## 3.3 字符集（2026-10-02 缺陷 B 加固）

源正文的字符集按源声明，并在归一化入口**单点收口**（`Normalizer.FeedAt`）：FileTailer / gzip 归档 /
受管 Raw / stdio 四条输入路径共享同一解码判定，保证同一份字节在任何路径上产出同一事件。

- **配置面**：`SourceConfig.Charset`（`auto`/`utf-8`/`gbk`/`gb18030`；`gb2312`/`cp936` 归一为 `gbk`）。
  空串表示跟随节点默认（`ingest.Options.DefaultCharset` ← 配置键 **`log_ingest.charset`**，2026-10-02
  复审 P2-3 接线；环境变量 `JIANMANAGER_LOG_INGEST_CHARSET` 同路径覆盖），默认 `auto`。未知取值在
  登记阶段（`ingest.Register`）与 `pipeline.New` 直接失败，`log_ingest.charset` 非法值在
  `Config.Load` **启动即拒**——按源声明错字符集会静默产出乱码（GBK 中文被当作非法 UTF-8 净化成
  替换字符），必须暴露。回归：`ingest.TestDefaultCharsetAppliesWhenSourceDoesNotConfigure`、
  `config.TestLoad_LogIngestCharset`。
- **判定（auto）**：合法 UTF-8 一律原样返回，绝不进入 GB 系分支；非法 UTF-8 先按 GB18030 解码，
  **解码结果含 U+FFFD 即判为不可信**并放弃解码（实测：随机损坏字节 `0xff 0xfe`、孤立截断字节 `0xc4`
  解码后都出现 U+FFFD，而真实 GBK 中文不会）。判定按源粘滞（同源的纯 ASCII 行沿用同一口径）；
  遇到「合法 UTF-8 且含非 ASCII」的行解除粘滞——真 UTF-8 源里的偶发损坏行不会把整源锁进 GB 系。
- **审计与历史数据**：成功转码的事件写入 `source_charset`（**仅非 UTF-8 时**），无法判定的字节保持
  原样并走既有净化路径（`encoding_sanitized`）。历史数据可按 `source_charset` 整源筛选；
  原地重写会伪造 `canonical_content_hash`/`event_id`，故历史重写必须走新 generation 重放。
- **零行为变化**：合法 UTF-8 源的正文、`canonical_content_hash` 与字段集合与加固前逐字节一致。

### 3.5 节点级默认的运行期自证与「继承 vs 显式」（2026-10-02 真机复验）

现场：`log_ingest.time_zone: local` 配上并重启后，新入库条目**仍是 +8h**；逐层核对（接线、索引
恢复路径、归一化链路）全都正确——因为 `local` 解析的是 **Worker 进程**的本地时区：容器
（alpine 基础镜像无 `TZ`、无 `/etc/localtime`）与部分 systemd 单元（`Environment=TZ=` 空串）里
它恰好等于 UTC，于是「配了 local」＝「什么都没配」，而**没有任何日志能看出这一点**。

- **取值口径**（不变）：空 = UTC、`local`/`node`/`host` = 进程本地时区、其余为 IANA 名；非法值启动即拒。
- **运行期自证**（新增）：`ingest.New` 打一条 `采集归一化节点默认时区已生效`（config / resolved /
  offsetSeconds），并在 `local` 解析为 UTC 时额外打 WARN，提示里含判定依据（`TZ=` 与
  `/etc/localtime` 是否存在）与可执行替代（显式 IANA 名）；每个源在登记时打一条
  `日志源归一化口径已生效`（source / timeZone(+是否继承) / resolved / offset / charset）。
  回归：`ingest.TestDefaultTimeZoneHintFlagsLocalResolvingToUTC`、`...StaysSilentForExplicitConfig`、
  `ingest.TestParseTimeZoneAliases`。
- **Worker 启动自证**：`apps/worker` 启动打印 `Worker 配置已加载 configPath=... logIngestTimeZone=...
  logIngestCharset=...`（`Config.ConfigPath()` = **真的读到并应用**的配置文件绝对路径；空串说明本次
  启动没有任何配置文件生效，例如路径写错或自动查找落空）。回归：`config.TestLoad_ConfigPathIsReported`。
- **继承 vs 显式的语义**（本轮明确，含一处行为收紧）：索引里只记**显式**口径（`Charset`/`TimeZone`
  为空即「跟随节点默认」，不再把继承来的值写回索引），因此
  ① 未显式配置的源**每次重启都跟随当前节点默认**（改 yml 重启即生效，不再被「上一次继承了什么」锁死）；
  ② 显式配置过的源**永不被节点默认覆盖**。
  回归：`ingest.TestNodeDefaultsFollowRestartAndExplicitValuesWin`、
  `TestDefaultTimeZoneAppliesToRestoredSource`、`TestDefaultCharsetAppliesToRestoredSource`。
- **生产建议**：显式写 IANA 名（如 `Asia/Hong_Kong`），跨裸机/容器/systemd 都稳定；`local` 只在
  「Worker 进程与节点 JVM 同机同 TZ」时才等价。

自动回归（转红实测见 CHANGELOG [Unreleased]）：
`normalize.TestGBKLineDecodesToCorrectUTF8`（现场同型文本 `[Lodestone] 已解析 BC 实例`）、
`normalize.TestUTF8SourceIsNeverMisdetected`、`normalize.TestCorruptBytesAreNotDecodedIntoChinese`、
`normalize.TestConfiguredCharsetOverridesDetection`、`normalize.TestStickyDetectionSurvivesAsciiAndRecoversOnUTF8`、
`normalize.TestParseCharsetRejectsUnknownValues`、`ingest.TestGBKSourceIsDecodedBeforeDelivery`、
`ingest.TestGBKSourceAutoDetectedWithoutExplicitConfig`、`ingest.TestUTF8SourceUnaffectedByCharsetDetection`。

## 3.4 时区（2026-10-02 缺陷 C 加固）

`[HH:MM:SS]` 的解释时区按源配置：`SourceConfig.TimeZone`（空串 = UTC，保持既有行为；`local` = 跟随
节点本地时区；其余按 IANA 名解析），可被节点级默认 `ingest.Options.DefaultTimeZone` 兜底，
**不硬编码任何时区**。包内嵌 `time/tzdata`：官方镜像（alpine）与部分裸机不带 zoneinfo 时，
合法 IANA 名同样可解析；非法名在登记阶段被显式拒绝（时区配错会让整源时间轴静默偏移，
不能用回退掩盖）。

- 换算与跨午夜回拨沿用既有 `applyClock`（相对基准 ±12h 回拨），只是现在按**源时区**进行。
- 非 UTC 源的事件写入 `event_time_zone` 字段（仅非 UTC 时）；UTC 源字段集合零变化。

**节点级默认的配置面**（`log_ingest.time_zone`，2026-10-02 补）：源级 `TimeZone` 只有实例登记
（CP 下发）一条路径，节点级默认 `Options.DefaultTimeZone` 此前**没有任何配置面**——生产要让时区
生效必须改代码。现在：

```yaml
log_ingest:
  time_zone: local        # 留空 = UTC（零配置零行为变化）；local = 跟随节点进程时区；
                          # 其余按 IANA 名解析（如 Asia/Hong_Kong）
```

`worker.yml` 读取 → `config.Load` 校验（**非法值启动即拒**，与源级同一取舍：时区配错会让整源
时间轴静默偏移，不能用回退掩盖）→ `Config.IngestDefaultTimeZone()` → `apps/worker/main.go`
装配进 `ingest.Options.DefaultTimeZone`。环境变量 `JIANMANAGER_LOG_INGEST_TIME_ZONE` 同路径覆盖。
实例源未显式配置时区时继承本默认（节点与 JVM 同机部署，`local` 即对齐本地日志时间）。

自动回归（转红实测见 CHANGELOG [Unreleased]）：
`normalize.TestSourceTimeZoneConvertsLocalClockToUTC`（现场同型：HKT `[23:54:02]` 必须落 `15:54:02Z`）、
`normalize.TestSourceTimeZoneCrossMidnightBoundary`、`normalize.TestDefaultTimeZoneStaysUTC`、
`ingest.TestSourceTimeZoneReachesStoredEvent`、`ingest.TestDefaultTimeZoneAppliesWhenSourceDoesNotConfigure`、
`ingest.TestDefaultTimeZoneLocalFollowsProcessTZ`（`local` 走进程 TZ，与 IANA 名不是同一条路径）、
`ingest.TestUnknownTimeZoneIsRejectedAtRegistration`；
配置面：`config.TestLoad_LogIngestTimeZone`（默认 UTC、`local`/IANA 名、非法值启动即拒）、
`config.TestLoad_LogIngestTimeZoneEnvOverride`。

**历史数据影响与修复路径**（加固前落库的事件 `event_time_utc` 带整段源时区偏移，且**没有**
`event_time_zone` 字段，可据此定位）：

1. **查询层修正**（推荐，零数据改动）：查询/告警/看板按已知偏移平移时间窗后再查；
2. **重放重建**：源文件仍在（或 deep archive 有副本）时，用正确时区配置重跑同一份字节到**新的
   generation**，新投影发布后旧代次退出默认查询集合，人工核对条数与时间范围；
3. 无法重放的旧数据显式标注「时区未校正」，只在明确知道偏移的场景下使用。

不采用「原地改写 `event_time_utc`」：`canonical_content_hash` 与 `event_id` 都绑定该字段，
原地改写等于伪造事件身份，且会破坏与 VL 已存内容的 hash 校验。
