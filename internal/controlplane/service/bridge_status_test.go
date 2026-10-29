package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// bridgeFakeWorker 是桥能力用例的伪 Worker：只实现的四个 RPC，
// 其余方法由内嵌接口保持 nil（被调用即 panic，能暴露越界调用）。
type bridgeFakeWorker struct {
	workerpb.WorkerServiceClient

	statusResp *workerpb.QueryBridgeStatusResponse
	statusErr  error

	subscribeResp *workerpb.SetBridgeSubscriptionResponse
	subscribeErr  error

	bizStatusResp *workerpb.QueryBusinessStatusResponse
	bizStatusErr  error

	// 捕获入参，供断言"覆盖而非追加"等语义。
	lastSubscribeReq *workerpb.SetBridgeSubscriptionRequest
	statusCalls      int
	bizStatusCalls   int
}

func (f *bridgeFakeWorker) QueryBridgeStatus(context.Context, *workerpb.QueryBridgeStatusRequest, ...grpc.CallOption) (*workerpb.QueryBridgeStatusResponse, error) {
	f.statusCalls++
	return f.statusResp, f.statusErr
}

func (f *bridgeFakeWorker) SetBridgeSubscription(_ context.Context, in *workerpb.SetBridgeSubscriptionRequest, _ ...grpc.CallOption) (*workerpb.SetBridgeSubscriptionResponse, error) {
	f.lastSubscribeReq = in
	return f.subscribeResp, f.subscribeErr
}

func (f *bridgeFakeWorker) QueryBusinessStatus(context.Context, *workerpb.QueryBusinessStatusRequest, ...grpc.CallOption) (*workerpb.QueryBusinessStatusResponse, error) {
	f.bizStatusCalls++
	return f.bizStatusResp, f.bizStatusErr
}

// newBridgeFixture 建库 + 节点 + 实例 + 伪 Worker，返回服务与各部分。
func newBridgeFixture(t *testing.T, worker *bridgeFakeWorker) (*BridgeService, *model.Instance, *model.Node) {
	t.Helper()
	db := newBusinessTestDB(t)
	node := &model.Node{Name: "n1", UUID: "node-uuid-bridge", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	inst := mkBizInstance(t, db, "bridge-inst", node.ID)
	pool := cpgrpc.NewClientPool()
	if worker != nil {
		pool.SetWorkerClientForTest(node.UUID, worker)
	}
	return NewBridgeService(db, pool), inst, node
}

// TestBridge_QueryStatus_OK 桥状态查询正常返回：契约字段从探针快照正确扁平化。
func TestBridge_QueryStatus_OK(t *testing.T) {
	worker := &bridgeFakeWorker{statusResp: &workerpb.QueryBridgeStatusResponse{
		Success:   true,
		Connected: true,
		FetchedAtMs: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC).
			UnixMilli(),
		StatusJson: `{
			"connected": true,
			"reconnects": 3,
			"lastDisconnectReason": "network_reset",
			"lastConnectedAtMs": 1761217200000,
			"version": "2",
			"sender": {"commandResultQueue": 2, "reliableQueue": 5,
			           "bestEffortDropped": 7, "reliableRejected": 1, "spillDropped": 2},
			"pools": {"cmd": {"queued": 3}, "event": {"queued": 4},
			          "reload": {"queued": 1, "active": 6}}
		}`,
	}}
	svc, inst, _ := newBridgeFixture(t, worker)

	res, err := svc.QueryBridgeStatus(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, inst.ID, res.InstanceID)
	assert.True(t, res.Connected)
	assert.Equal(t, int64(3), res.ReconnectCount)
	assert.Equal(t, "network_reset", res.LastDisconnectReason)
	assert.Equal(t, "2", res.ProbeVersion)
	// 队列深度 = commandResultQueue(2) + reliableQueue(5) + cmd.queued(3) + event.queued(4)
	assert.Equal(t, int64(14), res.QueueDepth)
	// 丢弃 = bestEffortDropped(7) + reliableRejected(1) + spillDropped(2)
	assert.Equal(t, int64(10), res.DroppedFrames)
	// 在途重载取 reload 池的 active
	assert.Equal(t, int64(6), res.PendingReloads)
	assert.NotEmpty(t, res.ConnectedSince)
	assert.NotEmpty(t, res.FetchedAt)
	assert.Empty(t, res.Error)
}

// TestBridge_QueryStatus_ProbeOffline 探针未连：connected=false 且不返回错误（排障接口的正常答案）。
func TestBridge_QueryStatus_ProbeOffline(t *testing.T) {
	worker := &bridgeFakeWorker{statusResp: &workerpb.QueryBridgeStatusResponse{
		Success: true, Connected: false, FetchedAtMs: time.Now().UnixMilli(),
	}}
	svc, inst, _ := newBridgeFixture(t, worker)

	res, err := svc.QueryBridgeStatus(inst.ID)
	require.NoError(t, err, "探针不在不是错误")
	assert.False(t, res.Connected)
	assert.Equal(t, int64(0), res.ReconnectCount)
	assert.NotEmpty(t, res.Error, "应给出降级原因供前端提示")
}

