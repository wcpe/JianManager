package acquire

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// CapacityBudget Worker 日志资源预算切片（数值以 FR-473 契约为准，此处为可注入上限）。
type CapacityBudget struct {
	// MaxWALBytes WAL 字节上限；0 表示不限制（测试默认）。
	MaxWALBytes uint64
	// MaxGaps 未解决缺口数量软上限。
	MaxGaps int
	// DiskUsagePercent 当前磁盘使用率；契约：80% 降级、90% 暂停不可恢复写入。
	DiskUsagePercent float64
	// DegradedAtPercent 默认 80。
	DegradedAtPercent float64
	// PauseAtPercent 默认 90。
	PauseAtPercent float64
}

// DefaultCapacityBudget 返回契约默认阈值（80/90）。
func DefaultCapacityBudget() CapacityBudget {
	return CapacityBudget{
		DegradedAtPercent: 80,
		PauseAtPercent:    90,
	}
}

// CapacityAction 容量决策结果。
type CapacityAction string

const (
	CapacityOK       CapacityAction = "OK"
	CapacityDegraded CapacityAction = "DEGRADED_STORAGE"
	CapacityPaused   CapacityAction = "PAUSED"
)

// CapacityDecision 门禁输出：禁止静默丢弃。
type CapacityDecision struct {
	Action CapacityAction
	Reason string
	// MustRecordGap 为 true 时调用方必须写账本缺口。
	MustRecordGap bool
	// GapStart/GapEnd 拟记录的缺口范围。
	GapStart uint64
	GapEnd   uint64
}

// EvaluateCapacity 评估是否允许继续采集/写入。
// 达到暂停阈值时返回 PAUSED，调用方必须 PauseAcquire + RecordGap。
func EvaluateCapacity(budget CapacityBudget, walBytes uint64, gapCount int) CapacityDecision {
	deg := budget.DegradedAtPercent
	if deg == 0 {
		deg = 80
	}
	pause := budget.PauseAtPercent
	if pause == 0 {
		pause = 90
	}
	if budget.DiskUsagePercent >= pause {
		return CapacityDecision{
			Action:        CapacityPaused,
			Reason:        fmt.Sprintf("disk usage %.1f%% >= %.1f%%; pause irreversible writes", budget.DiskUsagePercent, pause),
			MustRecordGap: true,
		}
	}
	if budget.MaxWALBytes > 0 && walBytes >= budget.MaxWALBytes {
		return CapacityDecision{
			Action:        CapacityPaused,
			Reason:        fmt.Sprintf("WAL budget exhausted (%d >= %d)", walBytes, budget.MaxWALBytes),
			MustRecordGap: true,
		}
	}
	if budget.MaxGaps > 0 && gapCount >= budget.MaxGaps {
		return CapacityDecision{
			Action:        CapacityDegraded,
			Reason:        fmt.Sprintf("unresolved gaps %d >= %d", gapCount, budget.MaxGaps),
			MustRecordGap: true,
		}
	}
	if budget.DiskUsagePercent >= deg {
		return CapacityDecision{
			Action: CapacityDegraded,
			Reason: fmt.Sprintf("disk usage %.1f%% >= %.1f%%; degraded", budget.DiskUsagePercent, deg),
		}
	}
	if budget.MaxWALBytes > 0 && walBytes*100 >= budget.MaxWALBytes*80 {
		return CapacityDecision{
			Action: CapacityDegraded,
			Reason: fmt.Sprintf("WAL at %.0f%% of budget", float64(walBytes)*100/float64(budget.MaxWALBytes)),
		}
	}
	return CapacityDecision{Action: CapacityOK}
}

// ApplyCapacity 将决策落到账本：暂停 + 必记缺口。禁止静默丢弃。
func ApplyCapacity(led *ledger.Ledger, key ledger.SourceKey, dec CapacityDecision, gapStart, gapEnd uint64) error {
	if dec.Action == CapacityOK {
		return nil
	}
	if dec.Action == CapacityPaused {
		if err := led.PauseAcquire(key, dec.Reason); err != nil {
			return err
		}
	}
	if dec.MustRecordGap {
		start, end := gapStart, gapEnd
		if end < start {
			end = start
		}
		return led.RecordGap(key, start, end, string(dec.Action), dec.Reason)
	}
	return nil
}
