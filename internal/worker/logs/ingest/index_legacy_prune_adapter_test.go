package ingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestIndexLegacyPruneBridgeUsesOriginalMinimumWatermark(t *testing.T) {
	for _, deleteOutside := range []bool{false, true} {
		t.Run(map[bool]string{false: "来源双水位内", true: "越过原始Delivery"}[deleteOutside], func(t *testing.T) {
			key := ledger.SourceKey{LogSourceID: "minimum-prune", SourceGeneration: "g1"}
			legacy := &persistedState{Sources: map[string]persistedSource{key.String(): {Ledger: []ledger.Entry{{
				Key: key, Positions: logtypes.Positions{Read: 1024, Durable: 1000, Delivery: 500, Reclaim: 800}, DeliveryState: logtypes.DeliveryUnknown,
				DeliveryBatches: []ledger.DeliveryBatch{{Start: 0, End: 400, State: logtypes.DeliveryRequestDone}, {Start: 400, End: 500, State: logtypes.DeliveryUnknown}, {Start: 500, End: 800, State: logtypes.DeliveryUnknown}},
			}}}}}
			root, _ := seedLegacyAdapter(t, true, legacy)
			m := &Manager{root: root, statePath: filepath.Join(root, "var", "log", "ingest.state.json"), indexCommit: stateindex.CommitBudget{MaxRows: 2, MinRows: 1, CycleMaxRows: 2}}
			raw, err := sql.Open("sqlite", m.indexPath())
			if err != nil {
				t.Fatal(err)
			}
			watermark := 500
			if deleteOutside {
				watermark = 800
			}
			if _, err := raw.Exec("DELETE FROM delivery_batch WHERE end_pos<=?", watermark); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec("UPDATE delivery_batch SET ordinal=ordinal-2"); err != nil {
				t.Fatal(err)
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
			for round := 1; round <= 3; round++ {
				err := m.openIndex()
				if deleteOutside {
					if err == nil {
						t.Fatal("用比对重算后的Delivery豁免原始双水位外删行")
					}
					if m.index != nil {
						_ = m.index.Close()
					}
					if _, err := os.Stat(m.statePath); err != nil {
						t.Fatal("拒绝后来源不应归档")
					}
					break
				}
				if err != nil {
					t.Fatalf("第%d次升级失败：%v", round, err)
				}
				loaded, err := m.loadStateFromIndex()
				if err != nil {
					t.Fatal(err)
				}
				entry := loaded.Sources[key.String()].Ledger[0]
				if len(entry.DeliveryBatches) != 1 || entry.DeliveryBatches[0].End != 800 {
					t.Fatalf("原始双水位之外批次丢失：%+v", entry.DeliveryBatches)
				}
				facts := eventStatusFactsOfPersisted(loaded.Sources[key.String()])
				if got := classifyEventStatus(facts, 1, 100); got.State != EventStateAcked {
					t.Fatalf("被裁真实ACK未从源恢复：%+v", got)
				}
				if got := classifyEventStatus(facts, 450, 480); got.State != EventStateSentUnacked {
					t.Fatalf("被裁真实UNKNOWN未从源恢复：%+v", got)
				}
				if err := m.index.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestIndexLegacyBridgeNoBatchDoesNotInventReceipt(t *testing.T) {
	key := ledger.SourceKey{LogSourceID: "legacy-no-receipt", SourceGeneration: "g1"}
	legacy := &persistedState{Sources: map[string]persistedSource{key.String(): {Ledger: []ledger.Entry{{Key: key, Positions: logtypes.Positions{Read: 1000, Durable: 1000, Delivery: 800, Reclaim: 800}}}}}}
	root, _ := seedLegacyAdapter(t, true, legacy)
	for round := 1; round <= 3; round++ {
		m := &Manager{root: root, statePath: filepath.Join(root, "var", "log", "ingest.state.json")}
		if err := m.openIndex(); err != nil {
			t.Fatal(err)
		}
		loaded, err := m.loadStateFromIndex()
		if err != nil {
			t.Fatal(err)
		}
		saved := loaded.Sources[key.String()]
		facts := eventStatusFactsOfPersisted(saved)
		if len(facts.RequestDoneRanges) != 0 || len(facts.Batches) != 0 || len(saved.Ledger[0].DeliveryEvidence) != 0 {
			t.Fatalf("无源batch却补造了独立证据：%+v", facts)
		}
		if got := classifyEventStatus(facts, 100, 200); got.State != EventStateUnknown {
			t.Fatalf("第%d次旧水位不能推出ACK或已发UNKNOWN：%+v", round, got)
		}
		if err := m.index.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
