package acquire

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestDurableOutboxWALSnapshotOwnsMutableFields(t *testing.T) {
	for _, scenario := range []string{"append", "seed", "replay_replace", "snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			key, led := testKey("private-fields", "g1"), ledger.New()
			wal := NewWAL(led, key)
			event := sixFixEvent(key, 0, 10)
			event.Fields = map[string]string{"私有字段": "原值"}
			switch scenario {
			case "replay_replace":
				before := event
				before.EventTimeUTC = "2026-10-22T00:00:00.1Z"
				before.CanonicalHash = logtypes.CanonicalContentHash(before.EventTimeUTC, before.Level, before.Stream, before.Message)
				require.NoError(t, wal.Append(before))
				require.NoError(t, wal.Commit())
				require.NoError(t, wal.AppendReplay(event))
			case "seed":
				require.NoError(t, wal.seedEntries([]WALEntry{{Seq: 1, Event: event, Appended: true, Durable: true}}))
			default:
				require.NoError(t, wal.Append(event))
			}
			require.NoError(t, wal.Commit())
			want, err := CommitIdentityOf(event)
			require.NoError(t, err)
			if scenario == "snapshot" {
				returned := wal.Snapshot()
				returned[0].Event.Fields["私有字段"] = "调用方不得改写活WAL"
			} else {
				event.Fields["私有字段"] = "入参不得在捕获后继续改写活WAL"
			}
			live, err := wal.CommittedIdentities()
			require.NoError(t, err)
			require.Contains(t, live, want, "锁外身份编码必须来自WAL拥有的完整私有正文，而非外部map别名")
		})
	}
}

// 谓词只拿到私有副本：留存或改写 map 都不得反写活 WAL，也不得凭别名改身份。
func TestDurableOutboxPredicatesCannotMutateLiveFields(t *testing.T) {
	key, led := testKey("predicate-alias", "g1"), ledger.New()
	wal := NewWAL(led, key)
	event := sixFixEvent(key, 0, 10)
	event.Fields = map[string]string{"字段": "活WAL原值"}
	require.NoError(t, wal.Append(event))
	require.NoError(t, wal.Commit())
	want, err := CommitIdentityOf(event)
	require.NoError(t, err)
	var kept map[string]string
	mutate := func(ev logtypes.Event) bool {
		if ev.Fields != nil {
			kept = ev.Fields
			ev.Fields["字段"] = "谓词改写"
		}
		return true
	}
	_, _ = wal.SnapshotWindow(0, 10, 8, mutate)
	require.NotNil(t, kept)
	kept["字段"] = "谓词留存后二次改写"
	live, err := wal.CommittedIdentities()
	require.NoError(t, err)
	require.Contains(t, live, want, "谓词别名不得改写活WAL完整正文")
	require.Equal(t, "活WAL原值", wal.Snapshot()[0].Event.Fields["字段"])
}

