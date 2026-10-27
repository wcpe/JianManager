// 本文件是 eventstore 中**唯一**会删除既有段的路径（Prune）。
//
// 设计前提（改动前必须理解，否则一定会造成数据事故）：
//
//  1. 本包其余部分一律「只追加、不回收、不替换」（见 snapshot.go 的 ReadSnapshot 注释、
//     compaction_estimate.go 的 CompactionEstimate 注释）。Prune 是刻意的例外，因此它必须
//     把「段只增不减」这条不变量原本免费提供的保证，逐条自己重建出来：
//     - 段内容与清单逐字节一致（回收前重扫，坏段一律不删）；
//     - 被删段的所有事件的 record_end 都在调用方给定的水位以内（有界回收，不按时间拍脑袋）；
//     - 只回收**头部连续前缀**，绝不挖洞（段内顺序是权威集合的追加顺序，中间挖洞会让
//     「段前缀」与「源位置前缀」的对应关系失效）；
//     - 回收是幂等的，且中途失败由 PRUNE.json 前滚收敛，不留下「删了一半」的撕裂状态。
//
//  2. 本包**不做**「这些事件是否还需要」的判断。水位由调用方给出，因为只有上层知道
//     「已投递并逐字段校验通过」的证据（VerifiedRuns / ledger 的 delivery 水位等）。
//     水位给错 = 数据被删，且不可恢复。因此 Prune 的全部条件都是 fail-closed：
//     水位为 0、段数不足、段未封印、清单与内容不符、有在途读取快照、只读实例 —— 任一成立即不删。
//
//  3. 为什么必须有回收：段承载的不只是「还没成功投递的事件」（ingest 的 appendEvents 只追加
//     段内尚不存在的身份），还包含投递成功后**仍然留在段里**的历史事件体。后者只写不删会
//     无限累积 —— FR-484 把正文从内存挪到了磁盘，但没有给出回收路径
//     （见 docs/specs/log-cost-governance/spec.md §7「eventstore 段/归档对象的回收 | 未做」）。
//
//  4. 上层依赖（Prune 一旦真正被调用，下列能力会对被回收区间失效，这是**契约级**影响，
//     接线前必须先由产品/运维决策确认，见 Prune 的文档注释逐条列举）。
package eventstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// pruneJournalName 是回收过程中间的意图日志。它不匹配 seg-*.ndjson 模式，
	// 因此 loadManifest 既不会把它当段，也不会因为它存在而判定「清单之外的段」。
	pruneJournalName = "PRUNE.json"
	pruneJournalVer  = 1
)

// errPruneInProgress 表示该源正在回收：此刻复制清单会读到「已前移/未前移」混合的段名，
// 因此新读取一律拒绝（上层重试即可），而不是让读者拿到撕裂视图。
var errPruneInProgress = errors.New("eventstore: prune in progress")

// errPruneJournalPresent 表示只读打开时发现未完成的回收日志。
// 只读取证实例不允许前滚（那要改介质），故必须硬失败而不是猜一个状态。
var errPruneJournalPresent = errors.New("eventstore: unfinished prune journal present")

// PruneResult 是一次回收的实测结果。Key/Watermark 回显入参，便于调用方对账与打点。
type PruneResult struct {
	Key       string `json:"key"`
	Watermark uint64 `json:"watermark"`
	// ReclaimedSegments/ReclaimedBytes/ReclaimedEvents 是本次真正删掉的段数、字节数、事件数。
	ReclaimedSegments int   `json:"reclaimed_segments"`
	ReclaimedBytes    int64 `json:"reclaimed_bytes"`
	ReclaimedEvents   int64 `json:"reclaimed_events"`
	// RemainingSegments 是回收后该源剩余的段数（含末尾未封印段）。
	RemainingSegments int `json:"remaining_segments"`
	// Skipped 非空表示本次**一个段都没删**及原因（可观测：不删也要说得清为什么）。
	Skipped string `json:"skipped,omitempty"`
}

// prunePlan 是一次已经证明安全的头部回收方案：丢弃前 Offset 段，其余段顺序前移。
type prunePlan struct {
	offset int
	bytes  int64
	events int64
	old    []SegmentMeta
	next   Manifest
}

