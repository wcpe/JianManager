# ADR-100: 归档代次归属必须有执行体（禁止"假设当前代次"）

- **日期**: 2026-10-02
- **状态**: accepted
- **关联**: FR-474（采集与持久化通道：源分段身份与 `latest.log→轮转→.gz` 关联）· FR-475（归一化：文件名推导的 `event_time`）· FR-473（事件身份契约 §3.1 `source_generation`）· [ADR-094](094-worker-log-local-data-plane.md) · [ADR-095](095-canonical-event-store-on-disk.md) · [ADR-099](099-log-cost-governance-and-invariant-r.md)（不变量 R 与缺口判据）· `docs/specs/worker-log-acquisition/spec.md` · `docs/specs/worker-log-normalizer/spec.md`

## 上下文

- **代次是事件身份的一部分**：契约 §3.1 规定 `event_id = hash(log_source_id, source_generation, record_start, record_end, parser_version)`。同一份字节若以不同代次入账，就是**两条不同的身份**，而 VL 侧**没有唯一键**可兜（canonical content hash 明确"不作为 VL 唯一键"）。
- **归档导入是"迟到的边"**：`.gz` 可能在源已经进入新代次之后才被导入（外部留存、手动回填、迁移后重扫）。此时若把归档"归到当前代次"：
  1. 同一份字节以新代次的 `event_id` 再进一次 VL ⇒ **静默重复**（不报错、不丢数据，只在跨代次重建/统计时暴露）；
  2. 新代次的 `VerifiedRuns`（代次内源位置区间的逐字段校验凭据）**认不了旧账** ⇒ 校验链断裂，缺口永远消解不掉。
- **此前没有执行体**：代次归属是"隐含约定"，导入边没有查表义务、也没有可查的注册表——于是"归到当前代次"成了默认实现，而它正是上一条静默重复的唯一真入口。

## 决策

1. **规则**：`event_id` 含代次，**跨副本、跨边必须完全一致**；**代次只由登记路径铸造**（源首次登记 / 内容重置），**导入边只许查表、不得自创**。任何"归到当前代次"的实现都是错的。
2. **执行体 = 既有持久化（零迁移）**：注册表就是**账本分段**——`(LogSourceID, 规范化路径, archive_object_id) → 代次`，其中 `archive_object_id` 已含**大小 + 内容哈希**；查询实现遍历同一 `LogSourceID` 的**各代次条目**（`Manager.archiveOwnerLookup`），不新建表、不做迁移。
   - 为什么不用新注册表：归档的逐件账本来就落在账本分段里（`Segment{Kind: SegmentGzip, Path, ArchiveObjectID}`）并随索引持久化；新表 + 迁移的风险（迁移期不可用、双写不一致）远大于这一次线性扫描的收益——**调用粒度是"每个待导入归档一次"，不是每批事件一次**。
3. **三条判据（按优先级，`ArchiveImporter` 归属闸）**：
   - ① 命中且归属是**别的代次** → **拒绝**：`RecordGap(..., "ARCHIVE_FOREIGN_GENERATION", reason)` + `MarkFailed` + WARN 告警（拒绝必须是**可见**的，不许静默跳过）；
   - ② 命中**本代次** → 放行（重试/幂等路径）；
   - ③ **未命中**（首次见到，含"首次登记时导入目录里已有的历史归档"）→ 放行，并在本次成功导入后由 `MarkImported` 把 `(规范化路径, archive_object_id) → 本代次` 的绑定写进账本分段——这就是"**实时边查表 / 无则登记**"的登记动作。此后任何其它代次再看这个对象都会命中 ① 而被拒。
   - **未命中为什么不能直接拒绝**（与"查不到就拒绝"的字面口径不同，此处是刻意的收窄）：`TestManagerAutoImportsHistoricalGzipBeforeCurrentFile` 保护的"历史归档先于本源存在、没有轮转关联"是一条**已交付且有回归保护**的能力，未命中即拒会把它直接删除；而且这些归档永远进不来——人工也无法指定一个**不存在**的"原代次"。因此闸门把"拒绝"精确落在**跨代次**这一种形态上：它才是重复的来源；首次归属不是自创代次（代次仍由登记路径铸造），而是**首次登记**。
4. **闸的位置在 gzip 头校验之后**：归档损坏/打不开是更基础的事实，其原因码（`ARCHIVE_OPEN_FAILED` / `ARCHIVE_GZIP_CORRUPT`）不应被归属问题顶掉（既有回归依赖这些原因码）。
5. **出口**：跨代次命中交**人工按原代次补账**（独立补账通道属后续项，见「后果」）。
6. **红线**：文件名推导**只允许**影响 `event_time_utc` / `canonical_content_hash`，**严禁影响 `event_id`**。

## 理由

- **静默重复是本平台最难排查的形态**：它不报错、不丢数据，只在跨代次重建、统计与导出时暴露；而 `VerifiedRuns` 的区间凭据按代次工作，认不了别的代次的账，于是"看起来只是多写了一次"会演变成"缺口永远消解不掉"。
- **把拒绝做成可见事件**（缺口 + 告警 + 失败标记），而不是"跳过这个文件"：运维必须知道有一份归档没进来，且它属于哪个代次。
- **零迁移是硬约束**：日志数据面已经在 FR-484/ADR-095 之后稳定，为一次归属查询引入新表与迁移，代价与收益完全不成比例。

## 后果

- **正面**：跨代次重复的唯一真入口被堵死；拒绝可见、可定位（`ARCHIVE_FOREIGN_GENERATION` + 路径 + 两个代次 + 原因）；首次登记的既有能力（历史归档导入）保持不变。
- **代价与边界**：查询是 O(同源代次数 × 分段数) 的线性扫描（调用粒度=每个待导入归档一次）；**跨代次命中后必须人工介入**——补账通道尚未实现，在此之前该归档不会入账（宁可缺、不可重复）。
- **可转红防护**：`internal/worker/logs/acquire/archive_ownership_test.go`（归属闸）、`internal/worker/logs/ingest/runtime_test.go` 的 `ARCHIVE_FOREIGN_GENERATION` 断言；去除 `lookupOwner` 接线或把未命中改成拒绝，回归即转红。
- **实现**：提交 `792ea9ea`（归档代次归属执行体）、`cc193c0f`（pipeline 注入点接线）。
- **后续项**：① **跨代次补账通道**（按原代次人工补账，独立项，见 [ADR-101](101-manual-gap-abandonment-with-audit-trail.md) 的后续清单）；② 常驻对账协程 + per-event 状态位（未来选项，当前不做）。