// 快照隔离（WAL.Snapshot）与锁外身份编码（committedIdentities）共同保证：
// 「锁外的完整身份」来自 WAL 自己的私有正文，而不是调用方持有的 map 别名。
//
// 2026-10-29 说明：本例原为 `...PrivateIdentityEncodingReleasesWALLock` 与
// `...EncodedSnapshotKeepsOwnerOrdinalAndOriginalBody` 两条，针对已被删除的
// WALCommitSnapshot/SplitCommitted/VisitEncoded（它们只为 source_wal 的 SQL 行编码而存在）。
// 保留下来的不变量是：编码期间不占锁 + 编码用的是私有副本。
func TestDurableOutboxPrivateIdentityEncodingOwnsBodiesAndReleasesLock(t *testing.T) {
	key, led := testKey("encode-lock", "g1"), ledger.New()
	wal := NewWAL(led, key)
	event := sixFixEvent(key, 0, 10)
	event.Fields = map[string]string{"快照": "旧正文"}
	require.NoError(t, wal.Append(event))
	require.NoError(t, wal.Commit())
	oldID, err := CommitIdentityOf(event)
	require.NoError(t, err)

	entered, release := make(chan struct{}), make(chan struct{})
	type result struct {
		ids map[CommitIdentity]struct{}
		err error
	}
	done := make(chan result, 1)
	go func() {
		ids, err := wal.committedIdentities(func() { close(entered); <-release })
		done <- result{ids, err}
	}()
	<-entered
	// 编码阶段不持活 WAL 锁：真实 Append/Commit 必须能推进。
	changed := event
	changed.Fields = map[string]string{"快照": "同ID同hash未来Fields"}
	changedID, err := CommitIdentityOf(changed)
	require.NoError(t, err)
	require.NotEqual(t, oldID, changedID)
	mutation := make(chan error, 1)
	go func() {
		if err := wal.Append(changed); err != nil {
			mutation <- err
			return
		}
		mutation <- wal.Commit()
	}()
	select {
	case err := <-mutation:
		require.NoError(t, err)
	case <-time.After(200 * time.Millisecond):
		close(release)
		<-done
		<-mutation
		t.Fatal("完整身份编码阶段仍占活WAL锁，阻断真实Append/Commit")
	}
	close(release)
	got := <-done
	require.NoError(t, got.err)
	require.Equal(t, map[CommitIdentity]struct{}{oldID: {}}, got.ids,
		"编码开始时的快照只含捕获前那一条；捕获后新增的变体不得混入")

	// 快照返回的是私有副本：调用方改写不得反写活 WAL，也不得改变后续身份。
	live, err := wal.CommittedIdentities()
	require.NoError(t, err)
	require.Contains(t, live, changedID, "活WAL内容必须仍是捕获后那份")
	snapshot := wal.Snapshot()
	require.Len(t, snapshot, 2)
	require.Equal(t, "旧正文", snapshot[0].Event.Fields["快照"])
	require.Equal(t, "同ID同hash未来Fields", snapshot[1].Event.Fields["快照"])
	snapshot[1].Event.Fields["快照"] = "调用方不得改写活WAL"
	require.Equal(t, "同ID同hash未来Fields", wal.Snapshot()[1].Event.Fields["快照"],
		"Snapshot 返回的必须是私有副本，改写它不得反写活队列")
}

func outboxAcquireFixture(t *testing.T) (*Pipeline, []logtypes.Event) {
	t.Helper()
	key, led := testKey("outbox-held", "g1"), ledger.New()
	wal := NewWAL(led, key)
	p := NewPipeline(led, key, wal)
	events := make([]logtypes.Event, 766)
	for i := range events {
		events[i] = sixFixEvent(key, uint64(i*10), uint64((i+1)*10))
	}
	require.NoError(t, led.AdvanceRead(key, 7660))
	require.NoError(t, p.AppendEvents(events))
	require.NoError(t, wal.Commit())
	require.NoError(t, wal.BindRecoverySegment("held", "raw", 0, 7660))
	require.NoError(t, led.SetRecoveryHold(key, "held", true))
	p.SetCommittedPersist(func(context.Context, []CommitIdentity) error { return nil })
	return p, events
}

func TestDurableOutboxPendingCursorRebasesAfterRestoreAndReplace(t *testing.T) {
	for _, scenario := range []string{"reseed", "replace"} {
		t.Run(scenario, func(t *testing.T) {
			key, led := testKey("cursor-rebase", "g1"), ledger.New()
			wal := NewWAL(led, key)
			events := make([]logtypes.Event, 900)
			for i := range events {
				events[i] = sixFixEvent(key, uint64(i*10), uint64((i+1)*10))
			}
			require.NoError(t, wal.Append(events...))
			require.NoError(t, wal.Commit())
			var epoch uint64
			first, cursor, _, err := wal.snapshotPending(500, 0, nil, &epoch)
			require.NoError(t, err)
			require.Len(t, first, 500)
			require.Equal(t, 500, cursor)
			want := events[0]
			switch scenario {
			case "reseed":
				require.NoError(t, wal.seedEntries(wal.Snapshot()))
			case "replace":
				old := events[0]
				want = logtypes.BuildEvent(old.Source, old.Record, "2026-10-22T12:00:00.1Z", old.IngestTimeUTC, old.Level, old.Stream, old.Message)
				require.Equal(t, old.EventID, want.EventID)
				require.NotEqual(t, old.CanonicalHash, want.CanonicalHash)
				require.NoError(t, wal.AppendReplay(want))
				require.NoError(t, wal.Commit())
			}
			next, _, _, err := wal.snapshotPending(500, cursor, nil, &epoch)
			require.NoError(t, err)
			require.Len(t, next, 500)
			gotID, err := CommitIdentityOf(next[0].Event)
			require.NoError(t, err)
			wantID, err := CommitIdentityOf(want)
			require.NoError(t, err)
			require.Equal(t, wantID, gotID, "恢复/替换使旧槽位失效，必须从当前真实首项重新取得完整身份")
		})
	}
}

