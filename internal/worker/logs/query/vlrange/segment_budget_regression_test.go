package vlrange

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

func TestPR61MergedSegmentsShareRemainingBytes(t *testing.T) {
	newer := `{"_time":"2026-10-08T10:00:00Z","event_id":"new","log_source_id":"src-a","source_generation":"g1","record_start":"1","record_end":"2"}` + "\n"
	older := `{"_time":"2026-09-01T10:00:00Z","event_id":"old","log_source_id":"src-b","source_generation":"g1","record_start":"1","record_end":"2"}` + "\n"
	ranges := []query.AuthoritativeRange{
		{Key: catalog.PartitionKey{StorageNamespace: "src-a", UTCDay: "2026-10-08"}, Owner: catalog.OwnerHot},
		{Key: catalog.PartitionKey{StorageNamespace: "src-b", UTCDay: "2026-09-01"}, Owner: catalog.OwnerHot},
	}
	for _, remaining := range []uint64{0, 20} {
		t.Run(fmt.Sprint(remaining), func(t *testing.T) {
			var actual atomic.Uint64
			var calls atomic.Int32
			client := pr61ByteClient(t, func(*http.Request) (*http.Response, error) {
				text := newer
				if calls.Add(1) > 1 {
					text = older
				}
				return &http.Response{StatusCode: 200, Body: &pr61Body{Reader: strings.NewReader(text), read: &actual}}, nil
			})
			budget := uint64(len(newer)) + remaining
			res, err := client.SearchMerged(context.Background(), ranges, query.RangeQuery{Budget: query.Budget{Limit: 100, MaxBytes: budget}})
			require.NoError(t, err)
			require.Equal(t, budget, actual.Load())
			require.Equal(t, actual.Load(), res.Bytes)
			require.Contains(t, res.CoverageReasons, query.ReasonBudgetExceeded)
			require.Equal(t, query.StatsPartial, res.StatsQuality)
			require.False(t, res.Exhausted)
			if remaining == 0 {
				require.Equal(t, int32(1), calls.Load(), "零剩余不发下一段，不得变默认32MiB")
			} else {
				require.Equal(t, int32(2), calls.Load())
				require.Equal(t, []string{"new"}, eventIDs(res.Items), "保留已完整新段，未知老段不能进入结果")
			}
		})
	}
}
