package router

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
)

// A live subscription has a session budget, not a historical page size. Keep
// explicit client limits and earlier authorization/query caps when they tighten it.
func liveRequestBudget(c *gin.Context, budget logcoord.QueryBudget, duration time.Duration) (logcoord.QueryBudget, time.Duration, error) {
	limit, err := boundedLogQueryParam(c, "limit", logcoord.LiveSessionRows)
	if err != nil {
		return budget, duration, err
	}
	maxBytes, err := boundedLogQueryParam(c, "maxBytes", logcoord.LiveSessionBytes)
	if err != nil {
		return budget, duration, err
	}
	timeout, err := boundedLogQueryParam(c, "timeoutMs", uint64(duration/time.Millisecond))
	if err != nil {
		return budget, duration, err
	}
	fanout, err := boundedLogQueryParam(c, "maxFanout", logcoord.LiveWorkerLimit)
	if err != nil {
		return budget, duration, err
	}
	budget.Limit = int(limit)
	if budget.MaxBytes > 0 && budget.MaxBytes < maxBytes {
		maxBytes = budget.MaxBytes
	}
	if budget.TimeoutMS > 0 && uint64(budget.TimeoutMS) < timeout {
		timeout = uint64(budget.TimeoutMS)
	}
	if budget.MaxFanout > 0 && uint64(budget.MaxFanout) < fanout {
		fanout = uint64(budget.MaxFanout)
	}
	budget.MaxBytes = maxBytes
	budget.TimeoutMS = uint32(timeout)
	budget.MaxFanout = uint32(fanout)
	return budget, time.Duration(timeout) * time.Millisecond, nil
}
