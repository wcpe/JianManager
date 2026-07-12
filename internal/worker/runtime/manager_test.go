package runtime

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

// fakeIndexJSON 模拟 nodejs.org/dist/index.json（新→旧排序、混杂多条 major、lts 字段两种形态）。
const fakeIndexJSON = `[
  {"version":"v24.4.0","date":"2025-07-08","lts":false},
  {"version":"v22.17.0","date":"2025-06-24","lts":"Jod"},
  {"version":"v20.19.3","date":"2025-06-10","lts":"Iron"},
  {"version":"v22.16.0","date":"2025-05-21","lts":"Jod"}
]`

func TestResolveNodeVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(fakeIndexJSON))
	}))
	defer srv.Close()

	tests := []struct {
		name    string
		major   int
		want    string
		wantErr bool
	}{
		{"取该 major 最新版", 22, "22.17.0", false},
		{"另一 major", 20, "20.19.3", false},
		{"index 无此 major", 18, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveNodeVersion(http.DefaultClient, srv.URL, tt.major)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("应失败，实际返回 %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("want %q got %q", tt.want, got)
			}
		})
	}
}

func TestResolveNodeVersion_IndexUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := resolveNodeVersion(http.DefaultClient, srv.URL, 22); err == nil {
		t.Fatal("index.json 不可达应失败")
	}
}

// nodeExeRelPath 返回当前平台归档内 node 可执行文件的相对路径（nodejs 官方归档布局）。
func nodeExeRelPath(version, arch string) string {
	top := fmt.Sprintf("node-v%s-%s-%s", version, nodeOSName(), arch)
	if goruntime.GOOS == "windows" {
		return top + "/node.exe" // win zip：node.exe 在顶层目录根，无 bin/
	}
	return top + "/bin/node"
}

// buildFakeArchive 按当前平台构造假 Node 归档（windows=zip / 其它=tar.gz），布局同官方。
func buildFakeArchive(t *testing.T, version, arch string) []byte {
	t.Helper()
	rel := nodeExeRelPath(version, arch)
	var buf bytes.Buffer
	if goruntime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		f, err := zw.Create(rel)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("stub-node")); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("stub-node")
	if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newFakeDistServer 起假 nodejs dist 服务器：/index.json + /v<ver>/node-v<ver>-<os>-<arch>.<ext>。
