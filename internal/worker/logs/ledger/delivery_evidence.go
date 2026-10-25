package ledger

import (
	"sort"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// RequestDoneRangesOf 从明确的请求完成证据及仍在的批次提取精确 ACK 区间。
// Delivery/Reclaim 仅是位置，绝不作为缺失历史的成功回执。
func RequestDoneRangesOf(entry *Entry) []PositionRange {
	if entry == nil {
		return nil
	}
	ranges := append([]PositionRange(nil), entry.RequestDoneRanges...)
	for _, batch := range entry.DeliveryBatches {
		if batch.State == logtypes.DeliveryRequestDone && batch.End >= batch.Start {
			ranges = append(ranges, PositionRange{From: batch.Start, To: batch.End})
		}
	}
	return MergePositionRanges(ranges)
}

// DeliveryEvidenceOf 返回按状态分别归并的精确诊断区间，不同状态的重叠证据均保留。
// 请求完成另由 RequestDoneRangesOf 最高优先判定；非成功态先判 UNKNOWN，再判其余结果。
func DeliveryEvidenceOf(entry *Entry) []DeliveryBatch {
	if entry == nil {
		return nil
	}
	evidence := make([]DeliveryBatch, 0, len(entry.DeliveryEvidence)+len(entry.DeliveryBatches))
	evidence = append(evidence, entry.DeliveryEvidence...)
	evidence = append(evidence, entry.DeliveryBatches...)
	return mergeDeliveryEvidence(evidence)
}

func ensureDeliveryEvidence(entry *Entry) {
	if entry.deliveryEvidenceReady {
		return
	}
	entry.RequestDoneRanges = RequestDoneRangesOf(entry)
	entry.DeliveryEvidence = DeliveryEvidenceOf(entry)
	entry.deliveryEvidenceReady = true
}

func preserveDeliveryEvidence(entry *Entry, batch DeliveryBatch) {
	if batch.End < batch.Start {
		return
	}
	if batch.State == logtypes.DeliveryRequestDone {
		entry.RequestDoneRanges = MergePositionRanges(append(entry.RequestDoneRanges, PositionRange{From: batch.Start, To: batch.End}))
	}
	entry.DeliveryEvidence = appendDeliveryEvidence(entry.DeliveryEvidence, batch)
}

func appendDeliveryEvidence(evidence []DeliveryBatch, batch DeliveryBatch) []DeliveryBatch {
	if batch.End < batch.Start {
		return evidence
	}
	items := make([]DeliveryBatch, 0, len(evidence)+1)
	items = append(items, evidence...)
	items = append(items, batch)
	return mergeDeliveryEvidence(items)
}

func mergeDeliveryEvidence(items []DeliveryBatch) []DeliveryBatch {
	if len(items) == 0 {
		return nil
	}
	// 先按状态归并，避免较早的未发送结果把后续真实发送但结果未知的证据切掉。
	sort.Slice(items, func(i, j int) bool {
		if items[i].State != items[j].State {
			return items[i].State < items[j].State
		}
		if items[i].Start != items[j].Start {
			return items[i].Start < items[j].Start
		}
		return items[i].End < items[j].End
	})
	out := items[:0]
	for _, item := range items {
		if item.End < item.Start {
			continue
		}
		if len(out) > 0 {
			previous := &out[len(out)-1]
			if previous.State == item.State && (item.Start <= previous.End || previous.End != ^uint64(0) && item.Start == previous.End+1) {
				previous.End = max(previous.End, item.End)
				continue
			}
		}
		out = append(out, item)
	}
	// 保持可持久化的确定顺序；判态优先级独立于这个排序，不能再用首个覆盖结果覆盖其它状态。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		if out[i].End != out[j].End {
			return out[i].End < out[j].End
		}
		return out[i].State < out[j].State
	})
	return out
}
