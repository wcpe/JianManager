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

// TestBusiness_Manifest_MetaQuery Manifest 复用 Dispatch 下发 jbis/manifest 元查询；节点未连时降级。
func TestBusiness_Manifest_MetaQuery(t *testing.T) {
	db := newBusinessTestDB(t)
	svc := NewBusinessService(db, cpgrpc.NewClientPool())
	require.NoError(t, db.Create(&model.Node{Name: "n1", UUID: "node-uuid-1", GRPCPort: 9100, WSPort: 9102}).Error)
	inst := mkBizInstance(t, db, "biz", 1)

	res, err := svc.Manifest(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "jbis", res.Domain, "manifest 应走保留元域")
	assert.Equal(t, "manifest", res.Action)
	assert.False(t, res.Available, "节点未连接应降级")
	assert.Equal(t, "节点未连接", res.Error)
}

// TestInjectWriteContext 覆盖业务写注入 payload 的全规则（FR-121，纯函数）。
func TestInjectWriteContext(t *testing.T) {
	parse := func(t *testing.T, s string) map[string]any {
		t.Helper()
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(s), &m))
		return m
	}

	t.Run("空 payload 起步 {} 并注入全字段", func(t *testing.T) {
		out, err := injectWriteContext("", WriteContext{
			TaskID: "op-1", Operator: "alice", OperatorID: 7, NodeID: "node-uuid", Reason: "活动补偿",
		})
		require.NoError(t, err)
		m := parse(t, out)
		assert.Equal(t, "op-1", m[payloadKeyTaskID])
		assert.Equal(t, "alice", m[payloadKeyOperator])
		assert.EqualValues(t, 7, m[payloadKeyOperatorID])
		assert.Equal(t, "node-uuid", m[payloadKeyNodeID])
		assert.Equal(t, "活动补偿", m[payloadKeyReason])
	})

	t.Run("保留业务方入参并补注入", func(t *testing.T) {
		out, err := injectWriteContext(`{"player":"bob","amount":"100"}`, WriteContext{
			TaskID: "op-2", Operator: "carol", OperatorID: 9, NodeID: "n2",
		})
		require.NoError(t, err)
		m := parse(t, out)
		assert.Equal(t, "bob", m["player"], "业务入参保留")
		assert.Equal(t, "100", m["amount"])
		assert.Equal(t, "op-2", m[payloadKeyTaskID])
		assert.Equal(t, "carol", m[payloadKeyOperator])
	})

	t.Run("不覆盖业务方已显式传入的同名键", func(t *testing.T) {
		out, err := injectWriteContext(`{"taskId":"caller-key","reason":"调用方原因"}`, WriteContext{
			TaskID: "cp-key", Reason: "CP原因",
		})
		require.NoError(t, err)
		m := parse(t, out)
		assert.Equal(t, "caller-key", m[payloadKeyTaskID], "调用方 taskId 不被覆盖")
		assert.Equal(t, "调用方原因", m[payloadKeyReason], "调用方 reason 不被覆盖")
	})

	t.Run("TaskID 为空兜底生成非空 UUID", func(t *testing.T) {
		out, err := injectWriteContext(`{"player":"dora"}`, WriteContext{Operator: "eve"})
		require.NoError(t, err)
		m := parse(t, out)
		tid, _ := m[payloadKeyTaskID].(string)
		assert.NotEmpty(t, tid, "缺 operationId 也应兜底注入非空 taskId")
	})

	t.Run("空 Operator/Reason/OperatorID=0 不写该键", func(t *testing.T) {
		out, err := injectWriteContext(`{"player":"f"}`, WriteContext{TaskID: "op-x"})
		require.NoError(t, err)
		m := parse(t, out)
		assert.NotContains(t, m, payloadKeyOperator)
		assert.NotContains(t, m, payloadKeyOperatorID)
		assert.NotContains(t, m, payloadKeyReason)
	})

	t.Run("非法 JSON payload 返回 error", func(t *testing.T) {
		_, err := injectWriteContext("{not-json", WriteContext{TaskID: "op"})
		require.Error(t, err)
	})

	t.Run("JSON 数组（非对象）payload 返回 error", func(t *testing.T) {
		_, err := injectWriteContext(`["a","b"]`, WriteContext{TaskID: "op"})
		require.Error(t, err)
	})
}

// TestBusiness_DispatchWrite_NodeMissing 写动作在节点缺失时同样降级（不报错），且注入鲁棒。
func TestBusiness_DispatchWrite_NodeMissing(t *testing.T) {
	db := newBusinessTestDB(t)
	svc := NewBusinessService(db, cpgrpc.NewClientPool())
	inst := mkBizInstance(t, db, "biz", 77) // 节点 77 不存在

	res, err := svc.DispatchWrite(inst.ID, "economy", "deposit", `{"player":"a","amount":"10"}`, WriteContext{
		TaskID: "op-1", Operator: "alice", OperatorID: 1,
	})
	require.NoError(t, err)
	assert.False(t, res.Available)
	assert.Equal(t, "节点不存在", res.Error)
}

// TestBusiness_DispatchWrite_InvalidPayload 写动作 payload 非法 JSON 返回 ErrInvalidBusinessCommand。
func TestBusiness_DispatchWrite_InvalidPayload(t *testing.T) {
	db := newBusinessTestDB(t)
	svc := NewBusinessService(db, cpgrpc.NewClientPool())
	require.NoError(t, db.Create(&model.Node{Name: "n1", UUID: "node-uuid-1", GRPCPort: 9100, WSPort: 9102}).Error)
	inst := mkBizInstance(t, db, "biz", 1)

	_, err := svc.DispatchWrite(inst.ID, "economy", "deposit", "{bad", WriteContext{TaskID: "op-1"})
	require.ErrorIs(t, err, ErrInvalidBusinessCommand)
}

// TestBusiness_DispatchWrite_InvalidCommand 缺 domain/action 返回 ErrInvalidBusinessCommand。
func TestBusiness_DispatchWrite_InvalidCommand(t *testing.T) {
	db := newBusinessTestDB(t)
	svc := NewBusinessService(db, cpgrpc.NewClientPool())
	_, err := svc.DispatchWrite(1, "", "deposit", "", WriteContext{})
	require.ErrorIs(t, err, ErrInvalidBusinessCommand)
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
