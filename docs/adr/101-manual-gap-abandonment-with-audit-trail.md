# ADR-101: 永久空洞只许人工裁定放弃，且必须留痕

- **日期**: 2026-10-02
- **状态**: accepted
- **关联**: FR-473（契约 §4.3 回收证明 / §9 验收条款"未确认数据不被静默删除"）· FR-474（缺口登记与自愈）· [ADR-099](099-log-cost-governance-and-invariant-r.md)（不变量 R：抑制不得造洞）· [ADR-100](100-archive-generation-ownership-enforcement.md)（代次归属：拒绝后交人工补账）· `docs/specs/worker-log-acquisition/spec.md` §3.3 · `docs/specs/worker-log-query-federation/spec.md`

## 上下文

- 缺口的正常出口有三条：**补投/重读**、**投递成功后自动消解**、**已发布投影消解**（FR-474 加固）。但存在**物理上永远补不上**的形态：归档对象本身丢失/不可读、源文件被外部删除、磁盘事故导致的分段永久缺失。
- 此前**没有任何"承认永久丢失"的通道**，后果是一整条死锁链：
  1. 缺口永不可解 → `ResumeAcquire` 要求"零未解决缺口" → **源永久 PAUSED**；
  2. 回收链也在缺口前卡住（`CanReclaim` 只认"责任已转移"的恢复分段，而该责任**不可能**再通过校验转移，因为数据物理没了）→ **WAL 积压永不回落** → 滞回永不满足 → 源再也不会自己恢复。
  这正是 2026-10-01/02 事故"13+ 小时零新数据、最终靠人工按源解算"的收尾形态。
- 但**自动放弃是不可接受的**：自动放弃 = 静默丢日志。既有守卫 `TestAutoResolveNeverMarksPermanentlyLost` 明确禁止自动路径写 `PERMANENTLY_LOST`——这条守卫必须保留，不得为"省事"放宽。

## 决策

1. **只允许人工/半自动放弃**；**自动路径永不放弃**（`TestAutoResolveNeverMarksPermanentlyLost` 保持为硬守卫）。
2. **`AbandonGapsThrough`（新增）专供放弃路径**：`Operator` 为空/全空白**一律拒绝执行**——放弃是唯一允许"回收链跨过一个永远补不上的空洞"的动作，**无痕放弃等于给静默丢日志开后门**。
3. **结构化留痕**：写 `ReasonCode = PERMANENTLY_LOST` + `ResolvedAtUTC` + `ResolvedBy`（操作人）+ 被放弃的源位置闭区间 + `Through`（"我确认该位置之前的缺口都不再补齐"）；同时把被解算缺口的 `Resolution` 写成可读文本。
4. **独立、有界、比缺口活得久的凭据**：`Entry.Abandonments`（单源上限 **64**，相邻/重叠裁定**就地合并**，触顶按 `From` 从旧到新裁剪）。**为什么独立于 `Gap` 存一份**：已解决缺口会被 `trimResolvedGapsLocked` 按数量裁剪（只留审计尾部），凭据若挂在缺口上，会随缺口一起被裁掉——而它要一直支撑回收链的放行判定。
5. **`ResolveGapsThroughExcept` 逐字不动**：它是缺口 **default-deny 判据的载体**（按原因排除的语义必须逐字保持）。放弃路径**刻意不与其共用实现**——两条语义的演化不得耦合，放弃路径的每条断言都必须能被独立读出。
6. **操作人来源 = 认证上下文，不是请求体**：incoming gRPC metadata 键 `x-jm-operator`（`OperatorMetadataKey`）。请求体可被任意伪造（PUT 一个字段就把责任推给别人），故操作人必须由传输层携带；**未携带时本层拒绝执行**并给出可执行补救（CP 侧需在发起 `LogResolveIngestGaps` 且带 `storage_namespace` 时设置该头为可审计的操作人标识，如控制台登录名 / API 密钥主体）。
7. **出口（回收链放行）**：`logtypes.CanReclaim` **仍是否决门禁**；仅当**本分段覆盖的区间内**存在与 `[Reclaim, CoversTo)` 相交的放弃凭据时放行（`tryReclaimLocked`）。三条约束缺一不可：
   - **hold 优先**：`ref.HasHold` 为真时不走凭据放行（hold 是运维显式钉住，裁定丢失是"数据没了"，冲突时以"不许动"为准）；
   - **上界 = 该分段的 `CoversTo`**：一张凭据不得把回收推到任意位置；
   - **无凭据 / 区间不相交 → 照旧拒绝**（default-deny 未动）。
   放行时写 WARN 审计（`logSourceID` / 代次 / `reclaimFrom` / `segmentCoversTo` / 被放弃区间 / 操作人 / 时间 / `through`）。
