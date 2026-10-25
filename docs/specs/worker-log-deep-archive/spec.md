# 功能规格：Deep Archive 与 Rehydrate（FR-478）

> 状态：已交付@v0.24.0（本轮读取前资源准入、预留/流式硬界、重复合并及取消释放修复与已覆盖回归完成；既有归档/Rehydrate 及 RustFS S3 验收历史保留，未新增生产 S3 认证）
> 本次提交/功能修复验收不承诺性能、长稳或全部故障覆盖；已有结果及延期范围统一见 [收口范围与验收表](../worker-log-platform-contract/spec.md#2026-10-25-功能修复收口与延期专项)。　·　关联 PRD：FR-478　·　依赖：FR-477

## 2026-10-22 Rehydrate 资源预算修复契约

以下内部接口已实现，当前已覆盖回归按顶部声明收口；下文旧候选进度保留原时点，不据局部证据宣称全部 RPC/生产 S3 新验收通过；不改变旧 `NewQueryBackend` 构造或新增 proto/Worker RPC。

- `QueryBackend.SetResourceBudget(RehydrateResourceBudget)` 接收两个闭包：`Admit(context.Context) error`、`ReserveStaging(context.Context, int64) (func(), error)`；装配复用运行时 controller 的后台准入与 staging 预留。
- 对象 IO 前根据受检 manifest 大小保守预留，读取过程中执行流式硬字节上界；预留持有到 IO、校验、发布真实结束，不能等进入 publisher 后才临时加门，也不重复计算同一预留。
- manager 在昂贵读取前 Start，以同 mergeKey 合并真实在途重复请求；完成后只保留有界任务摘要，不让正文长期挂在任务结果。复用历史成功任务不能代替当前 projection/readiness 验证，失效后的再次重建须有独立证据。
- 取消/TTL 禁止新使用者，但必须等实际 IO goroutine 退出才释放 consumer pin 与预留；不能只因调用方先返回就清理仍在使用的对象或目录。
- 预算拒绝按现有协议保留 `LOG_NOT_READY`、可重试和 `ARCHIVE_NOT_RESTORED` 语义，未恢复不能变成完整零结果；对象集、权限 scope 与归档身份仍须一致。

归档包初步闭环回归已执行且退出码 0：

```bash
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOMAXPROCS=2 go test -p 1 ./internal/worker/logs/archive -count=1 -v
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOMAXPROCS=2 go test -p 1 ./internal/worker/logs/archive -race -count=1
```

定向计量已证明：准入拒绝/unknown 时零对象读取；对象谎报 1 字节时只读 2 字节后拒绝；manifest 超界读至 4MiB+1 后拒绝且 raw 读取为 0；合法完成重试不再读正文，本地真实文件与 typed controller 联合路径保留原物和 WALDrain。覆盖 REHYDRATE_FAILED 语义，不等于完整 S3/生产验收。

S3 Download 非 2xx 错误路径另已闭合红转绿：应用层最多排空 64KiB 后 Close，取消会 Close 解除阻塞；保留原 HTTP 错误、成功 body 不预消费且不输出错误正文，不修改其它操作的全局 drain 语义。401/403/429/503 等构造错误已计量，未承诺 TCP 底层零预取。

追加两包回归均退出码 0：

```bash
go test -p 1 ./internal/worker/logs/archive ./internal/worker/storage -count=1 -v
go test -p 1 ./internal/worker/logs/archive ./internal/worker/storage -race -count=1
go vet -p 1 ./internal/worker/logs/archive ./internal/worker/storage
```

manifest 预扫已与 `encoding/json` 强类型字段折叠对齐：对重复/大小写/Unicode/转义后的同一受控容器拒绝处理，不能借同名容器绕过数量界；原始对象文件名仍区分大小写。生产预扫文件叶子测试/race 及包含该增量的完整 archive+storage 两包末轮 test/race/vet 均退出码 0，8 个本范围源码/测试文件各轮前后 SHA-256 一致；1025 对象在强类型解码前拒绝、大小写不同两对象真实恢复仍通过。这不等于真实 MinIO 或生产验收。

真实 MinIO 环境门控测试未执行，不作为通过项。待联合验收：Worker 整链与实际对象存储、取消/TTL 跨层 pin 及最终审查；不能由包测推定生产可用或全局 RSS 已达标。

### 已落地内部边界（2026-10-22，回归与装配验收中）

- 读取前预算 setter 已落地；本地、内存与真实 S3 Provider 的可选流式 `OpenArchiveObject` 接缝不支持时明确拒绝，不回退全量 `Get`。硬界为 manifest 4MiB、对象 1024、原始合计 64MiB、单行 1MiB、事件 100000；超界保持 not-ready/未恢复，不伪空成功。
- manifest 预留 32MiB，Unmarshal 前先检查 JSON 嵌套不超过 64 层、raw_files/校验对象等集合至多 1024、source/一般容器至多 100000，再复核强类型元数据总量，避免小正文的大量空对象放大。正文预留为 `4×原始总字节 + 事件数×2048 + 2×min(原始字节,1MiB) + 32KiB`；实际流最多读取每对象声明大小加 1 字节后判超界，并核对 ByteCount/记录数。metadata/body 预留持有到真实 IO、校验、Publish/Complete 结束及瞬时正文摘除。
- 仅 QueryBackend 生产任务完成后摘除 `entry.events`、`Result.Events` 和逐事件 `OriginalTimes`，保留任务/租约/状态、分区与临时目录引用；通用 Manager.Complete/RestoreEvents 不改，现有 Worker RPC 从未返回这些正文/逐事件时间 map。
- QueryBackend 另保留至多 128 条 summary；`PublishedRehydrateSummary(taskID)` 返回 `RehydrateSummary{TaskID,EventCount,TimeRange,Verified,Published}`，时间范围为最早事件至末事件加 1ns 的开界，不保存逐事件时间 map。缺摘要不代表历史任务未成功；成功任务复用须由 `CanReuseRehydrate` 检查当前发布凭据，不以历史成功代替 Catalog/Query readiness。

## 1. 背景与目标

以可校验 Raw/VL manifest 支持历史恢复。

本规格不得重新定义 FR-473 Shared Contracts；字段、状态和覆盖语义以 `docs/specs/worker-log-platform-contract/spec.md` 为准。

## 2. 需求（要什么）

- 契约继承：归档/恢复分段释放理由、责任接收者和 hold 规则继承 FR-473；本规格不把 manifest-only 当作可清理证明。
- 范围内：按 storage_namespace+UTC 日+generation 归档；Raw 复制/校验/登记；Rehydrate 任务有租约、合并、取消、超时、磁盘预留和临时 retention；恢复不改变原始 _time。
- 范围外：新增日志告警引擎、第二日志查询引擎、浏览器直连 Worker/VL。

## 3. 设计（怎么做）

按 storage_namespace+UTC 日+generation 归档；Raw 复制/校验/登记；Rehydrate 任务有租约、合并、取消、超时、磁盘预留和临时 retention；恢复不改变原始 _time。

所有跨 Worker 调用经 CP 反向 gRPC 隧道；所有失败返回结构化状态与可观测原因。实现前必须完成依赖 FR-473 的冻结条件，不得用接口占位绕过状态算法。

## 4. 任务拆分

- [ ] 将 FR-473 对应契约映射到本模块的状态、数据模型和 proto。
- [ ] 实现正常路径与崩溃/重启/资源耗尽路径。
- [ ] 编写单元、集成、真实 Worker/VL 或浏览器验收所需测试。
- [ ] 更新 ARCHITECTURE/API/CHANGELOG 及 FR-483 文档对账。

## 5. 验收标准

- [ ] 重复恢复幂等；对象损坏/上传中断可重试；恢复超过默认 retention 可查；临时实例和产物按 generation 隔离清理。
- [ ] 权限覆盖 Search/Stats/Fields/Facets/Tail/Rehydrate/Export；越权无字段或覆盖侧信道。
- [ ] 性能阈值、RSS、磁盘和临时空间使用 FR-473 冻结的实际数值，不自行发明未登记阈值。
- [ ] 真实环境验收证据与自动化测试分开记录；测试全绿不替代真 Worker/VL/浏览器验收。

## 6. 风险 / 待定

- FR-473 基线已冻结；发布 generation 清理矩阵、本批资源预算与发布级验收未完成前保持开发中。既有 RustFS S3 历史验收见 [受控记录](../worker-log-platform-contract/acceptance-record.md)，不以临时过程产物替代当前联合验收。
- 具体 VL tag、资产哈希和兼容矩阵由 FR-476 资产审批冻结。

## 3.1 Archive 与 Rehydrate 契约

- 受管 Raw Archive 是复制、校验、manifest 登记成功的恢复来源，不代表实例目录仍保留 `.gz`；STDIO_PRIMARY 必须由 Worker 生成受管 Raw 分段。
- VL 分区键为 `storage_namespace + UTC 日 + generation`，不是每实例每天一个天然分区；manifest 固定 schema、parser、engine、generation、内容校验和源范围。
- Rehydrate 任务必须有 `task_id`、租约、并发合并键、取消、超时、磁盘预留、临时 VL retention 覆盖原始 `_time` 和 generation 隔离；重复请求复用在途任务。
- 恢复完成前原始事件的 `_time`、event_id、ingest_seq 不变；临时分区清理不得删除仍被 Query View 或恢复任务持有的副本。

## 3.2 发布验收

对象上传中断、manifest 损坏、恢复超过默认 retention、同一归档并发恢复和任务超时均返回结构化状态；成功恢复必须可定位、可查询、可重复核验。
