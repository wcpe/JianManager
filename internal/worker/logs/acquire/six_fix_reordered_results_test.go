package acquire

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func sixFixReorderedPersist(t *testing.T, key ledger.SourceKey, led *ledger.Ledger, wal *WAL) func() error {
	t.Helper()
	store, err := stateindex.Open(filepath.Join(t.TempDir(), "ingest.index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return func() error {
		state, captureErr := sixFixQualificationState(key, led, wal)
		if captureErr != nil {
			return captureErr
		}
		return sixFixQualificationPersist(store, state)
	}
}

func sixFixReorderedEvidence(t *testing.T, led *ledger.Ledger, key ledger.SourceKey, state logtypes.DeliveryState, want []ledger.PositionRange) {
	t.Helper()
	var got []ledger.PositionRange
	for _, batch := range ledger.DeliveryEvidenceOf(led.Get(key)) {
		if batch.State == state {
			got = append(got, ledger.PositionRange{From: batch.Start, To: batch.End})
		}
	}
	require.Equal(t, want, got, "请求结果只承接真实外发区间并集，不能按首尾推测凸包")
	if state == logtypes.DeliveryRequestDone {
		require.Equal(t, want, ledger.RequestDoneRangesOf(led.Get(key)))
	} else {
		require.Empty(t, ledger.RequestDoneRangesOf(led.Get(key)), "未知请求结果不能伪造ACK")
	}
}

func TestSixFixReorderedResultsSurviveDurableReopen(t *testing.T) {
	for _, shape := range []string{"旧尾在前", "旧尾在中", "旧尾在末", "完整逆序"} {
		for _, result := range []string{"成功ACK", "ACK丢失", "请求拒绝"} {
			t.Run(shape+"/"+result, func(t *testing.T) {
				key, led := testKey("six-reordered-results", "g1"), ledger.New()
				build := func(start, end uint64) logtypes.Event {
					return sixFixQualificationEvent(key, start, end, "2026-10-22T00:00:00Z")
				}
				low, middle, tail := build(0, 7), build(28, 35), build(49, 56)
				wantEvents := []logtypes.Event{tail, low, middle}
				switch shape {
				case "旧尾在中":
					wantEvents = []logtypes.Event{low, tail, middle}
				case "旧尾在末":
					wantEvents = []logtypes.Event{low, middle, tail}
				case "完整逆序":
					wantEvents = []logtypes.Event{tail, middle, low}
				}
				wal := NewWAL(led, key)
				require.NoError(t, wal.Append(wantEvents...))
				require.NoError(t, wal.Commit())
				path := filepath.Join(t.TempDir(), "ingest.index.db")
				store, err := stateindex.Open(path)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, store.Close()) })
				state, err := sixFixQualificationState(key, led, wal)
				require.NoError(t, err)
				require.NoError(t, sixFixQualificationPersist(store, state))
				for round := 2; round <= 3; round++ {
					require.NoError(t, store.Close())
					store, err = stateindex.Open(path)
					require.NoError(t, err)
					led, wal = sixFixQualificationRestore(t, key, store)
					for i, entry := range wal.Snapshot() {
						require.Equal(t, uint64(i+1), entry.Seq)
						require.Equal(t, wantEvents[i], entry.Event)
						require.True(t, entry.Durable)
					}
				}
				pipe := NewPipeline(led, key, wal)
				persists, deliveries := 0, 0
				pipe.SetDurablePersist(func() error {
					persists++
					captured, captureErr := sixFixQualificationState(key, led, wal)
					if captureErr != nil {
						return captureErr
					}
					return sixFixQualificationPersist(store, captured)
				})
				status, ackLost := 200, false
				evidenceState := logtypes.DeliveryRequestDone
				switch result {
				case "ACK丢失":
					ackLost, evidenceState = true, logtypes.DeliveryUnknown
				case "请求拒绝":
					status, evidenceState = 503, logtypes.DeliveryUnknown
				}
				pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
					deliveries++
					require.Equal(t, 1, persists)
					require.Equal(t, wantEvents, events, "不能通过重排事件掩盖非同向WAL状态")
					return status, ackLost, nil
				})
				got, err := pipe.DeliverPending()
				require.NoError(t, err, "真实送达之后结果登记不得因首尾逆序生成非法区间")
				require.Equal(t, wantEvents, got)
				require.Equal(t, 1, deliveries)
				wantRanges := []ledger.PositionRange{{From: 0, To: 7}, {From: 28, To: 35}, {From: 49, To: 56}}
				sixFixReorderedEvidence(t, led, key, evidenceState, wantRanges)
				require.Equal(t, uint64(7), led.Get(key).Positions.Delivery, "不能越过两段从未投递的中间空洞")
				captured, err := sixFixQualificationState(key, led, wal)
				require.NoError(t, err)
				require.NoError(t, sixFixQualificationPersist(store, captured))
				require.NoError(t, store.Close())
				store, err = stateindex.Open(path)
				require.NoError(t, err)
				led, _ = sixFixQualificationRestore(t, key, store)
				sixFixReorderedEvidence(t, led, key, evidenceState, wantRanges)
			})
		}
	}
}

