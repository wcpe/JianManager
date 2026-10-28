# 功能规格：VictoriaLogs 资产分发与 Worker 运行时（FR-476）

> 状态：已交付@v0.24.0（本轮时长/TTL、资源准入、诊断脱敏与运行期接线修复及已覆盖回归完成；既有 v1.52.0 资产审批、运行时装配及验收历史保留）
> 本次提交/功能修复验收不承诺性能、长稳或全部故障覆盖，Windows 运行时与真实断电仍未认证；已有结果及延期范围统一见 [收口范围与验收表](../worker-log-platform-contract/spec.md#2026-10-25-功能修复收口与延期专项)。　·　关联 PRD：FR-476　·　依赖：FR-473

## 2026-10-22 运行时修复契约

以下保留本批修复约束，配置字段/默认值按实现及 [配置规范](../../CONVENTIONS.md) 对账；本轮已覆盖回归按顶部声明收口，下文旧候选进度只描述原时点，不外推完整性能、全部故障或 Windows 运行时通过。

- **native TTL 只有一个批准版本语义源**：受管 VL 参数及其余量校验复用版本化解析，支持裸月、大写 `M`、`w`、`y` 和小数；不把月份当 Go 分钟，也不预先取整成天。产品 TTL 的永久保留、分钟等策略语义仍单独解析，不能套用 VL 原生限制；两者转换须显式。默认值唯一，原有配置键保持回退兼容。
- **启层才要求可执行接收路径**：只有开启相应层时才校验接收端/driver、明确正的复制校验 SLA 与恢复 SLA 及 native TTL 余量；默认不开启新层。合法 HOT-only 不能因未启用 COLD/Rehydrate 的未知样本而禁止所有后台任务。native TTL、产品搬运年龄和恢复副本责任分别展示，有限 TTL 即使留有缓冲也不是最后副本永久保险。
- **运行期实际接线**：按 namespace 预建固定地址客户端，readiness 控制真实可用性；初始关闭、启动失败和随后恢复均须重新开放既有 ingest/query 能力，不能只把 supervisor 状态改为 RUNNING。健康、分区恢复与查询完整性独立。
- **资源是不同量纲**：各 namespace cache、Go 软限制和 RSS 与 Worker 全局预算分开；磁盘不与 RSS 比较，未知样本不当 0，显式停止的进程可标记 RSS 为已知 0。控制器启动立即有界采样、随后周期更新，预算超额须接到前台/后台/预留的实际准入，同时保留 WAL 与有界排空，不杀实例或删除数据“降级”。
- **诊断与退出事实**：子进程只 Wait 一次，固定该次 Wait 观察到的退出时刻与退出码，不让后续 Status/看护检查改写时间，也不宣称内核精确退出瞬间；stdout/stderr 持续排空到独立、脱敏、有界、不会递归写 VL 的诊断 sink。孤儿匹配只输出最小非秘密元数据，不能保存完整 cmdline、凭据或环境变量。
- **删除安全边界不变**：生产删除三闸默认全关，Sweeper 不擅自接线；仅独立构造数据的隔离实例可启用删除以验证代码生成的半开 `_time < cutoff`。来源/级别规范白名单须分别用 `log_source_id:=`/`level:=` 全值匹配，引号短语不等价；旧短语、混用或自由 filter 下发前拒绝。需核对 cutoff±1ns、合法邻接其它源/级别及任务完成后的保留集合。

待验收：准确时长与精度/非法值、停机与 unknown 样本、实际准入及恢复链、真实退出时刻、诊断脱敏/上界、真实 v1.52.0 删除边界。包校验不替代 Windows 运行时验证，有限资源测试不替代共享契约的持续性能门禁。

### 配置字段对账（2026-10-22，配置/假传输装配已过，真实整链待验）

字段及公共默认入口已落地，配置与假传输装配定向/整包回归已通过；下面不表示真实进程整链、生产或性能门禁已通过。所有 YAML/环境变量项均为启动期解析、重启生效，不因 namespace 运行期启停或健康恢复而成为通用热更新。

| 键 | 默认与兼容语义 |
|---|---|
| `log_vl.retention_period` / `cold_retention_period` | native HOT `120d`、COLD `730d`，唯一默认源为 `internal/platform/vlruntime`；显式旧 HOT-only 保留期不改写 |
| `log_retention.hot_retention` / `cold_retention` | 产品 HOT `90d`、COLD `730d`；不等同 native TTL |
| `log_retention.enabled` / `log_vl.start_cold` / `start_rehydrate` | 均 `false`；零配置不开层 |
| `log_retention.migration.copy_verify_budget` / `recovery_budget` | 默认空；仅启用产品分层时须显式正的有限 SLA，不能把 0 当无限保证 |
| `log_vl.hot_cache_bytes` / `memory_limit_bytes` | 兼容旧键：HOT cache 512MiB；Go 软限制 0 沿默认 512MiB、负值不注入 `GOMEMLIMIT` |
| `log_vl.namespace_budgets.{hot,cold,rehydrate}.{cache_bytes,go_memory_limit_bytes,rss_bytes}` | 原始默认 0；cache/Go 零值沿旧键或各层模板，非零新字段优先；HOT/COLD/Rehydrate cache 模板为 512/128/128MiB，RSS 0 表示未配该层阈值；cache/RSS 负值拒绝 |
| `log_vl.budget.disk_bytes` / `rss_bytes` | 默认 0 表示未配置对应全局上界，不能从 cache 或另一量纲猜测 |
| `log_vl.budget.reservations.{wal,staging,export}` | 各类 0 表示未配置该类上界，仍原子计量，不默认拒绝 Append；正的单类上界、全局磁盘 25% 与容量约束才强制。任一类配置正额度却未配正的全局 disk 时 Load 拒绝，因无法验证 25%；全零不代表已强制全局预算，仍由既有每源 WAL 有限上限兜底 |
| `log_ingest.read_limits.max_line_bytes` / `max_bytes_per_poll` | 原始默认 0，沿采集模板 256KiB/4MiB；负值或归并后每轮额度不大于单行上限时 Load 拒绝；这不是逻辑 Multiline 界，生产 pipeline 另沿唯一默认 1MiB/512 行，见 [归一化规格](../worker-log-normalizer/spec.md) |
| `log_vl.delete_enabled` / `log_retention.discard` / `log_retention.sweep.vl_sweep` | 均默认 `false`；生产 Sweeper 仍未接线，开启某一项不等于执行平台保留删除 |

固定 namespace 客户端与运行期接线已经落地，联合回归仍在进行：即使首次 HOT Start 失败或初始 STOPPED，也保留 RangeClient/lifecycle/publisher 接线；`Tiered.Ready` 每次检查真实进程 State 与 1s 有界 Health，Catalog/Projection readiness 另判。controller 启动立即采样、随后每 60s 更新；Worker 与三受管 PID 的 RSS 聚合，日志根及自定义 VL 数据根磁盘字节分别按 100000 项/100ms 有界扫描。失败/超界为 unknown，显式停机为 known 0；外部 HOT 无 PID 证据时 RSS 仍为 unknown。

既有 Frozen 物理日恢复已接入独立单协程：启动即调度，此后每 15s tick；retention 关闭仍只消费 `PendingPhysicalDays()` 摘要并 Resume 已持久未完成日/旧活动 journal，不新 Start namespace、不新开迁移或策略。每轮最多 4 日，5s 只限制轮内新步骤的让步窗口，不强杀在途物理 IO；失败推进轮转游标防饿死，无 pending 的 HOT-only 不 probe/告警。ctx 取消禁新步骤，done 在真实在途 Resume 退出后才关闭；shutdown 有界等待超时明确保留恢复责任，不伪装为已停止。生产调度正式红转绿与真实 FileJournal/DayManager 的接收端未 ready、预算 unknown、read pin 屏障解除后 CLEANED/新 permit 回归已通过，不等于真实 VL/完整 Worker 进程验收。

诊断采用独立内存 sink，不新增诊断文件：至多 64 行、含 stream 元数据的最终行至多 4096 字节、每秒 16 行；原始长度与速率门先执行，丢弃行不进入脱敏。秘密去重并按长度优先构建不可变 Replacer，只扫描原输入一次、不重扫掩码，bounded writer 在分配超过输出界之前停止并整体隐藏；跨 Write、短/重叠秘密、原始/URL/Basic 编码及有效 UTF8 反例已红转绿。`IndependentLoggerSink` 未给 logger 时使用 `slog.Default`，不得回写 VL 数据面；`ProcessSnapshot` 保存单次 Wait 观察到的固定退出时刻/退出码及丢弃/截断计数。stdout/stderr、Stop、自定义 Health 与 ClientFor 错误脱敏随本库末轮 test/race/vet 复验通过，不代表整个 Worker 的 RSS 门禁通过。

同进程预算观测使用只读 `BudgetController.Snapshot() BudgetSnapshot`：深拷贝 `Policy/Sample/Reserved`，并给出 `TotalReservedBytes/Allowed/Reasons/TakenAt`；不新增 RPC、端口或持久文件。它反映采样时刻与当前实际预留，`WorkWALDrain` 准入允许不代表任意 Reserve 已批准，更不代表零配置总预算已强制。

库增补末轮证据（2026-10-22）：以下三根全包 test/race、同范围 vet 与真实批准引擎 `TestApprovedVL` 均退出码 0，55 个 Go 文件同内容指纹固定；短密钥诊断及前导点单位新反例纳入本轮。原 6 进 4 的 cutoff 证据保留，另以 11 进 9 邻接来源/级别验证全值匹配；仅恢复旧短语、保留正确 `_time` 的变异仍实红。短命子进程延后检查使用固定 Wait 证据，不按死亡后的墙钟补足稳定窗口。

```bash
go test -p 1 ./internal/platform/vlruntime ./internal/worker/logs/vlsup ./internal/worker/logs/retention -count=1 -timeout=240s
go test -race -p 1 ./internal/platform/vlruntime ./internal/worker/logs/vlsup ./internal/worker/logs/retention -count=1 -timeout=240s
```

包含新增 Frozen 恢复接线的 Worker 配置/装配末轮 `go test -mod=readonly -p 1 ./internal/worker ./apps/worker -count=1 -timeout=180s` 与两包 vet 均退出码 0；`apps/worker` 的 `^TestWiringRecovery` 范围 race 也通过，不冒称两包全量 race。证据覆盖假传输初始关闭/失败恢复/同端口三启、全日冻结与 permit、unknown 后台拒绝/WALDrain，以及真实 FileJournal/DayManager 恢复；不是完整 Worker 进程或生产验收，helper 覆盖率不能冒称 apps/worker 全包覆盖率。真实双 VL 特定迁移场景见 [Lifecycle 规格](../worker-log-lifecycle/spec.md)，持续性能及全仓门禁仍未完成。

native TTL 解析由 `vlruntime.ParseRetention` 提供：裸数/大写 `M` 是固定 31 天月，`y` 是 365 天，支持 `w` 与小数，精确累计后遵从批准资产的毫秒精度，范围 1 天至 1200 个 31 天月；最终小写 `m`、NaN/Inf、溢出与非法输入拒绝。带单位分量的小数点前须有数字，`.5y`/`1d.5h` 与批准 CLI 一致拒绝；裸 `.5`/`.5M` 仍为合法月值，按总量累计再截毫秒，不逐分量提前截断。产品 `retention.ParseTTL` 仍允许分钟、`d/w`、小数及永久保留语义，不把两者混同。

启用产品分层时，native HOT 余量至少覆盖 **1 个 UTC 日界 + driver 周期（默认 15m）+ 复制校验 SLA + 恢复 SLA**，并须有受管资产/本地鉴权、显式 COLD 接收端和实际 driver；相等窗口不能通过。默认 HOT 120d 相对产品 90d 的 30 天只是有限缓冲，不保证无限故障或最后副本永久保留。关闭产品分层的合法 HOT-only 保持可配置有限 TTL，并明确告知原生淘汰风险；实际启动诊断/校验分支须以装配测试核实。

## 1. 背景与目标

在 Worker 受管运行 VL，隔离进程资源与失败日志。

本规格不得重新定义 FR-473 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：FR-476 资产审批已批准 VL v1.52.0 双平台包/解包 hash、Apache-2.0 许可证和 182 个 Go module 清单；其他版本须重跑受影响能力验证。
- 范围内：CP 只分发审批 tag、哈希和许可清单；Worker supervisor 按 namespace 管 HOT/COLD/Rehydrate，localhost+本地鉴权，健康与分区恢复分离；失败日志走独立 sink，禁止递归。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

CP 只分发审批 tag、哈希和许可清单；Worker supervisor 按 namespace 管 HOT/COLD/Rehydrate，localhost+本地鉴权，健康与分区恢复分离；失败日志走独立 sink，禁止递归。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-473 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-473 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-483 文档对账。

## 5. 验收标准

- [ ] Linux/Windows 包校验、解包校验、Apache-2.0 记录、优雅停机、预算/RSS、进程重启和无 VL 降级均有实验证据。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-473 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-473 已冻结；本规格仍须完成 CP 资产下载闭环、真机预算降级证据和 Runbook C 故障矩阵，未完成前保持开发中。预算采样与阈值评估（磁盘 80/90、RSS 超限判定）已在 `vlsup` 落地，真机数值取证属 Runbook C。
- v1.52.0 tag、双平台包/解包 hash、Apache-2.0 许可、构建日期和依赖清单见受控文档 [`asset-inventory.md`](asset-inventory.md)（代码真源 `internal/platform/logasset/approved.go`），作为当前资产基线与安装校验依据。

## 3.1 资产、进程与安全契约

- FR-476 资产清单必须登记审批 tag、下载包 SHA-256、解包后可执行文件 SHA-256、Apache-2.0 许可、linux-amd64/windows-amd64 兼容测试和构建日期；任一校验不匹配不得安装。
- Worker supervisor 分别管理 HOT、COLD、Rehydrate 实例；VL 进程 health、分区恢复完成和查询完整可用是不同状态。
  - 受管 VL 启动是异步的：接线 ingest/查询前必须等待实例就绪（`vlsup.Supervisor.WaitHealthy`），否则启动期 VL 写连接被拒会令采集运行时创建失败（FR-476 启动时序，见 CHANGELOG 修复）。
  - **启动结果必须如实上报（落实 §2「所有失败返回结构化状态与可观测原因」）**：`Start` 在子进程立即退出时（缺数据目录、端口被占、二进制不兼容等）必须返回错误并标记 `FAILED`，不得在 `factory.Start` 成功后就置 `RUNNING`；`Status`/`StatusAll` 以进程句柄实况校正，避免「启动窗口内尚存活、稍后才退出」的实例长期误报 `RUNNING`。`Start` 另须在启动前创建 namespace 数据根（VL 不自建）。
  - **受管 VL 生命周期与 Worker 绑定**：Worker 退出时须回收全部受管 namespace（`Supervisor.StopAll`），否则 VL 会成为孤儿继续占用 hot/cold/rehydrate 端口，导致下次启动的新实例因端口被占立即退出、日志中心长期降级。
- VL 仅监听 localhost，使用独立本地鉴权密钥和受管数据根；CP/Worker 对 Search、Stats、Fields、Facets、Tail、Rehydrate、Export 均执行 scope 校验。
- HOT cache 预算不等于 RSS 上限；supervisor 必须执行 Worker 级日志总预算和各子预算，超过预算进入降级。
- 受管 VL 的 `GOMEMLIMIT` 默认 512MiB，旧 `log_vl.memory_limit_bytes` 与新分层预算可覆盖、负值不注入；它是 Go 软目标，不是 RSS 硬限，`-memory.allowedBytes` 也只约束 cache。既有单实例真机观察曾由约 1.1GiB 降至约 131MiB，不能外推为本批多 namespace 聚合 RSS 或持续性能门禁已达标。
- VL 写失败日志必须走独立、限流且不回写 VL 的 sink，防止错误递归。

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-476 VictoriaLogs 资产分发与 Worker 运行时（feat）

**需求补充**：**CP 只内嵌/分发**经校验官方 OSS 资产（linux-amd64 + windows-amd64）；Worker 本地 supervisor 管理 HOT/COLD/Rehydrate（localhost + 本地鉴权）；资产审批=tag/包与解包校验/许可/兼容；安全停机；资源总预算；失败日志防递归；cache≠RSS。**验收含 Runbook C**（wiring/防递归/预算降级/老 Worker Unimplemented）→ `docs/specs/worker-victorialogs-runtime/spec.md`

**交付状态**：✅ 已交付@v0.24.0·v1.52.0 资产审批、supervisor/CP runtime 面、远程三实例、预算采样与 80/90 降级评估、**CP 资产上传→签名下载→Worker 安装真机闭环**、GOMEMLIMIT 内存调优均已通过；**Windows 包校验与解包已补真机证据**（真实包经 GitHub asset API 取得：双层哈希与审批值一致、`InstallApprovedPackage` 真实 zip 解包 PASS、CP `Cache`/`Open` 校验通过并拒绝篡改；见 `worker-victorialogs-runtime/acceptance-windows.md`）——**Windows 运行时行为仍未覆盖**（需 Windows 主机执行 `.exe`）
