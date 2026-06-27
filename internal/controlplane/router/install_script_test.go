package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInstallScript_LinuxServedAnonymously 复现并回归 BUG-B「一键安装失效」：
// 一键命令拼 `curl <cp>/install-worker.sh | sh`，但 CP 若不托管该路径则 curl 404、安装失败。
// CP 必须匿名（无 JWT，token 在命令参数里）以 200 返回脚本内容（FR-080，见 ADR-020 §2 CP 静态托管）。
func TestInstallScript_LinuxServedAnonymously(t *testing.T) {
	db := setupTestDB(t)
	r := setupEnrollRouter(t, db)

	req := httptest.NewRequest("GET", "/install-worker.sh", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /install-worker.sh 应匿名 200，得到 status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	// 必须是真实安装脚本（含 POSIX shebang + 关键参数），而非 SPA index.html 回退。
	if !strings.HasPrefix(body, "#!/bin/sh") {
		t.Fatalf("响应非安装脚本（缺 #!/bin/sh shebang），疑似命中 SPA 回退: 前 80 字节=%q", head(body, 80))
	}
	if !strings.Contains(body, "--control-plane") || !strings.Contains(body, "JIANMANAGER_ENROLL_TOKEN") {
		t.Fatalf("安装脚本内容不完整（缺关键参数）: 前 120 字节=%q", head(body, 120))
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/") && !strings.Contains(ct, "application/") {
		t.Fatalf("脚本 Content-Type 应为文本类，得到 %q", ct)
	}
}

// TestInstallScript_WindowsServedAnonymously 同上，PowerShell 一键命令拉 `iwr <cp>/install-worker.ps1 | iex`。
func TestInstallScript_WindowsServedAnonymously(t *testing.T) {
	db := setupTestDB(t)
	r := setupEnrollRouter(t, db)

	req := httptest.NewRequest("GET", "/install-worker.ps1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /install-worker.ps1 应匿名 200，得到 status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "function Install-JianManagerWorker") {
		t.Fatalf("响应非 PowerShell 安装脚本（缺 Install-JianManagerWorker 函数），疑似命中 SPA 回退: 前 120 字节=%q", head(body, 120))
	}
}

// head 截取字符串前 n 字节（调试用）。
func head(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
