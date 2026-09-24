package logcoord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	workerquery "github.com/wcpe/JianManager/internal/worker/logs/query"
)

func TestFederationPageCursorBindsViewOrderAndWorkerBoundary(t *testing.T) {
	key := SortKey{EventTimeUTC: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
		LogSourceID: "node:1", SourceGeneration: "g1", RecordStart: 20, RecordEnd: 30, EventID: "e1"}
	raw := EncodePageCursor(PageCursor{ViewID: "cv-1", OrderVersion: OrderVersion, SortKey: key, PageLimit: 200})
	decoded, err := DecodePageCursor(raw)
	require.NoError(t, err)
	require.Equal(t, "cv-1", decoded.ViewID)
	require.Equal(t, key, decoded.SortKey)

	workerRaw := workerCursor(raw, "worker-view-1", 200)
	workerDecoded, err := workerquery.DecodeCursor(workerRaw)
	require.NoError(t, err)
	require.Equal(t, "worker-view-1", workerDecoded.ViewID)
	require.Equal(t, key.EventTimeUTC.Format(time.RFC3339Nano), workerDecoded.SortKey.EventTimeUTC)
	require.Equal(t, key.EventID, workerDecoded.SortKey.EventID)
	require.EqualValues(t, 200, workerDecoded.PageLimit)
}
