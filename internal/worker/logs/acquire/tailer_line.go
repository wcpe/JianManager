package acquire

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

const PhysicalMappingUnknownReason = "PHYSICAL_MAPPING_UNKNOWN"

// checkPhysicalMapping 不把旧字段的 false/0 当成已证明的物理零起点。
// 旧非零逻辑段可能是纯截断重锚，也可能是普通轮转；无法区分时保留原物并稳定背压。
func (t *FileTailer) checkPhysicalMapping() error {
	if t.physicalMappingKnown || (t.segmentStart == 0 && t.physicalStart == 0) {
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
