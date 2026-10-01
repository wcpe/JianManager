# 功能规格：Worker 日志 Multiline 与归一化（FR-475）

> 状态：实现中（地基包 `internal/worker/logs/normalize` 已落地；**真机验收通过**——Multiline 5 行归并为 1 事件、跨午夜回拨、损坏编码完整性与半条事件恢复均以真 VL 核对，见 [`acceptance-real.md`](acceptance-real.md)）　·　关联 PRD：FR-475　·　依赖：FR-473/474

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

- FR-473 已冻结。**Multiline 真实采集与恢复验收已完成**（见状态行与 [`acceptance-real.md`](acceptance-real.md)）；**Windows 真机证据仍缺**。
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
  空串表示跟随节点默认（`ingest.Options.DefaultCharset`），默认 `auto`。未知取值在登记阶段
  （`ingest.Register`）与 `pipeline.New` 直接失败——按源声明错字符集会静默产出乱码，必须暴露。
- **判定（auto）**：合法 UTF-8 一律原样返回，绝不进入 GB 系分支；非法 UTF-8 先按 GB18030 解码，
  **解码结果含 U+FFFD 即判为不可信**并放弃解码（实测：随机损坏字节 `0xff 0xfe`、孤立截断字节 `0xc4`
  解码后都出现 U+FFFD，而真实 GBK 中文不会）。判定按源粘滞（同源的纯 ASCII 行沿用同一口径）；
  遇到「合法 UTF-8 且含非 ASCII」的行解除粘滞——真 UTF-8 源里的偶发损坏行不会把整源锁进 GB 系。
- **审计与历史数据**：成功转码的事件写入 `source_charset`（**仅非 UTF-8 时**），无法判定的字节保持
  原样并走既有净化路径（`encoding_sanitized`）。历史数据可按 `source_charset` 整源筛选；
  原地重写会伪造 `canonical_content_hash`/`event_id`，故历史重写必须走新 generation 重放。
- **零行为变化**：合法 UTF-8 源的正文、`canonical_content_hash` 与字段集合与加固前逐字节一致。

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

自动回归（转红实测见 CHANGELOG [Unreleased]）：
`normalize.TestSourceTimeZoneConvertsLocalClockToUTC`（现场同型：HKT `[23:54:02]` 必须落 `15:54:02Z`）、
`normalize.TestSourceTimeZoneCrossMidnightBoundary`、`normalize.TestDefaultTimeZoneStaysUTC`、
`ingest.TestSourceTimeZoneReachesStoredEvent`、`ingest.TestDefaultTimeZoneAppliesWhenSourceDoesNotConfigure`、
`ingest.TestUnknownTimeZoneIsRejectedAtRegistration`。

**历史数据影响与修复路径**（加固前落库的事件 `event_time_utc` 带整段源时区偏移，且**没有**
`event_time_zone` 字段，可据此定位）：

1. **查询层修正**（推荐，零数据改动）：查询/告警/看板按已知偏移平移时间窗后再查；
2. **重放重建**：源文件仍在（或 deep archive 有副本）时，用正确时区配置重跑同一份字节到**新的
   generation**，新投影发布后旧代次退出默认查询集合，人工核对条数与时间范围；
3. 无法重放的旧数据显式标注「时区未校正」，只在明确知道偏移的场景下使用。

不采用「原地改写 `event_time_utc`」：`canonical_content_hash` 与 `event_id` 都绑定该字段，
原地改写等于伪造事件身份，且会破坏与 VL 已存内容的 hash 校验。
