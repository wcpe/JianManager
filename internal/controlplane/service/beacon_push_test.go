package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// FR-443 验收测试：向 Beacon 推送拓扑变更。
//
// 覆盖 spec §5 验收表里 JM 侧可自证的部分：
//   #1 未配置协同端点时，实例创建/改名/删除全部正常且零请求
//   #3 Beacon 不可达时，本机操作照常成功，仅有审计记录
//   #4 推送失败有审计（动作名 + 错误详情）
//   #5 实例启停不产生推送（避免风暴）

// beaconPushRecord 是一次被 Beacon 侧收到的注册请求。
type beaconPushRecord struct {
	Path    string
	Method  string
	Token   string
	Payload BeaconServerPush
}

// beaconPushMock 是收集注册请求的伪 Beacon 注册端点。
type beaconPushMock struct {
	mu       sync.Mutex
	records  []beaconPushRecord
	status   int
	rawBody  []byte
	blocked  chan struct{}
	unblock  chan struct{}
	srv      *httptest.Server
	requests int
}

// newBeaconPushMock 起一个仅处理 v1 注册端点的伪 Beacon。
// status 非 0 时固定返回该状态码（用于构造失败场景）；blocked/unblock 非 nil 时请求会先阻塞。
func newBeaconPushMock(t *testing.T, status int) *beaconPushMock {
	t.Helper()
	m := &beaconPushMock{status: status}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload BeaconServerPush
		_ = json.NewDecoder(r.Body).Decode(&payload)
		m.mu.Lock()
		m.requests++
		m.records = append(m.records, beaconPushRecord{
			Path: r.URL.Path, Method: r.Method,
			Token: r.Header.Get("X-Beacon-Token"), Payload: payload,
		})
		blocked, unblock := m.blocked, m.unblock
		m.mu.Unlock()
		if blocked != nil {
			<-blocked
		}
		if unblock != nil {
			<-unblock
		}
		if m.status != 0 {
			w.WriteHeader(m.status)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instanceKey":"prod/x","machineRegistered":true}`))
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// recordsSnapshot 返回已收到的注册请求快照。
func (m *beaconPushMock) recordsSnapshot() []beaconPushRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]beaconPushRecord(nil), m.records...)
}

// requestCount 返回已收到的请求数。
func (m *beaconPushMock) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests
}

// newBeaconPushTestDB 建一份含实例/节点/审计的最小内存库。
func newBeaconPushTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Instance{},
		&model.Node{},
		&model.AuditLog{},
		&model.User{},
		&model.GroupInstance{},
		&model.ServerRegistration{},
		&model.NetworkMember{},
		&model.InstanceCrashSnapshot{},
		&model.Task{},
		// 删除事务内会级联清理快照底链并降级被增量引用的底链（N-6/R9/R27），缺表会让删除报错。
		&model.Backup{},
		&model.InstanceSnapshot{},
	))
	return db
}

// newBeaconPushTestEnv 组装「实例服务 + 推送服务」并接线，返回两者与伪 Beacon。
// enabled=false 时模拟「未配置 endpoint」：推送服务为 nil，实例服务不接线。
func newBeaconPushTestEnv(t *testing.T, mock *beaconPushMock, pushEnabled bool) (*InstanceService, *BeaconPushService, *gorm.DB) {
	t.Helper()
	db := newBeaconPushTestDB(t)
	svc := NewInstanceService(db, NewGroupService(db), nil)
	// 无 Worker 连接：立即关停后台委托，避免异步状态回写与本用例断言争用。
	svc.Shutdown()

	var pushSvc *BeaconPushService
	if mock != nil {
		client := NewBeaconPushClient(BeaconPushClientConfig{
			Endpoint:    mock.srv.URL,
			Token:       "shared-token",
			PushEnabled: pushEnabled,
			Namespace:   "prod",
		})
		require.NotNil(t, client)
		pushSvc = NewBeaconPushService(db, client)
		pushSvc.SetAuditService(NewAuditService(db))
		t.Cleanup(pushSvc.Shutdown)
		svc.SetBeaconPush(pushSvc)
	}
	return svc, pushSvc, db
}

