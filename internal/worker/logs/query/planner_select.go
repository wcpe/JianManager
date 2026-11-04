package query

import (
	"sort"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// selectKeys 按授权目标 + 时间范围筛选 Catalog 分区键。
func (p *Planner) selectKeys(req PlanRequest) []catalog.PartitionKey {
	all := p.cat.Keys()
	sort.Slice(all, func(i, j int) bool { return all[i].String() < all[j].String() })
	out := make([]catalog.PartitionKey, 0, len(all))
	for _, k := range all {
		if !matchTarget(k, req.TargetIDs) {
			continue
		}
		if !dayOverlapsRange(k.UTCDay, req.TimeRange) {
			continue
		}
		out = append(out, k)
	}
	return out
}

func matchTarget(key catalog.PartitionKey, authorized []string) bool {
	if len(authorized) == 0 {
		return true
	}
	id := key.String()
	for _, a := range authorized {
		if a == id || a == key.StorageNamespace || a == key.UTCDay {
			return true
		}
	}
	return false
}

// dayOverlapsRange 报告 utc_day（YYYY-MM-DD）是否与 [from,to) 有交集。
func dayOverlapsRange(day string, tr TimeRange) bool {
	if tr.FromUTC == "" && tr.ToUTC == "" {
		return true
	}
	dayStart, err := time.Parse(time.RFC3339, day+"T00:00:00Z")
	if err != nil {
		return true // 无法解析时不过滤，交给 Catalog 权威
	}
	dayEnd := dayStart.Add(24 * time.Hour)

	if tr.FromUTC != "" {
		from, err := time.Parse(time.RFC3339, tr.FromUTC)
		if err == nil && !dayEnd.After(from) {
			return false
		}
	}
	if tr.ToUTC != "" {
		to, err := time.Parse(time.RFC3339, tr.ToUTC)
		if err == nil && !dayStart.Before(to) {
			return false
		}
	}
	return true
}

func parseTargetKey(targetID string) (catalog.PartitionKey, bool) {
	// storage_namespace 可能含 '/'（如 ns/game-1）；utc_day 固定为最后一段。
	i := strings.LastIndex(targetID, "/")
	if i <= 0 || i == len(targetID)-1 {
		return catalog.PartitionKey{}, false
	}
	return catalog.PartitionKey{
		StorageNamespace: targetID[:i],
		UTCDay:           targetID[i+1:],
	}, true
}
