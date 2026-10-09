package process

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// TestIsolateAdoptedWrapperSurfacesZeroMigration 复现 2026-10-09 真机事故的**观测缺口**：
//
// 当实例的 wrapper 所在单元归 root（如 /system.slice/ssh.service）时，当前用户对源与目标的共同祖先
// 没有写权限，内核拒绝 cgroup 迁移，`MigratePIDTreeToCgroup` 逐进程失败后返回 (0, nil)。
// 此时 `isolateAdoptedWrapper` 的 `moved > 0` 分支不成立即**静默返回**——CP 侧看不到「这批实例
// 已在 Worker 单元 cgroup 内失去保护，重启该单元会被连坐」。真机上 13 个实例处于该状态。
//
// 期望：零迁移且 wrapper 仍存活时，必须落一条审计（非静默），使「无保护」可被观测。
func TestIsolateAdoptedWrapperSurfacesZeroMigration(t *testing.T) {
	f := newAdoptFixture(t)
	// 零迁移、无错误——这正是真机内核拒绝时的返回形态（错误只逐进程记在 wrapper 侧 WARN）。
	f.m.recoverMigrate = func(int, string) (int, error) { return 0, nil }
	// wrapper 仍存活：才会留下「实例仍在单元 cgroup 内」的隐患。
	f.m.recoverPIDAlive = func(pid int) bool { return pid == adoptWrapperPID }

	rec := &daemon.PIDRecord{WrapperPID: adoptWrapperPID, JavaPID: adoptJavaPID, InstanceUUID: adoptUUID}
	f.m.isolateAdoptedWrapper(adoptUUID, rec)

	require.Equal(t, 1, f.countAudit("cgroup.isolate_failed"),
		"零迁移且进程存活必须落审计，否则「实例已无隔离保护」对 CP 完全不可见")
}

// TestIsolateAdoptedWrapperSilentWhenWrapperGone 反向边界：wrapper 已退出即无需保护，不该误报失败。
func TestIsolateAdoptedWrapperSilentWhenWrapperGone(t *testing.T) {
	f := newAdoptFixture(t)
	f.m.recoverMigrate = func(int, string) (int, error) { return 0, nil }
	f.m.recoverPIDAlive = func(int) bool { return false }

	rec := &daemon.PIDRecord{WrapperPID: adoptWrapperPID, JavaPID: adoptJavaPID, InstanceUUID: adoptUUID}
	f.m.isolateAdoptedWrapper(adoptUUID, rec)

	require.Zero(t, f.countAudit("cgroup.isolate_failed"),
		"wrapper 已退出意味着无需保护，不是失败")
}

// TestIsolateAdoptedWrapperMigrateErrorIsAudited 迁移调用本身报错时也要落审计。
func TestIsolateAdoptedWrapperMigrateErrorIsAudited(t *testing.T) {
	f := newAdoptFixture(t)
	f.m.recoverMigrate = func(int, string) (int, error) { return 0, errors.New("模拟迁移调用失败") }

	rec := &daemon.PIDRecord{WrapperPID: adoptWrapperPID, JavaPID: adoptJavaPID, InstanceUUID: adoptUUID}
	f.m.isolateAdoptedWrapper(adoptUUID, rec)

	require.Equal(t, 1, f.countAudit("cgroup.isolate_failed"), "迁移报错必须落审计")
}

// TestIsolateAdoptedWrapperSuccessDoesNotAuditFailure 成功迁移不得误报失败。
func TestIsolateAdoptedWrapperSuccessDoesNotAuditFailure(t *testing.T) {
	f := newAdoptFixture(t)
	f.m.recoverMigrate = func(int, string) (int, error) { return 2, nil }

	rec := &daemon.PIDRecord{WrapperPID: adoptWrapperPID, JavaPID: adoptJavaPID, InstanceUUID: adoptUUID}
	f.m.isolateAdoptedWrapper(adoptUUID, rec)

	require.Zero(t, f.countAudit("cgroup.isolate_failed"), "成功迁移不得落失败审计")
}
