package process

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdoptedRecordPersistAndRestore 收养记录能落盘并在新 Manager 里恢复。
//
// 守的是真机反复踩到的那一条：Worker 一重启，收养关系就丢，
// 一批在跑的服全变回 STOPPED（面板与磁盘事实分家）。
func TestAdoptedRecordPersistAndRestore(t *testing.T) {
	dir := t.TempDir()
	const uuid = "inst-restore-1"
	self := os.Getpid()
	cmdline := "java -jar paper.jar nogui"

	// 第一个 Manager：登记实例 + 写收养记录（模拟“点了接管”）。
	m1 := NewManager(dir)
	m1.mu.Lock()
	m1.instances[uuid] = &Instance{UUID: uuid, State: StateStopped, StartCommand: cmdline}
	m1.mu.Unlock()
	m1.persistAdopted(uuid, self, cmdline)

	if _, err := os.Stat(filepath.Join(dir, uuid+".adopted")); err != nil {
		t.Fatalf("收养记录应已落盘: %v", err)
	}

	// 第二个 Manager（新进程语义）：先注册实例，再恢复。
	m2 := NewManager(dir)
	m2.mu.Lock()
	m2.instances[uuid] = &Instance{UUID: uuid, State: StateStopped, StartCommand: cmdline}
	m2.mu.Unlock()

	restored, cleaned := m2.RestoreAdoptedRuntimes()
	if restored != 1 {
		t.Fatalf("应恢复 1 个，实际 %d（cleaned=%d）", restored, cleaned)
	}
	m2.mu.RLock()
	inst := m2.instances[uuid]
	adoptedPID, state := inst.AdoptedPID, inst.State
	m2.mu.RUnlock()
	if adoptedPID != self {
		t.Fatalf("AdoptedPID 应为 %d，实际 %d", self, adoptedPID)
	}
	if state != StateRunning {
		t.Fatalf("状态应为 RUNNING，实际 %v", state)
	}
}

// TestRestoreDropsDeadAdopted 进程已死时恢复要放弃并把记录清掉（不粉饰成存活）。
func TestRestoreDropsDeadAdopted(t *testing.T) {
	dir := t.TempDir()
	const uuid = "inst-restore-dead"

	m := NewManager(dir)
	m.mu.Lock()
	// 关键前置：实例内存态是 RUNNING（收养着的），但那个进程已经没了。
	m.instances[uuid] = &Instance{UUID: uuid, State: StateRunning, StartCommand: "java -jar paper.jar", AdoptedPID: 4194303}
	m.mu.Unlock()
	// 写一个几乎不可能存在的 PID。
	m.persistAdopted(uuid, 4194303, "java -jar paper.jar")

	restored, cleaned := m.RestoreAdoptedRuntimes()
	if restored != 0 {
		t.Fatalf("死进程不应恢复，实际 restored=%d", restored)
	}
	if cleaned != 1 {
		t.Fatalf("应清掉陈旧记录，实际 cleaned=%d", cleaned)
	}
	if _, err := os.Stat(filepath.Join(dir, uuid+".adopted")); !os.IsNotExist(err) {
		t.Fatal("陈旧记录应已删除")
	}

	// 关键断言：状态必须回到 STOPPED、AdoptedPID 归零。
	// 只删记录而不改状态会让实例永久卡在 RUNNING：存活探测每拍报“进程不存活”，
	// 而且 Start 没有 RUNNING→STARTING 的合法转换，用户再也无法用平台拉起它。
	m.mu.RLock()
	inst := m.instances[uuid]
	state, pid := inst.State, inst.AdoptedPID
	m.mu.RUnlock()
	if state != StateStopped {
		t.Fatalf("放弃收养后状态应为 STOPPED（否则无法重启），实际 %v", state)
	}
	if pid != 0 {
		t.Fatalf("放弃收养后 AdoptedPID 应归零，实际 %d", pid)
	}
}

// TestRestoreSkipsUnregisteredInstance 实例还没注册时保留记录（后续轮次再恢复）。
func TestRestoreSkipsUnregisteredInstance(t *testing.T) {
	dir := t.TempDir()
	const uuid = "inst-not-yet"
	m := NewManager(dir)
	m.persistAdopted(uuid, os.Getpid(), "java -jar paper.jar")

	restored, cleaned := m.RestoreAdoptedRuntimes()
	if restored != 0 || cleaned != 0 {
		t.Fatalf("未注册实例应原封不动（等 Resync），实际 restored=%d cleaned=%d", restored, cleaned)
	}
	if _, err := os.Stat(filepath.Join(dir, uuid+".adopted")); err != nil {
		t.Fatal("记录应保留，便于后续恢复")
	}
}

