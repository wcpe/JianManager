package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// QQ 机器人扫码绑定（FR-495 增补）：生成绑定任务二维码 → 轮询授权结果 → 解密并落盘密钥。
//
// 为什么需要它：官方没有「机器人已加入群列表」接口，而手工抄录 appId/appSecret 出错率高
// 且不可验证。绑定流程让运维在控制台点一下、手机 QQ 扫一次就能拿到可用凭据。
//
// 与 QQDiscoveryService 的分工：本服务只管「拿凭据」，拿到后写进 <dataRoot>/etc 下的
// 密钥文件，通道配置只需引用 ${ENV} 名（见 backup_storage.go 的 resolveEnvRef 回落查找）；
// 凭据到手后的分享链接与网关连接归 QQDiscoveryService。
//
// 协议事实（已用官方 SDK 源码 + 真机实测确认，见 spec）：
//   - POST /lite/create_bind_task，body {"key": "<32 字节随机数的 base64>"}，响应 data.task_id；
//   - 二维码页 /qqbot/openclaw/connect.html?task_id=<id>&source=<来源>&_wv=2；
//   - POST /lite/poll_bind_result，body {"task_id": "<id>"}，响应 data.status ∈ {0,1,2,3}；
//   - COMPLETED 时 data 含 bot_appid / bot_encrypt_secret / user_openid，其中
//     bot_encrypt_secret = base64([12 字节 IV][密文][16 字节 authTag])，密钥就是本地那个随机数。
//
// 这两个端点**不带 Authorization 头**（它们是绑定前的公开接口），只要求 JSON Content-Type，
// 因此直接复用 ChannelNotifier.postJSONRaw（它设 Content-Type: application/json，且只在调用方
// 显式传入时才加鉴权头）。上游调用沿用 notifier.client 的 10s 超时。

const (
	// qqBindDefaultAPIBase 扫码绑定域名：创建任务、轮询与二维码页面同域。
	qqBindDefaultAPIBase = "https://q.qq.com"
	// qqBindSource 二维码 source 参数（官方用于区分接入方）。
	qqBindSource = "JianManager"
	// qqBindQRPath 二维码页面路径。
	qqBindQRPath = "/qqbot/openclaw/connect.html"
	// qqBindCreatePath 创建绑定任务端点。
	qqBindCreatePath = "/lite/create_bind_task"
	// qqBindPollPath 轮询绑定结果端点。
	qqBindPollPath = "/lite/poll_bind_result"
	// qqBindKeyBytes 本地随机 key 长度：32 字节（AES-256-GCM 密钥；base64 后 44 字符）。
	qqBindKeyBytes = 32
	// qqBindNonceLen 密文布局 [IV][密文][authTag] 的 IV 长度（GCM 标准 12 字节）。
	qqBindNonceLen = 12
	// qqBindTagLen 密文布局末尾的 GCM 认证标签长度。
	qqBindTagLen = 16
	// qqBindTaskTTL 本地 key 的保留时长：二维码有效期约 3 分钟，留足「扫完再点一下」的余量。
	qqBindTaskTTL = 15 * time.Minute
)

// 绑定状态归一值（前端据此驱动轮询与表单回填）。
const (
	// QQBindStatusNone 尚未产生任何绑定动作。
	QQBindStatusNone = "none"
	// QQBindStatusPending 已扫码、等待用户在手机上确认。
	QQBindStatusPending = "pending"
	// QQBindStatusCompleted 授权完成，凭据已就绪。
	QQBindStatusCompleted = "completed"
	// QQBindStatusExpired 二维码已过期，须重新生成。
	QQBindStatusExpired = "expired"
)

// 上游 data.status 取值（官方口径）。
const (
	qqBindStatusCodeNone      = 0
	qqBindStatusCodePending   = 1
	qqBindStatusCodeCompleted = 2
	qqBindStatusCodeExpired   = 3
)

var (
	// ErrQQBindKeyExpired 上游已完成绑定，但本地 key 已过期清理，无法解密机器人密钥。
	// 此时唯一出路是重新生成二维码再扫一次——错误信息必须指向这一点。
	ErrQQBindKeyExpired = errors.New("QQ 绑定任务本地密钥已过期")
)

