package router

import (
	"encoding/json"
	"net/http"
	"testing"
)

// 平台管理员可列出表清单，至少包含 users 表。
func TestDBBrowse_ListTables_AdminOK(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/db/tables", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Tables []struct {
			Name     string `json:"name"`
			RowCount int64  `json:"rowCount"`
		} `json:"tables"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	found := false
	for _, ti := range resp.Tables {
		if ti.Name == "users" {
			found = true
		}
	}
	if !found {
		t.Fatalf("表清单缺少 users 表: %+v", resp.Tables)
	}
}

// 普通成员访问数据库浏览端点应被拒绝（403）。
func TestDBBrowse_ListTables_MemberForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getMemberToken(t, r, "member1", "password123")

	w := makeRequest(r, "GET", "/api/v1/db/tables", nil, token)
	if w.Code != http.StatusForbidden {
		t.Fatalf("期望 403，实际 %d: %s", w.Code, w.Body.String())
	}
}

// 无 token 访问应被拒绝（401）。
func TestDBBrowse_NoToken(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	w := makeRequest(r, "GET", "/api/v1/db/tables", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("期望 401，实际 %d: %s", w.Code, w.Body.String())
	}
}

// 平台管理员查询 users 表行：password 列被脱敏，username 正常返回。
func TestDBBrowse_Rows_AdminMasksPassword(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r) // setup 已创建 admin 用户（含 password）

	w := makeRequest(r, "GET", "/api/v1/db/tables/users/rows", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Table   string `json:"table"`
		Columns []struct {
			Name      string `json:"name"`
			Sensitive bool   `json:"sensitive"`
		} `json:"columns"`
		Rows  []map[string]interface{} `json:"rows"`
		Total int64                    `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Total < 1 || len(resp.Rows) < 1 {
		t.Fatalf("期望至少 1 行用户，实际 total=%d rows=%d", resp.Total, len(resp.Rows))
	}
	// password 列脱敏。
	if got := resp.Rows[0]["password"]; got != "******" {
		t.Fatalf("password 应脱敏为 ******，实际 %v", got)
	}
	if resp.Rows[0]["username"] != "admin" {
		t.Fatalf("username 应为 admin，实际 %v", resp.Rows[0]["username"])
	}
	// 列定义里 password 标记敏感。
	sensitiveMarked := false
	for _, c := range resp.Columns {
		if c.Name == "password" && c.Sensitive {
			sensitiveMarked = true
		}
	}
	if !sensitiveMarked {
		t.Fatalf("password 列应标记 sensitive=true")
	}
}

// 平台管理员查询不存在的表应返回 404 TABLE_NOT_FOUND。
func TestDBBrowse_Rows_UnknownTable404(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/db/tables/no_such_table/rows", nil, token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d: %s", w.Code, w.Body.String())
	}
}

// 行查询分页/排序：插入多行后按 page/pageSize 切片，按 id 降序排序生效。
func TestDBBrowse_Rows_PaginationSort(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	// setup 已建 1 个 admin；再补几个节点行（nodes 表无敏感展示限制，便于分页观察）。
	for i := 0; i < 4; i++ {
		createTestNodeWithSuffix(t, db, "node-pg-"+string(rune('a'+i)))
	}

	w := makeRequest(r, "GET", "/api/v1/db/tables/nodes/rows?page=1&pageSize=2&sort=id&order=desc", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Rows     []map[string]interface{} `json:"rows"`
		Page     int                      `json:"page"`
		PageSize int                      `json:"pageSize"`
		Total    int64                    `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.PageSize != 2 || len(resp.Rows) != 2 {
		t.Fatalf("期望每页 2 行，实际 pageSize=%d rows=%d", resp.PageSize, len(resp.Rows))
	}
	if resp.Total < 4 {
		t.Fatalf("期望至少 4 个节点，实际 total=%d", resp.Total)
	}
}