// awaitBeaconPushRequests 等待伪 Beacon 收到期望数量的请求（异步推送的确定性等待）。
func awaitBeaconPushRequests(t *testing.T, mock *beaconPushMock, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mock.requestCount() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 Beacon 收到 %d 次推送超时，实际 %d 次", want, mock.requestCount())
}

// auditLogsByAction 按动作查审计。
func auditLogsByAction(t *testing.T, db *gorm.DB, action string) []model.AuditLog {
	t.Helper()
	var logs []model.AuditLog
	require.NoError(t, db.Where("action = ?", action).Find(&logs).Error)
	return logs
}

// seedBeaconPushInstance 建一台实例并发起一次「创建」推送（模拟已发生的拓扑变更）。
func seedBeaconPushInstance(t *testing.T, db *gorm.DB, name string) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		NodeID: 1, Name: name, Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDirect, StartCommand: "./run.sh",
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

// TestBeaconPush_CreateSendsRegisterRequest 验收 #2（JM 侧）：实例创建触发推送，
// 请求落在 v1 机器注册端点、带 X-Beacon-Token、体含 serverId/namespace/role。
func TestBeaconPush_CreateSendsRegisterRequest(t *testing.T) {
	mock := newBeaconPushMock(t, 0)
	_, pushSvc, db := newBeaconPushTestEnv(t, mock, true)
	require.True(t, pushSvc.Enabled())

	inst := seedBeaconPushInstance(t, db, "r1-z1-g1")
	pushSvc.PushInstance(0, "127.0.0.1", beaconPushEventCreate, inst)
	awaitBeaconPushRequests(t, mock, 1)

	rec := mock.recordsSnapshot()[0]
	require.Equal(t, BeaconAPIPathAgentRegister, rec.Path)
	require.Equal(t, http.MethodPost, rec.Method)
	require.Equal(t, "shared-token", rec.Token, "共享 token 必须以 X-Beacon-Token 发送（FR-222）")
	require.Equal(t, "prod", rec.Payload.Namespace)
	require.Equal(t, "r1-z1-g1", rec.Payload.ServerID)
	require.Equal(t, "bukkit", rec.Payload.Role)
	require.Equal(t, "jianmanager", rec.Payload.Metadata["source"])

	// 成功推送写 ok 审计，target 为实例名（验收 #4 的成功侧）。
	logs := auditLogsByAction(t, db, AuditActionBeaconPushOK)
	require.Len(t, logs, 1)
	require.Equal(t, "r1-z1-g1", logs[0].TargetID)
	require.False(t, logs[0].Failed)
	require.Contains(t, logs[0].Detail, "create")
	require.Empty(t, auditLogsByAction(t, db, AuditActionBeaconPushFail))
}

// TestBeaconPush_ProxyRoleMapsToBungee 代理角色映射为 Beacon 的 bungee（对端归为 proxy）。
func TestBeaconPush_ProxyRoleMapsToBungee(t *testing.T) {
	mock := newBeaconPushMock(t, 0)
	_, pushSvc, db := newBeaconPushTestEnv(t, mock, true)

	inst := seedBeaconPushInstance(t, db, "proxy-1")
	require.NoError(t, db.Model(inst).Update("role", model.InstanceRoleProxy).Error)
	require.NoError(t, db.First(inst, inst.ID).Error)

	pushSvc.PushInstance(0, "", beaconPushEventOwnership, inst)
	awaitBeaconPushRequests(t, mock, 1)
	require.Equal(t, "bungee", mock.recordsSnapshot()[0].Payload.Role)
}