// pruneJournal 是回收的意图日志：先耐久写下「回收后的清单长什么样」以及「前移多少段」，
// 然后才动文件。崩溃后由 recoverPruneJournal 按同一份日志前滚（幂等），
// 因此不存在「文件已前移、清单还是旧的」这种会让 loadManifest 硬失败的持久状态。
type pruneJournal struct {
	Version int `json:"version"`
	// Key 用于核对日志与目录归属，防止把 A 源的日志套到 B 源目录上。
	Key string `json:"key"`
	// Offset 是被回收的头部段数 k；前移规则固定为「新第 i 段 = 旧第 i+k 段」。
	Offset int `json:"offset"`
	// Old 是回收前的完整清单，New 是回收后应发布的清单。
	Old []SegmentMeta `json:"old_segments"`
	New Manifest      `json:"new_manifest"`
}

// Prune 回收该源**头部连续前缀**中「已被证明不再需要」的段，语义严格有界且 fail-closed。
//
// 入参 watermark 是调用方给出的**已确认水位**（源位置 record_end 的闭区间上界）：
// 只有「整段所有事件的 record_end 都 <= watermark」的段才可能被回收。传 0 表示「没有下界证据」，
// 本函数直接返回且一个字节都不删。
//
// 回收成立的全部必要条件（任一不成立即不删，宁可占盘）：
//  1. 实例可写（OpenReadOnly 实例一律 ErrReadOnly）；
//  2. watermark > 0；
//  3. 该源已存在且已通过首次校准（校准失败=状态不明 ⇒ 返回错误且不删）；
//  4. 上一次追加没有失败（writeErr 非空 ⇒ 需重开，跳过）；
//  5. 没有在途的 ReadSnapshot（有读者 ⇒ 跳过；读者的段路径会在前移后失效）；
//  6. 至少保留最后一段（永远不把该源的段删空，否则「段是权威集合」的判据会退化成空集）；
//  7. 前缀内每一段都已封印且 MaxEnd <= watermark；
//  8. 前缀内每一段都能被完整重扫，且重扫结果与清单逐字节一致（坏段/缺行 ⇒ ErrCorrupt 且不删）。
//
// 幂等：同一 watermark 重复调用，第二次的前缀条件不再成立 ⇒ 返回 0 回收，不产生任何副作用。
//
// **调用方必须同时接受的能力退化**（逐条对应 ingest 侧的代码位置，切勿只看本包）：
//   - `canonicalRecoveryEvents`（runtime.go）返回的「权威全量集合」将不再包含被回收区间，
//     即「VL 数据根丢失后重建 projection」对被回收区间**不再成立**；
//   - 发布确认证据（`snapshot.Count() < proof.rows ⇒ errPublicationStale`，runtime.go）
//     依赖段行数不减，回收后行数变小 ⇒ 该源的 pending 确认会判 stale；
//   - `captureProjectionHistory` 的 `proof.rows` 同上；
//   - `cachedStoredRows` / `storedCoverageMemo` 两个内存缓存都假设「段无裁剪路径」，
//     回收后必须由调用方 bump epoch 并丢弃缓存，否则会读到过期行数；
//   - `missingFromEventStore` 按段内已有身份求差集：被回收的身份会重新被判为「缺」；
//   - `CanonicalDayMigrationInput`（day_migration_repair.go）需要某天的完整正文，
//     被回收的天会以「input does not reach captured source prefix」硬失败（响亮失败，不是静默）；
//   - `locateEventAtOrAfter`（event_status.go，`log-event-status --at`）只能看到剩余段，
//     对更早的时间点会给出**看似正常但已偏后**的定位结果；
//   - `eventBodyLookup`（WAL 引用水合）对被回收事件返回缺失 ⇒ 走「记可见缺口」而非静默丢弃；
//   - `publishedClosedForSourceCtx` 的按日 MaxEnd 不再包含被整天回收的旧天。
//
// 本函数**目前没有任何自动调用点**：上述退化是契约级取舍，必须由产品决策与显式运维动作承担，
// 不能藏进周期路径里（护栏：宁占盘不丢数据）。
func (s *Store) Prune(key string, watermark uint64) (PruneResult, error) {
	result := PruneResult{Key: key, Watermark: watermark}
	if s.readOnly {
		return result, ErrReadOnly
	}
	if key == "" {
		return result, fmt.Errorf("eventstore: source key is required")
	}
	if watermark == 0 {
		// 0 表示「没有任何已确认水位」。绝不解成「全部可回收」——那是最危险的反向默认。
		result.Skipped = "水位为 0：没有下界证据，一律不回收"
		return result, nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return result, os.ErrClosed
	}
	sec := s.byKey[key]
	s.mu.Unlock()
	if sec == nil {
		// 未知源：没有任何段可回收。按幂等语义返回空结果而不是报错（与 Count/Segments 一致）。
		result.Skipped = "该源没有事件段"
		return result, nil
	}

	sec.mu.Lock()
	defer sec.mu.Unlock()
	if sec.closed {
		return result, os.ErrClosed
	}
	if sec.writeErr != nil {
		// 上次追加把段写成了未知状态：必须先重开校准，此处一律不删。
		result.Skipped = "上次追加失败，需重开后校准"
		return result, nil
	}
	// 校准失败即「段状态不明」：把错误如实上报，且此时尚未改动任何文件。
	if err := sec.calibrateLocked(); err != nil {
		return result, err
	}

	plan, skipped, err := sec.planPruneLocked(watermark)
	if err != nil {
		return result, err
	}
	if skipped != "" {
		result.Skipped = skipped
		result.RemainingSegments = len(sec.manifest.Segments)
		return result, nil
	}

	// 进入物理阶段前一次性完成「有无在途读者」的判定与「正在回收」的置位。
	// 二者必须在同一个临界区内，否则读者可能在判定之后、置位之前拿到旧清单，
	// 随后它的段路径又被前移改掉（撕裂读）。规划阶段（上面那次全段重扫）不进这个临界区，
	// 因为那时还没有改动任何文件，读者照常工作即可。
	sec.viewMu.Lock()
	if sec.liveSnapshots > 0 {
		sec.viewMu.Unlock()
		result.Skipped = "存在在途读取快照，本轮不回收"
		result.RemainingSegments = len(sec.manifest.Segments)
		return result, nil
	}
	sec.pruneActive = true
	sec.viewMu.Unlock()
	defer func() {
		sec.viewMu.Lock()
		sec.pruneActive = false
		sec.viewMu.Unlock()
	}()

	if err := sec.applyPruneLocked(plan); err != nil {
		return result, err
	}
	result.ReclaimedSegments = plan.offset
	result.ReclaimedBytes = plan.bytes
	result.ReclaimedEvents = plan.events
	result.RemainingSegments = len(plan.next.Segments)
	return result, nil
}

