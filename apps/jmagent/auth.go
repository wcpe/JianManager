package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

// runAuth 分发 `jm auth <sub>`（FR-565）。
func runAuth(cfg Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: jm auth login|status|logout [--host <url>]")
	}
	sub := args[0]
	rest := args[1:]

	// --host 覆盖本次操作的目标主机。
	host, remaining, err := extractHostFlag(rest)
	if err != nil {
		return err
	}
	if host != "" {
		cfg.BaseURL = normalizeURL(host)
		if k, err := hostKeyOf(cfg.BaseURL); err == nil {
			cfg.HostKey = k
			// 切换 host 后需要重新套用该 host 的元信息与令牌。
			cfg = reloadHostMeta(cfg, k)
		}
	}

	switch sub {
	case "login":
		return runAuthLogin(cfg, remaining)
	case "status":
		return runAuthStatus(cfg)
	case "logout":
		return runAuthLogout(cfg)
	default:
		return fmt.Errorf("未知 auth 子命令 %q；支持 login / status / logout", sub)
	}
}

// extractHostFlag 抽出 --host 值。
func extractHostFlag(args []string) (host string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--host":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--host 需要参数")
			}
			i++
			host = args[i]
		case strings.HasPrefix(a, "--host="):
			host = strings.TrimPrefix(a, "--host=")
		default:
			rest = append(rest, a)
		}
	}
	return host, rest, nil
}

// reloadHostMeta 按 host 键重新套用配置元信息与钥匙串令牌。
func reloadHostMeta(cfg Config, hostKey string) Config {
	cf, err := loadConfigFile()
	if err == nil {
		if h, ok := cf.Hosts[hostKey]; ok {
			cfg.Username = h.Username
			cfg.TokenID = h.TokenID
			cfg.TokenPrefix = h.TokenPrefix
		}
	}
	cfg.Token = ""
	if tok, ok := loadTokenFromKeyring(hostKey); ok {
		cfg.Token = tok
	}
	return cfg
}

// deviceCodeResponse 对应 POST /api/v1/oauth/device/code。
type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// deviceTokenResponse 成功时的响应。
type deviceTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Prefix      string `json:"prefix"`
}

// deviceErrorResponse RFC 8628 错误体。
type deviceErrorResponse struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// runAuthLogin 走 device flow 登录。
func runAuthLogin(cfg Config, args []string) error {
	if err := requireURL(cfg); err != nil {
		return err
	}
	dryRun := containsFlag(args, "--dry-run")

	client := newClient(Config{BaseURL: cfg.BaseURL})

	// 1. 申请设备码。
	label := deviceLabel()
	body, _ := json.Marshal(map[string]string{"deviceLabel": label})
	respBody, err := client.doRequest("POST", "/api/v1/oauth/device/code", nil, body)
	if err != nil {
		return fmt.Errorf("申请设备码失败: %w", err)
	}
	var dc deviceCodeResponse
	if err := json.Unmarshal(respBody, &dc); err != nil {
		return fmt.Errorf("解析设备码响应失败: %w", err)
	}

	fmt.Fprintf(os.Stderr, "请在浏览器打开：%s\n", dc.VerificationURI)
	fmt.Fprintf(os.Stderr, "输入代码：%s\n", dc.UserCode)
	fmt.Fprintf(os.Stderr, "等待授权中（Ctrl+C 取消，%d 秒内有效）...\n", dc.ExpiresIn)

	if dryRun {
		fmt.Fprintln(os.Stdout, "（--dry-run：未轮询，未保存令牌）")
		return nil
	}

	// 2. 轮询直到授权/拒绝/过期。
	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	var token string
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("授权超时，请重新运行 jm auth login")
		}
		time.Sleep(interval)

		plaintext, done, err := pollDeviceToken(client, dc.DeviceCode)
		if err != nil {
			return err
		}
		if done {
			token = plaintext
			break
		}
	}

	// 3. 保存到钥匙串 + 写配置。
	if err := saveTokenToKeyring(cfg.HostKey, token); err != nil {
		// 钥匙串不可用：不落盘明文，直接失败并提示改用环境变量。
		return fmt.Errorf("%s", explainKeyringError(err))
	}

	// 用新令牌查 whoami，拿到用户名与 tokenId 供后续 logout/status 使用。
	meta := fetchWhoami(Config{BaseURL: cfg.BaseURL, Token: token})
	if err := upsertHostConfig(cfg.HostKey, cfg.BaseURL, meta, token); err != nil {
		return err
	}

	if meta.Username != "" {
		fmt.Fprintf(os.Stdout, "已登录 %s（%s）\n", meta.Username, cfg.BaseURL)
	} else {
		fmt.Fprintf(os.Stdout, "已登录 %s\n", cfg.BaseURL)
	}
	return nil
}

