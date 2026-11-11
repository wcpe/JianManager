package logcoord

import (
	"errors"
	"fmt"
	"time"
)

// ErrExplorationInvalid is a malformed explicit document-exploration request.
var ErrExplorationInvalid = errors.New("logcoord: invalid field exploration")

const maxExplorationFields = 256

// These are request-wide caps: Worker shares include metadata and value reads.
// Keep the same ceilings at the Worker boundary, which may be called directly.
func restrictExplorationBudget(b QueryBudget) QueryBudget {
	if b.MaxBytes == 0 || b.MaxBytes > 1<<20 {
		b.MaxBytes = 1 << 20
	}
	if b.TimeoutMS == 0 || b.TimeoutMS > 5000 {
		b.TimeoutMS = 5000
	}
	if b.MaxFanout == 0 || b.MaxFanout > 16 {
		b.MaxFanout = 16
	}
	return b
}

func validateExplorationTime(from, to string) error {
	start, e1 := time.Parse(time.RFC3339Nano, from)
	end, e2 := time.Parse(time.RFC3339Nano, to)
	if e1 != nil || e2 != nil || start.IsZero() || end.IsZero() || !start.Before(end) {
		return fmt.Errorf("%w: a finite absolute from/to time range is required", ErrExplorationInvalid)
	}
	return nil
}

func metadataWorkerBudgets(order []string, budget QueryBudget, explore bool) map[string]QueryBudget {
	out := reserveWorkerBudgets(order, budget)
	if !explore {
		return out
	}
	// A Worker may split one range into several physical requests. Divide the
	// total physical fanout too, rather than granting every Worker the total.
	remaining, count := budget.MaxFanout, len(out)
	for _, id := range order {
		local, ok := out[id]
		if !ok {
			continue
		}
		share := remaining / uint32(count)
		if remaining%uint32(count) > 0 {
			share++
		}
		remaining -= share
		count--
		local.MaxFanout = share
		out[id] = local
	}
	return out
}

func unsupportedExplorationCoverage(cov *Coverage) *Coverage {
	if cov == nil {
		cov = &Coverage{}
	}
	cov.Complete = false
	cov.EnumerationState = EnumOpen
	cov.PartialReasons = appendUniqueReason(cov.PartialReasons, "LOG_UNSUPPORTED")
	return cov
}