// planPruneLocked 计算「可安全回收的头部前缀」，不做任何文件改动。
// 返回 skipped 非空表示不回收（及其原因）；返回 error 表示状态损坏（必须响亮上报）。
func (sec *section) planPruneLocked(watermark uint64) (prunePlan, string, error) {
	old := append([]SegmentMeta(nil), sec.manifest.Segments...)
	// 至少保留最后一段：段一旦被删空，「段持有权威事件」这条判据（canonicalRecoveryEvents
	// 里 storedCount > 0 的分支）就会退化成「没有权威集合」，比占盘危险得多。
	if len(old) < 2 {
		return prunePlan{}, "段数不足（至少保留最后一段）", nil
	}
	offset := 0
	for offset < len(old)-1 {
		meta := old[offset]
		if !meta.Sealed {
			// 未封印段是本进程的追加目标，且其清单字节数可能落后于物理末尾。
			break
		}
		if meta.MaxEnd > watermark {
			// MaxEnd 是该段所有事件 record_end 的最大值 ⇒ MaxEnd <= 水位等价于
			// 「段内**每一条**事件都在水位以下」，而不是「大致在下面」。
			break
		}
		offset++
	}
	if offset == 0 {
		return prunePlan{}, "头部段仍在水位之上或未封印", nil
	}

	// 删前重扫：把「清单声称的内容」与「物理内容」再对一次。
	// 单纯信任内存里的清单是不够的 —— 校准可能发生在很久以前，而这期间介质可能已经损坏。
	// 重扫成本与即将释放的字节同阶，作为回收前的最后一道闸是值得的。
	plan := prunePlan{offset: offset, old: old}
	for i := 0; i < offset; i++ {
		meta := old[i]
		// last=false：已封印段不允许任何尾部半行，出现即损坏。
		actual, err := calibrateSegment(context.Background(), sec.dir, meta, false)
		if err != nil {
			return prunePlan{}, "", err
		}
		if actual != meta {
			return prunePlan{}, "", fmt.Errorf(
				"%w: 回收候选段与清单不一致（拒绝删除）%s", ErrCorrupt, meta.Name)
		}
		plan.bytes += actual.Bytes
		plan.events += int64(actual.Count)
	}

	plan.next = cloneManifest(sec.manifest)
	plan.next.Segments = make([]SegmentMeta, 0, len(old)-offset)
	for i := offset; i < len(old); i++ {
		meta := old[i]
		// 段序号必须从 1 连续：loadManifest 会逐项校验 Seq == i+1 且 Name 由 Seq 派生。
		meta.Seq = i - offset + 1
		meta.Name = segmentFileName(meta.Seq)
		plan.next.Segments = append(plan.next.Segments, meta)
	}
	return plan, "", nil
}