// qqBindCreateResp POST /lite/create_bind_task 响应。
type qqBindCreateResp struct {
	RetCode int    `json:"retcode"`
	Msg     string `json:"msg"`
	Data    struct {
		TaskID string `json:"task_id"`
	} `json:"data"`
}

// qqBindPollResp POST /lite/poll_bind_result 响应。
type qqBindPollResp struct {
	RetCode int    `json:"retcode"`
	Msg     string `json:"msg"`
	Data    struct {
		Status int `json:"status"`
		// BotAppID 机器人 AppID（COMPLETED 时才有）。
		BotAppID string `json:"bot_appid"`
		// BotEncryptSecret 加密后的机器人密钥（COMPLETED 时才有）。
		BotEncryptSecret string `json:"bot_encrypt_secret"`
		// UserOpenID 完成授权的用户 openid（COMPLETED 时才有）。
		UserOpenID string `json:"user_openid"`
	} `json:"data"`
}

// qqBindTask 本地留存的绑定任务。
// key 是解密 bot_encrypt_secret 的唯一依据（官方不回传它），丢了只能重新扫码。
type qqBindTask struct {
	key       []byte
	createdAt time.Time
	// done 非空表示该任务已完成且密钥已落盘：重复轮询直接回放结果（幂等）。
	// 此时 key 已被清空——解密材料用完即弃，不再驻留内存。
	done *qqBindResult
}

// qqBindResult 绑定完成的结果快照，用于让重复轮询幂等。
//
// secret 在内存里保留到该任务被 TTL 清理（≤15 分钟）：它本就是运维刚扫码授权拿到的密钥，
// 同一时刻已按 spec 约定明文落盘在 etc/（0600），故不为这十几分钟另设一套「取不到密钥」的语义。
type qqBindResult struct {
	appID      string
	secret     string
	userOpenID string
}

// QQBindService QQ 机器人扫码绑定服务。
//
// 任务表是内存态：绑定任务寿命以分钟计，重启后重扫一次即可，不值得为它加一张表。
type QQBindService struct {
	notifier *ChannelNotifier
	baseURL  string

	// root 数据根：密钥落盘到 <root>/etc/qq-<appId>.key；nil 时按 JIANMANAGER_DATA_DIR 解析。
	root *dataroot.Root

	// now 可注入时钟（测试用）。
	now func() time.Time

	// mu 保护 tasks。
	mu    sync.Mutex
	tasks map[string]qqBindTask
}

// NewQQBindService 创建扫码绑定服务；notifier 为 nil 时用默认通知器（复用其 HTTP 客户端）。
func NewQQBindService(notifier *ChannelNotifier) *QQBindService {
	if notifier == nil {
		notifier = NewChannelNotifier()
	}
	return &QQBindService{
		notifier: notifier,
		baseURL:  qqBindDefaultAPIBase,
		now:      time.Now,
		tasks:    make(map[string]qqBindTask),
	}
}

// SetDataRoot 注入 CP 数据根，密钥文件写入 <root>/etc。
func (s *QQBindService) SetDataRoot(root *dataroot.Root) {
	s.root = root
}

// SetBaseURL 覆盖上游域名（默认 https://q.qq.com）：用于测试假服务端与域名变更兜底。
// 非 https 且非回环地址会被拒绝——请求里带着解密凭据，明文 http 等同外泄。
func (s *QQBindService) SetBaseURL(base string) error {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return fmt.Errorf("QQ 绑定服务地址非法: %q", base)
	}
	if u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("QQ 绑定服务地址必须是 https（当前 %q）", base)
	}
	s.baseURL = base
	return nil
}

// ── 绑定任务 ──