// TestBeaconPush_AddressFromNode 推送体带上节点 host + 实例端口（Beacon 侧排障与展示用）。
func TestBeaconPush_AddressFromNode(t *testing.T) {
	mock := newBeaconPushMock(t, 0)
	_, pushSvc, db := newBeaconPushTestEnv(t, mock, true)

	node := &model.Node{UUID: "n1", Name: "n1", Host: "10.0.0.7", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	inst := seedBeaconPushInstance(t, db, "r1-z1-g1")
	require.NoError(t, db.Model(inst).Updates(map[string]any{"node_id": node.ID, "server_port": 25565}).Error)
	require.NoError(t, db.First(inst, inst.ID).Error)

	pushSvc.PushInstance(0, "", beaconPushEventCreate, inst)
	awaitBeaconPushRequests(t, mock, 1)
	require.Equal(t, "10.0.0.7:25565", mock.recordsSnapshot()[0].Payload.Address)
}

// TestBeaconPush_DisabledSkipsEntirely 是 core 的「未配置即整体跳过」：
// push-enabled=false 时 Enabled()=false，推送一次不发、审计一条不写。
func TestBeaconPush_DisabledSkipsEntirely(t *testing.T) {
	mock := newBeaconPushMock(t, 0)
	_, pushSvc, db := newBeaconPushTestEnv(t, mock, false)
	require.False(t, pushSvc.Enabled())
	require.True(t, pushSvc.Configured(), "已配 endpoint 但未开 push-enabled：Configured=true / Enabled=false")

	inst := seedBeaconPushInstance(t, db, "r1-z1-g1")
	pushSvc.PushInstance(0, "", beaconPushEventCreate, inst)

	// 给异步路径充分的机会发请求（若实现有 bug 会在这里现形）。
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 0, mock.requestCount(), "未启用推送时不得发起任何请求")
	require.Empty(t, auditLogsByAction(t, db, AuditActionBeaconPushOK))
	require.Empty(t, auditLogsByAction(t, db, AuditActionBeaconPushFail))
}

// TestBeaconPush_NoEndpointSkipsEntirely 未配置 endpoint：服务未接线，触发点静默返回（验收 #1 的服务侧）。
func TestBeaconPush_NoEndpointSkipsEntirely(t *testing.T) {
	svc, pushSvc, db := newBeaconPushTestEnv(t, nil, false)
	require.Nil(t, pushSvc, "未配置 endpoint 时不应构造推送服务")
	require.Nil(t, NewBeaconPushClient(BeaconPushClientConfig{Endpoint: "  "}), "空 endpoint 必须返回 nil 客户端")

	// 触发器在未接线时是彻底的 no-op：不 panic、不写库。
	inst := seedBeaconPushInstance(t, db, "r1-z1-g1")
	svc.pushTopologyAsync(beaconPushEventCreate, inst)
	require.Empty(t, auditLogsByAction(t, db, AuditActionBeaconPushOK))
	require.Empty(t, auditLogsByAction(t, db, AuditActionBeaconPushFail))
}

// TestBeaconPush_UnreachableRecordsFailAudit Beacon 不可达：只写 fail 审计，不 panic、不阻塞（验收 #3/#4）。
func TestBeaconPush_UnreachableRecordsFailAudit(t *testing.T) {
	mock := newBeaconPushMock(t, http.StatusInternalServerError)
	_, pushSvc, db := newBeaconPushTestEnv(t, mock, true)

	inst := seedBeaconPushInstance(t, db, "r1-z1-g1")
	pushSvc.PushInstance(0, "10.1.2.3", beaconPushEventCreate, inst)
	awaitBeaconPushRequests(t, mock, 1)

	// 成功后立刻断言审计：PushInstance 不阻塞，但 Shutdown 保证在途推送已收尾。
	pushSvc.Shutdown()
	logs := auditLogsByAction(t, db, AuditActionBeaconPushFail)
	require.Len(t, logs, 1, "推送失败必须留一条 fail 审计")
	require.True(t, logs[0].Failed)
	require.Equal(t, "r1-z1-g1", logs[0].TargetID)
	require.Equal(t, "10.1.2.3", logs[0].IP)
	require.Contains(t, logs[0].Error, "HTTP 500")
	require.Contains(t, logs[0].Error, "boom", "错误详情应带上对端响应正文")
	require.Empty(t, auditLogsByAction(t, db, AuditActionBeaconPushOK))
}