// TestBridge_QueryStatus_NodeOffline 节点未连入时不打 RPC，降级为 connected=false。
func TestBridge_QueryStatus_NodeOffline(t *testing.T) {
	svc, inst, _ := newBridgeFixture(t, nil) // 不注入 Worker → pool.Get 失败
	res, err := svc.QueryBridgeStatus(inst.ID)
	require.NoError(t, err)
	assert.False(t, res.Connected)
	assert.Equal(t, "节点未连接", res.Error)
}

// TestBridge_QueryStatus_NotFound 实例不存在返回 ErrInstanceNotFound（供路由回 404）。
func TestBridge_QueryStatus_NotFound(t *testing.T) {
	svc, _, _ := newBridgeFixture(t, &bridgeFakeWorker{})
	_, err := svc.QueryBridgeStatus(9999)
	require.ErrorIs(t, err, ErrInstanceNotFound)
}

// TestBridge_SetSubscription_CoverNotAppend 重复 PUT 覆盖而不是追加：
// 第二次请求只带新表达式，Worker 收到的入参里不含第一次的表达式。
func TestBridge_SetSubscription_CoverNotAppend(t *testing.T) {
	worker := &bridgeFakeWorker{subscribeResp: &workerpb.SetBridgeSubscriptionResponse{
		Success: true, Connected: true, AppliedTopics: []string{"activity.*"},
	}}
	svc, inst, _ := newBridgeFixture(t, worker)

	applied, err := svc.SetSubscription(inst.ID, []string{"activity.spring"}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"activity.*"}, applied, "返回值应是探针回执的生效集合")

	applied2, err := svc.SetSubscription(inst.ID, []string{"activity.*"}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"activity.*"}, applied2)
	// 关键断言：第二次下发的 topics 只有本次的，没有与上次合并。
	assert.Equal(t, []string{"activity.*"}, worker.lastSubscribeReq.Topics,
		"订阅必须整批覆盖，不能把上次的表达式合并进来")
}

// TestBridge_SetSubscription_EmptyMeansAll 空 topics + 空 domains = 订阅全部。
func TestBridge_SetSubscription_EmptyMeansAll(t *testing.T) {
	worker := &bridgeFakeWorker{subscribeResp: &workerpb.SetBridgeSubscriptionResponse{
		Success: true, Connected: true, AppliedTopics: []string{},
	}}
	svc, inst, _ := newBridgeFixture(t, worker)

	applied, err := svc.SetSubscription(inst.ID, []string{}, []string{})
	require.NoError(t, err)
	assert.Empty(t, applied)
	// 下发的 topics 必须是空列表（探针据此 clear → 全量上报），且不能凭空补一个 `*`。
	assert.Empty(t, worker.lastSubscribeReq.Topics, "空数组表示订阅全部，不能补通配表达式")
}

// TestBridge_SetSubscription_DomainPassThrough domains 原样透传给 Worker，
// 由 Worker 展开成 `<domain>.*`（探针侧过滤维度只有 topic）。展开逻辑的单测在
// internal/worker/grpc/plugin_bridge_meta_test.go。
func TestBridge_SetSubscription_DomainPassThrough(t *testing.T) {
	worker := &bridgeFakeWorker{subscribeResp: &workerpb.SetBridgeSubscriptionResponse{
		Success: true, Connected: true, AppliedTopics: []string{"activity.spring", "economy.*"},
	}}
	svc, inst, _ := newBridgeFixture(t, worker)

	applied, err := svc.SetSubscription(inst.ID, []string{"activity.spring"}, []string{"economy"})
	require.NoError(t, err)
	assert.Equal(t, []string{"activity.spring"}, worker.lastSubscribeReq.Topics,
		"topics 原样透传")
	assert.Equal(t, []string{"economy"}, worker.lastSubscribeReq.Domains,
		"domains 原样透传，展开成通配是 Worker 侧的职责")
	assert.Equal(t, []string{"activity.spring", "economy.*"}, applied,
		"返回给前端的是探针的生效集合（已展开）")
}

// TestBridge_SetSubscription_ProbeOffline 探针未连 → ErrBridgeProbeUnavailable（路由回 409）。
func TestBridge_SetSubscription_ProbeOffline(t *testing.T) {
	worker := &bridgeFakeWorker{subscribeResp: &workerpb.SetBridgeSubscriptionResponse{
		Success: false, Connected: false, Error: "探针未连接",
	}}
	svc, inst, _ := newBridgeFixture(t, worker)

	_, err := svc.SetSubscription(inst.ID, []string{"activity.*"}, nil)
	require.ErrorIs(t, err, ErrBridgeProbeUnavailable)
}