func TestDurableOutboxPruneAppendSameLengthAndProbeDoNotConsumeCursorBasis(t *testing.T) {
	key, led := testKey("cursor-same-length", "g1"), ledger.New()
	wal := NewWAL(led, key)
	p := NewPipeline(led, key, wal)
	p.SetCommittedPersist(func(context.Context, []CommitIdentity) error { return nil })
	events := make([]logtypes.Event, 1400)
	for i := range events {
		events[i] = sixFixEvent(key, uint64(i*10), uint64((i+1)*10))
	}
	require.NoError(t, wal.Append(events[:900]...))
	require.NoError(t, wal.Commit())
	p.SetDeliver(func([]logtypes.Event, bool) (int, bool, error) { return 200, false, nil })
	first, err := p.DeliverPending()
	require.NoError(t, err)
	require.Len(t, first, 500)
	require.NoError(t, wal.BindRecoverySegment("prefix", "raw", 0, 5000))
	require.NoError(t, led.TransitionRecovery(key, "prefix", logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, led.TransitionRecovery(key, "prefix", logtypes.RecoveryWALResponsibilityXfer, "", "receiver"))
	_, err = wal.TryReclaim()
	require.NoError(t, err)
	require.Len(t, wal.Snapshot(), 400)
	require.NoError(t, wal.Append(events[900:]...))
	require.NoError(t, wal.Commit())
	require.Len(t, wal.Snapshot(), 900, "删500再加500，不能靠len变化猜索引是否有效")
	pending, err := p.HasPendingDelivery(context.Background())
	require.NoError(t, err)
	require.True(t, pending)
	second, err := p.DeliverPending()
	require.NoError(t, err)
	require.Len(t, second, 500)
	require.Equal(t, events[500].EventID, second[0].EventID, "只读探测不能消费重定位版本，再次跳过仍未投递的旧400")
	third, err := p.DeliverPending()
	require.NoError(t, err)
	require.Len(t, third, 400)
	require.Equal(t, events[1000].EventID, third[0].EventID)
}

func TestDurableOutboxQualificationFailureDoesNotAdvancePendingCursor(t *testing.T) {
	p, all := outboxAcquireFixture(t)
	attempts := 0
	p.SetCommittedPersist(func(context.Context, []CommitIdentity) error {
		attempts++
		if attempts == 1 {
			return errors.New("真实提交资格尚未取得")
		}
		return nil
	})
	calls := 0
	p.SetDeliver(func([]logtypes.Event, bool) (int, bool, error) { calls++; return 200, false, nil })
	_, err := p.DeliverPending()
	require.Error(t, err)
	require.Zero(t, calls)
	first, err := p.DeliverPending()
	require.NoError(t, err)
	require.Len(t, first, 500)
	require.Equal(t, all[0].EventID, first[0].EventID, "未获资格的旧批次不得在零外发时丢掉队列进度")
}

func TestDurableOutboxHeldACKHeadDoesNotStarveTail(t *testing.T) {
	p, all := outboxAcquireFixture(t)
	seen := make(map[string]int)
	p.SetDeliver(func(events []logtypes.Event, _ bool) (int, bool, error) {
		for _, event := range events {
			seen[event.EventID]++
		}
		return 200, false, nil
	})
	first, err := p.DeliverPending()
	require.NoError(t, err)
	require.Len(t, first, 500)
	require.Equal(t, uint64(0), p.led.Get(p.key).Positions.Reclaim)
	second, err := p.DeliverPending()
	require.NoError(t, err)
	require.Len(t, second, 266, "持续hold的已ACK前500不得挡住静默尾266")
	require.Equal(t, all[500].EventID, second[0].EventID)
	for _, event := range all {
		require.Equal(t, 1, seen[event.EventID])
	}
	pending, err := p.HasPendingDelivery(context.Background())
	require.NoError(t, err)
	require.False(t, pending)
	require.Len(t, p.wal.Snapshot(), 766, "投递完成不等于允许释放hold责任")
	require.True(t, p.led.Get(p.key).RecoveryRefs[0].HasHold)
}

func TestDurableOutboxFailedHeadFairCursorDoesNotSkipHole(t *testing.T) {
	p, _ := outboxAcquireFixture(t)
	calls := 0
	p.SetDeliver(func(events []logtypes.Event, _ bool) (int, bool, error) {
		calls++
		if calls == 1 {
			return 0, false, errors.New("首批未知")
		}
		return 200, false, nil
	})
	_, err := p.DeliverPending()
	require.Error(t, err)
	tail, err := p.DeliverPending()
	require.NoError(t, err)
	require.Len(t, tail, 266)
	require.Equal(t, uint64(5000), tail[0].Record.Start)
	require.Equal(t, uint64(0), p.led.Get(p.key).Positions.Delivery, "尾部成功不能用最大值越过首批未知洞")
	for n := 0; n < 3; n++ {
		_, err = p.DeliverPending()
		require.NoError(t, err)
	}
	require.Equal(t, uint64(7660), p.led.Get(p.key).Positions.Delivery)
	require.Zero(t, p.led.Get(p.key).Positions.Reclaim)
}

func TestDurableOutboxPreexistingCommittedSiblingsAcross500RemainDeliverable(t *testing.T) {
	key, led := testKey("outbox-existing-siblings", "g1"), ledger.New()
	wal := NewWAL(led, key)
	p := NewPipeline(led, key, wal)
	p.SetCommittedPersist(func(context.Context, []CommitIdentity) error { return nil })
	events := make([]logtypes.Event, 766)
	for i := range events {
		events[i] = sixFixEvent(key, uint64(i*10), uint64((i+1)*10))
	}
	original := events[0]
	events[765] = logtypes.BuildEvent(original.Source, original.Record, "2026-10-22T12:00:00.1Z", original.IngestTimeUTC, original.Level, original.Stream, original.Message)
	require.Equal(t, original.EventID, events[765].EventID)
	require.NotEqual(t, original.CanonicalHash, events[765].CanonicalHash)
	require.NoError(t, wal.Append(events...))
	require.NoError(t, wal.Commit())
	require.NoError(t, wal.BindRecoverySegment("hold", "raw", 0, 7660))
	require.NoError(t, led.SetRecoveryHold(key, "hold", true))
	seen := make(map[CommitIdentity]int)
	p.SetDeliver(func(actual []logtypes.Event, _ bool) (int, bool, error) {
		for _, event := range actual {
			id, err := CommitIdentityOf(event)
			require.NoError(t, err)
			seen[id]++
		}
		return 200, false, nil
	})
	first, err := p.DeliverPending()
	require.NoError(t, err, "捕获前已提交兄弟不是在飞变化，首500不能被尾兄弟阻塞")
	require.Len(t, first, 500)
	second, err := p.DeliverPending()
	require.NoError(t, err)
	require.Len(t, second, 266)
	pending, err := p.HasPendingDelivery(context.Background())
	require.NoError(t, err)
	require.False(t, pending)
	for _, event := range events {
		id, err := CommitIdentityOf(event)
		require.NoError(t, err)
		require.Equal(t, 1, seen[id])
	}
	require.Len(t, wal.Snapshot(), 766, "hold仍独立阻止正文回收，不能借兄弟资格释放")
}

func TestDurableOutboxPostCaptureSiblingRejectedButUnrelatedTailAllowed(t *testing.T) {
	for _, sibling := range []bool{false, true} {
		key, led := testKey("outbox-captured-boundary", "g1"), ledger.New()
		wal := NewWAL(led, key)
		p := NewPipeline(led, key, wal)
		p.SetCommittedPersist(func(context.Context, []CommitIdentity) error { return nil })
		event := sixFixEvent(key, 0, 10)
		require.NoError(t, wal.Append(event))
		require.NoError(t, wal.Commit())
		p.SetDeliver(func([]logtypes.Event, bool) (int, bool, error) {
			later := sixFixEvent(key, 10, 20)
			if sibling {
				later = logtypes.BuildEvent(event.Source, event.Record, "2026-10-22T12:00:00.1Z", event.IngestTimeUTC, event.Level, event.Stream, event.Message)
			}
			require.NoError(t, wal.AppendReplay(later))
			require.NoError(t, wal.Commit())
			return 200, false, nil
		})
		_, err := p.DeliverPending()
		if sibling {
			require.ErrorIs(t, err, ErrWALWindowChanged)
			require.Empty(t, ledger.RequestDoneRangesOf(led.Get(key)))
		} else {
			require.NoError(t, err)
			require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(led.Get(key)), 0, 10))
			require.False(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(led.Get(key)), 10, 20))
		}
	}
}

