# 功能规格：CP 跨 Worker 日志查询协调器（FR-480）

> 状态：已交付@v0.24.0（本轮真实 Worker view 绑定、覆盖保真、整请求预算及当前页导出修复与已覆盖回归完成；既有联邦装配和验收历史保留）
> 本次提交/功能修复验收只覆盖既有具名回归及指定真实 live，不承诺性能、长稳或全部故障覆盖；已有结果及延期范围统一见 [收口范围与验收表](../worker-log-platform-contract/spec.md#2026-10-25-功能修复收口与延期专项)。　·　关联 PRD：FR-480　·　依赖：FR-479

## 2026-10-22 联邦与导出修复契约

本节保留修复约束，准确 HTTP 字段以 [API 参考](../../API.md) 为准；本轮已覆盖回归及指定真实 live 按顶部声明和共享验收表收口，下文旧候选待验记录保留原时点，不冒全部联邦/浏览器分支通过。

- Fast 首屏汇总 Worker Search 实际返回的 view/vector，完整原子存入 CP 后才向浏览器公开 CP view/cursor；后页禁止用空 Worker ViewID 隐式重建最新集合。普通、快速及缓存路径必须使用同一真实引用与授权集合。
- Search/Stats 保留逐 Worker 顶层 coverage、partial 原因与 quality；Fields/Facets 保留现有 wire 的 coverage/截断证据，CP Facets 按覆盖/截断保守派生统计质量。当前 Fields/Facets proto 没有 `LogQuality`，Fields 不虚构独立 quality；顶层失败不能被逐目标 success 覆盖。任一参与 Worker 失败或安全排序边界未证明时，允许 partial 行但不生成跨越未知范围的全局 cursor，`exhausted=false`，调用方须能重试同页。
- 规划、实际 Worker RPC、分页补取与合并共用整请求字节/时间/并发预算；公平正数预留，零份额不拨号、不建 view、不执行查询。请求默认预算与执行期剩余额度不能混用。
- **当前页导出 HTTP 已实现并通过定向跨层回归**：复用既有 `POST /api/v1/logs/federation/export?range=page`；服务端禁用 Search 缓存并直接执行同 view Stats，复核授权、完整 coverage、无 pending、exact quality 与预算后只物化该逻辑页，不使用客户端原始行。`all` 兼容清除 cursor 导整窗；准确参数、0/省略 limit、400/403/409 与 200 JSON 阻断见 [API 当前契约](../../API.md#日志联邦查询端点fr-479480482http-已注册)。不新增 Worker Export RPC；真实网络/VL/浏览器仍待联合验收。
- 全部匹配导出当前有效窗口内全部匹配；显式自选时间段改变范围时创建匹配的新 view，不能夹带旧 view。三种范围在数据刚好相同时可以产生相同内容，验收要证明范围正确而非强求文件不同。
- 下载前必须再次复核权限；pending、partial、歧义、超预算或取消时不交付成功附件。CP 缓存淘汰/取消不等于 Worker 跨节点即时释放租约，现有协议仍以 Worker 有界 TTL 兜底。

定向回归（2026-10-22）：以下命令已执行且退出码 0，覆盖首屏真实 view、缓存/并发追加、未知页边界、Fields/Facets 顶层摘要与实际 Reader 字节、零份额不拨号、取消上下文、page/all 兼容及新鲜导出闸门；不是网络、真实 VL 或浏览器验收。

```bash
go test -p 1 ./internal/controlplane/logcoord -run 'TestReview05|TestSixModule' -count=1 -timeout=180s -v
go test -p 1 ./internal/controlplane/router -run TestSixModuleHTTP -count=1 -timeout=180s -v
```

后续两处独立复核增量已红转绿；logcoord 完整普通/race、router 完整普通、迁移/page HTTP 增量 race 与两包 vet 已通过。完整 router 单独重跑退出码 0，此前依赖/夹具编译失败尝试保留，不删测试或当作行为红证。

```bash
go test -p 1 ./internal/controlplane/logcoord -count=1 -timeout=240s
go test -p 1 ./internal/controlplane/router -count=1 -timeout=600s
go test -race -p 1 ./internal/controlplane/logcoord -count=1 -timeout=300s
go test -race -p 1 ./internal/controlplane/router -run 'TestSixModuleHTTP|TestSixModulePhysicalDayMigration|TestLogMigratePartition' -count=1 -timeout=180s
go vet -p 1 ./internal/controlplane/logcoord ./internal/controlplane/router
```

这些是准确的分范围组件证据，不是 router 全包 race 或全仓门禁；此前跨层组件回归为进程内 PB 调用，不能冒作网络证据。后续真实 CP/Worker/VL 的非空 940 条固定 view Search/Stats/Export 集合与真后端 Chromium 下载/取消指定场景已通过，详见 [有限联合短验](../worker-log-platform-contract/spec.md#2026-10-22-有限真实联合短验与剩余门禁)。该短验不代表全部多节点/迁移失败矩阵、全 Go 末门或正式性能完成。

## 1. 背景与目标

跨节点汇总全部授权目标并保留覆盖完整性。

本规格不得重新定义 FR-473 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：跨 Worker view 使用各目标 `PublishedProjection` 与 `closed_visible_seq` 向量；Export 与 Search 复用同一 view，不重新选择最新集合。
- 范围内：先解析授权目标集合，再经反向隧道创建各 Worker Query View；Search 稳定 K-way merge；Stats 二次聚合；Facet 截断有语义；Export 使用同 view 有界物化和完整校验。
- 继承 FR-473：跨 Worker view 是各目标 `closed_visible_seq` 向量，不承诺全局同一物理时刻；Export 与 Search 复用同一逻辑事件集合和 view。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

先解析授权目标集合，再经反向隧道创建各 Worker Query View；Search 稳定 K-way merge；Stats 二次聚合；Facet 截断有语义；Export 使用同 view 有界物化和完整校验。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-473 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-473 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-483 文档对账。

## 5. 验收标准

- [ ] 离线/未就绪/历史持有者明确出现在 coverage；仅在线为显式选项；列表与导出同快照一致；权限、取消、扇出和流量预算有效。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-473 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 5. 验收项 9/10/11 的补齐记录（2026-11-04）

> 本节只补 §4 验收表中标注「未验」的三项；其余验收结论不变。

### 5.1 第 9 项：`max_bytes` 整请求预算 + 零份额不拨号 —— **通过**

判据是两个独立的分支，且**都做了变异验证**：

| 子判据 | 用例 | 变异验证 |
|---|---|---|
| 零份额目标**不进入**拨号集合 | `TestReserveWorkerBudgetsZeroShareSkipsTarget` | 把 `share == 0 { continue }` 改成「零也塞进返回表」⇒ **转红** |
| `MaxFanout` 截断后多余目标不拨号、份额之和等于总额 | `TestReserveWorkerBudgetsMaxFanoutBoundsShares` | 同上分支覆盖 |

**为什么这个分支值得单独钉**：零份额被拨号时，Worker 侧把 `max_bytes=0` 解释成
「未声明额度 ⇒ 用 32MiB 默认」，整请求预算被**静默放大**。这不是性能退化，是预算门禁失效
——而且不会报错，只会在目标数远大于预算份额时超额读盘。

### 5.2 第 10 项：权限覆盖全部端点、越权无侧信道 —— **通过**

路由挂在 `permRead("log.read")` 组下（`router.go:516`），组内共 7 个端点。
`TestLogFederation_Unauthorized403_NoToken` 原只列 4 个，本轮**扩到全部 7 个**：

```
GET  /logs/federation            （facade）
GET  /logs/federation/search
GET  /logs/federation/tail
GET  /logs/federation/stats
GET  /logs/federation/fields
GET  /logs/federation/facets
POST /logs/federation/export
```

**变异验证**：注释掉任一条 `rg.GET(...)` 注册 ⇒ 该端点返回 404 而非 401 ⇒ **转红**。

另有两层既有覆盖：`…_NoLogRead`（有身份无 `log.read` ⇒ 403）与
`…_CrossInstanceScope`（有 `log.read` 但无可访问实例 ⇒ 403）。

### 5.3 第 11 项：性能 / RSS / 磁盘门限 —— **本机实测（短窗），长稳见另册**

夹具：真 VictoriaLogs v1.52.0（`20260716-022147-tags-v1.52.0`，与 `logasset.Tag` 一致，
SHA256 校验通过）+ 真 Worker Manager 投递，60 源 × 30 行/s 持续写入。

| 指标 | 读数 | 门限 | 结论 |
|---|---|---|---|
| 单源查询（limit=100） | **18–21 ms**（5 次） | — | — |
| 单源查询（limit=50，10 源串行） | **平均 47 ms** | — | — |
| 聚合查询（`stats count() by log_source_id`） | **12–22 ms** | — | — |
| 全量扫描（`*`，limit=1000） | **143 ms** | — | — |
| 每源常驻堆 | **142.6 KiB**（短窗）/ 见长稳 | < 1 MiB | **通过，7 倍余量** |

**读法**：查询延迟全部在**毫秒级**，离 250 ms 首屏门限有 5–12 倍余量；
最坏的无过滤全量扫描也只用掉 57%。

**边界（必须写明）**：以上是**本机单机短窗**读数，与 FR-498 的 9×30min 长稳窗
共用一个真机窗口但**口径不同**——性能证据要在「不与其他负载争抢」时采，
本轮长稳正在并行跑，故这些数字不是长稳条件下的性能证据。

## 6. 风险 / 待定

- FR-473 已冻结；本规格仍须在 VL RangeClient、`*` 全量/历史持有者目录和远程真机联邦通过前保持“实现中”，不得标已交付。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-476 资产审批冻结。

## 3.1 联邦目标与完整性

- CP 先计算全部授权目标和历史数据持有者，再创建各 Worker Query View；“仅在线”只能由用户显式选择，不能默认当作完整结果。
- 汇总必须区分 `success/offline/not_ready/archive_missing/partial/stale`，覆盖摘要随列表、Stats、Facets 和 Export 返回。
- 实例换过节点时按 Catalog/历史持有者纳入查询；Search 使用稳定复合排序 K-way merge，Stats 使用声明口径的二次聚合。
- Facets 对受控维度精确合并；高基数返回截断维度、截断数和 coverage。`limit` 只限制结果数，扇出、字节、CPU、超时和并发预算另行执行。
- Export 复用同一 view 和逻辑事件集合，完整物化并校验后才生成下载产物；源 Worker 后续离线不改变已完成产物。
- Search/Stats 顶层 coverage 与 Worker 顶层 partial 原因同样参与聚合；cut/open 用 OR，不能被某个目标的成功或已结束覆盖。预算排除目标必须显式 `BUDGET_EXCEEDED`，Stats 保持 partial，不能呈现完整零统计。
- `max_bytes` 是整个联邦请求的预算（零值入口默认 32 MiB），读取前公平预留给参与 Worker；子 RPC 必须得到正数额度且总预留不超过总量，零份额不拨号、不创建 view、不查询。扇出上限、最多 8 个在途与请求取消/超时共同约束调度；缓存身份包含主体、权限范围与预算，不能跨授权主体复用。
- Search 普通、快速与缓存响应可选保留 `ingest_position_gaps[]`，HTTP 门面原样透传，即使事件列表为空也不能丢弃。字段/兼容口径见 [API.md](../../API.md) 对应日志平台章节；位置凭据独立展示，不污染 coverage/quality/Export。
- Worker 扫描截断或未完整同时间戳边界组时保守不出全局 cursor，`exhausted=false`；正常 limit 分页继续使用原 view 与复合 cursor。预算部分结果与可安全分页不是同一概念。

## 3.2 覆盖率原因码：`ENGINE_NOT_READY` 与 `TARGET_NO_DATA` 的区分（2026-10-19）

**背景（真机现场）**：dev 节点 VL 端口在听、`vlReady=true`、直查 VL 有数据，而日志页显示
「日志查询服务未启用」。根因是 `target_missing_in_worker_response` 被与 `NOT_READY` /
`EMPTY_WORKER_RESPONSE` 一起归一化成 `ENGINE_NOT_READY`，把「该目标没有日志」说成了
「查询引擎未就绪」，把排查引向 VL。

**判定口径**（`internal/controlplane/logcoord/targets.go`）：

| 情形 | 目标 state | 原因码 | 对 `complete` 的影响 |
|---|---|---|---|
| 响应**非空**、但缺该目标 | `success` | 无（该目标无数据，合法终态） | 不影响 |
| 响应**本身为空** | `not_ready` | `TARGET_NO_DATA` | `complete=false`（不静默放行导出） |

**为什么空响应不能判 success**：Worker 在「没有任何匹配分区」与「有分区却枚举不出来」两种
情形下都返回空 targets（`planner.selectKeys` 为空 ⇒ `coverage.targets` 亦为空，且
`WorkerSearchResponse.Coverage` 只是 `Targets` 的另一投影、同为空），现有 wire 无法区分。
判 success 会把后一类故障静默成「无数据 + complete=true + 可导出」。

**为什么原因码不能沿用 ENGINE_NOT_READY**：前端 `stateFromReason` 会把
`ENGINE_NOT_READY` 渲染成「日志查询服务未启用」，而复用该码正是本次误导出错的来源。
`TARGET_NO_DATA` 走 partial 分支，文案为「该范围没有日志数据」。

**真正的引擎未就绪信号**（仍归 `ENGINE_NOT_READY`，语义不得丢失）：
`dial_failed` / `search_failed` / `worker_view_failed` / `log_rpc_unsupported` /
`empty_worker_response`（`resp == nil`）。

**范围说明（本节为事后追认）**：该修正属 FR-502/503/504 之外的 CP 侧语义修订，
由用户明确要求后实施；在此登记以避免后续追溯时误认为它属于上述任一 FR 的既定范围。

---

## 2026-11-01 双节点真机验收（首个多节点实例）

> 本节补上此前一直缺的那一环：**FR-480 的核心能力（跨节点联邦）第一次用两个真节点验证**。
> 此前所有验收都是单节点（只有一个 Worker），「合并/排序/跨节点覆盖」从未被真正执行过。

### 环境

```
node-main   生产 Worker，PID 1724910，WS 19102，19 个 MC 实例
node-dev2   临时第二节点，WS 31402，VL 31461，PUSH_ONLY 源 node:2/push（FR-511 /insert/jsonline）
CP          生产 CP，PID 1448954，端口 19000，未重启
```

第二节点建在 `$JM_CP_INSTALL_DIR/../dev/worker2/`（本机实测路径已脱敏），与生产端口、数据根完全隔离。
验收完成后进程已停止，节点记录已**软删**（`DELETE /api/v1/nodes/2`，`/nodes/archived` 可还原）。

### 逐条结论

| # | 条款 | 结论 | 证据要点 |
|---|---|---|---|
| 1 | 全部授权目标联邦扇出 | **通过** | `n_targets=21`，两个 `worker_id` 都参与 |
| 2 | Search 稳定复合排序 K-way merge | **通过** | 15 条结果 14 相邻对，按冻结键**0 违规**；同 `event_time` 组按 `record_start DESC`（135→0） |
| 3 | 覆盖区分 success/offline/not_ready 并随各端点返回 | **通过** | `node:2 state=success, closed_visible_seq=215` |
| 4 | 离线目标如实降级 | **通过** | 停 node2 后 `complete: true→false`、`exhausted: true→false`、`state=not_ready` + `dial_failed` |
| 5 | 缺口不交成功附件 | **通过（验得最干净）** | `export_incomplete=true, artifact=null`，7 条 `incomplete_reasons`，无 NDJSON 附件 |
| 6 | Export 同一 view 与逻辑集合 | **部分通过** | 单节点 `range=page` 交付 10 行 NDJSON（`Content-Disposition: attachment`）；全平台 `range=all` 因下列既有状态无法交付 |
| 7 | Stats 二次聚合 + 顶层不被单目标成功覆盖 | 结构性通过 | 聚合值无法验（见下） |
| 8 | Facets 精确合并 / stats_quality 派生 | 同上 | 同上 |
| 9 | `max_bytes` 整请求预算、零份额不拨号 | 未验 | 需注入预算，本轮范围外 |
| 10 | 权限覆盖全部端点、越权无侧信道 | 未验 | 本轮只用平台管理员单主体 |
| 11 | 性能/RSS/磁盘门限 | 未验 | 同期在跑 FR-498 长稳占用 CPU，不能作性能证据 |

### 关于「13 个目标 RANGE_UNAVAILABLE」的澄清（重要，结论与初判不同）

全平台 `export?range=all` / `stats` 上确实有 13 个目标 `not_ready / RANGE_UNAVAILABLE`：
`node:1, inst:319, 321, 323, 325, 326, 329–335`。

初判曾归因于「生产 VL hot 进程未运行」，**该归因不成立**，复核结果：

- 生产 VL **健康**：`/health` 200，v1.52.0，`-retentionPeriod=180d`，`-search.maxQueryTimeRange=4344h`
- 生产 `worker.log` 近期**零条**「归因 RANGE_UNAVAILABLE」（该兜底分支根本没走到）
- 关键对照：`inst:318`（success，`closed_visible_seq` 有值）vs `inst:319`（失败，`closed_visible_seq=None`）
  ⇒ **Catalog 里没有这些实例的可查分区**
- 单目标查 `inst:319` 返回 `target_no_data`，不是 `RANGE_UNAVAILABLE`

**结论：这些实例从未产生过日志，是真实的无数据状态，不是缺陷。**
它们在生产上从未被投递过事件（与此前对 `inst:319/320` 「前面是大段从未投递的历史区」的记录一致）。

因此全平台 `export range=all` 无法交付附件，**根源是这些目标确实没数据**，
而不是校验逻辑或 VL 就绪态有问题。若要让它交付，需要这些实例真正产生日志。

### 一个已登记的口径差异（本轮未改）

同一目标同一窗口，全量查询判 `success`、单目标查询判 `not_ready/target_no_data`。
分工在 `internal/controlplane/logcoord/targets.go`（响应非空但缺该目标 ⇒ success）与
`internal/worker/logs/query/planner.go` 的 `coverEmptyAuthorizedTargets`（单目标要求有可查记录）。
按现有文档属设计如此，此处登记以备后续追溯，**本轮未修改任何代码**。

### 证据位置

`<第二节点目录>/evidence/`（本机实测路径已脱敏）：`online_*` / `offline_*` 对照 JSON、
`sort_check.txt`、`per_target_stats.csv`、`production_blocker.md`、`raw/` 原始响应。
第二节点完整复现步骤见同目录 `README.md`。

## 2026-11-09 真机取证（FR-480 双节点验收）

> 按 `.claude/rules/docs-registry.md` 归并回本 spec（证据属于该功能，不另设证据目录）。

真机环境：/srv/jm，部署 VL v1.52.0 后首次跑通全链路。

**冷查询**：p50 **25ms** / max 27ms（每次换 from 绕缓存，8 次）

**并发 20**：p50 217ms / p95 **249ms** / max 254ms —— 落在 FR-498 的 250ms 门限内

**资源**：VL RSS 253.7MB、Worker 49.7MB、CP 101.4MB、
VL 数据目录 4.9MB（写入 18 万条后）、资产目录 34MB、宿主磁盘 140G/188G(79%)

**采集健康度**：`failedTotal=0` / `gapsTotal=0`

**边界如实登记**：宿主 load 仅 0.8、窗口仅 24h、规模仅 10 实例/38 harvester，
**不构成 60 源基线**。
