package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newMemGateDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Instance{}, &model.Node{}))
	return db
}

func memGateFixture(t *testing.T, db *gorm.DB, totalMB, usedMB int64, hbAge time.Duration) *model.Instance {
	t.Helper()
	hb := time.Now().Add(-hbAge)
	node := &model.Node{Name: "n1", UUID: "u1", Secret: "s", Host: "127.0.0.1",
		MemoryMB: totalMB, MemoryUsedMB: usedMB, LastHeartbeat: &hb}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{Name: "srv", NodeID: node.ID, Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -Xmx2G -jar server.jar nogui",
		Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

// TestMemoryGate_RejectsWhenHeartbeatShowsFull 心跳可信且明显塞不下 → CP 直接拒（FR-317）。
func TestMemoryGate_RejectsWhenHeartbeatShowsFull(t *testing.T) {
	db := newMemGateDB(t)
	svc := NewInstanceService(db, nil, nil)
	// 8G 总、7G 已用 → 可用 1G；需 2611 + 保留 819 → 拒
	inst := memGateFixture(t, db, 8192, 7168, 10*time.Second)

	err := svc.memoryGate(inst)
	require.Error(t, err)
	require.Contains(t, err.Error(), "内存不足")
	require.Contains(t, err.Error(), "n1", "错误应点名节点")
}

// TestMemoryGate_PassesWhenEnough 水位充足放行（FR-317）。
func TestMemoryGate_PassesWhenEnough(t *testing.T) {
	db := newMemGateDB(t)
	svc := NewInstanceService(db, nil, nil)
	// 8G 总、2G 已用 → 可用 6G，绰绰有余
	inst := memGateFixture(t, db, 8192, 2048, 10*time.Second)
	require.NoError(t, svc.memoryGate(inst))
}

// TestMemoryGate_StaleHeartbeatFailsOpen 心跳过旧（>90s）放行交 Worker 实时闸（FR-317）。
func TestMemoryGate_StaleHeartbeatFailsOpen(t *testing.T) {
	db := newMemGateDB(t)
	svc := NewInstanceService(db, nil, nil)
	inst := memGateFixture(t, db, 8192, 8000, 5*time.Minute)
	require.NoError(t, svc.memoryGate(inst), "心跳过旧应放行")
}

// TestMemoryGate_MissingFieldsFailsOpen 心跳字段缺失（旧 Worker 未上报）放行（FR-317）。
func TestMemoryGate_MissingFieldsFailsOpen(t *testing.T) {
	db := newMemGateDB(t)
	svc := NewInstanceService(db, nil, nil)
	inst := memGateFixture(t, db, 0, 0, 10*time.Second)
	require.NoError(t, svc.memoryGate(inst))
}
