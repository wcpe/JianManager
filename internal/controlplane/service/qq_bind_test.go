package service

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// ── 测试脚手架 ──

// qqBindFake 假绑定上游：记录请求形状，响应体可被用例改写。
// 真实 q.qq.com 不可在单测里访问，故把域名换成假服务端（SetBaseURL 只放行 https 与回环）。
type qqBindFake struct {
	server *httptest.Server

	mu           sync.Mutex
	createStatus int
	createBody   string
	pollStatus   int
	pollBody     string
	// keyB64 最近一次 create 请求里带的本地密钥（base64），用例用它造密文。
	keyB64 string
	// lastContentType / lastAuthHeader 记录请求头（断言「带 JSON、不带鉴权」）。
	lastContentType string
	lastAuthHeader  string
	createCalls     int
	pollCalls       int
	lastPollTaskID  string
}

func newQQBindFake(t *testing.T) *qqBindFake {
	t.Helper()
	f := &qqBindFake{
		createStatus: http.StatusOK,
		createBody:   `{"retcode":0,"data":{"task_id":"task-1"}}`,
		pollStatus:   http.StatusOK,
		pollBody:     `{"retcode":0,"data":{"status":1}}`,
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *qqBindFake) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.lastContentType = r.Header.Get("Content-Type")
	f.lastAuthHeader = r.Header.Get("Authorization")
	switch r.URL.Path {
	case qqBindCreatePath:
		f.createCalls++
		var req struct {
			Key string `json:"key"`
		}
		_ = json.Unmarshal(raw, &req)
		f.keyB64 = req.Key
		status, body := f.createStatus, f.createBody
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	case qqBindPollPath:
		f.pollCalls++
		var req struct {
			TaskID string `json:"task_id"`
		}
		_ = json.Unmarshal(raw, &req)
		f.lastPollTaskID = req.TaskID
		status, body := f.pollStatus, f.pollBody
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	default:
		f.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}
}

// setCreate 改写创建任务响应。
func (f *qqBindFake) setCreate(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createStatus, f.createBody = status, body
}

// setPoll 改写轮询响应。
func (f *qqBindFake) setPoll(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pollStatus, f.pollBody = status, body
}

// localKey 取出假上游收到的本地密钥（create 之后）。
func (f *qqBindFake) localKey(t *testing.T) []byte {
	t.Helper()
	f.mu.Lock()
	raw := f.keyB64
	f.mu.Unlock()
	key, err := base64.StdEncoding.DecodeString(raw)
	require.NoError(t, err)
	require.Len(t, key, qqBindKeyBytes)
	return key
}

// newQQBindTestService 造一个指向假上游、落盘目录为临时数据根的服务。
func newQQBindTestService(t *testing.T, f *qqBindFake) (*QQBindService, *dataroot.Root) {
	t.Helper()
	root, err := dataroot.Init(t.TempDir())
	require.NoError(t, err)
	svc := NewQQBindService(nil)
	svc.SetDataRoot(root)
	require.NoError(t, svc.SetBaseURL(f.server.URL))
	return svc, root
}

// encryptQQBindSecretForTest 按官方布局造密文：base64([12 字节 IV][密文][16 字节 authTag])。
func encryptQQBindSecretForTest(t *testing.T, key []byte, plain string) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	iv := make([]byte, gcm.NonceSize())
	for i := range iv {
		iv[i] = byte(i + 1)
	}
	sealed := gcm.Seal(nil, iv, []byte(plain), nil)
	require.Equal(t, qqBindNonceLen, len(iv))
	require.Equal(t, len(plain)+qqBindTagLen, len(sealed))
	return base64.StdEncoding.EncodeToString(append(iv, sealed...))
}