// applyPruneLocked 执行一次已证明安全的回收：关句柄 → 写意图日志 → 前移段名 → 发布清单 → 清日志。
// 调用方必须已持有 sec.mu（与追加互斥）。
//
// 关键不变式：**意图日志一旦落盘，本源就不再接受写入，直到重开时的前滚把它清掉**。
// 理由是日志里的 Old/New 清单与前移偏移必须与磁盘现状严格对应；如果日志还在盘上又继续追加，
// 清单会随追加演进，而日志仍描述旧状态 —— 下次崩溃后的前滚就会按过期偏移去动段，
// 那才是真正会撕掉数据的路径。因此日志落盘之后的任何失败都用 writeErr 冻结本源写入。
func (sec *section) applyPruneLocked(plan prunePlan) error {
	// 步骤 1：关闭活跃段句柄。前移会改掉它的路径；所有数据已在此前的 Append 里 fsync，
	// 关闭只是让下一次 Append 按新名字重新打开，不丢任何已提交内容。
	// 这一步失败时尚未写日志、也未动任何文件，目录完全一致，直接返回即可。
	if sec.active != nil {
		if err := sec.active.Close(); err != nil {
			return fmt.Errorf("eventstore: 回收前关闭活跃段: %w", err)
		}
		sec.active = nil
	}
	sec.activeMeta = nil

	// 步骤 2：意图日志先行并耐久。此后无论在哪一步崩溃，recoverPruneJournal 都能前滚收敛。
	journal := pruneJournal{
		Version: pruneJournalVer,
		Key:     sec.manifest.Key,
		Offset:  plan.offset,
		Old:     plan.old,
		New:     plan.next,
	}
	if err := writePruneJournal(sec.dir, journal); err != nil {
		return err
	}
	// 日志已在盘上：任何后续失败都必须冻结写入（见函数头的不变式）。
	freeze := func(err error) error {
		sec.writeErr = err
		return err
	}

	// 步骤 3：按升序前移段名并核对物理段集合（详见 executePruneChain 的正确性说明）。
	if err := executePruneChain(sec.dir, plan.offset, len(plan.old)); err != nil {
		return freeze(err)
	}
	// 步骤 4：发布新清单。在此之前磁盘上的 MANIFEST 仍是旧清单，
	// 只有这一步是「回收生效」的原子提交点。
	previous := sec.manifest
	sec.manifest = cloneManifest(plan.next)
	if err := sec.writeManifestLocked(); err != nil {
		sec.manifest = previous
		return freeze(err)
	}
	// 步骤 5：清掉意图日志。失败也无害（下次打开会把它当成已完成的前滚再走一遍），
	// 但按不变式仍要冻结写入。
	if err := removeDurableFile(sec.dir, pruneJournalName); err != nil {
		return freeze(err)
	}
	return nil
}