// TestRestoreAdoptedIdempotentNoRecount 重复调用恢复不得重复计数。
//
// 守的是真机日志噪声：CP 每 60–90s 重推一次，而本函数在重推里被补调一次；
// 没有早退就会每拍刷 5 条「已重新认领外部进程」，把真正的事件埋没。
func TestRestoreAdoptedIdempotentNoRecount(t *testing.T) {
	dir := t.TempDir()
	const uuid = "inst-idempotent"
	self := os.Getpid()
	cmdline := "java -jar paper.jar nogui"

	m := NewManager(dir)
	m.mu.Lock()
	m.instances[uuid] = &Instance{UUID: uuid, State: StateStopped, StartCommand: cmdline}
	m.mu.Unlock()
	m.persistAdopted(uuid, self, cmdline)

	first, _ := m.RestoreAdoptedRuntimes()
	if first != 1 {
		t.Fatalf("首次应恢复 1 个，实际 %d", first)
	}
	// 后续重复调用：结果相同，不计为新恢复。
	for i := 0; i < 3; i++ {
		again, cleaned := m.RestoreAdoptedRuntimes()
		if again != 0 || cleaned != 0 {
			t.Fatalf("重复调用不应重复计数（第 %d 次：restored=%d cleaned=%d）", i+2, again, cleaned)
		}
	}

	m.mu.RLock()
	inst := m.instances[uuid]
	pid, state := inst.AdoptedPID, inst.State
	m.mu.RUnlock()
	if pid != self || state != StateRunning {
		t.Fatalf("状态不得被重复调用改坏：pid=%d state=%v", pid, state)
	}
}

// TestAdoptInPlace_ReclaimsAfterServerRestart 运维重启服务器后重新接管必须能自愈。
//
// 守的是真机踩到的那条：tmux 里重启服后，旧 java 退出、新 java 起来，但实例状态被心跳
// 维持成 RUNNING，而 AdoptedPID 仍指向**已死掉的旧 PID**。
// 旧实现在 state==RUNNING 时直接 return nil（真幂等），于是：
//   - 每拍存活探测都报「进程不存活」，面板出现「RUNNING + 进程/容器不存活」的矛盾；
//   - 重新点接管也修不好（同样被那个早退吃掉），.adopted 记录一条不重建。
//
// 修正后：只有「已 RUNNING 且旧 PID 还活着」才幂等返回，否则重新认领目录里现在的进程。
func TestAdoptInPlace_ReclaimsAfterServerRestart(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	const uuid = "inst-restarted"
	newPID := 9601
	cmdline := "java -jar paper.jar nogui"

	m := NewManager(dir)
	require.NoError(t, m.Create(uuid, "restarted", cmdline, "", workDir, nil, false, ProcessTypeDirect, "", "", 0, 0))

	// 模拟「旧进程已死、但状态仍 RUNNING」：AdoptedPID 指向一个已不存在的 PID。
	const deadPID = 4194303
	m.mu.Lock()
	inst := m.instances[uuid]
	inst.State = StateRunning
	inst.AdoptedPID = deadPID
	m.mu.Unlock()

	// 目录里现在有的是新进程（用注入的存活读数表达），死 PID 一律报死。
	m.recoverPIDAlive = func(pid int) bool { return pid == newPID }
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }
	// 现探结果：目录里确实有新进程在跑。
	m.SetForeignRuntimes(map[string]ForeignRuntime{uuid: {PID: newPID, Cmdline: cmdline}})

	pid, err := m.AdoptInPlaceRuntime(uuid)
	require.NoError(t, err, "收养失败会让面板永远停在 RUNNING+不存活")
	assert.Equal(t, newPID, pid, "应重新认领当前进程，而不是被『state==RUNNING』的早退吃掉")

	m.mu.RLock()
	gotPID, gotState := m.instances[uuid].AdoptedPID, m.instances[uuid].State
	m.mu.RUnlock()
	assert.Equal(t, newPID, gotPID)
	assert.Equal(t, StateRunning, gotState)

	// 收养记录必须重建（否则 Worker 重启后又丢）。
	_, statErr := os.Stat(filepath.Join(dir, uuid+".adopted"))
	assert.NoError(t, statErr, "重新接管必须落盘收养记录")

	// 真幂等：现在收养的 PID 就是活的那一个，再点一次应直接返回 0。
	again, err := m.AdoptInPlaceRuntime(uuid)
	require.NoError(t, err)
	assert.Zero(t, again, "已接管且进程存活时是幂等的")
}

// TestForgetAdoptedRemovesRecord 释放后记录必须消失，否则重启会被重新认回来。
func TestForgetAdoptedRemovesRecord(t *testing.T) {
	dir := t.TempDir()
	const uuid = "inst-forget"
	m := NewManager(dir)
	m.persistAdopted(uuid, os.Getpid(), "java -jar paper.jar")
	m.forgetAdopted(uuid)
	if _, ok := m.loadAdopted(uuid); ok {
		t.Fatal("释放后不应还能读到记录")
	}
}