// qqBindFixedKey 固定内容的 32 字节测试密钥（避免用例依赖随机数）。
func qqBindFixedKey(seed byte) []byte {
	key := make([]byte, qqBindKeyBytes)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

// ── 创建绑定任务 ──

// TestQQBind_创建任务请求形状与二维码URL 创建接口的路径/头/body 必须与实测协议一致，
// 且本地必须留存同一把密钥（否则完成时解不开 bot_encrypt_secret）。
func TestQQBind_创建任务请求形状与二维码URL(t *testing.T) {
	f := newQQBindFake(t)
	svc, _ := newQQBindTestService(t, f)

	taskID, qrURL, err := svc.CreateBindTask()
	require.NoError(t, err)
	assert.Equal(t, "task-1", taskID)

	f.mu.Lock()
	assert.Equal(t, 1, f.createCalls)
	assert.Contains(t, f.lastContentType, "application/json")
	assert.Empty(t, f.lastAuthHeader, "绑定端点不带 Authorization 头")
	f.mu.Unlock()

	// 本地留存的就是刚发出去的那把 32 字节密钥。
	sent := f.localKey(t)
	task, ok := svc.lookupTask(taskID)
	require.True(t, ok, "本地必须留存绑定任务的密钥")
	assert.Equal(t, sent, task.key)
	assert.False(t, task.createdAt.IsZero())

	// 二维码 URL：域名 + 页面路径 + 三个参数。
	u, err := url.Parse(qrURL)
	require.NoError(t, err)
	assert.Equal(t, f.server.URL, u.Scheme+"://"+u.Host)
	assert.Equal(t, qqBindQRPath, u.Path)
	assert.Equal(t, "task-1", u.Query().Get("task_id"))
	assert.Equal(t, "JianManager", u.Query().Get("source"))
	assert.Equal(t, "2", u.Query().Get("_wv"))
}

// TestQQBind_创建任务错误处理 上游失败必须带可定位信息报错，且错误里不出现密钥。
func TestQQBind_创建任务错误处理(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantErrText string
	}{
		{"retcode非0带msg", http.StatusOK, `{"retcode":100017,"msg":"接口调用超过频率限制"}`, "接口调用超过频率限制"},
		{"HTTP非2xx", http.StatusInternalServerError, `{"retcode":0,"data":{}}`, "HTTP 状态码 500"},
		{"响应缺少task_id", http.StatusOK, `{"retcode":0,"data":{}}`, "缺少 task_id"},
		{"响应非法JSON", http.StatusOK, `{bad`, "解析 QQ 绑定任务响应失败"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newQQBindFake(t)
			f.setCreate(tc.status, tc.body)
			svc, _ := newQQBindTestService(t, f)

			taskID, qrURL, err := svc.CreateBindTask()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrText)
			assert.Empty(t, taskID)
			assert.Empty(t, qrURL)
			// 失败时不留下任务（否则凭据回来后没有对应密钥可用）。
			assert.Empty(t, svc.tasks)
		})
	}
}

// TestQQBind_创建任务上游不可达 上游不可达时报错而不是 panic/静默成功。
func TestQQBind_创建任务上游不可达(t *testing.T) {
	f := newQQBindFake(t)
	svc, _ := newQQBindTestService(t, f)
	f.server.Close() // 关掉假上游，制造连接失败

	_, _, err := svc.CreateBindTask()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "创建 QQ 绑定任务失败")
	assert.Empty(t, svc.tasks)
}

// TestQQBind_服务地址只接受https或回环 明文 http 会把凭据发往不可信地址，必须拒绝。
func TestQQBind_服务地址只接受https或回环(t *testing.T) {
	svc := NewQQBindService(nil)
	require.Equal(t, qqBindDefaultAPIBase, svc.baseURL)

	require.NoError(t, svc.SetBaseURL("https://q.qq.com/"))
	assert.Equal(t, qqBindDefaultAPIBase, svc.baseURL, "末尾斜杠应归一")

	require.NoError(t, svc.SetBaseURL("http://127.0.0.1:18080"))
	require.Error(t, svc.SetBaseURL("http://q.qq.com"))
	require.Error(t, svc.SetBaseURL("not-a-url"))
}

// ── 轮询与状态归一 ──

// TestQQBind_轮询状态归一 上游 status 0/1/3 归一为 none/pending/expired；未知码报错。
func TestQQBind_轮询状态归一(t *testing.T) {
	tests := []struct {
		name string
		code int
		want string
	}{
		{"NONE归一为none", qqBindStatusCodeNone, QQBindStatusNone},
		{"PENDING归一为pending", qqBindStatusCodePending, QQBindStatusPending},
		{"EXPIRED归一为expired", qqBindStatusCodeExpired, QQBindStatusExpired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newQQBindFake(t)
			svc, _ := newQQBindTestService(t, f)
			taskID, _, err := svc.CreateBindTask()
			require.NoError(t, err)

			f.setPoll(http.StatusOK, `{"retcode":0,"data":{"status":`+qqBindItoa(tc.code)+`}}`)
			status, appID, secret, openID, err := svc.PollBindResult(taskID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, status)
			assert.Empty(t, appID)
			assert.Empty(t, secret, "非完成态绝不返回密钥")
			assert.Empty(t, openID)
			// 请求体带的是 task_id。
			f.mu.Lock()
			assert.Equal(t, taskID, f.lastPollTaskID)
			f.mu.Unlock()
		})
	}
}