// CreateBindTask 创建绑定任务：本地生成密钥 → 上游建任务 → 拼二维码 URL。
// 本地 key 必须留存（完成时解密 bot_encrypt_secret 用），随任务表一起过期清理。
func (s *QQBindService) CreateBindTask() (string, string, error) {
	key := make([]byte, qqBindKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return "", "", fmt.Errorf("生成 QQ 绑定随机密钥失败: %w", err)
	}
	api := s.baseURL + qqBindCreatePath
	status, body, err := s.notifier.postJSONRaw(api, nil, map[string]string{
		"key": base64.StdEncoding.EncodeToString(key),
	})
	if err != nil {
		return "", "", fmt.Errorf("创建 QQ 绑定任务失败: %w", err)
	}
	// 与 checkQQResponse 同款语义：HTTP 状态与 retcode 都要判（失败时 body 里才有 msg）。
	if status < 200 || status >= 300 {
		return "", "", fmt.Errorf("创建 QQ 绑定任务失败: HTTP 状态码 %d，响应 %s", status, qqSnippet(body))
	}
	var parsed qqBindCreateResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", fmt.Errorf("解析 QQ 绑定任务响应失败（HTTP 状态码 %d，响应 %s）: %w", status, qqSnippet(body), err)
	}
	if parsed.RetCode != 0 {
		return "", "", fmt.Errorf("创建 QQ 绑定任务失败: retcode=%d msg=%s", parsed.RetCode, strings.TrimSpace(parsed.Msg))
	}
	taskID := strings.TrimSpace(parsed.Data.TaskID)
	if taskID == "" {
		return "", "", fmt.Errorf("创建 QQ 绑定任务失败: 响应缺少 task_id（HTTP 状态码 %d，响应 %s）", status, qqSnippet(body))
	}

	now := s.now()
	s.mu.Lock()
	s.pruneLocked(now)
	s.tasks[taskID] = qqBindTask{key: key, createdAt: now}
	s.mu.Unlock()

	return taskID, s.qrURL(taskID), nil
}

// qrURL 拼二维码页面 URL（source 固定为接入方标识；task_id 做 URL 转义防注入）。
func (s *QQBindService) qrURL(taskID string) string {
	q := url.Values{}
	q.Set("task_id", taskID)
	q.Set("source", qqBindSource)
	q.Set("_wv", "2")
	return s.baseURL + qqBindQRPath + "?" + q.Encode()
}

// pruneLocked 清理过期任务（释放本地密钥驻留时间）；调用方须持有 mu。
func (s *QQBindService) pruneLocked(now time.Time) {
	for id, task := range s.tasks {
		if now.Sub(task.createdAt) > qqBindTaskTTL {
			delete(s.tasks, id)
		}
	}
}

// lookupTask 取任务副本（不删除：轮询可能被前端重复触发）。
func (s *QQBindService) lookupTask(taskID string) (qqBindTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	task, ok := s.tasks[taskID]
	return task, ok
}

// dropTask 移除任务（过期或已无用途）。
func (s *QQBindService) dropTask(taskID string) {
	s.mu.Lock()
	delete(s.tasks, taskID)
	s.mu.Unlock()
}

// finishTask 记录完成结果并丢弃解密材料：重复轮询回放结果而不再次打扰上游。
//
// 为什么必须幂等：前端可能重复轮询（重试、StrictMode 双触发、超时重发）。若完成即删任务，
// 第二次轮询会得到「任务已过期」——绑定明明成功、密钥也已落盘，却给运维报错。
func (s *QQBindService) finishTask(taskID string, result qqBindResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		// 本地任务已被清理（极端竞态）：不重建条目，只让本次调用拿到结果。
		return
	}
	task.key = nil
	task.done = &result
	s.tasks[taskID] = task
}