func TestDurableOutboxFinalWindowLeaseBlocksOnlyIntersectingMutation(t *testing.T) {
	key, led := testKey("outbox-final-window", "g1"), ledger.New()
	wal := NewWAL(led, key)
	event := sixFixEvent(key, 0, 10)
	require.NoError(t, wal.Append(event))
	require.NoError(t, wal.Commit())
	release, err := wal.AcquireCurrentWindow([]logtypes.Event{event}, true, nil)
	require.NoError(t, err)
	defer func() {
		if release != nil {
			release()
		}
	}()
	variant := logtypes.BuildEvent(event.Source, event.Record, "2026-10-22T12:00:00.1Z", event.IngestTimeUTC, event.Level, event.Stream, event.Message)
	started, mutated := make(chan struct{}), make(chan error, 1)
	go func() { close(started); mutated <- wal.AppendReplay(variant) }()
	<-started
	select {
	case err := <-mutated:
		require.ErrorIs(t, err, ErrWALWindowBusy, "相交追加必须在任何条目变化前明确可重试拒绝，不能持outer采集锁等待journal")
	case <-time.After(time.Second):
		t.Fatal("相交追加不能跨journal等待")
	}
	// 恰好start==lease.to的尾部不是相交，不能被同窗等待者或Catalog IO串行化。
	require.NoError(t, wal.Append(sixFixEvent(key, 10, 20)))
	require.NoError(t, wal.Commit())
	positions, ok := led.Positions(key)
	require.True(t, ok)
	require.Equal(t, uint64(20), positions.Durable)
	release()
	release = nil
	require.NoError(t, wal.AppendReplay(variant))
	require.NoError(t, wal.Commit())
	_, err = wal.AcquireCurrentWindow([]logtypes.Event{event}, true, nil)
	require.ErrorIs(t, err, ErrWALWindowChanged)
}