// TestQQBind_未知状态码报错 官方新增状态码时要看得见（不静默当成功）。
func TestQQBind_未知状态码报错(t *testing.T) {
	f := newQQBindFake(t)
	svc, _ := newQQBindTestService(t, f)
	taskID, _, err := svc.CreateBindTask()
	require.NoError(t, err)

	f.setPoll(http.StatusOK, `{"retcode":0,"data":{"status":9}}`)
	_, _, _, _, err = svc.PollBindResult(taskID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "状态码未知")
}

// TestQQBind_轮询错误处理 上游 retcode / HTTP 失败都要报错且不含密钥。
func TestQQBind_轮询错误处理(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantErrText string
	}{
		{"retcode非0带msg", http.StatusOK, `{"retcode":11244,"msg":"task not found"}`, "task not found"},
		{"HTTP非2xx", http.StatusBadGateway, `{"retcode":0}`, "HTTP 状态码 502"},
		{"响应非法JSON", http.StatusOK, `{bad`, "解析 QQ 绑定结果响应失败"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newQQBindFake(t)
			svc, _ := newQQBindTestService(t, f)
			taskID, _, err := svc.CreateBindTask()
			require.NoError(t, err)

			f.setPoll(tc.status, tc.body)
			_, _, _, _, err = svc.PollBindResult(taskID)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrText)
		})
	}
}

// TestQQBind_任务ID为空报错 空 task_id 直接拒绝（不拿空串去打上游）。
func TestQQBind_任务ID为空报错(t *testing.T) {
	svc := NewQQBindService(nil)
	_, _, _, _, err := svc.PollBindResult("   ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "绑定任务 ID 不能为空")
}

// ── 完成态：解密 + 落盘 + ENV 引用 ──

// TestQQBind_完成态解密落盘与ENV引用 端到端：创建 → 用本地密钥造密文 → 完成 → 落盘 0600 →
// resolveEnvRef 用 ${QQ-<appId>} 取到密钥（配置里只需引用变量名，不必手工抄密钥）。
func TestQQBind_完成态解密落盘与ENV引用(t *testing.T) {
	f := newQQBindFake(t)
	svc, root := newQQBindTestService(t, f)

	taskID, _, err := svc.CreateBindTask()
	require.NoError(t, err)
	key := f.localKey(t)

	const (
		appID     = "1020001"
		secret    = "app-raw"
		userOpenI = "user-openid-1"
	)
	enc := encryptQQBindSecretForTest(t, key, secret)
	f.setPoll(http.StatusOK, `{"retcode":0,"data":{"status":2,"bot_appid":"`+appID+
		`","bot_encrypt_secret":"`+enc+`","user_openid":"`+userOpenI+`"}}`)

	status, gotAppID, gotSecret, gotOpenID, err := svc.PollBindResult(taskID)
	require.NoError(t, err)
	assert.Equal(t, QQBindStatusCompleted, status)
	assert.Equal(t, appID, gotAppID)
	assert.Equal(t, secret, gotSecret)
	assert.Equal(t, userOpenI, gotOpenID)

	// 落盘：etc/qq-<appId>.key，0600，内容就是明文密钥。
	path := filepath.Join(root.EtcDir(), QQSecretSubdir, "qq-"+appID+".key")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, secret, string(raw))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "密钥文件必须 0600")

	// 原子写：不留临时文件。
	leftovers, err := filepath.Glob(filepath.Join(root.EtcDir(), QQSecretSubdir, ".qq-secret-*.tmp"))
	require.NoError(t, err)
	assert.Empty(t, leftovers)

	// 变量名契约：前端拿到的 ${QQ-<appId>} 能被 resolveEnvRef 解析到同一密钥。
	ref, err := QQSecretEnvRef(appID)
	require.NoError(t, err)
	assert.Equal(t, "${QQ-1020001}", ref)
	SetCredentialDataRoot(root.Base())
	t.Cleanup(func() { SetCredentialDataRoot("") })
	got, err := resolveEnvRef(ref)
	require.NoError(t, err)
	assert.Equal(t, secret, got)

	// 重复轮询幂等：绑定成功的结论不会被第二次轮询推翻（前端重试/双触发不会看到「已过期」）。
	f.mu.Lock()
	pollsBefore := f.pollCalls
	f.mu.Unlock()
	status, gotAppID, gotSecret, gotOpenID, err = svc.PollBindResult(taskID)
	require.NoError(t, err)
	assert.Equal(t, QQBindStatusCompleted, status)
	assert.Equal(t, appID, gotAppID)
	assert.Equal(t, secret, gotSecret)
	assert.Equal(t, userOpenI, gotOpenID)
	f.mu.Lock()
	assert.Equal(t, pollsBefore, f.pollCalls, "完成态重复轮询不应再打扰上游")
	f.mu.Unlock()

	// 完成后本地不再驻留解密材料（key 已清空）。
	task, ok := svc.lookupTask(taskID)
	require.True(t, ok)
	assert.Nil(t, task.key)
	require.NotNil(t, task.done)
}

