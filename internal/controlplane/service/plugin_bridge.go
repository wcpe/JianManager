package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// PluginBridgeScope 是插件桥 token 的 scope claim 值，Worker 握手时据此与终端 token 区分。
// 与 internal/worker/ws.PluginBridgeScope 保持一致（见 ADR-016）。
const PluginBridgeScope = "plugin-bridge"

// pluginBridgeTokenTTL 插件桥 token 有效期。
// 插件桥 token 是【实例级持久连接凭据】——写入探针 config.yml 后，探针在整个生命周期内每次反向
// WS 握手都复用它，本质不同于一次性终端 token。普通重启/重连不会重新下发 config（DeployServerProbe
// 仅在建服与 FR-068 在线更新时调用），故 token 必须在实例整个运行期内有效；若取短 TTL，建服数分钟后
// 任何重启都会因 token 过期被 Worker 握手拒绝（401），桥永久连不上。安全上：桥仅本机回环可达、token
// 按实例隔离 scope=plugin-bridge、且已落在本机 config 文件（文件本身即信任边界），短 TTL 既挡不住实质
// 重放又必然弄坏重启，故取等效实例生命周期的长有效期。轮换由重新建服或 FR-068 在线更新下发新 token。
const pluginBridgeTokenTTL = 87600 * time.Hour // 约 10 年，等效实例生命周期

// PluginBridgeService 负责签发实例级插件桥连接 token，并生成探针 config.yml 的 bridge 段。
// 类比 TerminalService 的一次性 token，但 scope=plugin-bridge、不区分 read/write（见 ADR-016 / FR-065）。
type PluginBridgeService struct {
	jwtSecret string
}

// NewPluginBridgeService 创建插件桥服务。
func NewPluginBridgeService(jwtSecret string) *PluginBridgeService {
	return &PluginBridgeService{jwtSecret: jwtSecret}
}

// IssueToken 为指定实例签发插件桥连接 token（HS256，claims 含 instanceId + scope=plugin-bridge）。
// instanceUUID 为实例 UUID（与 Worker 会话表键、握手 query instance 一致）。
func (s *PluginBridgeService) IssueToken(instanceUUID string) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"instanceId": instanceUUID,
		"scope":      PluginBridgeScope,
		"exp":        now.Add(pluginBridgeTokenTTL).Unix(),
		"iat":        now.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(s.jwtSecret))
	if err != nil {
		return "", fmt.Errorf("签发插件桥 token 失败: %w", err)
	}
	return signed, nil
}

// BuildBridgeConfigBlock 生成探针 config.yml 的 bridge 段：worker WS 地址 + 实例级 token。
// 探针读取此段后主动反向连入 ws://<host>:<wsPort>/ws/plugin-bridge?token=&instance=（见 ADR-016）。
// token 为空（签发失败时调用方传空）则段内 enabled=false，探针不连，监控 /metrics 仍照常工作。
func (s *PluginBridgeService) BuildBridgeConfigBlock(wsURL, instanceUUID, token string) string {
	var b strings.Builder
	b.WriteString("# 插件桥（ServerProbe 反向 WS ↔ Worker，FR-065，见 ADR-016）：探针启用后主动连入本机 Worker。\n")
	b.WriteString("# 由 JianManager 建服时自动写入；token 为实例级 JWT（scope=plugin-bridge），仅握手校验一次。\n")
	b.WriteString("bridge:\n")
	enabled := token != "" && wsURL != ""
	fmt.Fprintf(&b, "  enabled: %t\n", enabled)
	fmt.Fprintf(&b, "  url: %q\n", wsURL)
	fmt.Fprintf(&b, "  instance: %q\n", instanceUUID)
	fmt.Fprintf(&b, "  token: %q\n", token)
	return b.String()
}

// pluginBridgeWSURL 由节点 host 与 WS 端口构造探针应连入的插件桥 WS 地址。
// 探针与 Worker 同机，走本机回环更稳妥（host 可能是对 CP 暴露的地址）：优先回环。
func pluginBridgeWSURL(wsPort int) string {
	return fmt.Sprintf("ws://127.0.0.1:%d/ws/plugin-bridge", wsPort)
}
