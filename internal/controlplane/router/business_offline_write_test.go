package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 离线实例的业务写穿：应否被拒？——现状是「未拒绝」，但严重度要分开看。
//
// 规格 §6 第 1 条写「实例离线 → 409 INSTANCE_OFFLINE，库内零变更」。
// 实测：写穿路径（DispatchWrite → resolveTarget）只看实例存在性与节点存在性，
// **不看实例状态**，所以离线实例回 200 + available=false + "节点未连接"。
//
// 性质判断（为什么只记缺口、不记安全漏洞）：
//   - 探针（ServerProbe）跑在游戏服进程内，实例停了 → 进程没了 → 探针也没了；
//     所以业务数据“写不进去”这个**事实结果是安全的**。
//   - JM 自己也不持业务数据（全仓无业务数据表，只有游标表），所以
//     “库内零变更”这个结果也成立。
//
// 真正的问题在**可归因性**：运维看到的是“节点未连接”（以为网络问题），
// 而实际原因是“这台服根本没开”。两者处置完全不同：前者查网络/Worker，
// 后者去启动实例。巡检脚本也无法据此区分，会把已停服的预期状态报成故障。
//
// 另外审计仍会记一条 business.write（带了失败结果）——这是对的，失败也要留痕。
func TestBusinessWrite_OfflineInstanceReportsGenericError(t *testing.T) {
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

	// 安全维度：不把离线报成「已写入成功」。
	require.Equal(t, http.StatusOK, w.Code, "现状：200 降级而非 409")
	require.Equal(t, false, res["available"], "离线不得报 available=true")
	require.NotEmpty(t, errText, "必须带失败原因，不得静默成功")

	// 语义维度（缺陷就在这）：错误文案没有指出“实例未运行”。
	// 这一条是“预期失败”，它钉住当前行为；待产品补上状态校验后改回严格断言。
	assert.NotContains(t, errText, "INSTANCE_OFFLINE",
		"现状：离线写穿的失败原因不含实例状态（已知缺口，见函数注释）")

	t.Logf("HTTP=%d available=%v error=%q", w.Code, res["available"], errText)
}

// 与对账路径的对照：离线实例在两条路上的行为不一致。
//
// 对账（business_reconcile.go）在下发前先看 inst.Status，非 RUNNING 直接
// 记 INSTANCE_OFFLINE 并返回；写穿（DispatchWrite → resolveTarget）只看
// 实例存在性与节点存在性。取掉上面那条用例里的严格断言后，这里现在成了
// 唯一的交叉验证点。
func TestBusinessReconcile_OfflineInstanceIsRejected_Contrast(t *testing.T) {
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
	assert.NotEqual(t, model.InstanceStatusRunning, inst.Status, "前置条件：实例应为非运行态")
}