// TestBeaconPush_RetriesNothing 失败不重试（决策 3A）：只发一次请求。
func TestBeaconPush_RetriesNothing(t *testing.T) {
	mock := newBeaconPushMock(t, http.StatusBadGateway)
	_, pushSvc, db := newBeaconPushTestEnv(t, mock, true)

	inst := seedBeaconPushInstance(t, db, "r1-z1-g1")
	pushSvc.PushInstance(0, "", beaconPushEventCreate, inst)
	awaitBeaconPushRequests(t, mock, 1)
	pushSvc.Shutdown()

	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 1, mock.requestCount(), "失败不得重试：请求数必须恰好为 1")
}

// TestBeaconPush_DoesNotBlockCaller 推送在 goroutine 中执行，不进用户操作响应路径（决策 3A）：
// 对端挂起时调用方仍立即返回。
func TestBeaconPush_DoesNotBlockCaller(t *testing.T) {
	mock := newBeaconPushMock(t, 0)
	mock.blocked = make(chan struct{})
	mock.unblock = make(chan struct{})
	_, pushSvc, db := newBeaconPushTestEnv(t, mock, true)

	inst := seedBeaconPushInstance(t, db, "r1-z1-g1")
	done := make(chan struct{})
	go func() {
		pushSvc.PushInstance(0, "", beaconPushEventCreate, inst)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("对端挂起时 PushInstance 必须立即返回（推送不得进用户操作响应路径）")
	}
	awaitBeaconPushRequests(t, mock, 1)

	// 放行：Shutdown 必须等回在途 goroutine，不留悬挂。
	close(mock.blocked)
	close(mock.unblock)
	pushSvc.Shutdown()
}

// TestBeaconPush_NamespaceUnsetRecordsFail 已开推送但未配 namespace：不盲推，留 fail 审计。
func TestBeaconPush_NamespaceUnsetRecordsFail(t *testing.T) {
	mock := newBeaconPushMock(t, 0)
	db := newBeaconPushTestDB(t)
	client := NewBeaconPushClient(BeaconPushClientConfig{
		Endpoint: mock.srv.URL, Token: "shared-token", PushEnabled: true,
	})
	require.NotNil(t, client)
	pushSvc := NewBeaconPushService(db, client)
	pushSvc.SetAuditService(NewAuditService(db))
	t.Cleanup(pushSvc.Shutdown)
	// 前置检查：Enabled 只看端点在位与否（namespace 缺失在推送时判定，避免启动期强校验阻断）。
	require.True(t, pushSvc.Enabled())

	inst := seedBeaconPushInstance(t, db, "r1-z1-g1")
	pushSvc.PushInstance(0, "", beaconPushEventCreate, inst)
	pushSvc.Shutdown()

	require.Equal(t, 0, mock.requestCount(), "未配置 namespace 时不得盲推")
	logs := auditLogsByAction(t, db, AuditActionBeaconPushFail)
	require.Len(t, logs, 1)
	require.Contains(t, logs[0].Error, "namespace")
}

// TestBeaconUpdateEvent 更新事件的判定：只有改名/改归属才推，其它维度变更不推。
func TestBeaconUpdateEvent(t *testing.T) {
	require.Equal(t, "rename", beaconUpdateEvent(true, false))
	require.Equal(t, "ownership", beaconUpdateEvent(false, true))
	require.Equal(t, "rename+ownership", beaconUpdateEvent(true, true), "两维度同改只推一次，事件名体现维度")
	require.Equal(t, "", beaconUpdateEvent(false, false), "非拓扑变更不得推送")
}

