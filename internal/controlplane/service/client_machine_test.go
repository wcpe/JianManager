package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newClientMachineDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ClientMachine{}))
	return db
}

// TestClientMachine_RecordUpsert 机器码登记 upsert（FR-092）：首登 hit=1、再登 hit=2 不增行、不同机器码独立、空入参不登记。
func TestClientMachine_RecordUpsert(t *testing.T) {
	db := newClientMachineDB(t)
	svc := NewClientMachineService(db)

	require.NoError(t, svc.Record("ch1", "machineA"))
	var m model.ClientMachine
	require.NoError(t, db.Where("channel_id = ? AND machine_id = ?", "ch1", "machineA").First(&m).Error)
	require.Equal(t, int64(1), m.HitCount)
	firstLastSeen := m.LastSeen

	time.Sleep(3 * time.Millisecond)
	require.NoError(t, svc.Record("ch1", "machineA"))
	require.NoError(t, db.Where("channel_id = ? AND machine_id = ?", "ch1", "machineA").First(&m).Error)
	require.Equal(t, int64(2), m.HitCount, "再次登记应累加 hit_count")
	require.False(t, m.LastSeen.Before(firstLastSeen), "last_seen 应抬升")

	// 同机器码再登记不新增行（upsert）。
	var rowsA int64
	db.Model(&model.ClientMachine{}).Where("channel_id = ? AND machine_id = ?", "ch1", "machineA").Count(&rowsA)
	require.Equal(t, int64(1), rowsA)

	// 不同机器码独立成行。
	require.NoError(t, svc.Record("ch1", "machineB"))
	var total int64
	db.Model(&model.ClientMachine{}).Count(&total)
	require.Equal(t, int64(2), total)

	// 空 channel/machine 不登记。
	require.NoError(t, svc.Record("ch1", ""))
	require.NoError(t, svc.Record("", "x"))
	db.Model(&model.ClientMachine{}).Count(&total)
	require.Equal(t, int64(2), total)
}

// TestClientMachine_TruncatesOverlongID 超长机器码截断登记（防滥用撑表）。
func TestClientMachine_TruncatesOverlongID(t *testing.T) {
	db := newClientMachineDB(t)
	svc := NewClientMachineService(db)
	long := ""
	for i := 0; i < 300; i++ {
		long += "a"
	}
	require.NoError(t, svc.Record("ch1", long))
	var m model.ClientMachine
	require.NoError(t, db.Where("channel_id = ?", "ch1").First(&m).Error)
	require.LessOrEqual(t, len(m.MachineID), machineIDMaxLen)
}
