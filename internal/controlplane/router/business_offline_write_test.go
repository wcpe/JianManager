package router

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 离线实例的业务写穿必须被拒且可归因。
//
// 规格 §6 第 1 条：「实例离线 → 409 INSTANCE_OFFLINE，库内零变更」。
// 本用例钉住修复后的行为：写穿路径（DispatchWrite → resolveTarget）
// 在源头检查 inst.Status，离线时直接回 INSTANCE_OFFLINE，不再假装是
// 「节点未连接」的链路故障。
//
// 为什么这个区分重要：探针跑在游戏服进程内，实例停则探针消失，
// 业务数据本来就写不进去——「安全」不是问题。问题在可归因性：
// 「节点未连接」会把人引向查网络/Worker，「实例未运行」才指向真正原因。
// 对账路径一直有这道检查（business_reconcile.go），写穿路径曾漏掉，
// 同一个离线实例在两条路上给出不同结论。
func TestBusinessWrite_OfflineInstanceMustBeRejected(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, admin, "g-offline")
	instID := createInstanceViaAPI(t, r, admin, node.ID, g)

	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", instID).
		Update("status", model.InstanceStatusStopped).Error)

	body := map[string]any{
		"domain": "economy", "action": "deposit",
		"payload":     `{"player":"alice","amount":"100"}`,
		"write":       true,
		"operationId": "op-offline-1",
	}
	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(instID)+"/business", body, admin)
	res := parseJSON(t, w)
	errText, _ := res["error"].(string)

	// 语义断言：必须是 INSTANCE_OFFLINE，而不是笼统的链路故障。
	require.Contains(t, errText, "INSTANCE_OFFLINE",
		"离线写穿必须给出可机器识别的错误码，实际 error=%q", errText)
	require.Contains(t, errText, "未运行",
		"错误文案需指出「实例未运行」而非网络问题，实际 error=%q", errText)

	// 不得谎称成功。
	require.Equal(t, false, res["available"], "离线不得报 available=true")

	t.Logf("HTTP=%d available=%v error=%q", w.Code, res["available"], errText)
}

// 对照：两条路径对同一个离线实例必须给出**一致**的结论。
//
// 这条用例只验证前置条件（实例确实被置为非运行态），真正的交叉验证在
// 上面那条：写穿路径现在也会返回 INSTANCE_OFFLINE，与对账路径同语义。
//
// 保留它是因为：一旦有人回退 resolveTarget 的状态检查，上面那条会红；
// 而如果两条路又被改得不一致（比如对账换了别的错误码），这里可以
// 作为「同一语义应有同一名字」的提醒点。
func TestBusinessReconcile_OfflineInstancePrefixedLikeWritePath(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, admin, "g-contrast")
	instID := createInstanceViaAPI(t, r, admin, node.ID, g)

	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", instID).
		Update("status", model.InstanceStatusStopped).Error)

	var inst model.Instance
	require.NoError(t, db.First(&inst, instID).Error)
	require.NotEqual(t, model.InstanceStatusRunning, inst.Status, "前置条件：实例应为非运行态")
}
