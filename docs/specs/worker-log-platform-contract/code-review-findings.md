# 日志平台代码审查发现与处置台账

> 状态：blocking 与 9 项 major **均已修复**（B-1/B-2 已部署生产；major 在 `fix/major-findings-worker-boundary` 分支），每项均带回归测试与变异验证
> 关联：[spec.md](spec.md)（契约）、[signoff-ledger.md](signoff-ledger.md)（签收台账）
> 目的：原审查证据曾只存于 `.tmp/code-review-20260926.md`（被 `.gitignore` 排除，清理即丢失）。
> 本文件为其受控版本，供排期与发布决策引用，不依赖临时目录。

## 审查范围与方式

- 对象：PR #16 已合并到 master 的日志平台实现（CP 联邦 + Worker 采集/查询），216 文件。
- 方式：两路并行只读审查，blocking 项另做独立实证；B-2 另在隔离环境（本进程启动的 VL v1.52.0 + 临时数据根，未触碰生产）做复现实验。

## Blocking（已修复）

### B-1 `worker_view_failed` 不参与覆盖降级 → 假 complete → 导出交付不完整附件

- 位置：`internal/controlplane/logcoord/targets.go`（原因白名单）、`coordinator.go` 三处调用点（Search/Stats/Facets）。
- 机理：未识别原因 → 退回 `ResolveReadinessToCoverage(ReadyOnline)` → `CoverageSuccess`；`BuildCoverage` 只在非 success 时收集原因 → 原因被丢弃 → `complete=true`。
- 影响：导出放行，用户拿到「成功」NDJSON 但静默缺整个 Worker 日志。触发为常规场景（混版 Worker 的 `Unimplemented`、日志面被禁用的 Worker）。
- 同源第二形态：Stats/Facets 在 RPC 失败时用 `callErr.Error()` 覆盖稳定字面量，使白名单识别锚点丢失；Facets 无独立质量标记，失败呈现为完整空面板。
- **状态：已修复**（PR #24，commit `482128ab`）。`worker_view_failed` 纳入 NotReady 白名单并归一为 `ENGINE_NOT_READY`，三处调用点补显式状态降级，Stats/Facets 保留稳定字面量。新增 3 条单测含「导出不得下发成功附件」，变异验证通过（撤销修复后三条全红）。

### B-2 用户过滤可越出授权作用域，读到同 Worker 其它实例日志

- 位置：`internal/controlplane/router/log_federation.go`（原样下发 keyword）；`internal/worker/logs/query/vlrange/client.go`（`base += " AND (" + Filter + ")"`）。
- 机理：LogsQL 中 `AND` 优先级高于 `OR`，`scope AND (f) OR (*)` 被解析为 `(scope AND f) OR (*)`，`OR (*)` 分支完全脱离 scope。
- **边界（隔离环境实测，推翻审查期的一次误判）**：授权 `inst:1` 时载荷返回同 VL 全部实例 10 条事件（含未授权的 `inst:2`），换行变体同样逃逸。**即确为跨实例越权读**，而非早期的「不能越过物理分区」判断——后者源于一次数据依赖的偶发现象（`ClosedVisibleSeq` 丢弃 / `limit` 截断），非安全屏障。物理边界为单 Worker（VL 为 127.0.0.1 本机进程）。
- 加剧因素：Worker 与 CP 均不对返回事件做归属复核；CP 适配器不读 coverage 级字段，并按「用户请求的 target」回填 `TargetID`（proto 的 `LogEvent` 不含 target 字段），结构上无法自查。
- **状态：已修复**（PR #24，commit `92261d64`，已部署生产）。作用域独占 `query`，用户过滤改由 VL 的 `extra_filters` 参数独立解析后与本 query 做 `AND`，用户语法不再与作用域条件交互；管道与畸形语法由 VL 直接拒绝。**行为边界：用户过滤不再支持 LogsQL 管道（`|`）**。新增真实 VL 集成回归（5 类逃逸载荷 + 正/负向对照 + 夹具非空前置断言），变异验证通过。

