package service

import (
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func newServerStateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Instance{}, &model.Node{}))
	return db
}

func mkStateInstance(t *testing.T, db *gorm.DB, name string, nodeID uint) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		Name: name, NodeID: nodeID, Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "x", Status: model.InstanceStatusRunning, ProbePort: 29940,
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

// TestServerState_QueryState_NotFound 实例不存在返回 ErrInstanceNotFound。
func TestServerState_QueryState_NotFound(t *testing.T) {
	db := newServerStateTestDB(t)
	svc := NewServerStateService(db, cpgrpc.NewClientPool())
	_, err := svc.QueryState(999)
	require.ErrorIs(t, err, ErrInstanceNotFound)
}

// TestServerState_QueryState_NodeNotConnected 节点不在连接池时降级（available=false + 友好提示），不报错。
func TestServerState_QueryState_NodeNotConnected(t *testing.T) {
	db := newServerStateTestDB(t)
	svc := NewServerStateService(db, cpgrpc.NewClientPool())
	// 建实例但其节点（ID=1）未在池中。
	require.NoError(t, db.Create(&model.Node{Name: "n1", UUID: "node-uuid-1", GRPCPort: 9100, WSPort: 9102}).Error)
	inst := mkStateInstance(t, db, "smp", 1)

	res, err := svc.QueryState(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, inst.ID, res.InstanceID)
	assert.False(t, res.Available)
	assert.False(t, res.Connected)
	assert.Nil(t, res.State)
	assert.Equal(t, "节点未连接", res.Error)
}

// TestServerState_QueryState_NodeMissing 实例的节点记录不存在时降级为「节点不存在」。
func TestServerState_QueryState_NodeMissing(t *testing.T) {
	db := newServerStateTestDB(t)
	svc := NewServerStateService(db, cpgrpc.NewClientPool())
	inst := mkStateInstance(t, db, "smp", 42) // 节点 42 不存在

	res, err := svc.QueryState(inst.ID)
	require.NoError(t, err)
	assert.False(t, res.Available)
	assert.Equal(t, "节点不存在", res.Error)
}

// TestMapServerStateResponse 覆盖 Worker 响应到透传结果的全降级矩阵。
func TestMapServerStateResponse(t *testing.T) {
	const validJSON = `{"server":{"version":"git-Paper-123"},"classloader":{"loadedClasses":42}}`

	tests := []struct {
		name          string
		resp          *workerpb.QueryServerStateResponse
		wantConnected bool
		wantAvailable bool
		wantState     bool   // 期望 State 非 nil
		wantErrSubstr string // 期望 Error 含子串（空=不校验）
	}{
		{
			name:          "nil 响应",
			resp:          nil,
			wantErrSubstr: "无状态响应",
		},
		{
			name:          "success=false（未启用插件桥）",
			resp:          &workerpb.QueryServerStateResponse{Success: false, Error: "本节点未启用插件桥"},
			wantErrSubstr: "本节点未启用插件桥",
		},
		{
			name:          "探针未连入",
			resp:          &workerpb.QueryServerStateResponse{Success: true, Connected: false},
			wantConnected: false,
			wantErrSubstr: "探针未连入",
		},
		{
			name:          "探针在线但采集超时（state 空）",
			resp:          &workerpb.QueryServerStateResponse{Success: true, Connected: true, Error: "状态查询超时"},
			wantConnected: true,
			wantErrSubstr: "超时",
		},
		{
			name:          "成功取回合法 JSON",
			resp:          &workerpb.QueryServerStateResponse{Success: true, Connected: true, StateJson: validJSON},
			wantConnected: true,
			wantAvailable: true,
			wantState:     true,
		},
		{
			name:          "非法 JSON 被拦（不给前端坏数据）",
			resp:          &workerpb.QueryServerStateResponse{Success: true, Connected: true, StateJson: "{not-json"},
			wantConnected: true,
			wantErrSubstr: "无法解析",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &ServerStateResult{InstanceID: 7}
			mapServerStateResponse(result, tt.resp)
			assert.Equal(t, tt.wantConnected, result.Connected)
			assert.Equal(t, tt.wantAvailable, result.Available)
			if tt.wantState {
				require.NotNil(t, result.State)
				var parsed map[string]any
				require.NoError(t, json.Unmarshal(result.State, &parsed))
				assert.Contains(t, parsed, "classloader")
			} else {
				assert.Nil(t, result.State)
			}
			if tt.wantErrSubstr != "" {
				assert.Contains(t, result.Error, tt.wantErrSubstr)
			}
		})
	}
}
