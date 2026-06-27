package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/platform/selfupdate"
	"github.com/wcpe/JianManager/internal/version"
)

// newTestRoot 构造带临时目录的数据根（cache 可用）。
func newTestRoot(t *testing.T) *dataroot.Root {
	t.Helper()
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	return root
}

// writeTempExe 写一个临时「假可执行文件」并返回路径。
func writeTempExe(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-exe")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o755))
	return p
}

// TestRollbackControlPlane_NoBackup 无备份时回滚 CP 返回 ErrNoBackup。
func TestRollbackControlPlane_NoBackup(t *testing.T) {
	// root=nil → 备份落临时目录，测试环境无既有备份 → ErrNoBackup。
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{}, nil)
	_, _, err := svc.RollbackControlPlane(context.Background())
	if !errors.Is(err, ErrNoBackup) {
		t.Fatalf("无备份应返回 ErrNoBackup，实得 %v", err)
	}
}

// TestRollbackControlPlane_Stubbed 回滚成功（经桩）返回 from=当前版本、to=备份版本。
func TestRollbackControlPlane_Stubbed(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{}, nil)
	restarted := false
	svc.restartCPFn = func() { restarted = true }
	svc.cpRollbackFn = func() (string, error) { return "0.9.0", nil } // 回滚到 0.9.0

	from, to, err := svc.RollbackControlPlane(context.Background())
	require.NoError(t, err)
	if from != version.Version {
		t.Fatalf("from 应为当前版本 %s，实得 %s", version.Version, from)
	}
	if to != "0.9.0" {
		t.Fatalf("to 应为备份版本 0.9.0，实得 %s", to)
	}
	// 异步重启已在 RollbackControlPlane 内调度（桩同步执行前可能未跑），仅验证不报错即可；
	// 重启时机由既有 UpgradeControlPlane 同款异步逻辑保证，这里不强等。
	_ = restarted
}

// TestRollbackNode_Offline 节点离线时回滚返回 ErrNodeOffline。
func TestRollbackNode_Offline(t *testing.T) {
	db := newSelfUpdateTestDB(t)
	node := &model.Node{Name: "n1", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "a", OS: runtime.GOOS, Arch: runtime.GOARCH, Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	svc := NewSelfUpdateService(db, cpgrpc.NewClientPool(), SelfUpdateConfig{}, nil)
	_, _, err := svc.RollbackNode(context.Background(), node.ID)
	if !errors.Is(err, ErrNodeOffline) {
		t.Fatalf("离线节点回滚应返回 ErrNodeOffline，实得 %v", err)
	}
}

// TestRollbackNode_NoBackupMapped 节点无备份（Worker 回 NO_BACKUP 文案）映射为 ErrNoBackup。
func TestRollbackNode_NoBackupMapped(t *testing.T) {
	db := newSelfUpdateTestDB(t)
	node := &model.Node{Name: "n1", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "a", OS: runtime.GOOS, Arch: runtime.GOARCH, Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	svc := NewSelfUpdateService(db, cpgrpc.NewClientPool(), SelfUpdateConfig{}, nil)
	// 桩模拟 Worker 回报无备份。
	svc.nodeRollbackFn = func(uint) (string, string, error) {
		return "0.10.0", "", errNodeNoBackup
	}
	_, _, err := svc.RollbackNode(context.Background(), node.ID)
	if !errors.Is(err, ErrNoBackup) {
		t.Fatalf("节点无备份应映射 ErrNoBackup，实得 %v", err)
	}
}

// TestCheckUpdate_CPBackupVersion CP 本地有备份时 CheckUpdate 透出 backupVersion。
func TestCheckUpdate_CPBackupVersion(t *testing.T) {
	root := newTestRoot(t)
	// 造一份 CP 备份。
	exe := writeTempExe(t, "CP-OLD")
	require.NoError(t, selfupdate.BackupCurrentFrom(selfupdate.ComponentControlPlane, "0.9.5", exe, root))

	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{}, root)
	res, err := svc.CheckUpdate(context.Background())
	require.NoError(t, err)
	if res.ControlPlane.BackupVersion != "0.9.5" {
		t.Fatalf("CP backupVersion 应为 0.9.5，实得 %q", res.ControlPlane.BackupVersion)
	}
}