// TestQQBind_上游已完成但本地密钥已过期 本地点已清理时给出可行动的明确错误（提示重扫）。
func TestQQBind_上游已完成但本地密钥已过期(t *testing.T) {
	f := newQQBindFake(t)
	svc, _ := newQQBindTestService(t, f)

	enc := encryptQQBindSecretForTest(t, qqBindFixedKey(1), "whatever")
	f.setPoll(http.StatusOK, `{"retcode":0,"data":{"status":2,"bot_appid":"1020001","bot_encrypt_secret":"`+enc+`"}}`)

	_, _, _, _, err := svc.PollBindResult("never-created")
	require.ErrorIs(t, err, ErrQQBindKeyExpired)
	assert.Contains(t, err.Error(), "1020001", "错误信息要带 appId，便于运维定位是哪台机器人")
}

// TestQQBind_过期清理 超过 TTL 的任务在新任务创建时被清理（不无限驻留本地密钥）。
func TestQQBind_过期清理(t *testing.T) {
	f := newQQBindFake(t)
	svc, _ := newQQBindTestService(t, f)

	base := time.Unix(1700000000, 0)
	svc.now = func() time.Time { return base }
	first, _, err := svc.CreateBindTask()
	require.NoError(t, err)
	require.Len(t, svc.tasks, 1)

	// TTL 内：仍在。
	svc.now = func() time.Time { return base.Add(qqBindTaskTTL - time.Second) }
	_, ok := svc.lookupTask(first)
	assert.True(t, ok)

	// 超 TTL：创建新任务时触发清理。
	f.setCreate(http.StatusOK, `{"retcode":0,"data":{"task_id":"task-2"}}`)
	svc.now = func() time.Time { return base.Add(qqBindTaskTTL + time.Minute) }
	_, _, err = svc.CreateBindTask()
	require.NoError(t, err)
	_, ok = svc.lookupTask(first)
	assert.False(t, ok, "超 TTL 的任务必须被清理")
	assert.Len(t, svc.tasks, 1)
}

// ── 解密（AES-256-GCM） ──

// TestQQBind_解密正确性 合法布局的密文（含中文明文）能解出原文。
func TestQQBind_解密正确性(t *testing.T) {
	key := qqBindFixedKey(7)
	for _, plain := range []string{"abc123", "含中文的密钥", strings.Repeat("x", 300)} {
		enc := encryptQQBindSecretForTest(t, key, plain)
		got, err := decryptQQBindSecret(key, enc)
		require.NoError(t, err)
		assert.Equal(t, plain, got)
	}
	// 密文首尾空白（官方偶有换行）应被容忍。
	enc := encryptQQBindSecretForTest(t, key, "padded")
	got, err := decryptQQBindSecret(key, "  "+enc+"\n")
	require.NoError(t, err)
	assert.Equal(t, "padded", got)
}

// TestQQBind_解密失败 篡改 / 错 key / 非法输入都必须报明确错误且不回显密钥。
func TestQQBind_解密失败(t *testing.T) {
	key := qqBindFixedKey(3)
	valid := encryptQQBindSecretForTest(t, key, "top-secret-value")

	// 篡改密文中段一个字节（GCM 认证必然失败）。
	raw, err := base64.StdEncoding.DecodeString(valid)
	require.NoError(t, err)
	tampered := append([]byte(nil), raw...)
	tampered[qqBindNonceLen] ^= 0xff
	tamperedB64 := base64.StdEncoding.EncodeToString(tampered)

	// 只改 IV（认证失败）。
	badIV := append([]byte(nil), raw...)
	badIV[0] ^= 0xff
	badIVB64 := base64.StdEncoding.EncodeToString(badIV)

	tests := []struct {
		name      string
		key       []byte
		encrypted string
		wantText  string
	}{
		{"密文被篡改", key, tamperedB64, "密文认证失败"},
		{"IV被篡改", key, badIVB64, "密文认证失败"},
		{"密钥不匹配", qqBindFixedKey(99), valid, "密文认证失败"},
		{"非法base64", key, "!!!not-base64!!!", "base64 解码失败"},
		{"密文过短", key, base64.StdEncoding.EncodeToString([]byte("short")), "密文长度非法"},
		{"密钥长度非法", []byte("16-bytes-key-abc"), valid, "本地密钥长度非法"},
		{"明文为空", key, encryptQQBindSecretForTest(t, key, ""), "解密结果为空"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decryptQQBindSecret(tc.key, tc.encrypted)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantText)
			assert.NotContains(t, err.Error(), "top-secret-value", "错误信息不得回显密钥")
			assert.NotContains(t, err.Error(), valid, "错误信息不得回显密文")
		})
	}
}

