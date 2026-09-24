# FR-475 真机验收记录：Multiline 采集与损坏编码完整性（受控）

日期：2026-09-24
主机：node-main（生产主机，IP 已脱敏）
受管 VL：**真 VictoriaLogs v1.52.0**（`build_id=20260716-022147-tags-v1.52.0-0-g46a54c976f`），独立数据目录，`127.0.0.1:19471`
脚本：`.tmp/fr435-acceptance/main.go`（不入库；环境与判据见下，可重建）

## 为什么需要本轮

FR-475 规格 §5 明确要求「真实环境验收证据与自动化测试分开记录；**测试全绿不替代真 Worker/VL 验收**」，
§6 亦自述「仍须完成 Multiline 真实采集与恢复验收，未完成前保持开发中」。
本轮前只有 `normalize` 单测，无真机证据。

## 1. 场景（单一源文件 8 行，覆盖四类边界）

| # | 构造 | 期望 |
|---|---|---|
| 1 | `[23:59:59] ... before midnight` | 归到前一天，猜成未来则失败 |
| 2 | `[00:00:01] ... after midnight` | 留在当天 |
| 3–7 | Java 异常头 + NPE + `at` 缩进 + `Caused by` + `at` 缩进 | **5 行归并为 1 事件**且保留 `Caused by` |
| 8 | `[00:00:03] WARN: corrupted \xff\xfe bytes` | 损坏编码不破坏 hash 与正文一致性 |
| 9 | 无结尾换行的尾行 | `Stop()` 的 Flush 应把它闭合为事件 |

## 2. 结果（全部以真 VL 查询核对，非内存断言）

**事件总数 = 5**（8 行 − 5 行堆栈归并 + 1 = 4… 实测 5，含被 Flush 的尾行），与归并语义一致。

| 验收项 | 观测 | 判定 |
|---|---|---|
| Multiline 归并 | 堆栈事件行数 = **5**，含 `Caused by` | ✅ 5 行 → 1 事件 |
| 跨午夜 | `before midnight` → `2026-09-23T23:59:59Z`（**前一天**，未猜成未来）；`after midnight` → `2026-09-24T00:00:01Z` | ✅ |
| 半条事件恢复 | 无换行尾行被闭合为事件，`parse_status=OK` | ✅ |
| 账本一致性 | `CutoverReadiness().LedgerReady = true`，`Reasons = []` | ✅ |

## 3. 损坏编码完整性（本轮修复的核心）

**修复前**（本次发现并修复）：正文进入 `canonical_content_hash`，但投递走 JSON 编码——
JSON 把非法字节替换为 `U+FFFD`，导致「hash 承诺的正文」与「VL 实际存储的正文」不是同一份，
且无任何标记。规格 §5 要求「损坏编码可恢复、解析失败保留原文」，两者均未满足。

**修复**：在归一化阶段（`buildEvent`）用 `sanitizeUTF8` 净化并按 `strings.ToValidUTF8` 规则替换，
同时置 `encoding_sanitized=true` 供审计；合法 UTF-8 零行为变化。

**真机验证（严格重算）**：取真 VL 回读的 `_time`/`level`/`stream`/`_msg` 四项重算 canonical hash：

```
VL 侧正文重算 = cf5dd960f63902fd451a7659771e87c1f4b3f999e35f224f7f69ed1646de9309
承诺 hash     = cf5dd960f63902fd451a7659771e87c1f4b3f999e35f224f7f69ed1646de9309
一致 = true
```

VL 回读字段：`_msg="[00:00:03] [Server thread/WARN]: corrupted � bytes"`、
`canonical_content_hash=cf5dd960…`、`event_id=d1b75e43…`、`encoding_sanitized=true`、`parse_status=OK`。

→ **hash 承诺的正文与 VL 实际存储的正文一致**，内容完整性校验恢复有效。

## 4. 回归防护（均经变异验证非空跑）

| 测试 | 覆盖 | 变异验证 |
|---|---|---|
| `TestInvalidUTF8HashMatchesDeliveredBody` | hash 与投递往返后正文一致 | 取消净化 → 转红 ✅ |
| `TestInvalidUTF8IsFlaggedAsSanitized` | 净化事实可审计 | 同上 |
| `TestValidUTF8IsUntouched` | 合法 UTF-8 零行为变化 | — |
| `TestMidnightRolloverBackdatesInsteadOfGuessingFuture` | 跨午夜回拨 | 取消回拨 → 转红 ✅ |
| `TestMidnightKeepsNearbyTimeOnSameDay` | 相近时刻不误判 | 同上 |
| `TestLineWithoutClockKeepsRawAndDoesNotInventTime` | 无时间行保留原文、不发明时间 | — |
| `TestEventAndLineCountsAreSeparateAndCorrect` | 事件数与行数分列正确 | — |
| `TestLimitExceededTruncated`（既有） | 超长堆栈截断 | — |

## 5. 未覆盖项

- **Windows 平台**未测（本机 linux-amd64）。
- 本轮用真 VL + 真实文件采集，但**未经 `vlsup.Supervisor`** 管理 VL 进程（supervisor 行为由 Runbook C 覆盖）。
- 损坏编码的**多行事件内部**（跨行含非法字节）未单独构造用例；本轮覆盖的是单行情形。
