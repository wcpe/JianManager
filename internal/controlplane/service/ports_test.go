package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newPortsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 内存库（共享缓存，按测试名隔离），避免 Windows 上 sqlite 文件在清理时被占用。
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Instance{}))
	return db
}

func mkInstance(name string, nodeID uint, ports AllocatedPorts) *model.Instance {
	return &model.Instance{
		Name: name, NodeID: nodeID, Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, StartCommand: "x",
		ServerPort: ports.ServerPort, RCONPort: ports.RCONPort, QueryPort: ports.QueryPort, ProbePort: ports.ProbePort,
	}
}

func TestAllocPortsForNode(t *testing.T) {
	db := newPortsTestDB(t)

	// 空节点：取各范围起点
	p1, err := allocPortsForNode(db, 1)
	require.NoError(t, err)
	require.Equal(t, 25565, p1.ServerPort)
	require.Equal(t, 25575, p1.RCONPort)
	require.Equal(t, p1.ServerPort, p1.QueryPort) // query 约定等于 server
	require.Equal(t, 29940, p1.ProbePort)         // 探针端口取起点

	// 落库后再分配应跳过已占用端口
	require.NoError(t, db.Create(mkInstance("a", 1, p1)).Error)
	p2, err := allocPortsForNode(db, 1)
	require.NoError(t, err)
	require.Equal(t, 25566, p2.ServerPort)
	require.Equal(t, 25576, p2.RCONPort)
	require.Equal(t, 29941, p2.ProbePort) // p1 占了 29940，跳到 29941

	// 不同节点独立计数
	p3, err := allocPortsForNode(db, 2)
	require.NoError(t, err)
	require.Equal(t, 25565, p3.ServerPort)

	// 同次分配内 server 与 rcon 不撞号
	require.NotEqual(t, p2.ServerPort, p2.RCONPort)

	// 软删除的实例释放其端口
	inst := mkInstance("b", 1, p2)
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Delete(inst).Error)
	p4, err := allocPortsForNode(db, 1)
	require.NoError(t, err)
	require.Equal(t, 25566, p4.ServerPort) // p2 的端口被回收
}

func TestNodePortUsage(t *testing.T) {
	db := newPortsTestDB(t)
	require.NoError(t, db.Create(mkInstance("b", 1, AllocatedPorts{ServerPort: 25566, RCONPort: 25576, QueryPort: 25566})).Error)
	require.NoError(t, db.Create(mkInstance("a", 1, AllocatedPorts{ServerPort: 25565, RCONPort: 25575, QueryPort: 25565})).Error)
	require.NoError(t, db.Create(mkInstance("other-node", 2, AllocatedPorts{ServerPort: 25565, RCONPort: 25575, QueryPort: 25565})).Error)
	// 无端口的实例不计入
	noPort := &model.Instance{Name: "noport", NodeID: 1, Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x"}
	require.NoError(t, db.Create(noPort).Error)

	usage, err := NodePortUsage(db, 1)
	require.NoError(t, err)
	require.Len(t, usage, 2)
	// 按 server_port 升序
	require.Equal(t, 25565, usage[0].ServerPort)
	require.Equal(t, 25566, usage[1].ServerPort)

	ranges := DefaultPortRanges()
	require.Equal(t, 25565, ranges.ServerPortBase)
	require.Equal(t, 2000, ranges.RangeSize)
}