func TestSixFixReorderedResultsDoNotCoverCappedOrNonDurableTail(t *testing.T) {
	for _, kind := range []string{"单轮上限", "未耐久尾部"} {
		t.Run(kind, func(t *testing.T) {
			key, led := testKey("six-reordered-cap", "g1"), ledger.New()
			build := func(start, end uint64) logtypes.Event {
				return sixFixQualificationEvent(key, start, end, "2026-10-22T00:00:00Z")
			}
			low, hole, tail := build(0, 7), build(7, 28), build(28, 35)
			wal := NewWAL(led, key)
			require.NoError(t, wal.Append(low, tail))
			if kind == "单轮上限" {
				require.NoError(t, wal.Append(hole))
			}
			require.NoError(t, wal.Commit())
			if kind == "未耐久尾部" {
				require.NoError(t, wal.Append(hole))
			}
			pipe := NewPipeline(led, key, wal)
			pipe.SetMaxReplayEventsPerDrain(2)
			pipe.SetDurablePersist(sixFixReorderedPersist(t, key, led, wal))
			pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
				require.Equal(t, []logtypes.Event{low, tail}, events)
				return 200, false, nil
			})
			got, err := pipe.DeliverPending()
			require.NoError(t, err)
			require.Len(t, got, 2)
			sixFixReorderedEvidence(t, led, key, logtypes.DeliveryRequestDone, []ledger.PositionRange{{From: 0, To: 7}, {From: 28, To: 35}})
			require.Equal(t, uint64(7), led.Get(key).Positions.Delivery)
			require.Len(t, wal.Snapshot(), 3, "未外发区间仍须留在WAL保有恢复责任")
		})
	}
}

func TestSixFixReorderedResultsMergeOnlyActualContinuousRecords(t *testing.T) {
	for _, ingest := range []bool{false, true} {
		t.Run(map[bool]string{false: "恢复排空", true: "正常摄取"}[ingest], func(t *testing.T) {
			key, led := testKey("six-reordered-continuous", "g1"), ledger.New()
			build := func(start, end uint64) logtypes.Event {
				return sixFixQualificationEvent(key, start, end, "2026-10-22T00:00:00Z")
			}
			want := []logtypes.Event{build(21, 28), build(0, 7), build(7, 14), build(14, 21)}
			wal := NewWAL(led, key)
			pipe := NewPipeline(led, key, wal)
			pipe.SetDurablePersist(sixFixReorderedPersist(t, key, led, wal))
			pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
				require.Equal(t, want, events)
				return 200, false, nil
			})
			if ingest {
				require.NoError(t, pipe.Ingest(want))
			} else {
				require.NoError(t, wal.Append(want...))
				require.NoError(t, wal.Commit())
				got, err := pipe.DeliverPending()
				require.NoError(t, err)
				require.Equal(t, want, got)
			}
			sixFixReorderedEvidence(t, led, key, logtypes.DeliveryRequestDone, []ledger.PositionRange{{From: 0, To: 28}})
			require.Equal(t, uint64(28), led.Get(key).Positions.Delivery, "真实连续并集应推进，不因插入顺序保守失证")
		})
	}
}

func TestSixFixReorderedResultsPersistOrDeliverErrorsDoNotPublishEvidence(t *testing.T) {
	for _, failure := range []string{"提交失败", "投递失败"} {
		t.Run(failure, func(t *testing.T) {
			key, led := testKey("six-reordered-failure", "g1"), ledger.New()
			wal := NewWAL(led, key)
			events := []logtypes.Event{
				sixFixQualificationEvent(key, 28, 35, "2026-10-22T00:00:00Z"),
				sixFixQualificationEvent(key, 0, 7, "2026-10-22T00:00:00Z"),
			}
			require.NoError(t, wal.Append(events...))
			require.NoError(t, wal.Commit())
			pipe := NewPipeline(led, key, wal)
			pipe.SetDurablePersist(sixFixReorderedPersist(t, key, led, wal))
			if failure == "提交失败" {
				pipe.SetDurablePersist(func() error { return errors.New("受控提交失败") })
			}
			deliveries := 0
			pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
				deliveries++
				return 0, false, errors.New("受控投递失败")
			})
			_, err := pipe.DeliverPending()
			require.Error(t, err)
			require.Empty(t, ledger.RequestDoneRangesOf(led.Get(key)))
			require.Empty(t, ledger.DeliveryEvidenceOf(led.Get(key)))
			if failure == "提交失败" {
				require.Zero(t, deliveries)
			} else {
				require.Equal(t, 1, deliveries)
			}
		})
	}
}
