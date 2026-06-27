package jdk

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFoojayDistribution(t *testing.T) {
	cases := map[string]string{
		"Temurin":   "temurin",
		"temurin":   "temurin",
		"Corretto":  "corretto",
		"Zulu":      "zulu",
		"Liberica":  "liberica",
		"Microsoft": "microsoft",
		"Semeru":    "semeru",
		"GraalVM":   "graalvm_community",
	}
	for in, want := range cases {
		if got := foojayDistribution(in); got != want {
			t.Errorf("foojayDistribution(%q) = %q, want %q", in, got, want)
		}
	}
	if got := foojayDistribution("totally-unknown-distro"); got != "totally-unknown-distro" {
		t.Errorf("未知发行版应原样透传，got %q", got)
	}
}

func TestFoojayArchOS(t *testing.T) {
	// foojay 用 aarch64/x64 与 windows/linux/macos；归一应稳定。
	if a := foojayArch("x64"); a != "x64" {
		t.Errorf("foojayArch x64 = %q", a)
	}
	if a := foojayArch("amd64"); a != "x64" {
		t.Errorf("foojayArch amd64 应归一为 x64, got %q", a)
	}
	if a := foojayArch("arm64"); a != "aarch64" {
		t.Errorf("foojayArch arm64 应归一为 aarch64, got %q", a)
	}
}

// fakeFoojay 返回一个最小 foojay disco /packages 响应。
func fakeFoojay(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/disco/v3.0/packages") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestFoojayResolveDownloadURL(t *testing.T) {
	srv := fakeFoojay(t, `{"result":[
		{"id":"abc","distribution":"liberica","major_version":21,"java_version":"21.0.4","archive_type":"tar.gz",
		 "links":{"pkg_download_redirect":"https://cdn.example.com/liberica-21.tar.gz"}}
	]}`)
	defer srv.Close()

	url, err := foojayResolveDownloadURL(srv.Client(), srv.URL, "liberica", 21, "", "x64", "tar.gz")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if url != "https://cdn.example.com/liberica-21.tar.gz" {
		t.Fatalf("解析出的下载 URL 不符: %q", url)
	}
}

func TestFoojayResolveEmptyResult(t *testing.T) {
	srv := fakeFoojay(t, `{"result":[]}`)
	defer srv.Close()
	if _, err := foojayResolveDownloadURL(srv.Client(), srv.URL, "liberica", 99, "", "x64", "tar.gz"); err == nil {
		t.Fatalf("空结果应返回错误")
	}
}

func TestFoojayCatalogParsesPackages(t *testing.T) {
	srv := fakeFoojay(t, `{"result":[
		{"distribution":"temurin","major_version":21,"java_version":"21.0.4","archive_type":"tar.gz","latest_build_available":true},
		{"distribution":"temurin","major_version":21,"java_version":"21.0.3","archive_type":"tar.gz","latest_build_available":false}
	]}`)
	defer srv.Close()

	pkgs, err := FoojayCatalog(srv.Client(), srv.URL, "temurin", 21, "x64")
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("应解析 2 个版本，got %d", len(pkgs))
	}
	if pkgs[0].JavaVersion != "21.0.4" || pkgs[0].MajorVersion != 21 {
		t.Fatalf("首个版本字段不符: %+v", pkgs[0])
	}
}

// TestBuildDownloadURLV_ExtendedVendorUsesFoojay 验证扩厂商（如 Liberica）经 foojay 解析。
func TestBuildDownloadURLV_ExtendedVendorUsesFoojay(t *testing.T) {
	srv := fakeFoojay(t, `{"result":[
		{"distribution":"liberica","major_version":17,"java_version":"17.0.12","archive_type":"tar.gz",
		 "links":{"pkg_download_redirect":"https://cdn.example.com/liberica-17.tar.gz"}}
	]}`)
	defer srv.Close()
	t.Setenv("JIANMANAGER_JDK_FOOJAY_BASE", srv.URL)

	u, err := buildDownloadURLV(srv.Client(), "Liberica", 17, "", "x64", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(u, "liberica-17.tar.gz") {
		t.Fatalf("Liberica 应经 foojay 解析: %q", u)
	}
}

// TestBuildDownloadURL_DirectVendorsUnchanged 验证 Temurin/Corretto 不带具体版本时仍走原直链回退。
func TestBuildDownloadURL_DirectVendorsUnchanged(t *testing.T) {
	if u, err := buildDownloadURLV(nil, "Temurin", 21, "", "x64", ""); err != nil || !strings.Contains(u, "api.adoptium.net") {
		t.Fatalf("Temurin 直链回退异常: url=%q err=%v", u, err)
	}
	if u, err := buildDownloadURLV(nil, "Corretto", 21, "", "x64", ""); err != nil || !strings.Contains(u, "corretto.aws") {
		t.Fatalf("Corretto 直链回退异常: url=%q err=%v", u, err)
	}
}
