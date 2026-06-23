package router

import (
	"net/http"
	"testing"
)

// 概览端点对平台管理员返回 FHS 子目录清单（含 cache、artifacts）与归档分布字段。
func TestStorageOverview_AdminOK(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/storage/overview", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	dirs, ok := resp["dirs"].([]interface{})
	if !ok || len(dirs) == 0 {
		t.Fatalf("响应缺少 dirs: %v", resp)
	}
	if _, ok := resp["archive"]; !ok {
		t.Fatalf("响应缺少 archive: %v", resp)
	}
	// cache 目录应标记 clearable=true。
	var sawCache bool
	for _, d := range dirs {
		dm, _ := d.(map[string]interface{})
		if dm["label"] == "cache" {
			sawCache = true
			if dm["clearable"] != true {
				t.Fatalf("cache 应可清理: %v", dm)
			}
		}
	}
	if !sawCache {
		t.Fatalf("dirs 中缺少 cache: %v", dirs)
	}
}

// 普通成员（非平台管理员）访问平台存储被拒（数据根仅平台管理员可见）。
func TestStorageOverview_MemberForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	_ = getAdminToken(t, r) // 先建管理员，使后续注册者为普通成员
	memberToken := getMemberToken(t, r, "member1", "password123")

	w := makeRequest(r, "GET", "/api/v1/storage/overview", nil, memberToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("期望 403，得到 %d: %s", w.Code, w.Body.String())
	}
}

// 列举端点对越界路径返回 400 INVALID_PATH。
func TestStorageFiles_PathEscapeRejected(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/storage/files?path=../../etc", nil, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，得到 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["error"] != "INVALID_PATH" {
		t.Fatalf("期望 INVALID_PATH，得到 %v", resp)
	}
}

// 列举端点对数据根本身（空 path）返回数组（FHS 顶层目录）。
func TestStorageFiles_RootListing(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/storage/files?path=", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %s", w.Code, w.Body.String())
	}
	arr := parseJSONArray(t, w)
	if len(arr) == 0 {
		t.Fatalf("数据根应至少含 FHS 顶层目录，得到空")
	}
}

// 清理缓存端点对平台管理员返回 removed 计数。
func TestStorageClearCache_AdminOK(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "POST", "/api/v1/storage/cache/clear", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if _, ok := resp["removed"]; !ok {
		t.Fatalf("响应缺少 removed: %v", resp)
	}
}

// 清理缓存端点对普通成员拒绝。
func TestStorageClearCache_MemberForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	_ = getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "member2", "password123")

	w := makeRequest(r, "POST", "/api/v1/storage/cache/clear", nil, memberToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("期望 403，得到 %d: %s", w.Code, w.Body.String())
	}
}
