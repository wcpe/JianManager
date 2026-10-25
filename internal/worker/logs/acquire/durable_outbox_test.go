package acquire

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestDurableOutboxWALSnapshotOwnsMutableFields(t *testing.T) {
	for _, scenario := range []string{"append", "restore", "restore_refs", "replay_replace", "snapshot"} {
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
			case "restore":
				require.NoError(t, wal.Restore([]WALEntry{{Seq: 1, Event: event, Appended: true, Durable: true}}))
			case "restore_refs":
				require.Empty(t, wal.RestoreMixed(nil, []WALRef{{Seq: 1, EventID: event.EventID, Appended: true, Durable: true}}, func(string) (logtypes.Event, bool) { return event, true }))
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

func TestDurableOutboxPrivateIdentityEncodingReleasesWALLock(t *testing.T) {
	for _, mode := range []string{"split", "live"} {
		t.Run(mode, func(t *testing.T) {
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
				copied := func() { close(entered); <-release }
				if mode == "live" {
					ids, err := wal.committedIdentities(copied)
					done <- result{ids, err}
					return
				}
				snapshot, err := wal.SnapshotSplitCommittedPlan(nil, copied)
				if err != nil {
					done <- result{err: err}
					return
				}
				_, _, ids := snapshot.SplitCommitted()
				done <- result{ids: ids}
			}()
			<-entered
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
			require.Contains(t, got.ids, oldID)
			require.NotContains(t, got.ids, changedID, "捕获后的Fields变体不能借旧私有快照或同ID摘要")
		})
	}
}

func TestDurableOutboxEncodedSnapshotKeepsOwnerOrdinalAndOriginalBody(t *testing.T) {
	for _, scenario := range []string{"append_fields", "replace", "restore", "refs"} {
		t.Run(scenario, func(t *testing.T) {
			key, led := testKey("encode-cut", "g1"), ledger.New()
			wal := NewWAL(led, key)
			event := sixFixEvent(key, 0, 10)
			event.Fields = map[string]string{"字段": "捕获前"}
			body, err := json.Marshal(event)
			require.NoError(t, err)
			identity, err := CommitIdentityOf(event)
			require.NoError(t, err)
			require.NoError(t, wal.Append(event))
			require.NoError(t, wal.Commit())
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan *WALCommitSnapshot, 1)
			go func() {
				snapshot, err := wal.SnapshotSplitCommittedPlan(func(logtypes.Event) bool { return scenario == "refs" }, func() { close(entered); <-release })
				if err != nil {
					done <- nil
					return
				}
				done <- snapshot
			}()
			<-entered
			changed := event
			changed.Fields = map[string]string{"字段": "捕获后"}
			switch scenario {
			case "replace":
				changed.EventTimeUTC = "2026-10-22T00:00:00.1Z"
				changed.CanonicalHash = logtypes.CanonicalContentHash(changed.EventTimeUTC, changed.Level, changed.Stream, changed.Message)
				require.NoError(t, wal.AppendReplay(changed))
			case "restore":
				require.NoError(t, wal.Restore([]WALEntry{{Seq: 1, Event: changed, Durable: true, Appended: true}}))
			default:
				require.NoError(t, wal.Append(changed))
			}
			require.NoError(t, wal.Commit())
			close(release)
			snapshot := <-done
			require.NotNil(t, snapshot)
			refs, inline, identities := snapshot.SplitCommitted()
			require.Len(t, identities, 1)
			require.Contains(t, identities, identity)
			if len(inline) > 0 {
				inline[0].Event.Fields["字段"] = "返回副本不得改写编码输入"
			}
			inlines, references := 0, 0
			require.NoError(t, snapshot.VisitEncoded(wal, func(entry WALEntry, encoded []byte) {
				inlines++
				require.Equal(t, uint64(1), entry.Seq)
				require.Equal(t, "捕获前", entry.Event.Fields["字段"])
				require.Equal(t, body, encoded, "SQL字节必须来自分流前的同一完整私有正文")
			}, func(ref WALRef) {
				references++
				require.Equal(t, event.EventID, ref.EventID)
			}))
			require.Equal(t, len(inline), inlines)
			require.Equal(t, len(refs), references)
			if scenario == "refs" {
				require.Len(t, refs, 1, "引用分流不许丢掉分流前完整正文的身份")
			}
			other := NewWAL(ledger.New(), key)
			require.ErrorIs(t, snapshot.VisitEncoded(other, func(WALEntry, []byte) { t.Fatal("旧owner不得借给新WAL") }, func(WALRef) { t.Fatal("旧owner不得借给新WAL") }), ErrWALWindowChanged)
		})
	}
}