## Major（逐项已实证）

### M-1 Stats/Facets 原始 gRPC 错误串当 coverage 原因

- 位置：`coordinator.go` Stats/Facets 失败分支。
- **状态：已由 B-1 同批修复**（稳定字面量保留，错误文本改为附加原因）。
- 残留（语义粗化，不造成假 complete）：Fields 路径在 Worker View 建立失败时使用 `fields_failed`，降为 `partial` 而非 Search/Stats/Facets 的 `not_ready`；未见 Fields 专门回归测试。

### M-2 Export 分页累计字节 O(n²)

- 位置：`internal/controlplane/logcoord/coordinator.go`（分页循环内每页从 `items[0]` 重新累计 `ApproxBytes()`）。
- 实证：确认真实退化。有 `p` 页、总行数 `n` 时累计约 O(n²)。触发前提：超过单页上限（200）且未因 coverage/截断/字节预算提前退出。默认导出上限 10,000 行 / 64 MiB，故当前量级为中等开销，但自定义更大 `limit` 会放大（HTTP `limit` 无非负以外的钳制）。
- 建议：把累计量提到循环外，仅对当前页增量累加；`BuildExport` 的最终 O(n) 校验可保留。
- **状态：已修复**（commit `456f3db6`）。改为只对当前页增量累加，判定语义与原实现一致；新增多页回归测试断言「累计超预算后立即停止取页」，变异验证通过。

### M-3 Worker Quality 在 CP 适配层丢失

- 位置：proto 与 Worker 侧均携带 Quality；CP 的 `WorkerSearchResponse`/`WorkerStatsResponse` 无该字段，`worker_adapter.go` 亦不读 `resp.GetQuality()`。CP 侧默认 `DuplicateQuality=exact`，只按 coverage partial/stale 改判。
- 实证：**当前可触发**。Worker 在 projection 冲突时设置 `DuplicateQuality=conflict`/`StatsQuality=partial` 而 coverage 仍可为 success（`CoverageComplete=true + ConflictCount>0`），CP 可能返回 `exact`，进而绕过导出质量门禁、下发成功附件。
- 建议：为 CP 侧响应结构补 Quality 字段、adapter 映射 proto Quality、Coordinator 按最差值聚合，并补端到端传播测试。
- **状态：已修复**（commit `e004ffd0`）。CP 响应结构补 Quality、adapter 映射 proto 质量、Coordinator 按最差值聚合；新增回归测试覆盖映射、跨 Worker 聚合与 conflict 阻断导出，变异验证通过。

### M-4A View ID 可预测

- 位置：CP `types.go`（时间纳秒 + 低位时间）；Worker `planner.go`（`UnixNano` + 自增序号）。
- 实证：可预测成立，但 **当前不是直接越权**——View 复用仍须通过 PrincipalKey、时间/Filter/OnlineOnly/PermissionScope、目标覆盖校验。存在低影响的存在性枚举侧信道（不匹配主体返回 403、View 不存在返回 400）。
- 建议：改用 `crypto/rand` 生成 ≥128 bit 不透明 ID；统一未知/跨主体 View 的错误表现。
- **状态：已修复**（commit `b32415c3`）。CP 与 Worker 均改用 128 位加密随机 ID；新增不透明性与唯一性回归测试，变异验证（改回时间戳派生）转红。

### M-4B `/log-archive/*` 的 target 缺 CP 授权校验

- 位置：`internal/controlplane/router/log_runtime.go` 路由只挂 `node.manage`；Handler 只有 NodeService 与 Worker Pool，无 AuthzService；`target` 直接来自用户输入，Worker 侧原样复制，archive backend 用首个 target 生成分区键。
- 实证：**条件性授权缺口**。默认 `node.manage` 仅属平台管理员（其本有全局权限，不新增越权）；但权限树支持自定义角色，若非平台用户被授予 `node.manage`，可提交任意 target 绕过实例组/目标校验，导致归档对象存在性跨目标泄露、并对非路由节点对应 namespace 触发 rehydrate（资源消耗与数据面污染）。
- 建议：Handler 注入 AuthzService，不信任请求体 target；解析为规范目标并校验与路由 `:id` 所属节点一致；status/rehydrate 分别定义读/写权限；空/跨节点/跨组统一拒绝。
- **状态：已修复**（commit `f98feef6`）。Handler 注入 AuthzService 与 InstanceService，校验 target 归属与实例权限、拒绝跨节点与未知格式，未接线时 fail-closed；新增 8 条分支回归测试，变异验证通过。

