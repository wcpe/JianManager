package ingest

import "github.com/wcpe/JianManager/internal/worker/logs/logtypes"

// 重建输入必须并入本次已接纳批次；只复制轮内结果，不修改既有不可变receipt。
func appendCanonicalBatch(base, batch []logtypes.Event) []logtypes.Event {
	out := append([]logtypes.Event(nil), base...)
	indices := make(map[string]int, len(out)+len(batch))
	for i, event := range out {
		indices[event.EventID] = i
	}
	for _, event := range batch {
		index, exists := indices[event.EventID]
		if !exists {
			indices[event.EventID] = len(out)
			out = append(out, event)
			continue
		}
		old := out[index]
		if old.CanonicalHash == event.CanonicalHash {
			continue
		}
		_, take := adjudicateSameIntervalIdentity(eventIdentity{hash: old.CanonicalHash, start: old.Record.Start, end: old.Record.End, size: len(old.Message), msg: old.Message, level: old.Level, stream: old.Stream}, event, len(event.Message))
		if take {
			out[index] = event
		}
	}
	return out
}
