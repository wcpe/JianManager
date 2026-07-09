package service

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

// CP↔Worker 专用 WS 令牌密钥（FR-275，见 ADR-061）。
//
// 该密钥只签发/校验「CP↔Worker WS 令牌」（终端一次性 token + 插件桥实例级 token），
// 与签用户会话的 jwt.secret 隔离——Worker 永不持有 jwt.secret，任一 Worker 沦陷
// 也无法伪造 CP 管理员会话。密钥经 gRPC RegisterResponse/HeartbeatResponse 下发给 Worker。
//
// 来源三轨（优先级由高到低，镜像 FR-263 ResolveKeyEncryptor 先例）：
//   - 显式配置（jwt.ws_secret / JIANMANAGER_JWT_WS_SECRET）：用之，不生成不持久化——
//     也是存量部署的过渡逃生口（可设回旧 jwt.secret 值兼容未升级 Worker）；
//   - 生产未配（dev_mode=false）：自动生成 32 字节随机密钥（base64 串）并持久化到
//     <dataRoot>/etc/ws-token-secret.key（0600，原子写），跨重启用同一密钥；
//   - dev 未配：回退 DevWSTokenSecret（与两端现状默认一致，保 dev 零配置连续性）。
//
// 解析/生成失败 → 返回错误，装配层 fail-fast（ADR-061 决策 2）：WS 密钥不可用则终端/
// 监控必坏，静默降级只会掩盖问题；且绝不回退 jwt.secret（重新引入主密钥下发缺口）。

// WS 令牌密钥来源标识（ResolveWSTokenSecret 返回的 source 值，FR-275）。
const (
	// WSTokenSecretSourceExplicit 显式配置（jwt.ws_secret 非空）。
	WSTokenSecretSourceExplicit = "explicit"
	// WSTokenSecretSourceGenerated 生产态自动生成并持久化到数据根文件。
	WSTokenSecretSourceGenerated = "generated"
	// WSTokenSecretSourceDev dev_mode 回退内置开发值。
	WSTokenSecretSourceDev = "dev"
)

// DevWSTokenSecret dev_mode 未配时的回退值。与 CP jwt.secret、Worker jwt_secret 的
// 历史默认一致——存量 dev 部署与已下发的探针长期 token 才不因升级失效（ADR-061 决策 2.3）。
const DevWSTokenSecret = "dev-secret-change-me"

// wsTokenSecretFileName 持久化 WS 令牌密钥的文件名（置于数据根 etc/ 下）。
const wsTokenSecretFileName = "ws-token-secret.key"

// wsTokenSecretLen 自动生成密钥的随机字节数（base64 后约 44 字符）。
const wsTokenSecretLen = 32

// ResolveWSTokenSecret 按三轨裁决 CP↔Worker WS 令牌密钥（FR-275，见 ADR-061）。
// keyFilePath 为持久化文件绝对路径（一般由 dataroot.Root.Abs("etc/ws-token-secret.key") 提供）。
func ResolveWSTokenSecret(explicit string, devMode bool, keyFilePath string) (secret, source string, err error) {
	if v := strings.TrimSpace(explicit); v != "" {
		return v, WSTokenSecretSourceExplicit, nil
	}
	if devMode {
		return DevWSTokenSecret, WSTokenSecretSourceDev, nil
	}
	s, gerr := loadOrGenerateWSTokenSecret(keyFilePath)
	if gerr != nil {
		return "", "", gerr
	}
	return s, WSTokenSecretSourceGenerated, nil
}

// loadOrGenerateWSTokenSecret 读取或生成并持久化 WS 令牌密钥。
// 文件存在 → 用其内容（跨重启稳定）；内容为空视为损坏 → 报错不覆盖现场（静默轮换会让
// 已下发 Worker/探针的令牌校验悄然漂移，必须留给运维排查）；不存在 → 生成随机密钥原子写入。
func loadOrGenerateWSTokenSecret(keyFilePath string) (string, error) {
	if data, rerr := os.ReadFile(keyFilePath); rerr == nil {
		secret := strings.TrimSpace(string(data))
		if secret == "" {
			return "", fmt.Errorf("WS 令牌密钥文件为空: %s", keyFilePath)
		}
		return secret, nil
	} else if !os.IsNotExist(rerr) {
		return "", fmt.Errorf("读取 WS 令牌密钥文件失败: %w", rerr)
	}
	b := make([]byte, wsTokenSecretLen)
	if _, gerr := rand.Read(b); gerr != nil {
		return "", fmt.Errorf("生成 WS 令牌密钥失败: %w", gerr)
	}
	secret := base64.StdEncoding.EncodeToString(b)
	// 复用 FR-263 的 0600 原子写（先临时文件再 rename，chmod 失败降级继续，见其注释）。
	if werr := writeKeyFileAtomic(keyFilePath, []byte(secret)); werr != nil {
		return "", werr
	}
	return secret, nil
}
