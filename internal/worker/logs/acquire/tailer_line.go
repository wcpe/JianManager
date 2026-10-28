package acquire

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

const PhysicalMappingUnknownReason = "PHYSICAL_MAPPING_UNKNOWN"

// checkPhysicalMapping 保证“物理起点已证明”这项前提。
//
// ## 基础语义（保留）
//
// 不把旧字段的 false/0 当成已证明的物理零起点：旧非零逻辑段可能是纯截断重锚，
// 也可能是普通轮转；**无法区分时**保留原物并稳定背压。
//
// ## 新增：已证明“文件确实换了”时重建映射（2026-10-28）
//
// 现场（生产 10/19 个源）：live 段 `[8131943,8131943)` 零宽、`physical_mapping_known` 缺失，
// 而文件在 14:05 被新实例重写（`Starting minecraft server version` 开头）
// 并持续写入。旧代码直接报 `PHYSICAL_MAPPING_UNKNOWN` ⇒ 永久拒读 ⇒ 采集死锁。
//
// ### 与上次失败尝试的区别（关键）
//
// 上次的判据是“分段零宽 + 文件存在 ⇒ 重建”，被
// `TestSixFixLegacyNonzeroLiveMappingCannotRereadPrefix` 抑住——
// 那个用例里文件内容是**无关**的旧前缀，重建会把它当成新日志重复入库。
//
// 本次加了**一项直接证据**：`IdentitySHA256` 与实际文件内容前缀**不匹配**。
// 含义是“当前文件的内容身份与当初记录的那份不同”⇒ 文件确实换了身份
// ⇒ 从头读不会重复（与 Filebeat 的 `file_identity: fingerprint` 不同时开新 harvester 同义）。
//
// 上次失败的用例里，账本记录的 identity **与文件当前内容一致**（该用例用
// `ContentIdentity(path)` 写入再验证），所以本判据在那里**不会**触发——两件事不矛盾。
//
// ### 四个条件（全部成立才重建）
//
//  1. 本段是 live（归档/轮转有自己的归属闸，不走这里）；
//  2. `physicalStart == 0`（旧数据特征；非零说明是重锚过的残段）；
//  3. 账本记录了内容身份（`IdentityBytes>0 && IdentitySHA256!=""`）；
//  4. 当前文件前缀哈希**与记录不同**（直接证据：文件已换）；
//  5. 文件大小 ≤ maxPhysicalMappingRebuildBytes（排除异常大文件）。
//
// 任一条不成立 ⇒ 保持原行为（拒绝 + 记可见缺口），不猜。
func (t *FileTailer) checkPhysicalMapping() error {
	if t.physicalMappingKnown || (t.segmentStart == 0 && t.physicalStart == 0) {
		// 前置已成立（含“升级前就已重建过”的情形）：遗留缺口必须撑销。
		//
		// 为什么不能只依赖重建分支里的消解（2026-10-28 真机）：v5 曾出现过
		// “重建成功但缺口没清”的源（inst:321）——它的 physicalMappingKnown 已是 true，
		// 下一轮直接走这个早退分支，**永远不会再进重建流程**
		// ⇒ 遗留缺口永久留在账本里 ⇒ 源永久停采。
		//
		// 不变量：缺口的存在应与“当前映射状态”一致——
		// 映射已证明时，同路径的 PHYSICAL_MAPPING_UNKNOWN 就应消解。
		t.clearStaleMappingGaps()
		return nil
	}
	if err := t.rebuildMappingForReplacedFile(); err == nil {
		return nil
	}
	detail := fmt.Sprintf("%s: path=%q logical_start=%d; refusing to assign new identities without a proven physical origin", PhysicalMappingUnknownReason, t.path, t.segmentStart)
	if entry := t.led.Get(t.key); entry != nil {
		for _, gap := range entry.Gaps {
			if gap.Reason == PhysicalMappingUnknownReason && gap.Detail == detail {
				return errors.New(detail)
			}
		}
	}
	gapErr := t.led.RecordGap(t.key, t.logicalPos, t.logicalPos, PhysicalMappingUnknownReason, detail)
	return errors.Join(errors.New(detail), gapErr)
}

