# 功能规格：分区 Catalog 与冷热 Lifecycle（FR-477）

> 状态：已交付@v0.24.0（本轮物理日迁移、固定成员、真实租约和退休屏障修复及已覆盖回归完成；既有 Runbook B 历史见 [验收记录](../worker-log-platform-contract/acceptance-record.md)）
> 本次提交/功能修复验收不承诺性能、长稳或全部故障覆盖，不外推浏览器迁移成功；已有双 VL 三启及延期范围统一见 [收口范围与验收表](../worker-log-platform-contract/spec.md#2026-10-25-功能修复收口与延期专项)。　·　关联 PRD：FR-477　·　依赖：FR-473/476

## 2026-10-22 物理日与 Catalog 修复契约

以下保留本批修复约束，当前已覆盖回归及真双 VL 三启按顶部声明收口；下文旧候选进度只描述原时点，未覆盖结论不得推定为通过；共享契约见 [Shared Contracts](../worker-log-platform-contract/spec.md)。

- **固定物理日批次**：同一 UTC 日的批次身份、成员白名单、来源/目标引用、捕获的完整逐源投影、校验凭据及进度须持久化；重复 Begin 续用原批次，不在不同快照起点混入新成员。journal 保存逐记录的完整后状态，恢复以逐记录为准，批摘要不能覆盖迟到成员或抬高其水位。
- **先冻结接纳、再排空、再复制**：Begin 原子关闭该物理日的新写许可，等待已获许可者真实完成物理写入、校验和发布后才 flush/snapshot。许可涵盖完整操作而不止 HTTP 写调用；冻结期间新 namespace 保留 WAL/canonical 恢复责任，不能默认回写 HOT，也不能先写 COLD 再拒绝发布。
- **同日 COLD 已存在不覆盖**：目标日不存在时可走原 snapshot 复制；目标日已有不同内容而 HOT 留有历史残留时，仅对固定成员使用受控 canonical 补录、独立持久物理代次、分批写入与逐字段核验。不得覆盖既有 COLD、拼接底层存储文件、删除目标后重拷或把目标存在当成功。
- **真实租约与读取钉住**：原子选择权威引用、深拷贝完整 projection 并登记非零有界 TTL 租约；每次真实读取持有 request read pin，只有执行 goroutine 退出才释放。租约增删使用专用 delta/独立 revision，不能用旧记录覆盖发布，也不能因每次查询使物化 CAS 失效。到期/取消/淘汰拒绝新读，但仍须等待已有 pin 排空。
- **按物理日退休**：只有所有引用成员均转移恢复责任，且无 HOT 权威、写许可、旧 view 租约或 read pin 后才 detach/cleanup。缺输入、预算或验证必须保持可重试状态并保留原物；`nil` 不代表完成。生产适配续跑整日未完成批次，不因 Owner 已为 COLD 或正常 pending 永久跳过。
- **旧 view 兼容边界**：纯迁移且旧引用仍可读时，旧 view 只读冻结 owner/目录，新 view 只读新 owner；源集合增删、逐源缩水或替换不能维持原集合时返回 `VIEW_STALE`。现有 RPC 无跨节点立即释放 view 的独立接口，CP 淘汰不能被描述为 Worker 即时释放，仍依赖有界 TTL。

指定真实双 VL 场景（2026-10-22）已通过：批准 v1.52.0 保持同日既有 COLD 原文不变，对两条 HOT 残留受控逐字段补录；缺 canonical 输入时明确失败并保原物。真实 HTTP 读取 goroutine 跨 owner switch 时，旧 view 继续旧 HOT、新 view 读 COLD，release/TTL 不抢实际 pin，真实读结束后才退休。同数据根 HOT/COLD 完整停启三轮，FileJournal 恢复的逐源投影、代次、白名单不变，旧 view 不复活。

复验入口：`JM_VL_BIN="$VL_BINARY" go test -mod=readonly -p 1 ./internal/worker/logs/lifecycle -run '^TestRealVLExistingColdDayResidualPinnedAcrossOwnerSwitch$' -count=1 -v -timeout=180s`。本地批准资产通过环境变量注入；缺环境的 Skip 不算通过。

全日授权已完成 HTTP 定向红转绿：`go test -p 1 ./internal/controlplane/router -run 'TestSixModulePhysicalDayMigration|TestLogMigratePartition' -count=1 -timeout=180s -v` 退出码 0，真实实例组 A 的节点管理主体对 `inst:`/`node:` 探针均 403、RPC 为 0，平台管理员路径与成功审计验证通过。准确顺序与错误见 [API](../../API.md)。这是越权迁移/数据面扰动修复，不是内容泄露或删除。

租约签发与独立 sidecar 已实现：`Catalog.NewQueryViewID(now)` 采用进程 epoch 与一次 issued 票据，pending TTL 为 1min、一次消费，active view 复用不续期；release 同时撤销未消费票据，closed/expired/旧进程 ID 不得复活。独立 lease sidecar 压缩不扰 authority CAS；不确定 append 后须先成功有界 compact 才可重试，compact 失败不能冒作成功快照或绕过准入故障。原混合日/已提交 owner-switch 重试、pending 撤销与该不确定写反例已正式红转绿，40 文件当前内容经独立逐项核对；普通 suite、只读同内容快照 race/vet 及真双 VL 三启通过。原 real-race 命令因跨模块在途编译/vet 退出 1 的记录保留，不冒作命令绿；Catalog/Lifecycle 范围 lint 仍有 9 项与基线相同、0 新增，不是 lint 全绿。

默认安全上界为 active/pending 各 4096、单快照分区 4096、总 key refs 65536、深快照 JSON 估算 128MiB；对应 `MaxQueryLeaseViews/MaxQuerySnapshotPartitions/MaxQueryLeaseReferences/MaxQueryLeaseSnapshotBytes`，Catalog lease 最大 15min，ViewID/Holder 长度至多 256/512；超额 `ErrQueryAdmission` 由查询层映射可重试预算错误。800×60 与 4096 创建/回收压力只覆盖旧 execution 内容、固定约 622 字节 PP；后续不确定写/编码错误检查小改动未重跑同等压力，不能把旧测量贴给当前 hash，更不能外推任意复杂 PP、总 RSS 硬界或性能最优。

待联合验收：跨模块当前候选完整矩阵、真实整链与正式性能长窗；上述真实 VL 是指定场景正常停启，不等于 `SIGKILL`、真正断电或整个 CP/Worker/浏览器链全部通过。

### 当前接口与影响范围（2026-10-22，真实 VL 联合验收中）

- 生产迁移单位是 **Worker 同一物理 UTC 日**，可能跨多个 `storage_namespace`；入口 namespace 只作授权定位/存在性探针，不是物理隔离或搬运范围。固定批次捕获该日 HOT 成员，受控补录只更新当前残留批次白名单，不修改已有 COLD 成员；退休复核全日引用。
- `SnapshotAndLease(QuerySnapshotRequest{ViewID,Holder,Keys,Now,TTL})` 原子返回 `QuerySnapshot{Key,Owner,DirID,Generation,Projection,LeaseID,ExpiresAt}` 的深副本。Catalog 层 TTL 必须在 `(0,15m]`，Query 消费层另限制默认/绝对最长 5m；同 view 重取只允许原 keys，不续期/扩集合。未返回的不可读键须由规划层保留 coverage，不能当无数据。
- `PinLeasedRead/ReleaseQueryView/PruneQueryLeases` 管理实际读者；`LeaseDelta` 使用独立 revision，物理日元数据和租约 delta 均不推进记录 authority CAS。重启后旧进程 view 不能借持久租约复活。
- `AcquireWritePermit/ReleaseWritePermit` 绑定固定 Key/Owner/DirID/Generation，`WriteAdmission` 只做预检；`CheckPhysicalDayDrained` 要求冻结且无许可，`CanRetirePhysicalDay` 另复核全日权威/接收证明/旧读取。
- `PhysicalDayMigration` 持久保存目标和各 `DayMigrationBatch`；批次成员、投影与补录来源/代次固定。重复 Begin 继续原批，迟到 HOT 在前批责任转移后另开受控批次，不混入旧捕获面。完整逐记录后状态优先于 journal 批摘要；指定真实双 VL/FileJournal 三启场景已过，仍不代表全部失败矩阵、最终 race 或生产整体验收通过。

## 1. 背景与目标

让迁移、迟到写入和重启恢复只有一个查询 owner。

本规格不得重新定义 FR-473 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：Catalog 发布必须携带 `PublishedProjection`；旧目录自动 attach 不得越过 Catalog，纯迁移不自动使仍可读的旧 view 失效。
- 范围内：实现 Catalog/journal 状态机：冻结路由、排空、snapshot、staging 校验、attach 排除、原子切 owner、租约排空、detach、清理；启动先恢复 Catalog，再处理 VL 残留目录。
- 继承 FR-473：租约包含 `lease_id/view_id/generation/expiry`；纯迁移不自动使仍可读的固定 view 失效，Catalog 冲突范围才进入 `VIEW_STALE`/`RECOVERY_REQUIRED`。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

实现 Catalog/journal 状态机：冻结路由、排空、snapshot、staging 校验、attach 排除、原子切 owner、租约排空、detach、清理；启动先恢复 Catalog，再处理 VL 残留目录。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-473 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-473 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-483 文档对账。

## 5. 验收标准

- [ ] 每个崩溃点验证 owner/generation、物理目录、写入路由和查询范围；残留旧目录不被查询；retention 不删除最后有效副本。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-473 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-473 已冻结；本规格仍须完成 Catalog 适配层崩溃恢复、re-attach 过滤。**Runbook B 真机验收已完成**（见状态行与验收记录 §2）；re-attach 过滤与迁移崩溃的具体覆盖度见 `signoff-ledger.md` 的 FR-477 行。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-476 资产审批冻结。

## 3.1 迁移不变量

状态转换固定为：`ROUTING_FROZEN → DRAINING → SNAPSHOTTING → STAGING_VERIFY → ATTACHED_STAGING → OWNER_SWITCHED → QUERY_LEASE_DRAINING → DETACHED → CLEANED`。

- 冻结日期写入路由并排空后才 snapshot；staging attach 成功也不得进入 QueryPlanner。
- `OWNER_SWITCHED` 原子提交 Catalog owner/generation、PublishedProjection 和写入路由；每个查询只选择一个不重叠权威副本。
- 旧查询租约结束后才 detach；detach 成功不等于迁移完成，残留目录重启自动 attach 时仍由 Catalog 排除。
- 迟到事件按 Catalog owner/generation 写 COLD、迁移暂存或受控补录，禁止按 `now-7d` 重新生成 HOT 权威分区。
- `move_after_age`、产品 `online_retention`、VL runtime retention 分离；下一层未完成责任接收和校验前，不得删除最后有效副本。

## 3.2 崩溃验收

每个状态中断都记录 owner/generation、物理目录、写入路由、查询范围和恢复动作；VL 自动 re-attach 不能绕过 Catalog。

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-477 分区 Catalog 与冷热 Lifecycle（feat）

**需求补充**：Partition Catalog（owner/generation/journal）；迁移状态机（冻结路由→排空→snapshot→staging→**QueryPlanner 排除**→原子切 owner→租约排空→detach→清理）；迟到日志按 Catalog 路由；retention 协同；`cold.enabled=false` 单层合法；**detach≠完成**。**验收含 Runbook B**（Catalog 适配层各状态崩溃恢复 + re-attach 后旧 owner 过滤）→ `docs/specs/worker-log-lifecycle/spec.md`

**交付状态**：✅ 已交付@v0.24.0·catalog 状态机单测绿·**Runbook B 真机迁移链已通过**（FROZEN→…→CLEANED，数据物理迁移、HOT 空/COLD 持有）