// PollBindResult 轮询绑定结果：归一状态；COMPLETED 时解密凭据并落盘密钥文件。
//
// status ∈ {none, pending, completed, expired}；只有 completed 才带 appID / appSecret / userOpenID。
// appSecret 只回给调用方（端点层绝不外传），且失败信息里不含密钥。
// 同一任务的重复轮询幂等（完成态回放同一结果，不再打扰上游）。
func (s *QQBindService) PollBindResult(taskID string) (string, string, string, string, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return "", "", "", "", fmt.Errorf("绑定任务 ID 不能为空")
	}
	task, hasTask := s.lookupTask(taskID)
	if hasTask && task.done != nil {
		return QQBindStatusCompleted, task.done.appID, task.done.secret, task.done.userOpenID, nil
	}

	api := s.baseURL + qqBindPollPath
	status, body, err := s.notifier.postJSONRaw(api, nil, map[string]string{"task_id": taskID})
	if err != nil {
		return "", "", "", "", fmt.Errorf("轮询 QQ 绑定结果失败: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", "", "", "", fmt.Errorf("轮询 QQ 绑定结果失败: HTTP 状态码 %d，响应 %s", status, qqSnippet(body))
	}
	var parsed qqBindPollResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", "", "", fmt.Errorf("解析 QQ 绑定结果响应失败（HTTP 状态码 %d，响应 %s）: %w", status, qqSnippet(body), err)
	}
	if parsed.RetCode != 0 {
		return "", "", "", "", fmt.Errorf("轮询 QQ 绑定结果失败: retcode=%d msg=%s", parsed.RetCode, strings.TrimSpace(parsed.Msg))
	}

	normalized, err := qqBindNormalizeStatus(parsed.Data.Status)
	if err != nil {
		return "", "", "", "", err
	}
	switch normalized {
	case QQBindStatusCompleted:
		if !hasTask {
			// 上游已完成但本地 key 已过期清理：只能重扫（此时报「任务不存在」会误导运维）。
			return "", "", "", "", fmt.Errorf("%w（appId=%s）", ErrQQBindKeyExpired, parsed.Data.BotAppID)
		}
		secret, err := decryptQQBindSecret(task.key, parsed.Data.BotEncryptSecret)
		if err != nil {
			return "", "", "", "", fmt.Errorf("解密 QQ 机器人密钥失败: %w", err)
		}
		appID := strings.TrimSpace(parsed.Data.BotAppID)
		if appID == "" {
			return "", "", "", "", fmt.Errorf("QQ 绑定结果缺少 bot_appid，无法落盘密钥")
		}
		if err := s.persistSecret(appID, secret); err != nil {
			return "", "", "", "", err
		}
		userOpenID := strings.TrimSpace(parsed.Data.UserOpenID)
		s.finishTask(taskID, qqBindResult{appID: appID, secret: secret, userOpenID: userOpenID})
		return normalized, appID, secret, userOpenID, nil
	case QQBindStatusExpired:
		// 二维码已过期：本地 key 不再有用（重扫会生成新任务）。
		s.dropTask(taskID)
		return normalized, "", "", strings.TrimSpace(parsed.Data.UserOpenID), nil
	default:
		return normalized, "", "", "", nil
	}
}

// qqBindNormalizeStatus 把上游状态码归一为前端可用字符串；未知码直接报错（协议变更要看得见）。
func qqBindNormalizeStatus(code int) (string, error) {
	switch code {
	case qqBindStatusCodeNone:
		return QQBindStatusNone, nil
	case qqBindStatusCodePending:
		return QQBindStatusPending, nil
	case qqBindStatusCodeCompleted:
		return QQBindStatusCompleted, nil
	case qqBindStatusCodeExpired:
		return QQBindStatusExpired, nil
	default:
		return "", fmt.Errorf("QQ 绑定结果状态码未知: %d", code)
	}
}

// decryptQQBindSecret 解密 bot_encrypt_secret（AES-256-GCM）。
// 密文布局：base64([12 字节 IV][密文][16 字节 authTag]），密钥为本地随机数（32 字节）。
func decryptQQBindSecret(key []byte, encrypted string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encrypted))
	if err != nil {
		return "", fmt.Errorf("密文 base64 解码失败: %w", err)
	}
	if len(key) != qqBindKeyBytes {
		return "", fmt.Errorf("本地密钥长度非法（%d 字节，应为 %d）", len(key), qqBindKeyBytes)
	}
	if len(raw) < qqBindNonceLen+qqBindTagLen {
		return "", fmt.Errorf("密文长度非法（%d 字节，至少 %d）", len(raw), qqBindNonceLen+qqBindTagLen)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("构造 AES 密码器失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("构造 AES-GCM 失败: %w", err)
	}
	nonce, sealed := raw[:qqBindNonceLen], raw[qqBindNonceLen:]
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		// 不打印密钥/密文：只报「解不开」这一事实。
		return "", fmt.Errorf("密文认证失败（本地密钥不匹配或密文被篡改）")
	}
	if len(plain) == 0 {
		return "", fmt.Errorf("解密结果为空")
	}
	return string(plain), nil
}