8. **`delivery_position` 语义不动**：它保持"诚实的连续前缀"。下游经**独立字段** `ingest_position_gaps` 显式获得"位置缺失"标记，**与 logcoord 的分区可查询性（coverage）分开标注**——"已放弃"绝不能被误读为"可查询性完整"。

## 理由

- **把"承认丢失"做成需要签名的动作**，而不是一个开关：留痕与实名带来的代价，远小于静默丢日志的代价。空操作人直接拒绝，等于把"谁放弃了这批数据"变成可追责的事实。
- **凭据必须比缺口活得久**：回收链的放行判定发生在缺口被裁剪之后，"凭什么跨过它"的证据若随缺口消失，事后就无法复查。
- **放行必须带上界与 hold 优先**：凭据证明的是"**这一段**不会有数据了"，而不是"以后都可以随便推进"；把范围钉死在本分段覆盖内，使放行不可能被用作绕过门禁的通道。
- **可见性分离**：放弃是"位置缺失"，不是"分区不可查询"。两者混在一个字段里，前端与导出会把"缺了一段"显示成"查询成功但为空"，这正是契约 §6.3 明令禁止的形态。

## 后果

- **正面**：永久空洞有了**唯一、可审计**的出口；不会再出现"因为一个补不上的空洞而永久停采 + WAL 永不回落"。自动路径的 default-deny 性质完全未变。
- **代价与边界**：跨空洞回收会**推进 WAL 回收水位**（数据确实没有补齐，只是被裁定放弃）；该缺失对下游**可见**（`ingest_position_gaps`）。放弃**不可撤销**——凭据只增不删；如需恢复数据，只能重新导入，且受 ADR-100 的代次归属闸约束（不得归到当前代次）。
- **可转红防护**：`internal/worker/logs/ledger/abandon_test.go`（`TestAbandonGapsThroughRequiresOperator`：去掉操作人空值检查即红；`TestAbandonGapsThroughRecordsStructuredEvidence`：去掉 `Entry.Abandonments` 写入即红；`TestAutoResolveNeverMarksPermanentlyLost`：自动路径守卫）、`internal/worker/logs/query/grpcsvc/service_test.go`（"已放弃位置"标记必须单独回填在 `ingest_position_gaps` 上，**不得污染 coverage 语义**）。
- **实现**：提交 `afeb5359`（空洞放弃协议批 1：结构化凭据、操作人闸与自动路径守卫）、`792ea9ea`（空洞放弃放行 = ADR-B 批 2）、`22112465`（查询面位置缺失标记：独立 proto 字段，与 coverage 语义分开）。
- **后续项（登记项，尚未实现）**：
  1. **跨代次补账通道**（ADR-100 的出口：按**原代次**人工补账）——独立项；
  2. **proto 生成物有 3 行注释偏差**：真 `protoc` 重跑即还原，**不得手改生成物**；
  3. **常驻对账协程 + per-event 状态位**：未来选项，当前不做（现有对账是启动期一次性 + 事件驱动）；
  4. **gz 手动/定时导入入口**：可选，与 ADR-098 的"gz 文件吸存（只归档不解析）"的关系待定。
