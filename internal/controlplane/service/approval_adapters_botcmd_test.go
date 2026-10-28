package service

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// bot_send_command 批准后的下发载荷（FR-492 剩余项的可证部分）。
//
// ## 这条用例解决的到底是什么
//
// 真机验收要求「批准后命令真正送达并生效」，而**生效**必须有真实 Minecraft
// 服务器与在线 Bot——那是部署环境才能给的证据，单测无法替代，也不该假装能替代。
//
// 但**送达**这一半是可证的：批准 → 执行器 → adapter → BotService →
// CP→Worker 的 `SendBotCommand` RPC。用假 Worker 客户端把这条链路走到边界，
// 断言「RPC 被调用、且 botUuid 与 command 与票据冻结内容完全一致」。
// 这样一旦哪天 adapter 解析参数写错（例如把 id 当成 uuid，或丢掉 command），
// CI 会直接红，而不是等真机才发现命令发到了别的 bot 上。
//
// 换句话说：这条用例守的是「送到门口且货是对的」，不声称「屋里已经生效」。

// fakeWorkerSendBotCommand 捕获 SendBotCommand 的入参，其余方法不实现（panic 即暴露越界调用）。
type fakeWorkerSendBotCommand struct {
	workerpb.WorkerServiceClient
	gotUUID string
	gotCmd  string
	calls   int
	err     error
}

func (f *fakeWorkerSendBotCommand) SendBotCommand(
	_ context.Context, in *workerpb.SendBotCommandRequest, _ ...grpc.CallOption,
) (*workerpb.SendBotCommandResponse, error) {
	f.calls++
	f.gotUUID = in.GetBotUuid()
	f.gotCmd = in.GetCommand()
	if f.err != nil {
		return nil, f.err
	}
	// 必须置 Success=true：生产路径以 `resp.Success` 判定成功，
	// 空响应会被当成「Worker 拒绝执行」，用例就测不到送达本身。
	return &workerpb.SendBotCommandResponse{Success: true}, nil
}

// newBotCommandTestRig 装配「一个已连接 Worker 的节点 + 一个 Bot」的最小环境。
func newBotCommandTestRig(t *testing.T) (*gorm.DB, *BotService, *model.Bot, *model.Node) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}

	// 关闭底层连接（共享内存库在最后一个连接关闭后才释放，见 testdb_test.go）。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.Bot{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	node := &model.Node{
		UUID: "node-uuid-botcmd", Name: "n1", Host: "127.0.0.1",
		Status: model.NodeStatusOnline,
	}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("建节点失败: %v", err)
	}
	inst := &model.Instance{
		NodeID: node.ID, Name: "bot-target", Type: model.InstanceTypeGeneric,
		ProcessType: model.ProcessTypeDirect, StartCommand: "echo", Status: model.InstanceStatusStopped,
	}
	if err := db.Create(inst).Error; err != nil {
		t.Fatalf("建实例失败: %v", err)
	}
	bot := &model.Bot{
		UUID: "bot-uuid-1", InstanceID: inst.ID, Name: "probe-bot", Status: model.BotStatusError,
	}
	if err := db.Create(bot).Error; err != nil {
		t.Fatalf("建 Bot 失败: %v", err)
	}
	pool := cpgrpc.NewClientPool()
	svc := NewBotService(db, pool)
	return db, svc, bot, node
}

// TestBotSendCommandAdapter_DeliversExactPayload 批准后的 adapter 必须把冻结的命令原样送达 Worker。
func TestBotSendCommandAdapter_DeliversExactPayload(t *testing.T) {
	_, svc, bot, node := newBotCommandTestRig(t)
	fake := &fakeWorkerSendBotCommand{}
	svc.pool.SetWorkerClientForTest(node.UUID, fake)

	ex := NewApprovalExecutor(nil, nil, nil)
	var capturedErr error
	RegisterApprovalAdapters(ex, ApprovalAdapterDeps{Bot: svc})

	adapter := ex.adapters["bot_send_command"]
	if adapter == nil {
		t.Fatal("bot_send_command 必须已登记 adapter（未登记时批准会失败关闭）")
	}
	// 票据的 payload 就是出票时冻结的那份：{id, command}。
	ticket := &model.ApprovalTicket{Action: "bot_send_command", TargetType: "bot"}
	capturedErr = adapter(context.Background(), ticket, map[string]any{
		"id": float64(bot.ID), "command": "say hello-from-approval",
	})
	if capturedErr != nil {
		t.Fatalf("假 Worker 在线时不应失败: %v", capturedErr)
	}
	if fake.calls != 1 {
		t.Fatalf("SendBotCommand 应被调用 1 次，实际 %d", fake.calls)
	}
	// 用 bot.UUID 而不是数据库 id：RPC 以 uuid 定位 Bot，发错就是「命令发给别的 bot」。
	if fake.gotUUID != bot.UUID {
		t.Fatalf("RPC botUuid 应为 %q，实际 %q", bot.UUID, fake.gotUUID)
	}
	if fake.gotCmd != "say hello-from-approval" {
		t.Fatalf("RPC command 应原样送达，实际 %q", fake.gotCmd)
	}
}

// TestBotSendCommandAdapter_PropagatesWorkerFailure 下游失败必须原样上抛。
//
// 执行器据这个 error 把票据落 failed 并写回执；若 adapter 吞掉错误，
// 票据会显示成功而命令其实没发出去——那是最坏的一种谎报。
func TestBotSendCommandAdapter_PropagatesWorkerFailure(t *testing.T) {
	_, svc, bot, node := newBotCommandTestRig(t)
	wantErr := errors.New("worker 拒绝了命令")
	svc.pool.SetWorkerClientForTest(node.UUID, &fakeWorkerSendBotCommand{err: wantErr})

	ex := NewApprovalExecutor(nil, nil, nil)
	RegisterApprovalAdapters(ex, ApprovalAdapterDeps{Bot: svc})
	err := ex.adapters["bot_send_command"](context.Background(),
		&model.ApprovalTicket{Action: "bot_send_command"},
		map[string]any{"id": float64(bot.ID), "command": "say x"})
	if err == nil {
		t.Fatal("下游失败必须上抛，不能当成成功")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("应保留原始错误链（便于分类），实际 %v", err)
	}
}

// TestBotSendCommandAdapter_RejectsMissingCommand 冻结载荷缺命令时拒绝，而不是发空命令。
//
// 空命令打到 Mineflayer 会变成一次无意义甚至报错的交互；宁可执行失败。
func TestBotSendCommandAdapter_RejectsMissingCommand(t *testing.T) {
	_, svc, bot, node := newBotCommandTestRig(t)
	fake := &fakeWorkerSendBotCommand{}
	svc.pool.SetWorkerClientForTest(node.UUID, fake)

	ex := NewApprovalExecutor(nil, nil, nil)
	RegisterApprovalAdapters(ex, ApprovalAdapterDeps{Bot: svc})
	err := ex.adapters["bot_send_command"](context.Background(),
		&model.ApprovalTicket{Action: "bot_send_command"},
		map[string]any{"id": float64(bot.ID)})
	if err == nil {
		t.Fatal("缺 command 应拒绝")
	}
	if fake.calls != 0 {
		t.Fatalf("缺命令时不得下发 RPC，实际调用 %d 次", fake.calls)
	}
}
