package botdist

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/context"
	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// buildArchive 构造与 embed-botworker 同构的测试归档（常规文件，路径可含子目录）。
func buildArchive(t *testing.T, files map[string]string) (data []byte, sha string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// fakeClient 只覆写 FetchBotWorkerArchive 的 WorkerServiceClient 替身；
// 记录每次请求携带的 known_sha256，按 CP 语义（指纹一致回空归档）应答。
type fakeClient struct {
	workerpb.WorkerServiceClient
	archive []byte
	sha     string
	err     error
	success bool
	respErr string
	// lastKnown 最近一次请求的 known_sha256；bytesServed 归档字节被下发的次数。
	lastKnown   string
	bytesServed int
}

func (f *fakeClient) FetchBotWorkerArchive(_ context.Context, req *workerpb.FetchBotWorkerArchiveRequest, _ ...grpc.CallOption) (*workerpb.FetchBotWorkerArchiveResponse, error) {
	f.lastKnown = req.KnownSha256
	if f.err != nil {
		return nil, f.err
	}
	if !f.success {
		return &workerpb.FetchBotWorkerArchiveResponse{Success: false, Error: f.respErr}, nil
	}
	resp := &workerpb.FetchBotWorkerArchiveResponse{Success: true, Sha256: f.sha, Version: "test"}
	if req.KnownSha256 != f.sha {
		resp.Archive = f.archive
		f.bytesServed++
	}
	return resp, nil
}

func testOptions(t *testing.T, c workerpb.WorkerServiceClient) Options {
	t.Helper()
	base := t.TempDir()
	return Options{
		NodeUUID: "u", NodeSecret: "s",
		Dir:               filepath.Join(base, "opt", "bot-worker"),
		GlobalNodeModules: filepath.Join(base, "global-nm"),
		client:            c,
	}
}

// TestEnsure_FreshDownloadExtractsAndLinks 首次自愈：下载、校验、物化、建 node_modules 链接（FR-308）。
func TestEnsure_FreshDownloadExtractsAndLinks(t *testing.T) {
	data, sha := buildArchive(t, map[string]string{
		"index.js":     "console.log('bot')",
		"package.json": `{"type":"module"}`,
		"ipc/types.js": "export {}",
	})
	fc := &fakeClient{archive: data, sha: sha, success: true}
	opts := testOptions(t, fc)

	entry, err := Ensure(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if entry != filepath.Join(opts.Dir, "index.js") {
		t.Fatalf("入口路径不对: %s", entry)
	}
	for _, rel := range []string{"index.js", "package.json", filepath.Join("ipc", "types.js")} {
		if _, err := os.Stat(filepath.Join(opts.Dir, rel)); err != nil {
			t.Fatalf("物化缺文件 %s: %v", rel, err)
		}
	}
	if got := readLocalSHA(opts.Dir); got != sha {
		t.Fatalf("本地清单指纹不对: %q != %q", got, sha)
	}
	// node_modules 链接功能性验证（机制无关：symlink 与 Windows junction 均适用）：
	// 往全局目录放探针文件，穿过链接可读到即证 Node 解析路径成立。
	if err := os.WriteFile(filepath.Join(opts.GlobalNodeModules, "probe.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(opts.Dir, "node_modules", "probe.txt"))
	if err != nil || string(got) != "hi" {
		t.Fatalf("穿过 node_modules 链接读探针失败: %v", err)
	}
}

// TestEnsure_SameShaSkipsRedownload 指纹一致：第二次自愈不再传归档字节（FR-308 省流语义）。
func TestEnsure_SameShaSkipsRedownload(t *testing.T) {
	data, sha := buildArchive(t, map[string]string{"index.js": "x"})
	fc := &fakeClient{archive: data, sha: sha, success: true}
	opts := testOptions(t, fc)

	if _, err := Ensure(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if fc.lastKnown != sha {
		t.Fatalf("第二次应上报本地指纹 %q，实报 %q", sha, fc.lastKnown)
	}
	if fc.bytesServed != 1 {
		t.Fatalf("归档字节应只下发 1 次，实际 %d", fc.bytesServed)
	}
}

// TestEnsure_FallbackToLocal CP 不可达/未内嵌：本地有副本沿用、无副本给可操作错误（FR-308 降级）。
func TestEnsure_FallbackToLocal(t *testing.T) {
	data, sha := buildArchive(t, map[string]string{"index.js": "x"})
	good := &fakeClient{archive: data, sha: sha, success: true}
	opts := testOptions(t, good)
	if _, err := Ensure(context.Background(), opts); err != nil {
		t.Fatal(err)
	}

	// CP 拉取报错 → 沿用本地。
	opts.client = &fakeClient{err: errors.New("unavailable")}
	entry, err := Ensure(context.Background(), opts)
	if err != nil || entry == "" {
		t.Fatalf("本地有副本应降级沿用: %v", err)
	}

	// CP 未内嵌（Success=false）→ 同样沿用本地。
	opts.client = &fakeClient{success: false, respErr: "未内嵌"}
	if _, err := Ensure(context.Background(), opts); err != nil {
		t.Fatalf("CP 未内嵌应降级沿用本地: %v", err)
	}

	// 本地无副本且拉取失败 → 明确报错。
	fresh := testOptions(t, &fakeClient{err: errors.New("unavailable")})
	if _, err := Ensure(context.Background(), fresh); err == nil || !strings.Contains(err.Error(), "不可用") {
		t.Fatalf("无副本且拉取失败应报错，实际: %v", err)
	}
}

// TestEnsure_ShaMismatchRejected 传输校验：字节与宣称指纹不符拒绝物化（FR-308）。
func TestEnsure_ShaMismatchRejected(t *testing.T) {
	data, _ := buildArchive(t, map[string]string{"index.js": "x"})
	fc := &fakeClient{archive: data, sha: strings.Repeat("0", 64), success: true}
	opts := testOptions(t, fc)
	if _, err := Ensure(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("指纹不符应拒绝，实际: %v", err)
	}
	if _, err := os.Stat(opts.Dir); !os.IsNotExist(err) {
		t.Fatalf("校验失败不应留下物化目录")
	}
}

// TestExtractTarGz_RejectsTraversal 路径穿越条目拒绝解压（防御纵深；正常归档来自 CP 构建期）。
func TestExtractTarGz_RejectsTraversal(t *testing.T) {
	data, _ := buildArchive(t, map[string]string{"../evil.js": "x"})
	if err := extractTarGz(data, t.TempDir()); err == nil || !strings.Contains(err.Error(), "非法路径") {
		t.Fatalf("路径穿越应被拒，实际: %v", err)
	}
}