// pollDeviceToken 单次轮询；done=false 表示仍在等待。
func pollDeviceToken(client *Client, deviceCode string) (plaintext string, done bool, err error) {
	body, _ := json.Marshal(map[string]string{"deviceCode": deviceCode})
	respBody, callErr := client.doRequest("POST", "/api/v1/oauth/device/token", nil, body)
	if callErr == nil {
		var dr deviceTokenResponse
		if err := json.Unmarshal(respBody, &dr); err != nil {
			return "", false, fmt.Errorf("解析令牌响应失败: %w", err)
		}
		return dr.AccessToken, true, nil
	}

	// 非 2xx：按 RFC 8628 的 error 分支处理。
	ae, ok := asAPIError(callErr)
	if !ok {
		return "", false, callErr
	}
	// client.doRequest 已把 message 填成 CP 的 error_description，
	// 这里再读一次原始 error 字段区分待定与终态。
	var de deviceErrorResponse
	if respBodyErr := decodeDeviceError(ae, &de); respBodyErr == nil {
		switch de.Error {
		case "authorization_pending":
			return "", false, nil
		case "slow_down":
			return "", false, nil
		case "access_denied":
			return "", false, fmt.Errorf("用户拒绝了授权")
		case "expired_token":
			return "", false, fmt.Errorf("设备码已过期，请重新运行 jm auth login")
		}
	}
	return "", false, callErr
}

// decodeDeviceError 从 APIError 还原 RFC 8628 错误码。
func decodeDeviceError(ae *APIError, out *deviceErrorResponse) error {
	if ae == nil {
		return fmt.Errorf("无错误信息")
	}
	// APIError.Code 保存的是 CP 响应体的 error 字段。
	out.Error = ae.Code
	out.Description = ae.Message
	return nil
}

// whoamiMeta 从 whoami 响应提取展示与吊销所需信息。
type whoamiMeta struct {
	Username    string
	TokenID     uint
	TokenPrefix string
	ExpiresAt   string
}

// fetchWhoami 查询当前令牌身份；失败返回空 meta（不阻断登录）。
func fetchWhoami(cfg Config) whoamiMeta {
	client := newClient(cfg)
	body, err := client.get("/api/v1/agent/whoami", nil)
	if err != nil {
		return whoamiMeta{}
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return whoamiMeta{}
	}
	m := whoamiMeta{}
	if v, ok := raw["username"].(string); ok {
		m.Username = v
	}
	if v, ok := raw["tokenId"].(float64); ok {
		m.TokenID = uint(v)
	}
	if v, ok := raw["tokenPrefix"].(string); ok {
		m.TokenPrefix = v
	}
	if v, ok := raw["expiresAt"].(string); ok {
		m.ExpiresAt = v
	}
	return m
}

