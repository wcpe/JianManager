package stateindex

import "testing"

// TestLoadKeepsOneSnapshotAcrossTables 用独立写连接在读完首表后提交整代，不能只靠本句柄互斥锁。
func TestLoadKeepsOneSnapshotAcrossTables(t *testing.T) {
	writer := newTestStore(t)
	old := fixtureState(1)
	if _, err := writer.Apply(old); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(writer.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	newState := cloneState(old)
	newState.Sources[0].StorageNamespace = "new-namespace"
	newState.Positions[0].ReadPos++
	newState.Gaps[0].Detail = "新缺口"
	newState.Projections[0].Generation = "new-generation"
	newState.Instances[0].WorkDir = "/new"
	newState.Aux[0].Payload = []byte(`{"new":true}`)
	newState.WAL[0].Body = []byte(`{"new":true}`)
	newState.WAL[0].FP = fingerprint(newState.WAL[0].Body)
	newState.Batches[0].State = "NEW"
	reader.testLoadTableHook = func(table int) {
		if table != tblSource {
			return
		}
		if stats, err := writer.Apply(newState); err != nil || stats.Incomplete {
			t.Fatalf("独立写事务提交失败: stats=%+v err=%v", stats, err)
		}
	}
	loaded, err := reader.Load()
	if err != nil {
		t.Fatal(err)
	}
	reader.testLoadTableHook = nil
	assertSameState(t, old, loaded)
	loaded, err = reader.Load()
	if err != nil {
		t.Fatal(err)
	}
	assertSameState(t, newState, loaded)
}