// 谓词只拿到私有副本：留存或改写map都不得反写活WAL，也不得凭别名改身份。
func TestDurableOutboxPredicatesCannotMutateLiveFields(t *testing.T) {
	for _, mode := range []string{"snapshot_window", "snapshot_split"} {
		t.Run(mode, func(t *testing.T) {
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
			switch mode {
			case "snapshot_window":
				_, _ = wal.SnapshotWindow(0, 10, 8, mutate)
			case "snapshot_split":
				wal.SnapshotSplit(mutate)
			}
			require.NotNil(t, kept)
			kept["字段"] = "谓词留存后二次改写"
			live, err := wal.CommittedIdentities()
			require.NoError(t, err)
			require.Contains(t, live, want, "谓词别名不得改写活WAL完整正文")
			require.Equal(t, "活WAL原值", wal.Snapshot()[0].Event.Fields["字段"])
		})
	}
}

func outboxAssertFreshDigestSnapshot(t *testing.T, snapshot *WALCommitSnapshot, wantSHA int) {
	t.Helper()
	require.Equal(t, len(snapshot.entries), snapshot.marshaledEvents, "完整成员数必须等于每轮实际Marshal调用数")
	expected := map[CommitIdentity]struct{}{}
	groups := map[walSeqRevision][]walCapturedMember{}
	for i, entry := range snapshot.entries {
		body, err := json.Marshal(entry.Event)
		require.NoError(t, err)
		identity := CommitIdentity{EventID: entry.Event.EventID, CanonicalHash: entry.Event.CanonicalHash, Record: entry.Event.Record, BodyDigest: sha256.Sum256(body)}
		if entry.Durable {
			expected[identity] = struct{}{}
		}
		group := walSeqRevision{entry.Seq, entry.appendRevision}
		groups[group] = append(groups[group], walCapturedMember{identity: identity, durable: entry.Durable})
		if snapshot.referenced[i] {
			require.Nil(t, snapshot.bodies[i], "引用仍不长期保留正文，但分流前必须已完整Marshal")
		} else {
			require.Equal(t, body, snapshot.bodies[i], "SQL正文只能来自本轮完整私有Event的新Marshal字节")
		}
	}
	require.Equal(t, expected, snapshot.identities, "所有复用身份必须等于新body的完整直接SHA，而非仅ID/hash/长度")
	require.Equal(t, groups, snapshot.bySeqRevision, "新index必须逐成员保留完整身份和原捕获Durable态")
	require.Equal(t, wantSHA, snapshot.hashedEvents, "只允许减少合格旧成员的SHA，不得借统计值伪装少算正文")
}