// TestBridge_BusinessStatus_CacheTTL 域状态 30s 缓存：TTL 内直接命中，过期后重新拉。
func TestBridge_BusinessStatus_CacheTTL(t *testing.T) {
	worker := &bridgeFakeWorker{bizStatusResp: &workerpb.QueryBusinessStatusResponse{
		Success:     true,
		Connected:   true,
		FetchedAtMs: time.Now().UnixMilli(),
		StatusJson: `{"domains":{"activity":{"available":true,"manifestDigest":"d1",
		               "lastDispatchAt":1700000000,"dispatchCount":12,"failureCount":1}}}`,
	}}
	svc, inst, _ := newBridgeFixture(t, worker)
	// 固定时钟便于精确推进 TTL。
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	first, err := svc.QueryBusinessStatus(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, worker.bizStatusCalls)
	assert.False(t, first.Stale)
	require.Len(t, first.Domains, 1)
	assert.Equal(t, "activity", first.Domains[0].Domain)
	assert.Equal(t, "d1", first.Domains[0].ManifestDigest)
	assert.Equal(t, int64(12), first.Domains[0].DispatchCount)

	// TTL 内（29s）：命中缓存，不再打 Worker。
	now = now.Add(29 * time.Second)
	second, err := svc.QueryBusinessStatus(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, worker.bizStatusCalls, "TTL 内应命中缓存")
	assert.False(t, second.Stale)

	// 跨过 TTL（再 +2s = 31s）：重新拉取。
	now = now.Add(2 * time.Second)
	third, err := svc.QueryBusinessStatus(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, worker.bizStatusCalls, "超过 TTL 应重新拉取")
	assert.False(t, third.Stale)
}

// TestBridge_BusinessStatus_StaleFallback 探针不可达时返回最后已知 + stale=true。
func TestBridge_BusinessStatus_StaleFallback(t *testing.T) {
	worker := &bridgeFakeWorker{bizStatusResp: &workerpb.QueryBusinessStatusResponse{
		Success:     true,
		Connected:   true,
		FetchedAtMs: time.Now().UnixMilli(),
		StatusJson:  `{"domains":{"economy":{"available":false,"degradedReason":"PLUGIN_DISABLED"}}}`,
	}}
	svc, inst, _ := newBridgeFixture(t, worker)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	ok, err := svc.QueryBusinessStatus(inst.ID)
	require.NoError(t, err)
	require.Len(t, ok.Domains, 1)

	// 探针掉线：此后查询返回 success=true/connected=false。
	worker.bizStatusResp = &workerpb.QueryBusinessStatusResponse{
		Success: true, Connected: false, FetchedAtMs: time.Now().UnixMilli(),
	}
	now = now.Add(2 * BusinessStatusCacheTTL) // 越过 TTL，强制走实时查询

	stale, err := svc.QueryBusinessStatus(inst.ID)
	require.NoError(t, err)
	assert.True(t, stale.Stale, "探针不可达时应回退最后已知并置 stale")
	require.Len(t, stale.Domains, 1, "stale 回退必须保住上次已知的域清单")
	assert.Equal(t, "economy", stale.Domains[0].Domain)
	assert.Equal(t, "PLUGIN_DISABLED", stale.Domains[0].DegradedReason)
	assert.False(t, stale.ProbeConnected)
	assert.NotEmpty(t, stale.Error, "应说明为什么是 stale")
}

// TestBridge_BusinessStatus_NeverCached 从未成功过且本次也失败：200 + 空域 + stale，不报错。
func TestBridge_BusinessStatus_NeverCached(t *testing.T) {
	svc, inst, _ := newBridgeFixture(t, nil) // 节点未连
	res, err := svc.QueryBusinessStatus(inst.ID)
	require.NoError(t, err)
	assert.True(t, res.Stale)
	assert.Empty(t, res.Domains)
	assert.False(t, res.ProbeConnected)
	assert.NotEmpty(t, res.Error)
}

// TestBridge_BusinessStatus_CacheIsIsolated 缓存写入后调用方修改返回值不污染缓存。
func TestBridge_BusinessStatus_CacheIsIsolated(t *testing.T) {
	worker := &bridgeFakeWorker{bizStatusResp: &workerpb.QueryBusinessStatusResponse{
		Success: true, Connected: true, FetchedAtMs: time.Now().UnixMilli(),
		StatusJson: `{"domains":{"a":{"available":true}}}`,
	}}
	svc, inst, _ := newBridgeFixture(t, worker)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	first, err := svc.QueryBusinessStatus(inst.ID)
	require.NoError(t, err)
	require.Len(t, first.Domains, 1)
	first.Domains[0].Domain = "被调用方改掉了"

	second, _ := svc.QueryBusinessStatus(inst.ID)
	require.Len(t, second.Domains, 1)
	assert.Equal(t, "a", second.Domains[0].Domain, "缓存必须与调用方拿到的是独立副本")
}