func TestDurableOutboxACKBodyGateRejectsPostACKUncommittedVariant(t *testing.T) {
	key, led := testKey("outbox-ack-variant", "g1"), ledger.New()
	wal := NewWAL(led, key)
	p := NewPipeline(led, key, wal)
	p.SetCommittedPersist(func(context.Context, []CommitIdentity) error { return nil })
	event := sixFixEvent(key, 0, 10)
	require.NoError(t, wal.Append(event))
	require.NoError(t, wal.Commit())
	require.NoError(t, p.RecordDeliveryResult(context.Background(), []logtypes.Event{event}, 200, false))
	require.NoError(t, wal.BindRecoverySegment("original", "raw", 0, 10))
	require.NoError(t, led.TransitionRecovery(key, "original", logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, led.TransitionRecovery(key, "original", logtypes.RecoveryWALResponsibilityXfer, "", "receiver"))
	variant := logtypes.BuildEvent(event.Source, event.Record, "2026-10-22T12:00:00.1Z", event.IngestTimeUTC, event.Level, event.Stream, event.Message)
	require.NoError(t, wal.AppendReplay(variant))
	_, err := wal.TryReclaim()
	require.ErrorIs(t, err, ledger.ErrReclaimPrecondition, "未Commit变体也必须阻止旧proof越位")
	require.Zero(t, led.Get(key).Positions.Reclaim)
	require.NotEmpty(t, wal.Snapshot())
	require.ErrorIs(t, p.RecordDeliveryResult(context.Background(), []logtypes.Event{event}, 200, false), ErrWALWindowChanged)
	require.NoError(t, wal.Commit())
	require.ErrorIs(t, p.RecordDeliveryResult(context.Background(), []logtypes.Event{event}, 200, false), ErrWALWindowChanged, "新变体已Commit也不能由旧正文的请求ACK")
	require.NoError(t, p.RecordDeliveryResult(context.Background(), []logtypes.Event{variant}, 200, false))
	pos, err := wal.TryReclaim()
	require.NoError(t, err)
	require.Equal(t, uint64(10), pos)
	require.Empty(t, wal.Snapshot())
}

func TestDurableOutboxACKStampNeverSurvivesRestoreOrOldReclaim(t *testing.T) {
	for _, restore := range []string{"memory", "json", "old-reclaim"} {
		t.Run(restore, func(t *testing.T) {
			key, led := testKey("outbox-restart", "g1"), ledger.New()
			wal := NewWAL(led, key)
			p := NewPipeline(led, key, wal)
			p.SetCommittedPersist(func(context.Context, []CommitIdentity) error { return nil })
			event := sixFixEvent(key, 0, 10)
			require.NoError(t, wal.Append(event))
			require.NoError(t, wal.Commit())
			require.NoError(t, p.RecordDeliveryResult(context.Background(), []logtypes.Event{event}, 200, false))
			require.NoError(t, wal.BindRecoverySegment("old", "raw", 0, 10))
			require.NoError(t, led.TransitionRecovery(key, "old", logtypes.RecoveryDurableVerified, "", ""))
			require.NoError(t, led.TransitionRecovery(key, "old", logtypes.RecoveryWALResponsibilityXfer, "", "receiver"))
			entries := wal.Snapshot()
			if restore == "json" {
				encoded, err := json.Marshal(entries)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "acknowledged")
				entries = nil
				require.NoError(t, json.Unmarshal(encoded, &entries))
			}
			if restore == "old-reclaim" {
				_, err := wal.TryReclaim()
				require.NoError(t, err)
				require.NoError(t, wal.AppendReplay(event))
				require.NoError(t, wal.Commit())
			} else {
				require.NoError(t, wal.seedEntries(entries))
			}
			_, err := wal.TryReclaim()
			if restore != "old-reclaim" {
				require.ErrorIs(t, err, ledger.ErrReclaimPrecondition)
			}
			require.NotEmpty(t, wal.Snapshot(), "旧ACK范围/旧reclaim不构成当前正文stamp，不能误删或释放预留")
			current := NewPipeline(led, key, wal)
			current.SetCommittedPersist(func(context.Context, []CommitIdentity) error { return nil })
			current.SetDeliver(func([]logtypes.Event, bool) (int, bool, error) { return 200, false, nil })
			pending, err := current.DeliverPending()
			require.NoError(t, err)
			require.NotEmpty(t, pending)
			_, err = wal.TryReclaim()
			require.NoError(t, err)
			require.Empty(t, wal.Snapshot(), "真实重发取得新stamp后必须可回收，不能永卡旧水位")
		})
	}
}