func TestDurableOutboxDigestReuseEveryEventFieldMatchesFreshSHA(t *testing.T) {
	mutations := []struct {
		name  string
		apply func(*logtypes.Event)
	}{
		{"event_id", func(ev *logtypes.Event) { ev.EventID += "新" }},
		{"source_id", func(ev *logtypes.Event) { ev.Source.LogSourceID += "新" }},
		{"source_generation", func(ev *logtypes.Event) { ev.Source.SourceGeneration += "新" }},
		{"parser", func(ev *logtypes.Event) { ev.Source.ParserVersion += "新" }},
		{"record_start", func(ev *logtypes.Event) { ev.Record.Start++ }},
		{"record_end", func(ev *logtypes.Event) { ev.Record.End++ }},
		{"event_time", func(ev *logtypes.Event) { ev.EventTimeUTC += "新" }},
		{"ingest_time", func(ev *logtypes.Event) { ev.IngestTimeUTC += "新" }},
		{"level", func(ev *logtypes.Event) { ev.Level += "新" }},
		{"stream", func(ev *logtypes.Event) { ev.Stream += "新" }},
		{"message", func(ev *logtypes.Event) { ev.Message += "新" }},
		{"canonical_hash", func(ev *logtypes.Event) { ev.CanonicalHash += "新" }},
		{"fields_value", func(ev *logtypes.Event) { ev.Fields["字段"] = "同ID同hash异Fields" }},
		{"fields_key", func(ev *logtypes.Event) { ev.Fields["新键"] = "新增完整字段" }},
		{"fields_nil", func(ev *logtypes.Event) { ev.Fields = nil }},
		{"fields_empty", func(ev *logtypes.Event) { ev.Fields = map[string]string{} }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			key := testKey("digest-each-field", "g1")
			wal := NewWAL(ledger.New(), key)
			event := sixFixEvent(key, 0, 10)
			event.Fields = map[string]string{"字段": "原值"}
			require.NoError(t, wal.Append(event))
			require.NoError(t, wal.Commit())
			prior, err := wal.SnapshotSplitCommittedPlan(nil, nil)
			require.NoError(t, err)
			outboxAssertFreshDigestSnapshot(t, prior, 1)
			changed := cloneWALEvent(event)
			mutation.apply(&changed)
			oldBody, err := json.Marshal(event)
			require.NoError(t, err)
			newBody, err := json.Marshal(changed)
			require.NoError(t, err)
			require.NotEqual(t, oldBody, newBody, "前置：必须真的改变一个完整Event字段")
			require.NoError(t, wal.Append(changed))
			require.NoError(t, wal.Commit())
			snapshot, err := wal.SnapshotSplitCommittedPlan(nil, nil, prior.WALCommitIndex)
			require.NoError(t, err)
			outboxAssertFreshDigestSnapshot(t, snapshot, 1)
			require.Len(t, snapshot.entries, 2)
		})
	}
}

func TestDurableOutboxDigestReuseVersionDurabilityAndOwnerGuards(t *testing.T) {
	cases := []struct {
		name string
		sha  int
	}{
		{"unchanged", 0}, {"no_index", 2}, {"foreign_owner", 2}, {"future_index_cut", 2},
		{"future_member_cut", 2}, {"ambiguous_fields", 2}, {"mixed_durability", 2},
		{"identical_mixed_durability", 2}, {"captured_non_durable", 2}, {"current_non_durable", 2},
		{"restore", 2}, {"restore_refs", 2}, {"replay_replace", 2}, {"prune_same_length", 1},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			key := testKey("digest-guards", "g1")
			wal := NewWAL(ledger.New(), key)
			first, second := sixFixEvent(key, 0, 10), sixFixEvent(key, 10, 20)
			first.Fields, second.Fields = map[string]string{"字段": "原值A"}, map[string]string{"字段": "原值B"}
			entries := []WALEntry{{Seq: 1, Event: first, Appended: true, Durable: true}, {Seq: 2, Event: second, Appended: true, Durable: true}}
			switch scenario.name {
			case "ambiguous_fields", "mixed_durability", "identical_mixed_durability":
				entries[1].Seq = 1
				if scenario.name == "ambiguous_fields" || scenario.name == "identical_mixed_durability" {
					entries[1].Event = cloneWALEvent(first)
				}
				if scenario.name == "ambiguous_fields" {
					entries[1].Event.Fields["字段"] = "同ID同hash异正文"
				} else {
					entries[1].Durable = false
				}
			case "captured_non_durable", "current_non_durable":
				entries[0].Durable, entries[1].Durable = false, false
			}
			require.NoError(t, wal.Restore(entries))
			prior, err := wal.SnapshotSplitCommittedPlan(nil, nil)
			require.NoError(t, err)
			seed := prior.WALCommitIndex
			switch scenario.name {
			case "no_index":
				seed = nil
			case "foreign_owner":
				wal = NewWAL(ledger.New(), key)
				entries[0].Event.Fields = map[string]string{"字段": "异owner同seq同rev异Fields"}
				require.NoError(t, wal.Restore(entries))
			case "future_index_cut":
				// 故障注入超出本次capture的索引，即使桶键相同也必须拒绝复用。
				seed.cut = wal.revision + 1
			case "future_member_cut":
				entries[0].Event.Fields = map[string]string{"字段": "超旧cut的新体"}
				require.NoError(t, wal.Restore(entries))
				// 主动伪造未来桶键，确保不能仅凭命中key省掉版本cut防线。
				for i, entry := range wal.entries {
					seed.bySeqRevision[walSeqRevision{entry.Seq, entry.appendRevision}] = seed.bySeqRevision[walSeqRevision{prior.entries[i].Seq, prior.entries[i].appendRevision}]
				}
			case "restore":
				entries[0].Event.Fields = map[string]string{"字段": "恢复后的新值"}
				require.NoError(t, wal.Restore(entries))
			case "restore_refs":
				require.Empty(t, wal.RestoreMixed(nil, []WALRef{{Seq: 1, EventID: first.EventID, Appended: true, Durable: true}, {Seq: 2, EventID: second.EventID, Appended: true, Durable: true}}, func(id string) (logtypes.Event, bool) {
					if id == first.EventID {
						first.Fields = map[string]string{"字段": "hydrate后的新值"}
						return first, true
					}
					return second, true
				}))
			case "replay_replace":
				changed := cloneWALEvent(first)
				changed.EventTimeUTC = "2026-10-22T00:00:00.1Z"
				changed.CanonicalHash = logtypes.CanonicalContentHash(changed.EventTimeUTC, changed.Level, changed.Stream, changed.Message)
				require.NoError(t, wal.AppendReplay(changed))
			case "prune_same_length":
				wal.pruneReclaimed(10)
				require.NoError(t, wal.Append(sixFixEvent(key, 20, 30)))
			}
			if scenario.name != "current_non_durable" {
				require.NoError(t, wal.Commit())
			}
			snapshot, err := wal.SnapshotSplitCommittedPlan(nil, nil, seed)
			require.NoError(t, err)
			outboxAssertFreshDigestSnapshot(t, snapshot, scenario.sha)
		})
	}
}

