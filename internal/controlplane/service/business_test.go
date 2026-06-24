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

func newBusinessTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Instance{}, &model.Node{}))
	return db
}

func mkBizInstance(t *testing.T, db *gorm.DB, name string, nodeID uint) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		Name: name, NodeID: nodeID, Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "x", Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

// TestBusiness_Dispatch_InvalidCommand 缺 domain/action 返回 ErrInvalidBusinessCommand。
func TestBusiness_Dispatch_InvalidCommand(t *testing.T) {
	db := newBusinessTestDB(t)
	svc := NewBusinessService(db, cpgrpc.NewClientPool())
	_, err := svc.Dispatch(1, "", "balance", "")
	require.ErrorIs(t, err, ErrInvalidBusinessCommand)
	_, err = svc.Dispatch(1, "economy", "  ", "")
	require.ErrorIs(t, err, ErrInvalidBusinessCommand)
}

// TestBusiness_Dispatch_NotFound 实例不存在返回 ErrInstanceNotFound。
func TestBusiness_Dispatch_NotFound(t *testing.T) {
	db := newBusinessTestDB(t)
	svc := NewBusinessService(db, cpgrpc.NewClientPool())
	_, err := svc.Dispatch(999, "economy", "balance", `{"player":"alice"}`)
	require.ErrorIs(t, err, ErrInstanceNotFound)
}

// TestBusiness_Dispatch_NodeMissing 实例的节点记录不存在时降级为「节点不存在」（不报错）。
func TestBusiness_Dispatch_NodeMissing(t *testing.T) {
	db := newBusinessTestDB(t)
	svc := NewBusinessService(db, cpgrpc.NewClientPool())
	inst := mkBizInstance(t, db, "biz", 42) // 节点 42 不存在

	res, err := svc.Dispatch(inst.ID, "economy", "balance", "")
	require.NoError(t, err)
	assert.Equal(t, inst.ID, res.InstanceID)
	assert.Equal(t, "economy", res.Domain)
	assert.Equal(t, "balance", res.Action)
	assert.False(t, res.Available)
	assert.Nil(t, res.Output)
	assert.Equal(t, "节点不存在", res.Error)
}

// TestBusiness_Dispatch_NodeNotConnected 节点不在连接池时降级（available=false + 友好提示）。
func TestBusiness_Dispatch_NodeNotConnected(t *testing.T) {
	db := newBusinessTestDB(t)
	svc := NewBusinessService(db, cpgrpc.NewClientPool())
	require.NoError(t, db.Create(&model.Node{Name: "n1", UUID: "node-uuid-1", GRPCPort: 9100, WSPort: 9102}).Error)
	inst := mkBizInstance(t, db, "biz", 1)

	res, err := svc.Dispatch(inst.ID, "economy", "balance", "")
	require.NoError(t, err)
	assert.False(t, res.Available)
	assert.Nil(t, res.Output)
	assert.Equal(t, "节点未连接", res.Error)
}

// TestMapBusinessResponse 覆盖 Worker 响应到透传结果的全降级矩阵（纯函数，免 mock gRPC）。
func TestMapBusinessResponse(t *testing.T) {
	const validJSON = `{"balance":"100.50","currency":"coin"}`

	tests := []struct {
		name          string
		resp          *workerpb.SendPluginCommandResponse
		wantAvailable bool
		wantOutput    bool   // 期望 Output 非 nil
		wantErrSubstr string // 期望 Error 含子串（空=不校验）
	}{
		{name: "nil 响应", resp: nil, wantErrSubstr: "无业务响应"},
		{name: "success=false（域不可用/探针未连）", resp: &workerpb.SendPluginCommandResponse{Success: false, Error: "economy 域不可用"}, wantErrSubstr: "economy 域不可用"},
		{name: "success=false 无 error 文案兜底", resp: &workerpb.SendPluginCommandResponse{Success: false}, wantErrSubstr: "业务动作执行失败"},
		{name: "success + 空 output（即发即忘）", resp: &workerpb.SendPluginCommandResponse{Success: true, Output: ""}, wantAvailable: true},
		{name: "success + 合法 JSON", resp: &workerpb.SendPluginCommandResponse{Success: true, Output: validJSON}, wantAvailable: true, wantOutput: true},
		{name: "success + 非法 JSON 被拦", resp: &workerpb.SendPluginCommandResponse{Success: true, Output: "{not-json"}, wantErrSubstr: "无法解析"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &BusinessResult{InstanceID: 7, Domain: "economy", Action: "balance"}
			mapBusinessResponse(result, tt.resp)
			assert.Equal(t, tt.wantAvailable, result.Available)
			if tt.wantOutput {
				require.NotNil(t, result.Output)
				var parsed map[string]any
				require.NoError(t, json.Unmarshal(result.Output, &parsed))
				assert.Contains(t, parsed, "balance")
			} else {
				assert.Nil(t, result.Output)
			}
			if tt.wantErrSubstr != "" {
				assert.Contains(t, result.Error, tt.wantErrSubstr)
			}
		})
	}
}
