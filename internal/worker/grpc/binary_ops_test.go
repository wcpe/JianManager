package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fetchBinaryStreamStub 捕获 FetchBinary 发出的帧，供断言进度与终态。
type fetchBinaryStreamStub struct {
	grpc.ServerStream
	frames []*workerpb.FetchBinaryProgress
}

func (s *fetchBinaryStreamStub) Send(p *workerpb.FetchBinaryProgress) error {
	s.frames = append(s.frames, p)
	return nil
}

func (s *fetchBinaryStreamStub) Context() context.Context { return context.Background() }

// lastFrame 返回终态帧（未收到任何帧时返回 nil）。
func (s *fetchBinaryStreamStub) lastFrame() *workerpb.FetchBinaryProgress {
	if len(s.frames) == 0 {
		return nil
	}
	return s.frames[len(s.frames)-1]
}

// newBinaryWorker 建起一个已注册实例的 Worker 服务与其工作目录。
func newBinaryWorker(t *testing.T) (*Server, *fetchBinaryStreamStub, string) {
	t.Helper()
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)

	const uuid = "22222222-2222-2222-2222-222222222222"
	workDir := filepath.Join(tmp, "inst")
	resp, err := srv.CreateInstance(context.Background(), &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid, Name: "binary", StartCommand: "./beacon", WorkDir: workDir, ProcessType: "daemon",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	return srv, &fetchBinaryStreamStub{}, workDir
}

// TestFetchBinary_URLDownloadAndVerify url 来源：Worker 直连下载、校验摘要、置可执行位（FR-441 验收项 2/7）。
func TestFetchBinary_URLDownloadAndVerify(t *testing.T) {
	srv, stream, workDir := newBinaryWorker(t)
	payload := []byte("#!/bin/sh\necho beacon\n")
	sum := sha256.Sum256(payload)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer ts.Close()

	err := srv.FetchBinary(&workerpb.FetchBinaryRequest{
		InstanceUuid: "22222222-2222-2222-2222-222222222222",
		SourceKind:   "url",
		DestFilename: "beacon-1.1.0-linux-amd64",
		DownloadUrl:  ts.URL,
		Sha256:       hex.EncodeToString(sum[:]),
		Executable:   true,
	}, stream)
	require.NoError(t, err)

	final := stream.lastFrame()
	require.NotNil(t, final, "必须收到终态帧")
	require.True(t, final.Done)
	require.True(t, final.Success, final.Error)
	require.Equal(t, int64(len(payload)), final.Size)
	require.Equal(t, hex.EncodeToString(sum[:]), final.Sha256)
	require.False(t, final.FromLocal, "远程下载不是本地上游")

	got, readErr := os.ReadFile(filepath.Join(workDir, "beacon-1.1.0-linux-amd64"))
	require.NoError(t, readErr)
	require.Equal(t, payload, got)

	// 原子落盘：不残留 .part 半成品。
	_, statErr := os.Stat(filepath.Join(workDir, "beacon-1.1.0-linux-amd64.part"))
	require.True(t, os.IsNotExist(statErr), "不应残留临时文件")

	if runtime.GOOS != "windows" {
		st, statErr := os.Stat(filepath.Join(workDir, "beacon-1.1.0-linux-amd64"))
		require.NoError(t, statErr)
		require.NotZero(t, st.Mode().Perm()&0o100, "应置属主可执行位，实际 %v", st.Mode().Perm())
	}
}

// TestFetchBinary_SHA256MismatchRemovesTarget 摘要不符必须删除落盘文件且失败
// （否则半截/被篡改的二进制会被当成有效文件启动，FR-441 验收项 2）。
func TestFetchBinary_SHA256MismatchRemovesTarget(t *testing.T) {
	srv, stream, workDir := newBinaryWorker(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("tampered-payload"))
	}))
	defer ts.Close()

	err := srv.FetchBinary(&workerpb.FetchBinaryRequest{
		InstanceUuid: "22222222-2222-2222-2222-222222222222",
		SourceKind:   "url", DestFilename: "beacon",
		DownloadUrl: ts.URL, Sha256: "deadbeef", Executable: true,
	}, stream)
	require.NoError(t, err)

	final := stream.lastFrame()
	require.NotNil(t, final)
	require.True(t, final.Done)
	require.False(t, final.Success)
	require.Contains(t, final.Error, "sha256 校验不符")

	_, statErr := os.Stat(filepath.Join(workDir, "beacon"))
	require.True(t, os.IsNotExist(statErr), "校验失败的文件必须删除")
}