func TestDurableOutboxManualEvidenceExactClosedUnionAndThroughLimit(t *testing.T) {
	proof := func(from, to uint64) ledger.GapAbandonment {
		return ledger.GapAbandonment{From: from, To: to, ReasonCode: ledger.GapReasonPermanentlyLost, Operator: "ops", Reason: "真实人工终局"}
	}
	require.True(t, walRecordAbandoned(logtypes.RecordRange{Start: 10, End: 20}, []ledger.GapAbandonment{proof(10, 19)}))
	require.True(t, walRecordAbandoned(logtypes.RecordRange{Start: 10, End: 20}, []ledger.GapAbandonment{proof(10, 14), proof(15, 19)}))
	require.False(t, walRecordAbandoned(logtypes.RecordRange{Start: 10, End: 20}, []ledger.GapAbandonment{proof(10, 13), proof(15, 19)}), "不能用凸包填健康洞")
	require.True(t, walRecordAbandoned(logtypes.RecordRange{Start: ^uint64(0) - 1, End: ^uint64(0)}, []ledger.GapAbandonment{proof(^uint64(0)-1, ^uint64(0))}))
	p, events := outboxAcquireFixture(t)
	require.NoError(t, p.led.SetRecoveryHold(p.key, "held", false))
	require.NoError(t, p.led.TransitionRecovery(p.key, "held", logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, p.led.TransitionRecovery(p.key, "held", logtypes.RecoveryWALResponsibilityXfer, "", "receiver"))
	require.NoError(t, p.RecordDeliveryResult(context.Background(), events, 200, false))
	_, err := p.wal.TryReclaimThrough(5000)
	require.ErrorIs(t, err, ledger.ErrReclaimPrecondition, "actual target7660不得越当前发布proof5000")
	require.Zero(t, p.led.Get(p.key).Positions.Reclaim)
	require.Len(t, p.wal.Snapshot(), 766)
	_, err = p.wal.TryReclaimThrough(7660)
	require.NoError(t, err)
	require.Empty(t, p.wal.Snapshot())
	for _, complete := range []bool{false, true} {
		key, led := testKey("outbox-manual-real", "g1"), ledger.New()
		wal := NewWAL(led, key)
		current := NewPipeline(led, key, wal)
		current.SetCommittedPersist(func(context.Context, []CommitIdentity) error { return nil })
		require.NoError(t, wal.Append(sixFixEvent(key, 0, 10), sixFixEvent(key, 10, 20)))
		require.NoError(t, wal.Commit())
		from := uint64(10)
		if complete {
			from = 0
		}
		require.NoError(t, led.RecordGap(key, from, 19, ledger.GapReasonWALBodyMissing, "人工审计正文损毁"))
		_, err := led.AbandonGapsThrough(key, 20, proof(from, 19))
		require.NoError(t, err)
		require.NoError(t, wal.BindRecoverySegment("manual", "raw", 0, 20))
		pos, err := wal.TryReclaim()
		if complete {
			require.NoError(t, err)
			require.Equal(t, uint64(20), pos)
			require.Empty(t, wal.Snapshot())
		} else {
			require.ErrorIs(t, err, ledger.ErrReclaimPrecondition)
			require.Zero(t, pos)
			require.Len(t, wal.Snapshot(), 2)
		}
		require.Empty(t, ledger.RequestDoneRangesOf(led.Get(key)), "人工终局是独立审计出口，绝不能伪造ACK stamp")
	}
}

func TestDurableOutboxQualificationBindsFullBodyAndOwner(t *testing.T) {
	p, all := outboxAcquireFixture(t)
	changed := all[0]
	changed.Fields = map[string]string{"changed": "not-in-committed-body"}
	_, err := p.CommitAndQualify(context.Background(), []logtypes.Event{changed})
	require.ErrorContains(t, err, "without a matching WAL commit")
	qualified, err := p.CommitAndQualify(context.Background(), all[:1])
	require.NoError(t, err)
	other := NewPipeline(p.led, p.key, p.wal)
	require.ErrorContains(t, other.DeliverQualified(context.Background(), qualified), "invalid or consumed")
	require.NoError(t, p.DeliverQualified(context.Background(), qualified))
	require.ErrorContains(t, p.DeliverQualified(context.Background(), qualified), "invalid or consumed")
}

func TestDurableOutboxAppendReservationRejectsLeaseRaceWithoutStateMutation(t *testing.T) {
	key, led := testKey("archive-reservation", "g1"), ledger.New()
	wal := NewWAL(led, key)
	event := sixFixEvent(key, 0, 10)
	tail := sixFixEvent(key, 10, 20)
	reservation, err := wal.ReserveAppend([]logtypes.Event{event})
	require.NoError(t, err)
	defer reservation.Release()
	require.Empty(t, wal.Snapshot())
	release, err := wal.AcquireCapturedWindow([]logtypes.Event{event}, false, nil, true, nil)
	require.ErrorIs(t, err, ErrWALWindowBusy, "采样后的prefix lease不能抢到已预留窗")
	require.Nil(t, release)
	require.ErrorIs(t, wal.Append(event), ErrWALWindowBusy)
	require.ErrorIs(t, wal.AppendReplay(event), ErrWALWindowBusy)
	require.Empty(t, wal.Snapshot(), "无凭据的同窗追加/重放应条目前零变更")
	require.NoError(t, wal.Append(tail), "start等于预留窗end的新尾不受阻")
	require.NoError(t, wal.AppendReserved(reservation, event))
	require.Len(t, wal.Snapshot(), 2)
	reservation.Release()
	reservation.Release()
	require.ErrorIs(t, wal.AppendReserved(reservation, event), ErrWALWindowBusy, "已释放凭据不可复用")
	require.Len(t, wal.Snapshot(), 2)
}

func TestDurableOutboxPublicationWindowRegistrationIsAtomicWithReservation(t *testing.T) {
	key, led := testKey("registered-window-race", "g1"), ledger.New()
	wal := NewWAL(led, key)
	event := sixFixEvent(key, 0, 10)
	registered, proceed := make(chan struct{}), make(chan struct{})
	wal.windowRegistered = func() { close(registered); <-proceed }
	type leaseResult struct {
		release func()
		err     error
	}
	done := make(chan leaseResult, 1)
	go func() {
		release, err := wal.AcquireCapturedWindow([]logtypes.Event{event}, false, nil, true, nil)
		done <- leaseResult{release, err}
	}()
	defer func() {
		close(proceed)
		result := <-done
		if result.release != nil {
			result.release()
		}
	}()
	select {
	case <-registered:
	case <-time.After(time.Second):
		t.Fatal("真实发布窗未达到注册观测点")
	}
	reservation, err := wal.ReserveAppend([]logtypes.Event{event})
	if reservation != nil {
		defer reservation.Release()
	}
	require.ErrorIs(t, err, ErrWALWindowBusy, "已登记窗必须已持读锁，不能让mutation预留穿过注册与RLock间隙")
	require.Nil(t, reservation)
	require.Empty(t, wal.Snapshot())
}

func TestDurableOutboxSubsetCaptureRetainsOriginalCutAndFullBodies(t *testing.T) {
	key, led := testKey("mixed-capture", "g1"), ledger.New()
	wal := NewWAL(led, key)
	x, y := sixFixEvent(key, 0, 10), sixFixEvent(key, 10, 20)
	require.NoError(t, wal.Append(y))
	require.NoError(t, wal.Commit())
	active, missing, ids, capture, err := wal.CaptureDeliverySubset([]logtypes.Event{x, y}, nil)
	require.NoError(t, err)
	require.Equal(t, []logtypes.Event{y}, active)
	require.Equal(t, []logtypes.Event{x}, missing)
	require.Len(t, ids, 1)
	variant := logtypes.BuildEvent(y.Source, y.Record, y.EventTimeUTC, y.IngestTimeUTC, y.Level, y.Stream, y.Message+"changed")
	require.NoError(t, wal.Append(variant))
	require.NoError(t, wal.Commit())
	_, _, _, _, err = wal.CaptureDeliverySubset([]logtypes.Event{variant}, capture)
	require.ErrorIs(t, err, ErrWALWindowChanged, "不能用当前WAL快照重造原cut")
	_, err = wal.MarkDeliveryMembers([]logtypes.Event{variant}, capture)
	require.ErrorIs(t, err, ErrWALWindowChanged)
}
