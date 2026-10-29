package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// 用生产映射与真实 SQLite 替换**账本**快照后再恢复采集器，避免只验证结构体相等。
//
// 2026-10-29：WAL 改为纯内存队列后不再落盘，也没有从库恢复队列的路径。本 helper 因此只做
// 账本的真库往返（账本才是持久化的真源）；返回的 WAL 是**空的新队列**——这正是新架构下
// 重启后的真实形态（队列重启即丢，由 position 水位回退重读接替）。
func indexReloadAcquire(t *testing.T, path string, key ledger.SourceKey, led *ledger.Ledger) (*ledger.Ledger, *acquire.WAL) {
	t.Helper()
	m := &Manager{}
	rows, err := m.stateToIndexState(&persistedState{Sources: map[string]persistedSource{key.String(): {Ledger: led.Snapshot()}}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := stateindex.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for {
		stats, err := s.Apply(rows)
		if err != nil {
			t.Fatal(err)
		}
		if !stats.Incomplete {
			break
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = stateindex.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := indexStateToState(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored := ledger.New()
	if err := restored.Restore(saved.Sources[key.String()].Ledger); err != nil {
		t.Fatal(err)
	}
	restoredWAL := acquire.NewWAL(restored, key)
	return restored, restoredWAL
}

func TestIndexTruncatedPhysicalMappingActuallyPollsAfterThreeRestarts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("[00:00:%02d] line-%02d padding padding padding padding", i, i))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	key := ledger.SourceKey{LogSourceID: "sqlite-truncation", SourceGeneration: "g1"}
	led := ledger.New()
	wal := acquire.NewWAL(led, key)
	tailer := acquire.NewFileTailer(led, key, path, wal, acquire.NewLineHook())
	events, err := tailer.Poll()
	if err != nil || len(events) != 30 {
		t.Fatalf("初读：%d %v", len(events), err)
	}
	if err := wal.Append(events...); err != nil {
		t.Fatal(err)
	}
	if err := wal.Commit(); err != nil {
		t.Fatal(err)
	}
	originalEnd := tailer.LogicalPos()
	prefix := strings.Join(lines[:5], "\n") + "\n"
	if err := os.Truncate(path, int64(len(prefix))); err != nil {
		t.Fatal(err)
	}
	if rotated, err := tailer.PrepareRotation(); err != nil || rotated {
		t.Fatalf("纯截断被判轮转：%v %v", rotated, err)
	}
	if events, err := tailer.Poll(); err != nil || len(events) != 0 {
		t.Fatalf("截断残留重读：%d %v", len(events), err)
	}
	for round := 1; round <= 3; round++ {
		led, wal = indexReloadAcquire(t, filepath.Join(root, "index.db"), key, led)
		tailer = acquire.NewFileTailer(led, key, path, wal, nil)
		if events, err := tailer.Poll(); err != nil || len(events) != 0 {
			t.Fatalf("第%d次SQLite重启残留前缀被重投：%d %v", round, len(events), err)
		}
		if tailer.LogicalPos() != originalEnd {
			t.Fatalf("第%d次SQLite重启逻辑EOF变化", round)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("fresh\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	events, err = tailer.Poll()
	if err != nil || len(events) != 1 || events[0].Record.Start != originalEnd {
		t.Fatalf("追加没有沿用原逻辑EOF：%+v %v", events, err)
	}
}

func TestIndexRotatedConfirmedFingerprintActuallyPollsAfterThreeRestarts(t *testing.T) {
	root := t.TempDir()
	live, rotated := filepath.Join(root, "latest.log"), filepath.Join(root, "latest.log.1")
	old, current := strings.Repeat("old-old-old\n", 70), strings.Repeat("new-new-new\n", 140)
	if err := os.WriteFile(live, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	key := ledger.SourceKey{LogSourceID: "sqlite-old-fingerprint", SourceGeneration: "g1"}
	led := ledger.New()
	wal := acquire.NewWAL(led, key)
	tailer := acquire.NewFileTailer(led, key, live, wal, nil)
	tailer.SetRotateTarget(rotated)
	events, err := tailer.Poll()
	if err != nil {
		t.Fatal(err)
	}
	if err := wal.Append(events...); err != nil {
		t.Fatal(err)
	}
	if err := wal.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := led.RecordDelivery(key, 0, uint64(len(old)), "REQUEST_DONE"); err != nil {
		t.Fatal(err)
	}
	if _, err := tailer.Poll(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(live, rotated); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte(current), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, err := tailer.PrepareRotation(); err != nil || !changed {
		t.Fatalf("真实轮转未识别：%v %v", changed, err)
	}
	events, err = tailer.Poll()
	if err != nil || len(events) != 140 {
		t.Fatalf("新live未完整采集：%d %v", len(events), err)
	}
	if err := wal.Append(events...); err != nil {
		t.Fatal(err)
	}
	if err := wal.Commit(); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 3; round++ {
		led, wal = indexReloadAcquire(t, filepath.Join(root, "index.db"), key, led)
		tailer = acquire.NewFileTailer(led, key, live, wal, nil)
		f, err := os.OpenFile(live, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("fresh-after-restart\n"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		events, err = tailer.Poll()
		if err != nil || len(events) != 1 {
			t.Fatalf("第%d次SQLite重启把旧段指纹套新live：%d %v", round, len(events), err)
		}
		if led.UnresolvedGapCount(key) != 0 {
			t.Fatalf("第%d次SQLite重启产生假FILE_REWRITTEN：%+v", round, led.Get(key).Gaps)
		}
		if err := wal.Append(events...); err != nil {
			t.Fatal(err)
		}
		if err := wal.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}