// clearStaleMappingGaps 消解本路径上遗留的 PHYSICAL_MAPPING_UNKNOWN 诊断缺口。
//
// 调用时机：仅在映射**已被证明**时（早退分支或重建成功）。
// 幂等：没有可消解的缺口就什么也不做。
func (t *FileTailer) clearStaleMappingGaps() {
	if t.led == nil {
		return
	}
	resolved, err := t.led.ResolveGapsMatching(t.key, PhysicalMappingUnknownReason,
		fmt.Sprintf("path=%q", t.path), "physical mapping is proven; stale isolation gap cleared")
	if err != nil || resolved == 0 {
		return
	}
	slog.Info("物理映射已证明，已撑销遗留隔离缺口",
		"logSourceID", t.key.LogSourceID, "path", t.path, "resolvedGaps", resolved)
}

// maxPhysicalMappingRebuildBytes 是重建时可接受的活文件大小上限。
//
// 取 64 MiB：远大于正常轮转间隔内的写入量（生产实测单文件 <20 KB），
// 又远小于“明显是别人的大文件”的量级。
const maxPhysicalMappingRebuildBytes = 64 << 20

var errMappingNotRebuildable = errors.New("acquire: physical mapping not rebuildable")

// rebuildMappingForReplacedFile 仅在能**直接证明文件已换**时才重建物理映射。
//
// 证据：账本记录的 `IdentitySHA256` 与实际文件当前前缀**不相等**。
// 这与“零宽”无关——零宽只说明未读过字节，不能单独作为“文件换了”的依据。
func (t *FileTailer) rebuildMappingForReplacedFile() error {
	if t.physicalStart != 0 {
		return errMappingNotRebuildable
	}
	if t.identityBytes <= 0 || t.identitySHA256 == "" {
		return errMappingNotRebuildable // 无内容身份：无法证明文件已换
	}
	info, err := os.Stat(t.path)
	if err != nil {
		return errMappingNotRebuildable
	}
	if info.Size() < t.identityBytes || info.Size() > maxPhysicalMappingRebuildBytes {
		return errMappingNotRebuildable
	}
	current, hashErr := filePrefixSHA256(t.path, t.identityBytes)
	if hashErr != nil {
		return errMappingNotRebuildable
	}
	if current == t.identitySHA256 {
		// 内容就是当初那份：不能重建（那会把已投递的残留前缀当新日志重读）。
		// 这正是 TestSixFixLegacyNonzeroLiveMappingCannotRereadPrefix 护的场景。
		return errMappingNotRebuildable
	}
	// 内容身份不同 ⇒ 文件确实换了 ⇒ 逻辑起点对应新文件第 0 字节。
	t.physicalStart = 0
	t.physicalMappingKnown = true
	// **必须同时消解已登记的旧缺口**（2026-10-28 现场教训）：
	//
	// `ResumeAcquire` 看的是账本里的未解决 gap，而不是本对象的内存状态。
	// 只把 `physicalMappingKnown` 置真、不消缺口 ⇒ 下一轮照样被拒，
	// 而这一轮又不会再报错（因为 check 已通过）⇒ **静默死锁**。
	// 这与 `ARCHIVE_CONTENT_UNKNOWN` 的隔离撑销是同一类问题。
	//
	// 只消“本路径 + PHYSICAL_MAPPING_UNKNOWN”的那几条，其它原因一律不动。
	if resolved, resolveErr := t.led.ResolveGapsMatching(t.key, PhysicalMappingUnknownReason,
		fmt.Sprintf("path=%q", t.path), "physical mapping rebuilt: live file was replaced (content identity differs)"); resolveErr == nil && resolved > 0 {
		slog.Info("物理映射重建后已撑销旧隔离缺口",
			"logSourceID", t.key.LogSourceID, "path", t.path, "resolvedGaps", resolved)
	}
	// 同时重置内容身份到**当前**文件，否则下一轮会再次判成“换了”。
	t.identityBytes, t.identitySHA256 = info.Size(), ""
	if t.identityBytes > contentIdentityBytes {
		t.identityBytes = contentIdentityBytes
	}
	if sum, sumErr := filePrefixSHA256(t.path, t.identityBytes); sumErr == nil {
		t.identitySHA256 = sum
	}
	slog.Info("活文件已被替换且旧内容身份不匹配，物理映射已重建（旧账本缺字段的自愈）",
		"logSourceID", t.key.LogSourceID, "path", t.path,
		"logicalStart", t.segmentStart, "fileSize", info.Size())
	return nil
}

