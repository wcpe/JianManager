package router

import (
	"encoding/json"
	"net/http"
	"testing"
)

// FR-107 更新器 jar 端点：版本/可用性查询、组件校验、鉴权。
// 断言**与构建期是否内嵌 jar 无关**（CI 干净检出无 jar / 本地 make embed-client-updater 后有 jar 均须通过）：
// Info 不断言 available 的具体值；下载据 Info 的 available 判 200（真字节）或 404（未内嵌兜底）。

func TestClientUpdaterJars_Info(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/client-dist/updater-jars", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("Info 应 200，实 %d (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Version string `json:"version"`
		Wedge   struct {
			Available bool `json:"available"`
			Size      int  `json:"size"`
		} `json:"wedge"`
		Core struct {
			Available bool `json:"available"`
			Size      int  `json:"size"`
		} `json:"core"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if resp.Version == "" {
		t.Error("应返回内嵌版本号")
	}
	// 可用时 size 必为正；不可用时 size 应为 0（结构自洽，不断言是否内嵌）。
	if resp.Wedge.Available != (resp.Wedge.Size > 0) {
		t.Errorf("wedge available 与 size 不自洽: %+v", resp.Wedge)
	}
	if resp.Core.Available != (resp.Core.Size > 0) {
		t.Errorf("core available 与 size 不自洽: %+v", resp.Core)
	}
}

func TestClientUpdaterJars_DownloadInvalidComponent(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/client-dist/updater-jars/bogus", nil, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法组件应 400，实 %d (%s)", w.Code, w.Body.String())
	}
}

func TestClientUpdaterJars_DownloadMatchesAvailability(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	// 先取可用性，再据此判下载结果（内嵌→200 真字节；未内嵌→404 兜底）。
	info := makeRequest(r, "GET", "/api/v1/client-dist/updater-jars", nil, token)
	var resp struct {
		Wedge struct{ Available bool } `json:"wedge"`
		Core  struct{ Available bool } `json:"core"`
	}
	if err := json.Unmarshal(info.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Info 解析失败: %v", err)
	}
	cases := map[string]bool{"wedge": resp.Wedge.Available, "core": resp.Core.Available}
	for comp, available := range cases {
		w := makeRequest(r, "GET", "/api/v1/client-dist/updater-jars/"+comp, nil, token)
		if available {
			if w.Code != http.StatusOK {
				t.Errorf("%s 已内嵌应 200，实 %d", comp, w.Code)
			}
			if w.Body.Len() == 0 {
				t.Errorf("%s 已内嵌下载体不应为空", comp)
			}
			if cd := w.Header().Get("Content-Disposition"); cd == "" {
				t.Errorf("%s 下载应带 Content-Disposition", comp)
			}
		} else if w.Code != http.StatusNotFound {
			t.Errorf("%s 未内嵌应 404，实 %d", comp, w.Code)
		}
	}
}

func TestClientUpdaterJars_RequiresAuth(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	w := makeRequest(r, "GET", "/api/v1/client-dist/updater-jars", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401，实 %d", w.Code)
	}
}