// ── 凭据落盘 ──

// qqCredentialEnvName 由 appId 派生凭证环境变量名：QQ-<appId>。
//
// 命名契约（前端表单、密钥文件、resolveEnvRef 回落查找三处必须一致）：
//
//	appId=1020001 → 变量名 QQ-1020001 → 密钥文件 etc/qq-1020001.key → 配置里填 ${QQ-1020001}
//
// 变量名带连字符是因为它同时决定了密钥文件名（spec 约定 etc/qq-<appId>.key）。
// appId 只允许字母数字与 _-，否则无法派生出干净的变量名（也顺带挡住路径穿越）。
func qqCredentialEnvName(appID string) (string, error) {
	id := strings.TrimSpace(appID)
	if id == "" {
		return "", fmt.Errorf("appId 不能为空")
	}
	name := "QQ-" + id
	if !envRefPattern.MatchString("${" + name + "}") {
		return "", fmt.Errorf("appId %q 无法派生合法的凭证环境变量名", appID)
	}
	return name, nil
}

// QQSecretEnvRef 返回前端该填进 appSecret 的 ${ENV} 引用名（如 ${QQ-1020001}）。
func QQSecretEnvRef(appID string) (string, error) {
	name, err := qqCredentialEnvName(appID)
	if err != nil {
		return "", err
	}
	return "${" + name + "}", nil
}

// secretDir 密钥落盘目录：`<dataRoot>/etc/qq/`。
//
// 必须与 resolveEnvRef 的回落目录（SetCredentialKeyDir 的入参）指向同一处，
// 否则扫码写入的密钥解析不到；且必须落在 QQ 专用子目录，避免与 CP 主密钥同目录
// 而被通用回落规则命中（见 QQSecretSubdir 注释）。
func (s *QQBindService) secretDir() (string, error) {
	if s.root != nil {
		return filepath.Join(s.root.EtcDir(), QQSecretSubdir), nil
	}
	root, err := dataroot.Resolve("")
	if err != nil {
		return "", fmt.Errorf("解析数据根失败: %w", err)
	}
	return filepath.Join(root.EtcDir(), QQSecretSubdir), nil
}

// persistSecret 把 appSecret 写入 <dataRoot>/etc/qq/qq-<appId>.key（0600，先写临时文件再 rename）。
func (s *QQBindService) persistSecret(appID, secret string) error {
	name, err := qqCredentialEnvName(appID)
	if err != nil {
		return err
	}
	dir, err := s.secretDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建密钥目录失败（%s）: %w", dir, err)
	}
	fileName := credentialKeyFileName(name)
	finalPath := filepath.Join(dir, fileName)
	tmp, err := os.CreateTemp(dir, ".qq-secret-*.tmp")
	if err != nil {
		return fmt.Errorf("创建密钥临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	// 失败路径统一清理临时文件（rename 成功后它已不在原路径）。
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("设置密钥文件权限失败: %w", err)
	}
	if _, err := tmp.WriteString(secret); err != nil {
		return fmt.Errorf("写入密钥文件失败: %w", err)
	}
	// 先落盘再改名：避免 rename 后内容仍在页缓存里，断电留下空密钥文件。
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("同步密钥文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭密钥文件失败: %w", err)
	}
	// 同名临时文件与目标同目录，rename 在同一文件系统内是原子替换。
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("落盘密钥文件失败（%s）: %w", finalPath, err)
	}
	// 只记文件名，不记路径全貌与密钥本身。
	slog.Info("QQ 机器人密钥已落盘（配置中引用对应 ${ENV} 变量名）", "appId", appID, "file", fileName)
	return nil
}
