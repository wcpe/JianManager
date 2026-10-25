package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func indexLegacyAdapterFixture(t *testing.T) *persistedState {
	t.Helper()
	key := ledger.SourceKey{LogSourceID: "legacy-adapter", SourceGeneration: "g1"}
	led := ledger.New()
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	for i := 0; i < 65; i++ {
		if err := led.RecordGap(key, uint64(i*100+10), uint64(i*100+20), ledger.GapReasonDeliverError, "旧JSON离散缺口"); err != nil {
			t.Fatal(err)
		}
	}
	entry := led.Get(key)
	entry.Gaps = append(entry.Gaps, ledger.Gap{StartPos: 7000, EndPos: 7010, Resolved: true, Reason: "OLD_AUDIT", Resolution: "人工裁定", ReasonCode: ledger.GapReasonPermanentlyLost, ResolvedAtUTC: "2026-10-22T12:00:00Z", ResolvedBy: "旧操作者"})
	return &persistedState{Sources: map[string]persistedSource{key.String(): {Ledger: []ledger.Entry{*entry}, WALRefs: []acquire.WALRef{{Seq: 1, EventID: "old-reference", RecordStart: 1, RecordEnd: 5, Appended: true, Durable: true, Replay: true}}}}}
}

func TestIndexLegacyAdapterFixtureHelper(t *testing.T) {
	root := os.Getenv("JM_INDEX_ADAPTER_FIXTURE")
	if root == "" {
		return
	}
	m := &Manager{root: root, statePath: filepath.Join(root, "var", "log", "ingest.state.json")}
	legacy, err := m.readLegacyState()
	if err != nil {
		t.Fatal(err)
	}
	current, err := m.stateToIndexState(legacy)
	if err != nil {
		t.Fatal(err)
	}
	old, err := legacyMappingV1(current)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := stateindex.OpenWithBudget(m.indexPath(), stateindex.CommitBudget{MaxRows: 3, MinRows: 1, CycleMaxRows: 3})
	if err != nil {
		t.Fatal(err)
	}
	migration, err := s.BeginMigration(context.Background(), fmt.Sprintf("%x", sha256.Sum256(data)), 1, old)
	if err != nil {
		t.Fatal(err)
	}
	for {
		stats, err := s.ResumeMigration(context.Background(), migration)
		if err != nil {
			t.Fatal(err)
		}
		if os.Getenv("JM_INDEX_ADAPTER_COMPLETE") != "1" {
			break
		}
		if !stats.Incomplete {
			if err := s.FinalizeMigration(context.Background(), migration); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	raw, err := sql.Open("sqlite", m.indexPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"fold_spans", "fold_span_merges", "reason_code", "resolved_at_utc", "resolved_by", "fold_spans_unknown"} {
		if _, err := raw.Exec("ALTER TABLE gap DROP COLUMN " + column); err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(0)
}

func seedLegacyAdapter(t *testing.T, complete bool, fixtures ...*persistedState) (string, *persistedState) {
	t.Helper()
	root := t.TempDir()
	legacy := indexLegacyAdapterFixture(t)
	if len(fixtures) > 0 { legacy = fixtures[0] }
	path := filepath.Join(root, "var", "log", "ingest.state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	// 只比较旧文件真实携带的持久字段，账本进程内折叠计数不属于序列化契约。
	legacy = &persistedState{}
	if err := json.Unmarshal(data, legacy); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^TestIndexLegacyAdapterFixtureHelper$", "-test.timeout=30s")
	command.Env = append(os.Environ(), "JM_INDEX_ADAPTER_FIXTURE="+root)
	if complete {
		command.Env = append(command.Env, "JM_INDEX_ADAPTER_COMPLETE=1")
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("旧JSON/SQLite夹具失败：%s %v", output, err)
	}
	info, err := os.Stat(filepath.Join(root, "var", "log", "ingest.index.db-wal"))
	if err != nil || info.Size() == 0 {
		t.Fatalf("没有真实SQLite残留WAL：%v", err)
	}
	return root, legacy
}

func TestIndexLegacyCompletedV1WithPrunedBatchesStillUpgrades(t *testing.T) {
	key := ledger.SourceKey{LogSourceID:"old-pruned",SourceGeneration:"g1"}
	legacy := &persistedState{Sources:map[string]persistedSource{key.String():{Ledger:[]ledger.Entry{{Key:key,Positions:logtypes.Positions{Read:1024,Durable:1000,Delivery:800,Reclaim:800},DeliveryState:logtypes.DeliveryUnknown,DeliveryBatches:[]ledger.DeliveryBatch{{Start:0,End:500,State:logtypes.DeliveryRequestDone},{Start:500,End:800,State:logtypes.DeliveryUnknown}}}}}}}
	root,_ := seedLegacyAdapter(t,true,legacy)
	m := &Manager{root:root,statePath:filepath.Join(root,"var","log","ingest.state.json"),indexCommit:stateindex.CommitBudget{MaxRows:2,MinRows:1,CycleMaxRows:2}}
	raw,err := sql.Open("sqlite",m.indexPath())
	if err != nil { t.Fatal(err) }
	if _,err := raw.Exec("DELETE FROM delivery_batch"); err != nil { t.Fatal(err) }
	if err := raw.Close(); err != nil { t.Fatal(err) }
	for round := 1; round <= 3; round++ {
		if err := m.openIndex(); err != nil { t.Fatalf("第%d次重开：合法旧完成凭据的水位内裁剪导致升级拒启：%v",round,err) }
		loaded,err := m.loadStateFromIndex()
		if err != nil { t.Fatal(err) }
		facts := eventStatusFactsOfPersisted(loaded.Sources[key.String()])
		if got := classifyEventStatus(facts,600,700); got.State != EventStateSentUnacked { t.Fatalf("旧源证明的UNKNOWN未恢复：%+v",got) }
		if got := classifyEventStatus(facts,1,100); got.State != EventStateAcked { t.Fatalf("旧源证明的真实请求完成未恢复：%+v",got) }
		if err := m.index.Close(); err != nil { t.Fatal(err) }
	}
}

func TestIndexLegacyJSONBridgeManagerThreeOpens(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(map[bool]string{false: "旧未完成", true: "旧已完成"}[complete], func(t *testing.T) {
			root, legacy := seedLegacyAdapter(t, complete)
			for round := 0; round < 3; round++ {
				m := &Manager{root: root, statePath: filepath.Join(root, "var", "log", "ingest.state.json"), indexCommit: stateindex.CommitBudget{MaxRows: 8, MinRows: 1, CycleMaxRows: 8}}
				if err := m.openIndex(); err != nil {
					t.Fatalf("第%d次打开Manager索引失败：%v", round+1, err)
				}
				loaded, err := m.loadStateFromIndex()
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(comparisonState(legacy), comparisonState(loaded)) {
					t.Fatalf("第%d次重开未完整保存旧JSON缺口/审计/WAL引用：want=%#v got=%#v", round+1, legacy, loaded)
				}
				if err := m.index.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "var", "log", "ingest.state.json.migrated")); err != nil {
				t.Fatalf("未完成最终验真归档：%v", err)
			}
		})
	}
}

func TestIndexLegacyJSONBridgeRefusesChangedSourceAndRows(t *testing.T) {
	for _, complete := range []bool{false, true} {
		for _, tamperSource := range []bool{false, true} {
			root, _ := seedLegacyAdapter(t, complete)
			m := &Manager{root: root, statePath: filepath.Join(root, "var", "log", "ingest.state.json")}
			if tamperSource {
				file, err := os.OpenFile(m.statePath, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString("\n"); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				db, err := sql.Open("sqlite", m.indexPath())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("UPDATE gap SET detail='篡改已提交旧行'"); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.openIndex(); err == nil {
				t.Fatalf("旧凭据complete=%v，sourceTamper=%v仍被接受", complete, tamperSource)
			}
			if m.index != nil {
				_ = m.index.Close()
			}
			if _, err := os.Stat(m.statePath); err != nil {
				t.Fatal("拒绝后旧来源被归档或删除")
			}
		}
	}
}

func TestIndexOldFoldWithoutSourceRemainsUnknown(t *testing.T) {
	root := t.TempDir()
	m := &Manager{root: root, statePath: filepath.Join(root, "var", "log", "ingest.state.json")}
	legacy := indexLegacyAdapterFixture(t)
	rows, err := m.stateToIndexState(legacy)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = legacyMappingV1(rows)
	if err != nil {
		t.Fatal(err)
	}
	s, err := stateindex.Open(m.indexPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(rows); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.openIndex(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.index.Close() }()
	loaded, err := m.loadStateFromIndex()
	if err != nil {
		t.Fatal(err)
	}
	entry := loaded.Sources["legacy-adapter/g1"].Ledger[0]
	if !entry.Gaps[0].FoldSpansUnknown {
		t.Fatal("旧凸包缺少精确证据，必须明确未知")
	}
	led := ledger.New()
	if err := led.Restore([]ledger.Entry{entry}); err != nil {
		t.Fatal(err)
	}
	if _, err := led.AbandonGapsThrough(entry.Key, 6500, ledger.GapAbandonment{Operator: "测试", AtUTC: "2026-10-22T12:00:00Z"}); err == nil {
		t.Fatal("不能凭旧凸包放弃健康字节")
	}
}