func (t *FileTailer) recordReadLimit(err *ReadLimitError) error {
	entry := t.led.Get(t.key)
	if entry != nil {
		for _, gap := range entry.Gaps {
			if gap.Reason == ReadLineTooLongReason && gap.StartPos == err.LogicalStart && gap.Detail == err.Error() {
				return nil
			}
		}
	}
	// 未消费长行不冒称已取得精确末端；零宽缺口只承载可见背压责任，原文件仍是权威副本。
	return t.led.RecordGap(t.key, err.LogicalStart, err.LogicalStart, ReadLineTooLongReason, err.Error())
}

// FlushPhysicalEOF 只供显式停止/有限分段闭合调用，活文件普通 Poll 绝不走此策略。
// 无末尾换行的最后一行按原字节区间形成单个事件，不补造换行或拆成多个普通事件。
func (t *FileTailer) FlushPhysicalEOF() ([]logtypes.Event, error) {
	return t.flushPhysicalLineAt(t.path)
}

func (t *FileTailer) closeRotatedPhysicalLine() error {
	if !t.pendingPhysicalLine || t.rotateTo == "" || DetectArchiveFormat(t.rotateTo) != ledger.SegmentRotatedText {
		return nil
	}
	events, err := t.flushPhysicalLineAt(t.rotateTo)
	if err != nil {
		return err
	}
	t.closedEvents = append(t.closedEvents, events...)
	return nil
}

func (t *FileTailer) flushPhysicalLineAt(path string) ([]logtypes.Event, error) {
	if !t.pendingPhysicalLine {
		return nil, nil
	}
	limits := t.readLimits
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(t.cursor, io.SeekStart); err != nil {
		return nil, err
	}
	round := &roundReader{reader: f, remaining: limits.MaxBytesPerPoll}
	line, readErr := readPhysicalLine(bufio.NewReaderSize(round, 32<<10), limits.MaxLineBytes)
	if limitErr, ok := readErr.(*ReadLimitError); ok {
		limitErr.Path, limitErr.LogicalStart, limitErr.PhysicalFrom = path, t.logicalPos, t.cursor
		return nil, errors.Join(limitErr, t.recordReadLimit(limitErr))
	}
	if readErr != nil && readErr != io.EOF {
		return nil, readErr
	}
	if len(line) == 0 {
		return nil, nil
	}
	start, end := t.logicalPos, t.logicalPos+uint64(len(line))
	raw := physicalLineBody(line)
	eventEnd, complete, emit := t.hook.Feed(raw, start, end)
	if eventEnd == 0 || (complete && emit) {
		eventEnd = end
	}
	if err := t.led.AdvanceRead(t.key, eventEnd); err != nil {
		return nil, err
	}
	t.cursor += int64(len(line))
	t.logicalPos = end
	t.pendingPhysicalLine = false
	if err := t.registerLiveSegment(); err != nil {
		return nil, err
	}
	if !complete || !emit {
		return nil, nil
	}
	t.eventsEmitted++
	return []logtypes.Event{logtypes.BuildEvent(t.parser, logtypes.RecordRange{Start: start, End: eventEnd}, "", "", "", string(t.sourceCategory), string(raw))}, nil
}