// TestFetchBinary_HTTPErrorFails 非 200 响应即失败（含 404 等），不落任何文件。
func TestFetchBinary_HTTPErrorFails(t *testing.T) {
	srv, stream, workDir := newBinaryWorker(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	err := srv.FetchBinary(&workerpb.FetchBinaryRequest{
		InstanceUuid: "22222222-2222-2222-2222-222222222222",
		SourceKind:   "url", DestFilename: "beacon", DownloadUrl: ts.URL, Executable: true,
	}, stream)
	require.NoError(t, err)
	require.False(t, stream.lastFrame().Success)
	require.Contains(t, stream.lastFrame().Error, "HTTP 404")

	_, statErr := os.Stat(filepath.Join(workDir, "beacon"))
	require.True(t, os.IsNotExist(statErr))
}

// TestFetchBinary_NodeFileCopy node_file 来源：就地复制到工作目录并置可执行位（FR-441 验收项 3）。
func TestFetchBinary_NodeFileCopy(t *testing.T) {
	srv, stream, workDir := newBinaryWorker(t)
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "beacon-src")
	payload := []byte("local-binary-bytes")
	require.NoError(t, os.WriteFile(srcPath, payload, 0o600))
	sum := sha256.Sum256(payload)

	err := srv.FetchBinary(&workerpb.FetchBinaryRequest{
		InstanceUuid: "22222222-2222-2222-2222-222222222222",
		SourceKind:   "node_file", DestFilename: "beacon", NodePath: srcPath,
		Sha256: hex.EncodeToString(sum[:]), Executable: true,
	}, stream)
	require.NoError(t, err)

	final := stream.lastFrame()
	require.True(t, final.Success, final.Error)
	require.True(t, final.FromLocal, "本地上游应标记 from_local")
	require.Equal(t, int64(len(payload)), final.Size)

	got, readErr := os.ReadFile(filepath.Join(workDir, "beacon"))
	require.NoError(t, readErr)
	require.Equal(t, payload, got)

	// 源文件保持原样（复制而非搬移）。
	_, statErr := os.Stat(srcPath)
	require.NoError(t, statErr, "源文件不应被删除（就地复制语义）")

	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(workDir, "beacon"))
		require.NotZero(t, st.Mode().Perm()&0o100, "应置可执行位")
	}
}

// TestFetchBinary_NodeFileRejectsNonRegular node_file 只接受常规文件：
// 符号链接/目录/设备一律拒绝（防 `link -> /etc/shadow` 绕过常规文件检查）。
func TestFetchBinary_NodeFileRejectsNonRegular(t *testing.T) {
	srv, stream, _ := newBinaryWorker(t)
	tmp := t.TempDir()

	secret := filepath.Join(tmp, "secret")
	require.NoError(t, os.WriteFile(secret, []byte("secret"), 0o600))
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(secret, link); err == nil {
		err = srv.FetchBinary(&workerpb.FetchBinaryRequest{
			InstanceUuid: "22222222-2222-2222-2222-222222222222",
			SourceKind:   "node_file", DestFilename: "beacon", NodePath: link, Executable: true,
		}, stream)
		require.NoError(t, err)
		require.False(t, stream.lastFrame().Success)
		require.Contains(t, stream.lastFrame().Error, "常规文件")
	}

	// 目录同样拒绝
	stream2 := &fetchBinaryStreamStub{}
	err := srv.FetchBinary(&workerpb.FetchBinaryRequest{
		InstanceUuid: "22222222-2222-2222-2222-222222222222",
		SourceKind:   "node_file", DestFilename: "beacon", NodePath: tmp, Executable: true,
	}, stream2)
	require.NoError(t, err)
	require.False(t, stream2.lastFrame().Success)
	require.Contains(t, stream2.lastFrame().Error, "常规文件")
}