func TestDurableOutboxDigestReuseKeepsPrivateAliasesAndIndependentIndex(t *testing.T) {
	key := testKey("digest-private", "g1")
	wal := NewWAL(ledger.New(), key)
	event := sixFixEvent(key, 0, 10)
	event.Fields = map[string]string{"字段": "原值"}
	require.NoError(t, wal.Append(event))
	require.NoError(t, wal.Commit())
	prior, err := wal.SnapshotSplitCommittedPlan(nil, nil)
	require.NoError(t, err)
	originalGroup := walSeqRevision{prior.entries[0].Seq, prior.entries[0].appendRevision}
	originalBucket := append([]walCapturedMember(nil), prior.bySeqRevision[originalGroup]...)
	event.Fields["字段"] = "入参改写"
	_, returned, _ := prior.SplitCommitted()
	returned[0].Event.Fields["字段"] = "返回副本改写"
	var predicate map[string]string
	snapshot, err := wal.SnapshotSplitCommittedPlan(func(value logtypes.Event) bool {
		predicate = value.Fields
		value.Fields["字段"] = "谓词改写"
		return false
	}, func() {
		predicate["字段"] = "谓词留存后改写"
		changed := wal.Snapshot()
		changed[0].Event.Fields["字段"] = "捕获后真实Restore替换"
		require.NoError(t, wal.Restore(changed))
	}, prior.WALCommitIndex)
	require.NoError(t, err)
	outboxAssertFreshDigestSnapshot(t, snapshot, 0)
	require.Equal(t, "原值", snapshot.entries[0].Event.Fields["字段"], "已捕获私有新body不受任何外部map或捕获后替换污染")
	snapshot.bySeqRevision[originalGroup] = nil
	require.Equal(t, originalBucket, prior.bySeqRevision[originalGroup], "新计划不能复用旧map而改变已发布回执索引")
	require.NotSame(t, prior.WALCommitIndex, snapshot.WALCommitIndex)
}