// upsertHostConfig 写入或更新 host 配置。
func upsertHostConfig(hostKey, baseURL string, meta whoamiMeta, token string) error {
	cf, err := loadConfigFile()
	if err != nil {
		return err
	}
	cf.Hosts[hostKey] = HostConfig{
		URL:            baseURL,
		Username:       meta.Username,
		TokenID:        meta.TokenID,
		TokenPrefix:    tokenPrefixOf(token),
		TokenExpiresAt: meta.ExpiresAt,
	}
	// 首次登录自动设为默认。
	if cf.Default == "" {
		cf.Default = hostKey
	}
	return saveConfigFile(cf)
}

// tokenPrefixOf 取令牌前 9 位（jmut_ + 4）作展示前缀。
func tokenPrefixOf(token string) string {
	if len(token) < 9 {
		return token
	}
	return token[:9]
}

// runAuthStatus 显示当前登录状态。
func runAuthStatus(cfg Config) error {
	out := map[string]any{
		"host":     cfg.BaseURL,
		"loggedIn": cfg.Token != "",
	}
	if cfg.Username != "" {
		out["username"] = cfg.Username
	}
	if cfg.TokenPrefix != "" {
		out["tokenPrefix"] = cfg.TokenPrefix
	}
	if cfg.TokenID != 0 {
		out["tokenId"] = cfg.TokenID
	}

	if cfg.Output == "json" {
		return printJSON(out)
	}

	fmt.Fprintf(os.Stdout, "主机:   %s\n", cfg.BaseURL)
	if cfg.Token == "" {
		fmt.Fprintln(os.Stdout, "状态:   未登录")
		return nil
	}
	fmt.Fprintln(os.Stdout, "状态:   已登录")
	if cfg.Username != "" {
		fmt.Fprintf(os.Stdout, "用户:   %s\n", cfg.Username)
	}
	if cfg.TokenPrefix != "" {
		fmt.Fprintf(os.Stdout, "令牌:   %s…\n", cfg.TokenPrefix)
	}
	return nil
}

// runAuthLogout 本地删除 + 远端吊销（FR-565 决策 5）。
func runAuthLogout(cfg Config) error {
	if cfg.HostKey == "" {
		return fmt.Errorf("未确定当前主机，无法登出")
	}
	cf, err := loadConfigFile()
	if err != nil {
		return err
	}
	hostCfg, existed := cf.Hosts[cfg.HostKey]

	var remoteErr error
	// 远端吊销：需要令牌与 tokenId。
	if cfg.Token != "" && hostCfg.TokenID != 0 {
		client := newClient(cfg)
		path := fmt.Sprintf("/api/v1/user/tokens/%d", hostCfg.TokenID)
		if _, err := client.doRequest("DELETE", path, nil, nil); err != nil {
			remoteErr = err
		}
	}

	// 本地清理：无论远端成功与否都要做，否则用户会被卡在「登不出去」。
	if err := deleteTokenFromKeyring(cfg.HostKey); err != nil {
		return fmt.Errorf("%s", explainKeyringError(err))
	}
	if existed {
		delete(cf.Hosts, cfg.HostKey)
		if cf.Default == cfg.HostKey {
			cf.Default = ""
			// 还有其它 host 时顺延一个当默认，避免下次必须显式指定。
			for k := range cf.Hosts {
				cf.Default = k
				break
			}
		}
		if err := saveConfigFile(cf); err != nil {
			return err
		}
	}

	if remoteErr != nil {
		fmt.Fprintf(os.Stderr, "提示: 本地已登出，但远端吊销失败（%v）。\n", remoteErr)
		fmt.Fprintln(os.Stderr, "      请到 Web 「设置 → 我的令牌」手动吊销该令牌。")
		return nil
	}
	fmt.Fprintf(os.Stdout, "已登出 %s（本地清除 + 远端吊销）\n", cfg.BaseURL)
	return nil
}

// deviceLabel 生成本机标识，展示在授权页让用户确认是自己发起的。
func deviceLabel() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s (%s)", host, runtime.GOOS)
}

// containsFlag 判断选项是否出现（含 = 形式）。
func containsFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}