// TestFetchBinary_RejectsBadRequests 非法请求一律给出业务级失败帧（不 panic、不落盘）。
func TestFetchBinary_RejectsBadRequests(t *testing.T) {
	srv, _, workDir := newBinaryWorker(t)

	for _, tc := range []struct {
		name string
		req  *workerpb.FetchBinaryRequest
		want string
	}{
		{
			name: "未注册实例",
			req:  &workerpb.FetchBinaryRequest{InstanceUuid: "nope", SourceKind: "url", DestFilename: "b", DownloadUrl: "https://x.com/b"},
			want: "未注册",
		},
		{
			name: "路径穿越的目标文件名",
			req: &workerpb.FetchBinaryRequest{
				InstanceUuid: "22222222-2222-2222-2222-222222222222",
				SourceKind:   "url", DestFilename: "../../escape", DownloadUrl: "https://x.com/b",
			},
			want: "非法的目标文件名",
		},
		{
			name: "未知来源类型",
			req: &workerpb.FetchBinaryRequest{
				InstanceUuid: "22222222-2222-2222-2222-222222222222",
				SourceKind:   "ftp", DestFilename: "b",
			},
			want: "未知来源类型",
		},
		{
			name: "缺来源类型",
			req: &workerpb.FetchBinaryRequest{
				InstanceUuid: "22222222-2222-2222-2222-222222222222", DestFilename: "b",
			},
			want: "缺少来源类型",
		},
		{
			name: "url 来源缺地址",
			req: &workerpb.FetchBinaryRequest{
				InstanceUuid: "22222222-2222-2222-2222-222222222222",
				SourceKind:   "url", DestFilename: "b",
			},
			want: "下载地址为空",
		},
		{
			name: "node_file 缺路径",
			req: &workerpb.FetchBinaryRequest{
				InstanceUuid: "22222222-2222-2222-2222-222222222222",
				SourceKind:   "node_file", DestFilename: "b",
			},
			want: "缺少节点本地文件路径",
		},
		{
			name: "node_file 相对路径",
			req: &workerpb.FetchBinaryRequest{
				InstanceUuid: "22222222-2222-2222-2222-222222222222",
				SourceKind:   "node_file", DestFilename: "b", NodePath: "relative/path",
			},
			want: "必须是绝对路径",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := &fetchBinaryStreamStub{}
			require.NoError(t, srv.FetchBinary(tc.req, stream))
			final := stream.lastFrame()
			require.NotNil(t, final)
			require.True(t, final.Done)
			require.False(t, final.Success)
			require.Contains(t, final.Error, tc.want)
		})
	}

	// 以上均不应落任何文件（工作目录可能因从未成功落盘而不存在，两者都算「无残留」）。
	entries, err := os.ReadDir(workDir)
	if err == nil {
		assert.Empty(t, entries, "失败请求不应残留文件，实际: %v", entries)
	} else {
		assert.True(t, os.IsNotExist(err), "读取工作目录应要么为空要么不存在，实际: %v", err)
	}
}

// TestFetchBinary_ExecutableFalseKeepsMode executable=false 时不改权限位（纯数据文件场景）。
func TestFetchBinary_ExecutableFalseKeepsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不适用 POSIX 权限位断言")
	}
	srv, stream, workDir := newBinaryWorker(t)
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "data.bin")
	require.NoError(t, os.WriteFile(srcPath, []byte("data"), 0o600))

	err := srv.FetchBinary(&workerpb.FetchBinaryRequest{
		InstanceUuid: "22222222-2222-2222-2222-222222222222",
		SourceKind:   "node_file", DestFilename: "data.bin", NodePath: srcPath,
		Executable: false,
	}, stream)
	require.NoError(t, err)
	require.True(t, stream.lastFrame().Success, stream.lastFrame().Error)

	st, statErr := os.Stat(filepath.Join(workDir, "data.bin"))
	require.NoError(t, statErr)
	require.Zero(t, st.Mode().Perm()&0o111, "executable=false 不应置可执行位，实际 %v", st.Mode().Perm())
}

// TestFetchBinary_ProgressStrideOnLargePayload 大文件应按步长上报进度（FR-441 §3.3 的字节进度）。
func TestFetchBinary_ProgressStrideOnLargePayload(t *testing.T) {
	srv, stream, _ := newBinaryWorker(t)
	// 3MiB 载荷 > 1MiB 步长，应产生至少一个中间进度帧。
	payload := make([]byte, 3<<20)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer ts.Close()

	err := srv.FetchBinary(&workerpb.FetchBinaryRequest{
		InstanceUuid: "22222222-2222-2222-2222-222222222222",
		SourceKind:   "url", DestFilename: "big", DownloadUrl: ts.URL, Executable: true,
	}, stream)
	require.NoError(t, err)
	require.True(t, stream.lastFrame().Success, stream.lastFrame().Error)

	var sawProgress bool
	for _, f := range stream.frames {
		if !f.Done && f.Downloaded > 0 {
			sawProgress = true
		}
	}
	require.True(t, sawProgress, "大文件应上报中间进度帧")
}