// TestBridge_QueryStatus_RPCError 调用失败降级成 error 字段，不向上抛错。
func TestBridge_QueryStatus_RPCError(t *testing.T) {
	worker := &bridgeFakeWorker{statusErr: errors.New("boom")}
	svc, inst, _ := newBridgeFixture(t, worker)
	res, err := svc.QueryBridgeStatus(inst.ID)
	require.NoError(t, err)
	assert.False(t, res.Connected)
	assert.NotEmpty(t, res.Error)
}

// TestBridge_BusinessStatus_DomainsSorted 域清单按域名排序，渲染稳定。
func TestBridge_BusinessStatus_DomainsSorted(t *testing.T) {
	worker := &bridgeFakeWorker{bizStatusResp: &workerpb.QueryBusinessStatusResponse{
		Success: true, Connected: true, FetchedAtMs: time.Now().UnixMilli(),
		StatusJson: `{"domains":{"zeta":{"available":true},"alpha":{"available":true},"mid":{"available":true}}}`,
	}}
	svc, inst, _ := newBridgeFixture(t, worker)
	res, err := svc.QueryBusinessStatus(inst.ID)
	require.NoError(t, err)
	require.Len(t, res.Domains, 3)
	assert.Equal(t, []string{"alpha", "mid", "zeta"},
		[]string{res.Domains[0].Domain, res.Domains[1].Domain, res.Domains[2].Domain})
}

// TestBridge_BusinessStatus_InvalidatedOnProbeReconnect 探针连入时域状态缓存必须失效。
//
// 真机验收发现的自相矛盾：探针刚连上时，bridge/status（无缓存、实时查）已返回
// connected=true，而 business/status 的 30s 缓存还停在探针离线时写下的
// probeConnected=false + stale=true —— 同一实例两块面板同时展示就互相打脸。
//
// 修法：连入事件到来时清缓存，而不是在查询路径上每次反查连接态
//（后者会让 TTL 内的每次命中都多打一次 QueryBridgeStatus，代价过大）。
func TestBridge_BusinessStatus_InvalidatedOnProbeReconnect(t *testing.T) {
	worker := &bridgeFakeWorker{bizStatusResp: &workerpb.QueryBusinessStatusResponse{
		Success: true, Connected: true, FetchedAtMs: time.Now().UnixMilli(),
		StatusJson: `{"domains":{"activity":{"available":true,"manifestDigest":"d1"}}}`,
	}}
	svc, inst, _ := newBridgeFixture(t, worker)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	// 首次查询填充缓存。
	first, err := svc.QueryBusinessStatus(inst.ID)
	require.NoError(t, err)
	require.Len(t, first.Domains, 1)
	assert.Equal(t, 1, worker.bizStatusCalls)

	// TTL 内再查：命中缓存，不打 Worker（行为与既有 TTL 测试一致）。
	now = now.Add(10 * time.Second)
	if _, err := svc.QueryBusinessStatus(inst.ID); err != nil {
		t.Fatalf("TTL 内查询失败: %v", err)
	}
	assert.Equal(t, 1, worker.bizStatusCalls, "TTL 内应命中缓存，不应多打 Worker")

	// 模拟探针（重）连入：缓存必须被清掉。
	svc.InvalidateOnProbeConnected(inst.UUID)

	// 仍在 TTL 内，但缓存已失效 → 必须重新拉取。
	now = now.Add(10 * time.Second)
	third, err := svc.QueryBusinessStatus(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, worker.bizStatusCalls, "探针连入后应清缓存并重新拉取")
	assert.False(t, third.Stale)
	require.Len(t, third.Domains, 1)
}

// TestBridge_BusinessStatus_InvalidateUnknownUUIDNoPanic 未知/空 UUID 清缓存不得 panic。
func TestBridge_BusinessStatus_InvalidateUnknownUUIDNoPanic(t *testing.T) {
	worker := &bridgeFakeWorker{bizStatusResp: &workerpb.QueryBusinessStatusResponse{
		Success: true, Connected: true, FetchedAtMs: time.Now().UnixMilli(),
		StatusJson: `{"domains":{}}`,
	}}
	svc, _, _ := newBridgeFixture(t, worker)
	assert.NotPanics(t, func() {
		svc.InvalidateOnProbeConnected("")
		svc.InvalidateOnProbeConnected("no-such-uuid")
	})
}