### M-5 Worker plan 级 partial 被抹掉并强制完整

- 位置：`internal/worker/logs/query/service.go` 的 `remapCoverageTargets` 无条件重置 `cov.Complete = true`、`cov.PartialReasons = nil`，再仅按目标状态重建。
- 实证：**确认存在**。`MarkIncomplete` 保存的是 plan 级字段，而 budget/MaxFanout/底层 `CoverageReasons`（Search/Stats/Fields/Facets 各链路）不一定改变目标状态；CP 适配器又主要只提取目标覆盖，故截断可能被上报为完整。
- 触发前提：`AuthorizedTargets` 非空、存在覆盖目标、partial 仅存在于 plan 级而目标仍为 `CoverageSuccess`。
- 建议：保存并合并原始 `Complete`/`PartialReasons`，最终完整性 = 原始 complete 且所有目标 success；plan 级原因映射到受影响目标或透传给 CP。
- **状态：已修复**（commit `6ff68971`）。保留 plan 级 Complete/PartialReasons 作为基线再叠加目标级原因；新增两条回归测试（含全健康对照），变异验证通过。

### M-6 pending spool 无容量门禁与上限

- 位置：`internal/worker/logs/ingest/instances.go` 的 pending 追加路径（`O_CREATE|O_APPEND|O_WRONLY` + `Write` + `Sync`，无 `Stat`/上限/quota 检查）；磁盘暂停与配额检查只覆盖已绑定后的 Raw 写入。
- 实证：**确认存在**。未绑定实例输出持续追加且无界；未绑定时无 pipeline/ledger，故 pending 写失败不进入 gap/pause 语义。可由 Worker 重启后绑定未到达、`LogTargetId` 为空、绑定 RPC 失败或 CP 长时间不可达等故障触发。
- 影响：pending spool 可耗尽 Worker 数据盘，进而影响 `ingest.state.json`、事件段、WAL、VL 运行时。
- 建议：增加单流/单实例/全局上限与磁盘阈值检查（在 `pendingMu` 内避免竞态）；超限返回结构化错误并记录 pending 专用 gap/pause；纳入容量采样。
- **状态：已修复**（commit `14585934`）。暂存写入增加磁盘阈值与单流 64MiB/总量 512MiB 上限，用量实时统计不缓存；新增四条路径回归测试，变异验证通过。

### M-7 采集路径不收敛到受管根

- 位置：`RegisterInstance` 仅校验 uuid/targetID 前缀/generation/mode，对 `workDir` 只做 `filepath.Clean`；`Root.Abs` 明确允许外部绝对路径；gRPC 注册直接 `MkdirAll`。
- 实证：**确认存在**。FILE_PRIMARY 路径由该 workDir 拼接，`os.Stat`/`os.Open`/gzip/`HashFile` 均会跟随符号链接。可读受管目录外的 `latest.log` 与归档；注册阶段可在根外建目录；direct/daemon 实例以外部 WorkDir 作为 cwd。Raw 文件创建/追加同样无防 symlink（若目标路径被替换为 symlink 可写到外部）。
- 建议：Worker 侧把 WorkDir 限制为受管 `ServersDir()` 的严格子目录，拒绝绝对外部路径与 `..`，对已有路径做 `EvalSymlinks` 确认仍在根内，文件读取用 `Lstat`/`O_NOFOLLOW` 或目录 fd 相对打开以避免 TOCTOU；理想做法是由 Worker 依实例 UUID/slug 自行生成目录，不信任 CP 下发的任意路径。
- **状态：已修复**（commit `d6635c0b`）。拒绝符号链接与退化目录，同时保留根外绝对路径的既有契约（生产存在合法的外来接管目录）；新增四条回归测试含「根外合法目录放行」对照，变异验证通过。