// TestInstanceUpdate_PushesOnlyTopologyChanges 验收 #5 的核心：
// 改名/改归属（role、tags）触发推送，启停相关字段（启动命令/环境变量/资源限额）不触发。
func TestInstanceUpdate_PushesOnlyTopologyChanges(t *testing.T) {
	cases := []struct {
		name  string
		event string
		field UpdateInstanceFields
		want  int
	}{
		{"改名", "rename", UpdateInstanceFields{Name: strPtr("r1-z1-g2")}, 1},
		{"改角色", "ownership", UpdateInstanceFields{Role: rolePtr(model.InstanceRoleProxy)}, 1},
		{"改标签", "ownership", UpdateInstanceFields{Tags: &[]string{"region:r2"}}, 1},
		{"改名同值", "", UpdateInstanceFields{Name: strPtr("r1-z1-g1")}, 0},
		{"改角色同值", "", UpdateInstanceFields{Role: rolePtr(model.InstanceRoleBackend)}, 0},
		{"改启动命令", "非拓扑变更不推", UpdateInstanceFields{StartCommand: strPtr("./other.sh")}, 0},
		{"改环境变量", "非拓扑变更不推", UpdateInstanceFields{EnvVars: &map[string]string{"A": "1"}}, 0},
		{"改自动重启", "非拓扑变更不推", UpdateInstanceFields{AutoRestart: boolPtr(false)}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newBeaconPushMock(t, 0)
			svc, pushSvc, db := newBeaconPushTestEnv(t, mock, true)
			inst := seedBeaconPushInstance(t, db, "r1-z1-g1")

			_, err := svc.Update(inst.ID, tc.field)
			require.NoError(t, err)
			pushSvc.Shutdown()
			require.Equal(t, tc.want, mock.requestCount(), "推送次数不符（%s）", tc.event)
		})
	}
}

// TestInstanceUpdate_PushesOnceWhenBothChange 改名与改归属同时发生只推一次。
func TestInstanceUpdate_PushesOnceWhenBothChange(t *testing.T) {
	mock := newBeaconPushMock(t, 0)
	svc, pushSvc, db := newBeaconPushTestEnv(t, mock, true)
	inst := seedBeaconPushInstance(t, db, "r1-z1-g1")

	_, err := svc.Update(inst.ID, UpdateInstanceFields{
		Name: strPtr("r1-z1-g9"),
		Role: rolePtr(model.InstanceRoleProxy),
	})
	require.NoError(t, err)
	awaitBeaconPushRequests(t, mock, 1)
	pushSvc.Shutdown()

	require.Equal(t, 1, mock.requestCount(), "两个维度同改应只推一次")
	rec := mock.recordsSnapshot()[0]
	require.Equal(t, "r1-z1-g9", rec.Payload.ServerID, "推送体必须带改名后的新名")
	require.Equal(t, "bungee", rec.Payload.Role, "推送体必须带改后的新角色")
	logs := auditLogsByAction(t, db, AuditActionBeaconPushOK)
	require.Len(t, logs, 1)
	require.Contains(t, logs[0].Detail, "rename+ownership")
}

// TestInstanceStartStopNeverPush 验收 #5：实例启停/重启不产生任何推送（避免风暴）。
func TestInstanceStartStopNeverPush(t *testing.T) {
	mock := newBeaconPushMock(t, 0)
	svc, pushSvc, db := newBeaconPushTestEnv(t, mock, true)
	inst := seedBeaconPushInstance(t, db, "r1-z1-g1")

	// 无 Worker 连接：Start/Stop/Restart 会失败或不推进，但推送与否不受其影响。
	_ = svc.Start(inst.ID)
	_ = svc.Stop(inst.ID)
	_ = svc.Restart(inst.ID)
	_ = svc.Kill(inst.ID)
	pushSvc.Shutdown()

	require.Equal(t, 0, mock.requestCount(), "启停/重启绝不得触发 Beacon 推送（ADR-090 §6）")
}

