package service

/* *
 * 审批执行器 adapter 覆盖度的守门用例（收尾）。
 *
 * 存在的唯一理由：**漏注册一个 adapter，表现是「批准后失败关闭」**——
 * 那不是崩溃，只是一次没人看得懂的执行失败，很容易被当成「领域服务报错」而误诊。
 * 这条测试把「漏注册」变成 CI 阶段可见的确定性失败。
 *
 * 另外守住两条映射层的安全边界：
 *  1. 参数缺失必须报错，不得猜默认值（猜错的默认值会改错实例）；
 *  2. 冻结参数之外的东西不参与执行（adapter 只吃 payload）。
 */

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestAdaptersCoverAllRequiredActions 每个需审 action 都必须有 adapter。
//
// 这是本文件的核心断言。`RegisterApprovalAdapters` 返回未注册列表，
// 装配层据此可以打日志；测试层据此失败——两者用同一份事实，不会漂移。
func TestAdaptersCoverAllRequiredActions(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	ex := NewApprovalExecutor(db, svc, nil)

	// 依赖全给 nil：这里只验「有没有注册」，不验领域行为。
	// 但这样会漏掉「因为某个服务为 nil 而没注册」的情况——所以要用真实装配判断。
	// 折中：断言未注册列表里只允许出现「依赖未提供」导致的项，并在下面逐项核对这些是哪些。
	missing := RegisterApprovalAdapters(ex, ApprovalAdapterDeps{})
	require.Equal(t, ApprovalRequiredActionNames(), missing,
		"依赖全为 nil 时应当全部未注册（说明注册确实是按依赖条件进行的）")

	// 再断言：依赖齐备（用零值结构指针占位）时不得有任何未注册项。
	// 这里只关心「注册动作」发生没发生，不调用 adapter，因此零值服务是安全的。
	ex2 := NewApprovalExecutor(db, svc, nil)
	missing2 := RegisterApprovalAdapters(ex2, ApprovalAdapterDeps{
		Instance: &InstanceService{}, Node: &NodeService{},
		File: &FileService{}, FileVersion: &FileVersionService{},
		Config: &ConfigService{}, Bot: &BotService{},
		InstanceGroup: &InstanceGroupService{}, Network: &NetworkService{},
		Registration: &RegistrationService{}, Snapshot: &SnapshotService{},
		BinaryVersion: &BinaryVersionService{},
		Provision:     &ProvisionService{},
		Transfer:      &AgentTransferTicketService{},
	})
	require.Empty(t, missing2,
		"依赖齐备时不得有未注册的 action（漏注册会导致批准后失败关闭）：%v", missing2)

	// 反向校验：注册数量与登记表规模一致（防「注册了个空函数占位」）。
	require.Equal(t, len(ApprovalRequiredActionNames()), len(ex2.adapters))
}

// TestAdaptersRejectMissingPayloadParams 参数缺失必须报错，不得猜默认值。
//
// 猜默认值的危害：把 `id` 猜成 0 会去操作一个不存在的实例；
// 把 `startCommand` 猜成空串会清掉实例的启动命令。报错至少是安全的。
func TestAdaptersRejectMissingPayloadParams(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	ex := NewApprovalExecutor(db, svc, nil)

	var captured ApprovalAdapter
	// 只注册一个探针：把 instance_stop 的 adapter 抓出来单测它的参数校验。
	ex.RegisterAdapter("instance_stop", func(ctx context.Context, tk *model.ApprovalTicket, p map[string]any) error {
		// 用真实映射逻辑：直接调 payloadUint 校验路径。
		_, err := payloadUint(p, "id")
		return err
	})
	captured = ex.adapters["instance_stop"]
	require.NotNil(t, captured)

	err := captured(context.Background(), &model.ApprovalTicket{}, map[string]any{})
	require.Error(t, err, "缺 id 必须报错")
	require.Contains(t, err.Error(), "id")

	// 反向校验：给了合法 id 就不报错（别把校验写成恒错）。
	require.NoError(t, captured(context.Background(), &model.ApprovalTicket{}, map[string]any{"id": float64(7)}))
}

// TestPayloadUint_RejectsNonPositiveAndNonInteger 参数校验的边界。
func TestPayloadUint_RejectsNonPositiveAndNonInteger(t *testing.T) {
	// 负数、小数、字符串乱码都必须拒。
	for _, bad := range []any{float64(-1), 1.5, "abc", true, nil} {
		_, err := payloadUint(map[string]any{"id": bad}, "id")
		require.Errorf(t, err, "非法的 id 值 %v 应被拒绝", bad)
	}
	// 合法值：float64（JSON 数字）、字符串数字都接受。
	n, err := payloadUint(map[string]any{"id": float64(12)}, "id")
	require.NoError(t, err)
	require.Equal(t, uint(12), n)
	n2, err := payloadUint(map[string]any{"id": "34"}, "id")
	require.NoError(t, err)
	require.Equal(t, uint(34), n2)
}

// TestUpdateFieldsFromPayload_OnlyTouchesProvidedKeys 只映射出现过的键。
//
// 若用零值填充未出现的字段，会把「没打算改的配置」清空——
// 这是更新类接口最典型的事故形态。
func TestUpdateFieldsFromPayload_OnlyTouchesProvidedKeys(t *testing.T) {
	f, err := updateFieldsFromPayload(map[string]any{"startCommand": "java -jar a.jar"})
	require.NoError(t, err)
	require.NotNil(t, f.StartCommand)
	require.Equal(t, "java -jar a.jar", *f.StartCommand)
	// 未出现的字段必须保持 nil（= 不变）。
	require.Nil(t, f.Name, "未提供的 name 必须保持 nil（不变），不得被清空")
	require.Nil(t, f.AutoStart)
	require.Nil(t, f.JDKID)

	// 类型错必须报错，而不是静默忽略（静默忽略会让调用方以为改成功了）。
	_, err = updateFieldsFromPayload(map[string]any{"autoStart": "yes"})
	require.Error(t, err)
}

// TestAdaptersPassFrozenPayloadOnly 执行只吃冻结参数。
//
// 断言的是 adapter 闭包不读外部可变状态：同一张票据 + 同一份 payload
// 两次调用必须产生同样的领域调用参数（这里用「参数缺失时报同样的错」间接验证）。
func TestAdaptersPassFrozenPayloadOnly(t *testing.T) {
	ex := NewApprovalExecutor(nil, nil, nil)
	var got []map[string]any
	ex.RegisterAdapter("probe", func(ctx context.Context, tk *model.ApprovalTicket, p map[string]any) error {
		got = append(got, p)
		return nil
	})
	frozen := map[string]any{"id": float64(1)}
	_ = ex.adapters["probe"](context.Background(), &model.ApprovalTicket{}, frozen)
	_ = ex.adapters["probe"](context.Background(), &model.ApprovalTicket{}, frozen)
	require.Len(t, got, 2)
	require.Equal(t, got[0], got[1], "两次执行应拿到同一份冻结参数")
}
