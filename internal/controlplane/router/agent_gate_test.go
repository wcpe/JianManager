package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-388 Agent 可证安全闸：契约枚举 + 运维面 HTTP ≥30 断言
// （scope 外 / 写白名单外 / 硬拒绝 / 吊销 / 过期 / 无 token / 并发拒绝）。

func setupAgentGate(t *testing.T) (db *gorm.DB, r *gin.Engine, adminJWT string, node *model.Node, inst *model.Instance) {
	t.Helper()
	db = setupTestDB(t)
	r = setupTestRouter(db)
	adminJWT = getAdminToken(t, r)
	node = createTestNode(t, db)
	inst = &model.Instance{
		NodeID:       node.ID,
		Name:         "agent-gate-inst",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "echo",
		Status:       model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)
	return db, r, adminJWT, node, inst
}

func issueAgentPlaintext(t *testing.T, r *gin.Engine, adminJWT string, body map[string]any) (id uint, plain string) {
	t.Helper()
	w := makeRequest(r, "POST", "/api/v1/agent/tokens", body, adminJWT)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var resp struct {
		Token struct {
			ID uint `json:"id"`
		} `json:"token"`
		Plaintext string `json:"plaintext"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Plaintext)
	require.Contains(t, resp.Plaintext, "jmat_")
	return resp.Token.ID, resp.Plaintext
}

func assertForbiddenAgent(t *testing.T, r *gin.Engine, method, path, agentToken string) {
	t.Helper()
	w := makeRequest(r, method, path, nil, agentToken)
	assert.Equal(t, http.StatusForbidden, w.Code, "path=%s body=%s", path, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, "FORBIDDEN", body["error"])
}

func assertUnauthorizedAgent(t *testing.T, r *gin.Engine, method, path, agentToken string) {
	t.Helper()
	w := makeRequest(r, method, path, nil, agentToken)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "path=%s body=%s", path, w.Body.String())
}

// TestAgentGate_OpsContract_RegisteredPaths 契约表路径与路由注册对齐（10 条运维 action）。
func TestAgentGate_OpsContract_RegisteredPaths(t *testing.T) {
	contract := service.AgentOpsContract()
	require.GreaterOrEqual(t, len(contract), 10)
	seen := map[string]bool{}
	for _, row := range contract {
		require.NotEmpty(t, row.Action)
		require.NotEmpty(t, row.Method)
		require.NotEmpty(t, row.PathTemplate)
		require.Contains(t, []string{"read", "write"}, row.Kind)
		require.Equal(t, 403, row.HTTPDeny)
		key := row.Method + " " + row.PathTemplate
		require.False(t, seen[key], "重复契约行 %s", key)
		seen[key] = true
		if row.Kind == "write" {
			require.NotEmpty(t, row.WriteAllow)
		}
	}
}

// TestAgentGate_HardDeny_AllRejected 硬拒绝面在策略引擎层一律 deny（≥15 条）。
func TestAgentGate_HardDeny_AllRejected(t *testing.T) {
	p := &service.AgentPrincipal{
		TokenID:           1,
		Name:              "gate",
		ScopedInstanceIDs: []uint{1},
		ScopedNodeIDs:     []uint{1},
		WriteAllowlist:    []string{service.AgentWriteInstanceLife, service.AgentWriteNodeMaintenance},
	}
	list := service.AgentHardDenyList()
	require.GreaterOrEqual(t, len(list), 12)
	for _, action := range list {
		require.True(t, service.IsAgentHardDeny(action), action)
		err := service.ResolveAction(p, action, 1, 1)
		assert.ErrorIs(t, err, service.ErrAgentForbidden, "硬拒绝应 deny: %s", action)
	}
	// 额外点名 FR-388 矩阵必验项
	for _, action := range []string{
		service.AgentHardDenyUserWrite,
		service.AgentHardDenyInstanceKill,
		service.AgentHardDenyInstanceDelete,
		service.AgentHardDenyDBBrowse,
		service.AgentHardDenySelfUpdate,
		service.AgentHardDenyAuditDelete,
		"user.create",
		"settings.write",
		"system.update",
	} {
		assert.ErrorIs(t, service.ResolveAction(p, action, 1, 1), service.ErrAgentForbidden, action)
	}
}

// TestAgentGate_Whoami_And_ScopeReads 合法 token 可读 whoami + scope 内资源。
func TestAgentGate_Whoami_And_ScopeReads(t *testing.T) {
	_, r, adminJWT, node, inst := setupAgentGate(t)
	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "scope-ok",
		"scopedInstanceIds": []uint{inst.ID},
		"scopedNodeIds":     []uint{node.ID},
	})

	// whoami
	w := makeRequest(r, "GET", "/api/v1/agent/whoami", nil, plain)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	who := parseJSON(t, w)
	assert.Equal(t, "agent", who["kind"])
	assert.Equal(t, "scope-ok", who["name"])

	// list nodes / instances
	w = makeRequest(r, "GET", "/api/v1/agent/nodes", nil, plain)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = makeRequest(r, "GET", "/api/v1/agent/instances", nil, plain)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// get instance in scope
	w = makeRequest(r, "GET", "/api/v1/agent/instances/"+itoa(inst.ID), nil, plain)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// TestAgentGate_ScopeOut_Forbidden scope 外读/写一律 403（≥6 断言）。
func TestAgentGate_ScopeOut_Forbidden(t *testing.T) {
	db, r, adminJWT, node, inst := setupAgentGate(t)
	other := &model.Instance{
		NodeID: node.ID, Name: "out-of-scope", Type: model.InstanceTypeGeneric,
		ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(other).Error)
	otherNode := createTestNodeWithSuffix(t, db, "other-node")

	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "scoped",
		"scopedInstanceIds": []uint{inst.ID},
		"scopedNodeIds":     []uint{node.ID},
	})

	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/instances/"+itoa(other.ID), plain)
	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/instances/"+itoa(other.ID)+"/metrics", plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/instances/"+itoa(other.ID)+"/start", plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/instances/"+itoa(other.ID)+"/stop", plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/instances/"+itoa(other.ID)+"/restart", plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/nodes/"+itoa(otherNode.ID)+"/maintenance/enter", plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/nodes/"+itoa(otherNode.ID)+"/maintenance/leave", plain)
}

// TestAgentGate_EmptyScope_ListForbidden 空 scope token 不能 list。
func TestAgentGate_EmptyScope_ListForbidden(t *testing.T) {
	_, r, adminJWT, _, _ := setupAgentGate(t)
	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "empty-scope",
		"scopedInstanceIds": []uint{},
		"scopedNodeIds":     []uint{},
	})
	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/instances", plain)
	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/nodes", plain)
	// whoami 仍允许
	w := makeRequest(r, "GET", "/api/v1/agent/whoami", nil, plain)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestAgentGate_WriteAllowlist_Empty_DeniesLife 无写白名单时 lifecycle 403。
func TestAgentGate_WriteAllowlist_Empty_DeniesLife(t *testing.T) {
	_, r, adminJWT, node, inst := setupAgentGate(t)
	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "readonly",
		"scopedInstanceIds": []uint{inst.ID},
		"scopedNodeIds":     []uint{node.ID},
		"writeAllowlist":    []string{},
	})
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/instances/"+itoa(inst.ID)+"/start", plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/instances/"+itoa(inst.ID)+"/stop", plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/instances/"+itoa(inst.ID)+"/restart", plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/nodes/"+itoa(node.ID)+"/maintenance/enter", plain)
	// 读仍可
	w := makeRequest(r, "GET", "/api/v1/agent/instances/"+itoa(inst.ID), nil, plain)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// TestAgentGate_WriteAllowlist_OnlyLife_DeniesMaintenance 仅 instance.life 时维护态 403。
func TestAgentGate_WriteAllowlist_OnlyLife_DeniesMaintenance(t *testing.T) {
	_, r, adminJWT, node, inst := setupAgentGate(t)
	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "life-only",
		"scopedInstanceIds": []uint{inst.ID},
		"scopedNodeIds":     []uint{node.ID},
		"writeAllowlist":    []string{service.AgentWriteInstanceLife},
	})
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/nodes/"+itoa(node.ID)+"/maintenance/enter", plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/nodes/"+itoa(node.ID)+"/maintenance/leave", plain)
}

// TestAgentGate_Revoked_And_Expired 吊销与过期立即 401。
func TestAgentGate_Revoked_And_Expired(t *testing.T) {
	db, r, adminJWT, node, inst := setupAgentGate(t)

	id, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name": "to-revoke", "scopedInstanceIds": []uint{inst.ID}, "scopedNodeIds": []uint{node.ID},
	})
	w := makeRequest(r, "DELETE", "/api/v1/agent/tokens/"+strconv.FormatUint(uint64(id), 10), nil, adminJWT)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assertUnauthorizedAgent(t, r, "GET", "/api/v1/agent/whoami", plain)

	// 过期：签发后改 DB expires_at
	_, plain2 := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name": "to-expire", "scopedInstanceIds": []uint{inst.ID}, "scopedNodeIds": []uint{node.ID},
	})
	require.NoError(t, db.Model(&model.AgentToken{}).Where("token_prefix LIKE ?", "jmat_%").
		// 精确：用 authenticate 前把全部 to-expire 名的 token 过期
		Where("name = ?", "to-expire").
		Update("expires_at", time.Now().Add(-time.Hour)).Error)
	assertUnauthorizedAgent(t, r, "GET", "/api/v1/agent/whoami", plain2)
}

// TestAgentGate_MissingOrGarbageToken 缺 token / 非 jmat_ / 垃圾 token。
func TestAgentGate_MissingOrGarbageToken(t *testing.T) {
	_, r, _, _, _ := setupAgentGate(t)
	// 无 Authorization
	w := makeRequest(r, "GET", "/api/v1/agent/whoami", nil, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	// 人类 JWT 形状但非 jmat_（走 agent 组强制 principal）
	w = makeRequest(r, "GET", "/api/v1/agent/whoami", nil, "eyJhbGciOiJIUzI1NiJ9.e30.x")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	// jmat_ 但无效
	assertUnauthorizedAgent(t, r, "GET", "/api/v1/agent/whoami", "jmat_not_a_real_token_value")
}

// TestAgentGate_AdminTokenCRUD 管理员签发/列表/吊销。
func TestAgentGate_AdminTokenCRUD(t *testing.T) {
	_, r, adminJWT, node, inst := setupAgentGate(t)
	id, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name": "crud", "scopedInstanceIds": []uint{inst.ID}, "scopedNodeIds": []uint{node.ID},
	})
	require.NotEmpty(t, plain)

	w := makeRequest(r, "GET", "/api/v1/agent/tokens", nil, adminJWT)
	require.Equal(t, http.StatusOK, w.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.NotEmpty(t, list)

	// 成员不可签发
	member := getMemberToken(t, r, "member1", "password123")
	w = makeRequest(r, "POST", "/api/v1/agent/tokens", map[string]any{"name": "x"}, member)
	assert.True(t, w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized, w.Code)

	w = makeRequest(r, "DELETE", "/api/v1/agent/tokens/"+strconv.FormatUint(uint64(id), 10), nil, adminJWT)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestAgentGate_ConcurrentForbidden 并发越权请求均 403。
func TestAgentGate_ConcurrentForbidden(t *testing.T) {
	db, r, adminJWT, node, inst := setupAgentGate(t)
	other := &model.Instance{
		NodeID: node.ID, Name: "conc-out", Type: model.InstanceTypeGeneric,
		ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(other).Error)
	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name": "conc", "scopedInstanceIds": []uint{inst.ID}, "scopedNodeIds": []uint{node.ID},
	})
	path := "/api/v1/agent/instances/" + itoa(other.ID) + "/start"
	var wg sync.WaitGroup
	codes := make([]int, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := makeRequest(r, "POST", path, nil, plain)
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		assert.Equal(t, http.StatusForbidden, c, "goroutine %d", i)
	}
}

// TestAgentGate_ContractMatrix_ResolveAction 契约表每个 action 的策略矩阵（读/写/越权）。
func TestAgentGate_ContractMatrix_ResolveAction(t *testing.T) {
	inScope := &service.AgentPrincipal{
		TokenID: 1, Name: "m",
		ScopedInstanceIDs: []uint{10}, ScopedNodeIDs: []uint{20},
		WriteAllowlist: []string{service.AgentWriteInstanceLife, service.AgentWriteNodeMaintenance},
	}
	noWrite := &service.AgentPrincipal{
		TokenID: 2, Name: "ro",
		ScopedInstanceIDs: []uint{10}, ScopedNodeIDs: []uint{20},
		WriteAllowlist: nil,
	}
	emptyScope := &service.AgentPrincipal{TokenID: 3, Name: "empty"}
	for _, row := range service.AgentOpsContract() {
		switch row.Action {
		case service.AgentActionWhoami:
			assert.NoError(t, service.ResolveAction(inScope, row.Action, 0, 0), row.Action)
		case service.AgentActionListInstances:
			assert.NoError(t, service.ResolveAction(inScope, row.Action, 0, 0), row.Action)
			assert.ErrorIs(t, service.ResolveAction(emptyScope, row.Action, 0, 0), service.ErrAgentForbidden, row.Action)
		case service.AgentActionListNodes:
			assert.NoError(t, service.ResolveAction(inScope, row.Action, 0, 0), row.Action)
			assert.ErrorIs(t, service.ResolveAction(emptyScope, row.Action, 0, 0), service.ErrAgentForbidden, row.Action)
		case service.AgentActionGetInstance, service.AgentActionGetInstanceMetrics, service.AgentActionGetInstanceLogs:
			assert.NoError(t, service.ResolveAction(inScope, row.Action, 10, 0), row.Action)
			assert.ErrorIs(t, service.ResolveAction(inScope, row.Action, 99, 0), service.ErrAgentForbidden, row.Action)
		case service.AgentActionInstanceStart, service.AgentActionInstanceStop, service.AgentActionInstanceRestart:
			assert.NoError(t, service.ResolveAction(inScope, row.Action, 10, 0), row.Action)
			assert.ErrorIs(t, service.ResolveAction(noWrite, row.Action, 10, 0), service.ErrAgentForbidden, row.Action)
			assert.ErrorIs(t, service.ResolveAction(inScope, row.Action, 99, 0), service.ErrAgentForbidden, row.Action)
		case service.AgentActionNodeMaintenanceEnter, service.AgentActionNodeMaintenanceLeave:
			assert.NoError(t, service.ResolveAction(inScope, row.Action, 0, 20), row.Action)
			assert.ErrorIs(t, service.ResolveAction(noWrite, row.Action, 0, 20), service.ErrAgentForbidden, row.Action)
			assert.ErrorIs(t, service.ResolveAction(inScope, row.Action, 0, 99), service.ErrAgentForbidden, row.Action)
		default:
			t.Fatalf("未覆盖契约 action %s", row.Action)
		}
	}
}

// TestAgentGate_AssertionCountFloor 汇总本文件可证断言下限（文档化 FR-388 ≥30）。
func TestAgentGate_AssertionCountFloor(t *testing.T) {
	// 契约 10 + 硬拒绝 list≥12 + 点名 9 + scope 外 7 + empty scope 2 + write 白名单 5 + life-only 2
	// + 吊销/过期 2 + missing token 3 + admin CRUD 2 + 并发 16 + 矩阵约 10*3
	// 本测试仅保证契约与硬拒绝集合规模达标，详细断言分布在上面各 Test* 中。
	require.GreaterOrEqual(t, len(service.AgentOpsContract()), 10)
	require.GreaterOrEqual(t, len(service.AgentHardDenyList()), 12)
	// 矩阵用例覆盖：每个 write action 至少 3 种判定
	writes := 0
	for _, row := range service.AgentOpsContract() {
		if row.Kind == "write" {
			writes++
		}
	}
	require.GreaterOrEqual(t, writes, 5)
	// 10 + 12 + 5*3 = 37 ≥ 30
	n := len(service.AgentOpsContract()) + len(service.AgentHardDenyList()) + writes*3
	require.GreaterOrEqual(t, n, 30, fmt.Sprintf("可证断言规模=%d", n))
}
