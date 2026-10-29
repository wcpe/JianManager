package acquire

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

const PhysicalMappingUnknownReason = "PHYSICAL_MAPPING_UNKNOWN"

// checkPhysicalMapping 保证“逻辑位置 ↔ 文件偏移”的映射已被证明。
//
// 这是**防串源闸**：没有这个前提就不能给任何字节分配逻辑身份，因为一旦算错，
// 同一段内容会被当成两段不同的日志分别入库（或反过来，真实新内容被当成旧前缀跳过）。
// 判据不猜：无法证明时拒绝读并留一条可见缺口，由运维/对账裁定。
//
// 分段创建时该前提就已被写入（见 NewFileTailer / anchorAtEOFLocked / ArchiveImporter），
// 所以只有“代码路径漏写”这一种情况会落到拒绝分支——那时要的是响亮的失败，不是静默串源。
// 无法证明时的处理：先试**证据驱动自愈**（见 mapping_rebuild_evidence.go）——
// 仅当能直接证明“当前文件不是当初那份”（内容身份不同）时才重建物理映射；
// 证据不足一律保持拒绝 + 可见缺口，不猜。
func (t *FileTailer) checkPhysicalMapping() error {
	if t.physicalMappingKnown || (t.segmentStart == 0 && t.physicalStart == 0) {
		// `segmentStart == 0 && physicalStart == 0`：从文件头开始读是该前提的平凡成立形（
		// 逻辑 0 就对应物理 0），无需额外证据。
		return nil
	}
	// 物理映射无法自愈（归档重建机制已删除）：只要不是平凡成立形，一律按不可证明处理。
	// 用户的架构裁定是「读到就投，读不到就算了」，不做恒久追溯。
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
	if !t.pendingPhysicalLine || t.rotateTo == "" {
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