func newFakeDistServer(t *testing.T, version, arch string) *httptest.Server {
	t.Helper()
	archive := buildFakeArchive(t, version, arch)
	archivePath := fmt.Sprintf("/v%s/node-v%s-%s-%s.%s", version, version, nodeOSName(), arch, nodeArchiveExt())
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			_, _ = w.Write([]byte(fakeIndexJSON))
		case archivePath:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestInstallNodeJS_HappyPath 下载+解压+完成标记（node 可执行文件）全链路（httptest 假归档服务器）。
func TestInstallNodeJS_HappyPath(t *testing.T) {
	srv := newFakeDistServer(t, "22.17.0", "x64")
	defer srv.Close()
	root := t.TempDir()
	m := NewManager(filepath.Join(root, "runtimes"))

	var lines []string
	info, err := m.InstallNodeJS(context.Background(), 22, "x64", srv.URL, func(percent int, line string) {
		if line != "" {
			lines = append(lines, line)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Type != "nodejs" || info.Major != 22 || info.Version != "22.17.0" || info.Arch != "x64" || !info.Managed {
		t.Fatalf("Info 字段不符: %+v", info)
	}
	if info.Name != "Node.js 22" {
		t.Fatalf("展示名不符: %q", info.Name)
	}
	// path = node 可执行文件绝对路径（与扫描器 nodejs 候选同语义），位于托管目录 nodejs-22 下。
	if _, statErr := os.Stat(info.Path); statErr != nil {
		t.Fatalf("完成标记 node 可执行文件应存在: %v", statErr)
	}
	wantDir := filepath.Join(root, "runtimes", "nodejs-22")
	if rel, relErr := filepath.Rel(wantDir, info.Path); relErr != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("path 应位于托管目录 %s 下，实际 %s", wantDir, info.Path)
	}
	if len(lines) == 0 {
		t.Fatal("应有进度阶段日志")
	}
}

// TestInstallNodeJS_ResidueDirAutoCleared 上次失败遗留的残骸目录（无完成标记）自动清除重装（FR-291）。
func TestInstallNodeJS_ResidueDirAutoCleared(t *testing.T) {
	srv := newFakeDistServer(t, "22.17.0", "x64")
	defer srv.Close()
	root := t.TempDir()
	m := NewManager(filepath.Join(root, "runtimes"))
	residue := filepath.Join(root, "runtimes", "nodejs-22")
	if err := os.MkdirAll(residue, 0o755); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(residue, "half-downloaded.tmp")
	if err := os.WriteFile(junk, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	info, err := m.InstallNodeJS(context.Background(), 22, "x64", srv.URL, nil)
	if err != nil {
		t.Fatalf("残骸目录不应堵死重装: %v", err)
	}
	if _, statErr := os.Stat(junk); !os.IsNotExist(statErr) {
		t.Fatal("残骸内容应被清除")
	}
	if _, statErr := os.Stat(info.Path); statErr != nil {
		t.Fatalf("重装后完成标记应存在: %v", statErr)
	}
}

// TestInstallNodeJS_CompleteDirRejected 完好已装目录（有 node 完成标记）拒绝覆盖（FR-291 语义不变）。
func TestInstallNodeJS_CompleteDirRejected(t *testing.T) {
	root := t.TempDir()
	m := NewManager(filepath.Join(root, "runtimes"))
	dir := filepath.Join(root, "runtimes", "nodejs-22")
	marker := filepath.Join(dir, filepath.FromSlash(nodeExeRelPath("22.17.0", "x64")))
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := m.InstallNodeJS(context.Background(), 22, "x64", "http://127.0.0.1:0", nil)
	if err == nil || !strings.Contains(err.Error(), "目标目录已存在") {
		t.Fatalf("完好目录应拒绝覆盖，实际: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("完好目录内容不得被动: %v", statErr)
	}
}

// TestInstallNodeJS_StallAborted 下载流卡死（连续无字节进展）由看门狗中断并归为「下载停滞」（FR-290）。
func TestInstallNodeJS_StallAborted(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.json" {
			_, _ = w.Write([]byte(fakeIndexJSON))
			return
		}
		// 写少量字节后挂起，模拟代理断流。
		_, _ = w.Write([]byte("partial"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	m := NewManager(filepath.Join(root, "runtimes"))
	m.stall, m.interval = 120*time.Millisecond, 20*time.Millisecond

	_, err := m.InstallNodeJS(context.Background(), 22, "x64", srv.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "下载停滞") {
		t.Fatalf("应产出下载停滞专属错误，实际: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "runtimes", "nodejs-22")); !os.IsNotExist(statErr) {
		t.Fatal("失败后半截安装目录应被清理")
	}
}

// TestRemove_NestedPathClearsTopLevelDir 删除按登记的内层路径（…/bin/node）时归一到
// 托管根下顶层子目录整体删除，不留父壳（FR-292）。
func TestRemove_NestedPathClearsTopLevelDir(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	inner := filepath.Join(root, "nodejs-22", "node-v22.17.0-linux-x64", "bin")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(inner, "node")
	if err := os.WriteFile(exe, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := m.Remove(exe); err != nil {
		t.Fatalf("删除内层路径应成功: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "nodejs-22")); !os.IsNotExist(err) {
		t.Fatal("顶层目录 nodejs-22 应被整体清除，不留父壳")
	}
}

// TestRemove_RootItselfRejected 托管根本身不得作为删除目标（FR-292）。
func TestRemove_RootItselfRejected(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	keep := filepath.Join(root, "nodejs-20")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(root); err == nil {
		t.Fatal("删除托管根本身应被拒绝")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("托管根内容不得被动: %v", err)
	}
}

// TestRemove_OutsideRootRejected 托管根之外的路径拒绝删除（只删托管目录下的运行时）。
func TestRemove_OutsideRootRejected(t *testing.T) {
	root := t.TempDir()
	m := NewManager(filepath.Join(root, "runtimes"))
	outside := filepath.Join(root, "elsewhere", "node")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(outside); err == nil {
		t.Fatal("托管根外路径应被拒绝")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("根外文件不得被动: %v", err)
	}
}