// TestInstanceCreateHook_PushesAfterSuccess 创建路径真的接了推送钩子：
// 经 Create 建实例 → 异步推一条 create。
func TestInstanceCreateHook_PushesAfterSuccess(t *testing.T) {
	mock := newBeaconPushMock(t, 0)
	svc, pushSvc, _ := newBeaconPushTestEnv(t, mock, true)

	inst, err := svc.Create(CreateInstanceRequest{
		NodeID: 1, Name: "r1-z1-new", Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDirect, StartCommand: "./run.sh",
	})
	require.NoError(t, err)
	awaitBeaconPushRequests(t, mock, 1)
	pushSvc.Shutdown()

	rec := mock.recordsSnapshot()[0]
	require.Equal(t, "r1-z1-new", rec.Payload.ServerID)
	require.Equal(t, inst.Name, rec.Payload.ServerID)
}

// TestInstanceDeleteHook_PushesOnlyAfterCommit 删除路径接了推送钩子，且推送在事务提交后才发。
func TestInstanceDeleteHook_PushesAfterCommit(t *testing.T) {
	mock := newBeaconPushMock(t, 0)
	db := newBeaconPushTestDB(t)
	node := &model.Node{
		UUID: "dn", Name: "dn", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s",
		Status: model.NodeStatusOnline,
	}
	require.NoError(t, db.Create(node).Error)

	pool := newBeaconPushWorkerPool(t, node.UUID)
	svc := NewInstanceService(db, NewGroupService(db), pool)
	svc.Shutdown()
	client := NewBeaconPushClient(BeaconPushClientConfig{
		Endpoint: mock.srv.URL, Token: "shared-token", PushEnabled: true, Namespace: "prod",
	})
	pushSvc := NewBeaconPushService(db, client)
	pushSvc.SetAuditService(NewAuditService(db))
	t.Cleanup(pushSvc.Shutdown)
	svc.SetBeaconPush(pushSvc)

	inst := &model.Instance{
		NodeID: node.ID, Name: "r1-z1-del", Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDirect,
		StartCommand: "./run.sh", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)

	require.NoError(t, svc.Delete(inst.ID))
	awaitBeaconPushRequests(t, mock, 1)
	pushSvc.Shutdown()

	rec := mock.recordsSnapshot()[0]
	require.Equal(t, "r1-z1-del", rec.Payload.ServerID, "推送必须用删除前的实例快照")
	require.Equal(t, "prod", rec.Payload.Namespace)
}

// TestInstanceDeleteHook_SkipsPushWhenDeleteFails 删除失败（事务未提交）不得推删除——
// 否则两侧不一致（本地实例还在，Beacon 已归档）。
func TestInstanceDeleteHook_SkipsPushWhenDeleteFails(t *testing.T) {
	mock := newBeaconPushMock(t, 0)
	svc, pushSvc, db := newBeaconPushTestEnv(t, mock, true)

	// 实例所属节点不存在：删除在 removeWorkerDataSettled 阶段即中止（记录保留）。
	inst := seedBeaconPushInstance(t, db, "r1-z1-g1")
	require.Error(t, svc.Delete(inst.ID))
	pushSvc.Shutdown()

	require.Equal(t, 0, mock.requestCount(), "删除失败不得推送")
	require.NotNil(t, mustInstance(t, db, inst.ID), "删除失败后实例记录必须保留")
}

// TestBeaconPush_ShutdownDropsNewPushes 关闭后不再接受新推送（进程退出收尾）。
func TestBeaconPush_ShutdownDropsNewPushes(t *testing.T) {
	mock := newBeaconPushMock(t, 0)
	_, pushSvc, db := newBeaconPushTestEnv(t, mock, true)
	inst := seedBeaconPushInstance(t, db, "r1-z1-g1")

	pushSvc.Shutdown()
	pushSvc.PushInstance(0, "", beaconPushEventCreate, inst)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 0, mock.requestCount(), "服务已关闭时不得再起新推送")
}

func strPtr(s string) *string { return &s }

func boolPtr(b bool) *bool { return &b }

