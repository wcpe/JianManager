package ingest

import (
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

// 2026-10-30 P2.1：本文件原有两个用例
//   TestIndexTruncatedPhysicalMappingActuallyPollsAfterThreeRestarts
//   TestIndexRotatedConfirmedFingerprintActuallyPollsAfterThreeRestarts
// 验的是「已确认水位指纹跨重启不得被误用」。那条指纹链（ConfirmedFingerprint /
// refreshConfirmedFingerprintLocked / 续读前三态校验）随「跨实例 resume」一起下线了
// （用户裁定 D：起点改由 ingest 的游标表决定），故两条用例一并删除。
// indexReloadAcquire 保留：它仍被其它回归用例用于「账本经真 SQLite 往返」。
