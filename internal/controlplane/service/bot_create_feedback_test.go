package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	grpcapi "google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// failingCreateBotWorker CreateBot 恒返回 Success=false（模拟 bot-worker 依赖未装/node 缺失），
// 其余复用 fakeFR038BotWorker 的成功桩。
type failingCreateBotWorker struct {
	fakeFR038BotWorker
	reason string
}

func (w *failingCreateBotWorker) CreateBot(context.Context, *workerpb.CreateBotRequest, ...grpcapi.CallOption) (*workerpb.CreateBotResponse, error) {
	return &workerpb.CreateBotResponse{Success: false, Error: w.reason}, nil
}

// TestBotCreate_DelegateFailureSurfacesInBot 真机回归：委托 Worker 失败（如「bot 依赖未安装」）
// 此前只 slog.Warn 吞掉、API 照发 201，bot 永卡 pending、两侧零反馈。修复后失败必须写进
// Bot 本身（status=error + lastError）随创建响应带回，前端据此显示可操作原因。
func TestBotCreate_DelegateFailureSurfacesInBot(t *testing.T) {
	db := newFR038BotScaleDB(t)
	pool := cpgrpc.NewClientPool()
	svc := NewBotService(db, pool)
	node := createFR038Node(t, db, "节点A", "node-feedback-a")
	inst := createFR038Instance(t, db, node.ID, "生存服", 25565)

	const reason = "启动 bot-worker 失败: bot 依赖未安装：请到节点『全局包管理』安装 mineflayer 与 mineflayer-pathfinder"
	pool.SetWorkerClientForTest(node.UUID, &failingCreateBotWorker{reason: reason})

	bot, err := svc.Create(CreateBotRequest{InstanceID: inst.ID, Name: "DeadBot", Config: `{"auth":"offline"}`, Behavior: "idle"})
	require.NoError(t, err, "记录创建不因委托失败而报错（保留可重连）")
	require.Equal(t, model.BotStatusError, bot.Status, "委托失败必须反映在返回的 Bot 状态上")
	require.Contains(t, bot.LastError, "bot 依赖未安装", "失败原因必须随响应带回")

	var fromDB model.Bot
	require.NoError(t, db.First(&fromDB, bot.ID).Error)
	require.Equal(t, model.BotStatusError, fromDB.Status)
	require.Contains(t, fromDB.LastError, "mineflayer", "失败原因必须落库供行内展示")
}

// TestBotCreate_DelegateSuccessClearsLastError 委托成功路径：状态置 connecting 且 lastError 清空
// （覆盖「上次失败→依赖装好重连成功」翻篇）。
func TestBotCreate_DelegateSuccessClearsLastError(t *testing.T) {
	db := newFR038BotScaleDB(t)
	pool := cpgrpc.NewClientPool()
	svc := NewBotService(db, pool)
	node := createFR038Node(t, db, "节点B", "node-feedback-b")
	inst := createFR038Instance(t, db, node.ID, "空岛服", 25566)
	pool.SetWorkerClientForTest(node.UUID, &fakeFR038BotWorker{})

	bot, err := svc.Create(CreateBotRequest{InstanceID: inst.ID, Name: "OkBot", Config: `{"auth":"offline"}`, Behavior: "idle"})
	require.NoError(t, err)
	require.Equal(t, model.BotStatusConnecting, bot.Status)
	require.Empty(t, bot.LastError)

	var fromDB model.Bot
	require.NoError(t, db.First(&fromDB, bot.ID).Error)
	require.Equal(t, model.BotStatusConnecting, fromDB.Status)
	require.Empty(t, fromDB.LastError)
}

// TestAccumulateStressBotOutcome 压测结果账本累计纯函数。
func TestAccumulateStressBotOutcome(t *testing.T) {
	s, f := accumulateStressBotOutcome(0, 0, true)
	require.Equal(t, 1, s)
	require.Equal(t, 0, f)
	s, f = accumulateStressBotOutcome(s, f, false)
	require.Equal(t, 1, s)
	require.Equal(t, 1, f)
}