### M-8 eventstore.Count 与尾段实际完整行数不一致

- 位置：`internal/worker/logs/eventstore/store.go` 的 `Count` 仅累加 `MANIFEST.json` 的 `SegmentMeta.Count`；`readSegment` 遇坏尾行时静默返回已读前缀。`ingest/runtime.go` 的 `appendEvents` 用 `Count` 作权威前缀长度，`stored >= len(events)` 时无错误跳过追加。
- 实证：**确认存在**，可静默丢单事件。可达路径：清单记 10 而尾段实际剩 9 → `Iterate` 返 9 但 `Count` 返 10 → 追加时跳过缺失项只追加后续，或 WAL 已回收时恢复集合更短仍静默成功。现有测试直接构造尾段截断却未断言 `Count`/`appendEvents`/恢复后投影完整性，故测试通过但不能发现该缺陷。
- 影响：事件可从 canonical 投影永久消失且不产生 gap/corrupt/recovery 错误；WAL 按回收水位裁剪后 eventstore 成为唯一副本，故影响不可逆。
- 建议：不以 manifest count 直接计算追加下标，按实际完整行与 EventID/CanonicalHash 校验连续前缀；实际数小于清单时能从 WAL 补齐则修复，否则返回明确恢复失败/数据损坏，禁止静默接受。
- **状态：已修复**（commit `d438b237`）。追加与计数前校准末段（按完整行重算元数据并截掉半行），并就「段多于权威集合」硬失败；新增回归测试含变异验证。

### M-9 Worker 侧 scope 校验缺失

- 位置：`planner.go` 把空 `TargetIDs` 定义为「本 Worker 全部 Catalog 分区」，`matchTarget` 对空授权返回 `true`；gRPC 映射中缺字段或空列表都落成空授权；`LogCreateView`/`LogSearch`/`LogStats`/`LogTail` 均无 Worker 边界拒绝；`planRangesForView` 的授权检查分支为空操作，视图复用不会收窄。
- 实证：**纵深防御缺失/潜在越权原语**，非当前直接可利用漏洞。正常 CP 路径基本恒传非空目标并做覆盖校验；且 Worker 不监听 CP 直拨端口，只经反向隧道注册（需节点 UUID 与 secret）。但 Worker query 层本身存在「空授权=全量读取」原语，其它内部调用方、未来直连入口或被攻陷的 CP 组件均可利用。
- 建议：Worker gRPC 边界统一拒绝空 `authorized_targets`（返回权限错误）；如需全量能力，使用显式受信路径而非把空列表解释为全量；视图复用须重新校验授权集合，删除空操作分支。
- **状态：已修复**（commit `a59fb039`）。Worker 边界拒绝空授权且无 View 的请求，视图复用真正落实授权收窄（UNAUTHORIZED_TARGET 降级）；新增三类边界回归测试。

## Minor（原报告所列）

游标路径不发 `limit` 致 `Exhausted` 失真；`ingest.New` 失败路径泄漏 eventstore 句柄；关闭顺序让采集循环活过最终持久化；`persist()` 全程持 `mu` 做 fsync。

## 未发现问题的维度

- 并发安全（锁顺序/死锁/竞态）：两路审查均未发现。
- goroutine/连接/句柄泄漏：未发现。
- 授权与 scope 收敛的越权绕过（CP 侧）：未发现绕过路径，`authorizedTargets` 对非管理员 fail-closed。
- 实例 UUID 作路径成分：已 sha256，无注入风险。

## 处置顺序（建议）

1. **M-8**（数据一致性，不可逆）→ 2. **M-3 / M-4B / M-5 / M-6 / M-7**（安全与覆盖完整性）→ 3. **M-9**（纵深防御）→ 4. **M-2、M-4A**（性能与加固）。