// TestQQBind_解密失败时端点不落盘 解密失败必须中断，绝不写入半成品密钥文件。
func TestQQBind_解密失败时端点不落盘(t *testing.T) {
	f := newQQBindFake(t)
	svc, root := newQQBindTestService(t, f)
	taskID, _, err := svc.CreateBindTask()
	require.NoError(t, err)

	// 用另一把 key 造密文（模拟密钥不匹配）。
	enc := encryptQQBindSecretForTest(t, qqBindFixedKey(42), "not-for-you")
	f.setPoll(http.StatusOK, `{"retcode":0,"data":{"status":2,"bot_appid":"1020001","bot_encrypt_secret":"`+enc+`"}}`)

	_, _, _, _, err = svc.PollBindResult(taskID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "解密 QQ 机器人密钥失败")

	files, err := filepath.Glob(filepath.Join(root.EtcDir(), "*.key"))
	require.NoError(t, err)
	assert.Empty(t, files, "解密失败不得落盘任何密钥文件")
	// 任务仍在：允许重试（不静默丢凭据）。
	_, ok := svc.lookupTask(taskID)
	assert.True(t, ok)
}

// ── 落盘细节 ──

// TestQQBind_落盘覆盖旧文件并收紧权限 重复绑定（同名文件已存在且权限过宽）必须原子替换为 0600。
func TestQQBind_落盘覆盖旧文件并收紧权限(t *testing.T) {
	f := newQQBindFake(t)
	svc, root := newQQBindTestService(t, f)

	require.NoError(t, os.MkdirAll(filepath.Join(root.EtcDir(), QQSecretSubdir), 0o755))
	path := filepath.Join(root.EtcDir(), QQSecretSubdir, "qq-1020001.key")
	require.NoError(t, os.WriteFile(path, []byte("old-secret"), 0o644))

	require.NoError(t, svc.persistSecret("1020001", "new-secret"))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "new-secret", string(raw))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	leftovers, err := filepath.Glob(filepath.Join(root.EtcDir(), QQSecretSubdir, ".qq-secret-*.tmp"))
	require.NoError(t, err)
	assert.Empty(t, leftovers)
}

// TestQQBind_appId非法时拒绝落盘 appId 只允许字母数字与 _-（挡住路径穿越与脏变量名）。
func TestQQBind_appId非法时拒绝落盘(t *testing.T) {
	f := newQQBindFake(t)
	svc, root := newQQBindTestService(t, f)

	for _, appID := range []string{"", "  ", "../evil", "a/b", "a.b", "a b"} {
		err := svc.persistSecret(appID, "secret")
		require.Error(t, err, "非法 appId %q 必须被拒", appID)
	}
	// 数据根之外不得出现任何 .key 文件。
	parent := filepath.Dir(root.Base())
	files, err := filepath.Glob(filepath.Join(parent, "*.key"))
	require.NoError(t, err)
	assert.Empty(t, files)
}

// TestQQBind_未注入数据根时按环境变量解析 生产由 main 注入数据根，未注入时回落
// JIANMANAGER_DATA_DIR（与 CP 其余组件同口径），仍能落盘。
func TestQQBind_未注入数据根时按环境变量解析(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(dataroot.EnvVar, dir)

	svc := NewQQBindService(nil)
	require.NoError(t, svc.persistSecret("1020002", "env-root-secret"))

	raw, err := os.ReadFile(filepath.Join(dir, "etc", QQSecretSubdir, "qq-1020002.key"))
	require.NoError(t, err)
	assert.Equal(t, "env-root-secret", string(raw))
}

// itoa 让表格用例里的 JSON 拼接保持紧凑。
func qqBindItoa(v int) string { return strconv.Itoa(v) }