func rolePtr(r model.InstanceRole) *model.InstanceRole { return &r }

// beaconLifecycleWorker 是生命周期端到端用例的伪 Worker：复用 fakeWorkerClient 的
// 预检/注册/启动行为，补上删除编排需要的 RemoveInstance（否则嵌入接口为 nil 会 panic）。
type beaconLifecycleWorker struct {
	fakeWorkerClient
}

func (f *beaconLifecycleWorker) RemoveInstance(_ context.Context, _ *workerpb.RemoveInstanceRequest, _ ...grpc.CallOption) (*workerpb.RemoveInstanceResponse, error) {
	return &workerpb.RemoveInstanceResponse{Success: true}, nil
}

// TestBeaconPush_AbsentBeaconLeavesLifecycleUntouched 是 spec 验收 #1 的端到端断言：
// **未配置协同端点时，实例创建/启动/改名/删除全部正常，无任何报错**（可选协同，绝非依赖）。
//
// 这里刻意走真实服务路径（Create → Start → Update → Delete），不是只调触发器：
// 若推送实现里有任何阻塞/返回错误/影响返回值的瑕疵，本用例会直接失败。
func TestBeaconPush_AbsentBeaconLeavesLifecycleUntouched(t *testing.T) {
	db := newBeaconPushTestDB(t)
	node := newTestNode(t, db, "n-nobeacon")
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, &beaconLifecycleWorker{
		fakeWorkerClient: fakeWorkerClient{
			preflightResp: &workerpb.InstanceActionResponse{Success: true},
		},
	})
	svc := NewInstanceService(db, NewGroupService(db), pool)
	// 无 Beacon：完全不动 SetBeaconPush（与「只用 JianManager，不部署 Beacon」的部署形态一致）。

	inst, err := svc.Create(CreateInstanceRequest{
		NodeID: node.ID, Name: "lone-inst", Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "java -jar server.jar",
	})
	require.NoError(t, err, "未部署 Beacon 时创建必须成功")

	require.NoError(t, svc.Start(inst.ID), "未部署 Beacon 时启动必须成功")
	svc.Shutdown() // 停掉启动委托的异步回写，避免与本用例的后续断言争用

	updated, err := svc.Update(inst.ID, UpdateInstanceFields{Name: strPtr("lone-inst-2")})
	require.NoError(t, err, "未部署 Beacon 时改名必须成功")
	require.Equal(t, "lone-inst-2", updated.Name)
	_, err = svc.Update(inst.ID, UpdateInstanceFields{Role: rolePtr(model.InstanceRoleProxy)})
	require.NoError(t, err, "未部署 Beacon 时改归属必须成功")

	// 删除前把状态归位为 STOPPED，避免走删除编排的停止窗口。
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusStopped).Error)
	require.NoError(t, svc.Delete(inst.ID), "未部署 Beacon 时删除必须成功")

	// 全程一条 beacon 审计都不该有（未接线＝彻底 no-op，不产生噪声）。
	require.Empty(t, auditLogsByAction(t, db, AuditActionBeaconPushOK))
	require.Empty(t, auditLogsByAction(t, db, AuditActionBeaconPushFail))
}

// mustInstance 读回实例（不存在返回 nil）。
func mustInstance(t *testing.T, db *gorm.DB, id uint) *model.Instance {
	t.Helper()
	var inst model.Instance
	if err := db.First(&inst, id).Error; err != nil {
		return nil
	}
	return &inst
}

// newBeaconPushWorkerPool 建一个「节点在线且 Worker 的 RemoveInstance 恒成功」的连接池，
// 让删除编排能走完（复用 instance_delete_cleanup_test.go 的 fakeRemoveWorker，不另造伪件）。
func newBeaconPushWorkerPool(t *testing.T, nodeUUID string) *cpgrpc.ClientPool {
	t.Helper()
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(nodeUUID, &fakeRemoveWorker{})
	t.Cleanup(pool.Close)
	return pool
}