// executePruneChain 把「旧第 i+k 段」前移到「新第 i 段」（i 从 1 到 total-k），
// 删掉没有被任何前移覆盖到的残余头部段，同步目录项，最后核对物理段集合。
//
// 正确性（升序前移为什么不会互相覆盖）：
//   - 目标名 i 的来源是 i+k，且 k >= 1 ⇒ 来源名严格大于目标名；
//   - 目标名 i 要么是待丢弃的头部段（i <= k，内容本来就要丢），
//     要么是在更早一步（i-k < i）已经腾空的旧名 ⇒ 不会误删仍需要的段；
//   - 因此「按 i 升序、先删目标再改名」在任一步骤中断，都只剩「一部分旧名还在」的形态，
//     前滚只需「旧名还在就继续前移」，天然幂等。
//
// 为什么还要单独删残余头部段：当 k > m（= total-k，即头部比保留部分更长）时，
// 前移只覆盖目标名 1..m，头部名 m+1..k 既不是目标也不是来源，会作为「清单之外的段」
// 残留下来 —— 而 loadManifest 对未列出的段一律判 ErrCorrupt，所以必须显式清掉。
func executePruneChain(dir string, offset, total int) error {
	next := total - offset
	for i := 1; i <= next; i++ {
		dst := segmentFileName(i)
		src := segmentFileName(i + offset)
		dstPath := filepath.Join(dir, dst)
		srcPath := filepath.Join(dir, src)
		if _, err := os.Lstat(srcPath); err != nil {
			if os.IsNotExist(err) {
				// 该来源已经前移过（日志前滚重放的正常命中）。
				continue
			}
			return fmt.Errorf("eventstore: 回收检查来源段 %s: %w", src, err)
		}
		// 先删目标再改名：POSIX rename 本身可以覆盖，但 Windows 不允许，
		// 显式 Remove 让两个平台走同一条路径（且目标内容本就是要丢弃的头部段）。
		if err := os.Remove(dstPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("eventstore: 回收清理目标段 %s: %w", dst, err)
		}
		if err := os.Rename(srcPath, dstPath); err != nil {
			return fmt.Errorf("eventstore: 回收前移段 %s→%s: %w", src, dst, err)
		}
	}
	// 残余头部段：名字落在 (next, offset] 的段既未参与前移也不该被保留。
	for i := next + 1; i <= offset; i++ {
		name := segmentFileName(i)
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("eventstore: 回收删除残余头部段 %s: %w", name, err)
		}
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("eventstore: 回收后同步目录: %w", err)
	}
	return verifyPruneOutcome(dir, next)
}

// writePruneJournal 耐久写下回收意图日志：临时文件 → fsync → 原子改名 → 同步目录。
func writePruneJournal(dir string, journal pruneJournal) error {
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("eventstore: 编码回收日志: %w", err)
	}
	return writeDurableFile(dir, pruneJournalName, data)
}