func TestDurableOutboxReceiptIndexMatchesStrictLiveSet(t *testing.T) {
	for _, scenario := range []struct {
		name string
		want int
	}{
		{"unchanged", 0}, {"future_append", 1}, {"same_seq_bodies", 2},
		{"same_id_hash_fields", 2}, {"mixed_durability", 2}, {"identical_mixed_durability", 2},
		{"all_non_durable", 2}, {"restore_after", 2}, {"restore_refs_after", 2},
		{"replay_replace", 2}, {"prune_same_length", 1}, {"nil_index", 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			key := testKey("receipt-index", "g1")
			wal := NewWAL(ledger.New(), key)
			first, second := sixFixEvent(key, 0, 10), sixFixEvent(key, 10, 20)
			first.Fields, second.Fields = map[string]string{"字段": "原值A"}, map[string]string{"字段": "原值B"}
			entries := []WALEntry{{Seq: 1, Event: first, Appended: true, Durable: true}, {Seq: 2, Event: second, Appended: true, Durable: true}}
			switch scenario.name {
			case "same_seq_bodies", "mixed_durability", "identical_mixed_durability", "same_id_hash_fields":
				entries[1].Seq = 1
				if scenario.name == "same_id_hash_fields" {
					entries[1].Event = first
					entries[1].Event.Fields = map[string]string{"字段": "同ID同hash不同Fields"}
				}
				if scenario.name == "identical_mixed_durability" {
					entries[1].Event = first
				}
				if scenario.name == "mixed_durability" || scenario.name == "identical_mixed_durability" {
					entries[1].Durable = false
				}
			case "all_non_durable":
				entries[0].Durable, entries[1].Durable = false, false
			}
			require.NoError(t, wal.Restore(entries))
			snapshot, err := wal.SnapshotSplitCommittedPlan(nil, nil)
			require.NoError(t, err)
			index := snapshot.WALCommitIndex
			// 只留下轻量索引；原快照的正文、编码和身份集合全部不可再通过它取得。
			snapshot.entries, snapshot.bodies, snapshot.identities, snapshot.referenced = nil, nil, nil, nil
			snapshot.WALCommitIndex = nil
			switch scenario.name {
			case "future_append", "prune_same_length":
				if scenario.name == "prune_same_length" {
					wal.pruneReclaimed(10)
				}
				require.NoError(t, wal.Append(sixFixEvent(key, 20, 30)))
			case "restore_after":
				entries[0].Event.Fields = map[string]string{"字段": "Restore未来新体"}
				require.NoError(t, wal.Restore(entries))
			case "restore_refs_after":
				require.Empty(t, wal.RestoreMixed(nil, []WALRef{{Seq: 1, EventID: first.EventID, Appended: true, Durable: true}, {Seq: 2, EventID: second.EventID, Appended: true, Durable: true}}, func(id string) (logtypes.Event, bool) {
					if id == first.EventID {
						first.Fields = map[string]string{"字段": "hydrate未来新体"}
						return first, true
					}
					return second, true
				}))
			case "replay_replace":
				changed := first
				changed.EventTimeUTC = "2026-10-22T00:00:00.1Z"
				changed.CanonicalHash = logtypes.CanonicalContentHash(changed.EventTimeUTC, changed.Level, changed.Stream, changed.Message)
				changed.Fields = map[string]string{"字段": "Replay原位新体"}
				require.NoError(t, wal.AppendReplay(changed))
			case "nil_index":
				index = nil
			}
			require.NoError(t, wal.Commit())
			live, recomputed, err := index.LiveCommittedIdentities(wal)
			require.NoError(t, err)
			strict, err := wal.CommittedIdentities()
			require.NoError(t, err)
			require.Equal(t, strict, live, "轻量索引必须逐完整CommitIdentity等于旧实时全量集合")
			require.Equal(t, scenario.want, recomputed, "完整JSON编码次数必须来自实际fallback成员，而非墙钟或去重后集合大小")
		})
	}
}

func TestDurableOutboxReceiptIndexRejectsForeignOwnerAndFutureCut(t *testing.T) {
	for _, scenario := range []string{"foreign_owner", "future_cut"} {
		t.Run(scenario, func(t *testing.T) {
			key := testKey("receipt-owner-cut", "g1")
			wal := NewWAL(ledger.New(), key)
			event := sixFixEvent(key, 0, 10)
			event.Fields = map[string]string{"字段": "捕获原值"}
			require.NoError(t, wal.Append(event))
			require.NoError(t, wal.Commit())
			snapshot, err := wal.SnapshotSplitCommittedPlan(nil, nil)
			require.NoError(t, err)
			index := snapshot.WALCommitIndex
			changed := event
			changed.Fields = map[string]string{"字段": "禁止借旧体的新值"}
			if scenario == "foreign_owner" {
				other := NewWAL(ledger.New(), key)
				require.NoError(t, other.Restore([]WALEntry{{Seq: 1, Event: changed, Appended: true, Durable: true}}))
				live, _, err := index.LiveCommittedIdentities(other)
				require.ErrorIs(t, err, ErrWALWindowChanged)
				require.Nil(t, live, "相同Seq和revision的另一owner不得借旧身份")
				return
			}
			oldGroup := walSeqRevision{wal.entries[0].Seq, wal.entries[0].appendRevision}
			require.NoError(t, wal.Restore([]WALEntry{{Seq: 1, Event: changed, Appended: true, Durable: true}}))
			futureGroup := walSeqRevision{wal.entries[0].Seq, wal.entries[0].appendRevision}
			// 主动放入超cut桶验证防御边界；即使键误含未来成员，也必须完整编码当前正文而非借旧摘要。
			index.bySeqRevision[futureGroup] = index.bySeqRevision[oldGroup]
			require.Greater(t, futureGroup.revision, index.cut)
			live, recomputed, err := index.LiveCommittedIdentities(wal)
			require.NoError(t, err)
			strict, err := wal.CommittedIdentities()
			require.NoError(t, err)
			require.Equal(t, strict, live, "超cut轻量桶不能授出旧正文身份")
			require.Equal(t, 1, recomputed)
		})
	}
}