// writeDurableFile 把 data 作为 dir/name 的内容耐久落盘（临时文件 + fsync + 原子改名 + 目录同步）。
func writeDurableFile(dir, name string, data []byte) error {
	tmp := filepath.Join(dir, name+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("eventstore: 创建 %s 临时文件: %w", name, err)
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("eventstore: 写入 %s: %w", name, err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("eventstore: 发布 %s: %w", name, err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("eventstore: 同步 %s 所在目录: %w", name, err)
	}
	return nil
}

// removeDurableFile 删除文件并同步目录项；文件本就不存在视为成功（幂等）。
func removeDurableFile(dir, name string) error {
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("eventstore: 删除 %s: %w", name, err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("eventstore: 同步 %s 所在目录: %w", name, err)
	}
	return nil
}

// recoverPruneJournal 在载入源清单**之前**收敛未完成的回收。
//
// 为什么必须早于 loadManifest：回收中途崩溃会留下「清单列出的段名与实际段名不一致」的目录，
// 而 loadManifest 对这种形态一律判 ErrCorrupt。前滚把目录恢复到与某一份完整清单一致后，
// 后续校验才有意义。
//
// allowWrite=false（只读取证）时不做前滚，直接硬失败：只读实例不得改动介质，
// 也不能假装自己看懂了一个中间状态。
func recoverPruneJournal(dir string, allowWrite bool) error {
	path := filepath.Join(dir, pruneJournalName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("eventstore: 读取回收日志 %s: %w", path, err)
	}
	if !allowWrite {
		return fmt.Errorf("%w: %s", errPruneJournalPresent, path)
	}
	var journal pruneJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return fmt.Errorf("%w: 解析回收日志 %s: %w", ErrCorrupt, path, err)
	}
	if err := validatePruneJournal(journal); err != nil {
		return err
	}
	// 日志必须属于本目录的源：目录里若已有清单，其 Key 必须与日志一致。
	if raw, readErr := os.ReadFile(filepath.Join(dir, manifestName)); readErr == nil {
		var onDisk Manifest
		if err := json.Unmarshal(raw, &onDisk); err != nil {
			return fmt.Errorf("%w: 回收前滚时清单无法解析 %s: %w", ErrCorrupt, dir, err)
		}
		if onDisk.Key != journal.Key {
			return fmt.Errorf("%w: 回收日志的源与清单不一致 %s", ErrCorrupt, dir)
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("%w: 回收前滚时清单不可读 %s: %w", ErrCorrupt, dir, readErr)
	}
	// 前滚：先把段名前移到位（executePruneChain 自带「物理段集合必须恰好是 1..N」的核对），
	// 再发布清单并清日志。
	if err := executePruneChain(dir, journal.Offset, len(journal.Old)); err != nil {
		return err
	}
	newData, err := json.MarshalIndent(journal.New, "", "  ")
	if err != nil {
		return fmt.Errorf("eventstore: 编码回收后清单: %w", err)
	}
	if err := writeDurableFile(dir, manifestName, newData); err != nil {
		return err
	}
	return removeDurableFile(dir, pruneJournalName)
}

// validatePruneJournal 校验意图日志自洽：前移只改 Seq/Name，其余元数据必须逐字段不变。
// 这样前滚就必然是「同一批事件的换名」，不可能凭空造出或丢掉内容。
func validatePruneJournal(journal pruneJournal) error {
	if journal.Version != pruneJournalVer {
		return fmt.Errorf("%w: 不支持的回收日志版本 %d", ErrCorrupt, journal.Version)
	}
	if journal.Key == "" {
		return fmt.Errorf("%w: 回收日志缺少源键", ErrCorrupt)
	}
	if journal.Offset < 1 || len(journal.Old) < journal.Offset+1 {
		return fmt.Errorf("%w: 回收日志的段偏移非法（%d/%d）", ErrCorrupt, journal.Offset, len(journal.Old))
	}
	if len(journal.New.Segments) != len(journal.Old)-journal.Offset {
		return fmt.Errorf("%w: 回收日志的新清单段数非法", ErrCorrupt)
	}
	if journal.New.Version != manifestVer || journal.New.Key != journal.Key {
		return fmt.Errorf("%w: 回收日志的新清单头非法", ErrCorrupt)
	}
	for i, meta := range journal.New.Segments {
		want := journal.Old[journal.Offset+i]
		if meta.Seq != i+1 || meta.Name != segmentFileName(i+1) {
			return fmt.Errorf("%w: 回收日志的新清单序号非法（第 %d 段）", ErrCorrupt, i+1)
		}
		// 逐字段比对：序号与名字是唯一允许变化的两个字段。
		want.Seq, want.Name = meta.Seq, meta.Name
		if want != meta {
			return fmt.Errorf("%w: 回收日志的新清单与旧清单内容不一致（第 %d 段）", ErrCorrupt, i+1)
		}
	}
	return nil
}

// verifyPruneOutcome 核对物理段集合恰好是 seg-000001..seg-<next>。
// 这是「不留下删了一半」的最后一道闸：集合不符就拒绝发布清单（目录里还有意图日志，下次重开重试）。
func verifyPruneOutcome(dir string, next int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("eventstore: 回收后读取目录 %s: %w", dir, err)
	}
	got := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if !isSegmentName(name) {
			continue
		}
		if entry.IsDir() {
			return fmt.Errorf("%w: 回收后发现同名目录 %s", ErrCorrupt, name)
		}
		got[name] = true
	}
	if len(got) != next {
		return fmt.Errorf("%w: 回收后段数不符（期望 %d，实际 %d）", ErrCorrupt, next, len(got))
	}
	for i := 1; i <= next; i++ {
		name := segmentFileName(i)
		if !got[name] {
			return fmt.Errorf("%w: 回收后缺少段 %s", ErrCorrupt, name)
		}
		delete(got, name)
	}
	if len(got) != 0 {
		return fmt.Errorf("%w: 回收后仍有多余段", ErrCorrupt)
	}
	return nil
}