func TestDurableOutboxReceiptIndexHasNoBodyReferencesOrMutableAliases(t *testing.T) {
	key := testKey("receipt-no-body", "g1")
	wal := NewWAL(ledger.New(), key)
	event := sixFixEvent(key, 0, 10)
	event.Fields = map[string]string{"字段": "捕获原值"}
	require.NoError(t, wal.Append(event))
	require.NoError(t, wal.Commit())
	var predicateFields map[string]string
	snapshot, err := wal.SnapshotSplitCommittedPlan(func(value logtypes.Event) bool {
		predicateFields = value.Fields
		value.Fields["字段"] = "谓词改写"
		return false
	}, nil)
	require.NoError(t, err)
	index := snapshot.WALCommitIndex
	_, inline, ids := snapshot.SplitCommitted()
	event.Fields["字段"], predicateFields["字段"], inline[0].Event.Fields["字段"] = "入参改写", "谓词留存改写", "返回map改写"
	live, recomputed, err := index.LiveCommittedIdentities(wal)
	require.NoError(t, err)
	require.Zero(t, recomputed)
	strict, err := wal.CommittedIdentities()
	require.NoError(t, err)
	require.Equal(t, ids, live)
	require.Equal(t, strict, live)
	var check func(reflect.Type)
	check = func(typ reflect.Type) {
		if typ == reflect.TypeOf((*WAL)(nil)) {
			return
		}
		switch typ.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Func:
			t.Fatalf("轻量成员索引存在正文或闭包可达引用: %s", typ)
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				check(typ.Field(i).Type)
			}
		case reflect.Map:
			require.NotEqual(t, reflect.TypeOf(map[string]string{}), typ, "禁止Fields可达")
			check(typ.Key())
			check(typ.Elem())
		case reflect.Slice:
			require.NotEqual(t, reflect.Uint8, typ.Elem().Kind(), "禁止编码正文切片可达")
			check(typ.Elem())
		case reflect.Array:
			check(typ.Elem())
		}
	}
	check(reflect.TypeOf(*index))
	require.Equal(t, 3, reflect.TypeOf(*index).NumField(), "索引只可持owner/cut/轻量成员桶")
	members := 0
	for _, bucket := range index.bySeqRevision {
		members += len(bucket)
	}
	t.Logf("light_index_buckets=%d members=%d index_header_bytes=%d group_key_bytes=%d member_scalar_bytes=%d", len(index.bySeqRevision), members, reflect.TypeOf(*index).Size(), reflect.TypeOf(walSeqRevision{}).Size(), reflect.TypeOf(walCapturedMember{}).Size())
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
	for _, scenario := range []string{"restore", "restore_refs", "replace"} {
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
			case "restore":
				require.NoError(t, wal.Restore(wal.Snapshot()))
			case "restore_refs":
				refs, inline := wal.SnapshotSplit(func(logtypes.Event) bool { return true })
				byID := make(map[string]logtypes.Event, len(events))
				for _, event := range events {
					byID[event.EventID] = event
				}
				missing := wal.RestoreMixed(inline, refs, func(id string) (logtypes.Event, bool) { event, ok := byID[id]; return event, ok })
				require.Empty(t, missing)
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
				require.NoError(t, wal.Restore(entries))
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
