package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/assistantmcp"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// 外部 MCP 连接治理（§4 / §6 / §7）。
//
// 本 FR 的重点不是「能连上」，而是**边界**：
//   - 端点准入走受保护出站（默认禁私网/回环/元数据，管理员可精确 origin+CIDR 放行）；
//   - 自环检测：endpoint 指向本平台自身即拒绝，否则模型可借外部工具回打自身 API；
//   - 凭据只属于该连接，不转发面板 JWT/Cookie/其它连接凭据；
//   - 共享上游权限的连接（带凭据**或**无凭据但可访问）在 V1 **仅平台管理员可用**；
//   - 上游工具描述/返回都是不可信数据，不改变 system 规则、不触发自动批准。

const (
	// assistantMCPSlugReservedSystem 保留 slug：与内置工具命名空间冲突。
	assistantMCPSlugReservedSystem = "system"
	// assistantMCPDescriptionMax 上游描述截断上限（不再往下游传整段）。
	assistantMCPDescriptionMax = 1024
	// assistantMCPDiscoveryFailureStaleMinutes 发现失败后多久开始提示「目录可能陈旧」。
	assistantMCPDiscoveryFailureStaleMinutes = 0
)

// 错误族（映射成 spec §6 的错误码）。
var (
	ErrAssistantMCPNotFound    = errors.New("外部连接不存在")
	ErrAssistantMCPConflict    = errors.New("外部连接冲突")
	ErrAssistantMCPUnreachable = errors.New("上游 MCP 不可达")
	ErrAssistantMCPProtocol    = errors.New("上游 MCP 协议错误")
	ErrAssistantMCPTooLarge    = errors.New("上游 MCP 响应过大")
	ErrAssistantMCPToolsLimit  = errors.New("上游 MCP 工具数超过上限")
	// ErrAssistantMCPAuthRejected 上游拒绝认证（凭据错误）。
	// 与「不可达」分开：凭据错要改凭据，报成不可达会把人引去看网络。
	ErrAssistantMCPAuthRejected = errors.New("上游 MCP 拒绝认证")
	ErrAssistantMCPSelfLoop     = errors.New("端点指向本平台自身")
)

// AssistantMCPService 外部连接管理。
type AssistantMCPService struct {
	db *gorm.DB
	// enc 凭据加密器（复用的同一把主密钥）。
	enc *KeyEncryptor
	// guardFactory 按放行段构造受保护出站。为 nil 时用默认（仅 https 公网）。
	guardFactory func(origins []httpclient.AllowedOrigin) *httpclient.Guard
	// allowInsecure 由 dev 模式放开明文 http。
	allowInsecure bool
	// selfAddrs 本平台自身地址（用于自环检测）。
	selfAddrs []string
	// mu 保护 toolsCache 的并发访问（工具目录读多写少）。
	mu sync.RWMutex
}

// NewAssistantMCPService 构造服务。
func NewAssistantMCPService(db *gorm.DB, enc *KeyEncryptor) *AssistantMCPService {
	return &AssistantMCPService{db: db, enc: enc}
}

// SetGuardFactory 注入受保护出站工厂（装配层按部署配置构造）。
func (s *AssistantMCPService) SetGuardFactory(f func(origins []httpclient.AllowedOrigin) *httpclient.Guard) {
	s.guardFactory = f
}

// SetAllowInsecure 设置是否允许明文 http（dev 模式）。
func (s *AssistantMCPService) SetAllowInsecure(v bool) { s.allowInsecure = v }

// SetSelfAddrs 登记本平台自身的可达地址（自环检测用）。
//
// 传进来的通常是监听地址与公网域名。空列表时自环检测退化为「只拦明显自指」，
// 不会因此放行——见 checkSelfLoop 的注释。
func (s *AssistantMCPService) SetSelfAddrs(addrs []string) { s.selfAddrs = addrs }

// guard 构造受保护出站。
func (s *AssistantMCPService) guard(origins []httpclient.AllowedOrigin) *httpclient.Guard {
	if s.guardFactory != nil {
		return s.guardFactory(origins)
	}
	return httpclient.NewGuard(httpclient.GuardOptions{
		AllowInsecure:         s.allowInsecure,
		AllowedOrigins:        origins,
		Timeout:               model.AssistantMCPCallTimeout,
		ResponseHeaderTimeout: 10 * time.Second,
	})
}

// ── 连接 CRUD ────────────────────────────────────────────────────

// MCPConnectionInput 创建/更新连接的入参。
type MCPConnectionInput struct {
	Slug           string
	Name           string
	Endpoint       string
	AuthStyle      string
	AuthHeaderName string
	AuthSource     string
	// AuthEnv 仅 authSource=env。
	AuthEnv string
	// AuthValue 明文凭据（仅 authSource=stored；空表示保留原值）。
	AuthValue string
	// ClearCredential 显式清除已存凭据。
	ClearCredential bool
	// AllowedOrigins 精确 origin + CIDR。
	AllowedOrigins []MCPAllowedOriginInput
	Enabled        bool
	// Version 乐观并发（更新时必填）。
	Version int
}

// MCPAllowedOriginInput 一条 origin 放行规则。
type MCPAllowedOriginInput struct {
	Scheme string   `json:"scheme"`
	Host   string   `json:"host"`
	Port   int      `json:"port"`
	CIDRs  []string `json:"cidrs"`
}

// MCPConnectionView 响应视图（凭据永不出现）。
type MCPConnectionView struct {
	ID             uint   `json:"id"`
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	Endpoint       string `json:"endpoint"`
	AuthStyle      string `json:"authStyle"`
	AuthHeaderName string `json:"authHeaderName"`
	AuthSource     string `json:"authSource"`
	// HasCredential 只回「有没有」，不回值（与同口径）。
	HasCredential  bool                    `json:"hasCredential"`
	AllowedOrigins []MCPAllowedOriginInput `json:"allowedOrigins"`
	Enabled        bool                    `json:"enabled"`
	PolicyVersion  int                     `json:"policyVersion"`
	LastProbeAt    *time.Time              `json:"lastProbeAt"`
	LastProbeOK    *bool                   `json:"lastProbeOk"`
	// DiscoveryStale 上次发现失败 → 目录可能是陈旧的（spec §4.3 冻结）。
	DiscoveryStale bool      `json:"discoveryStale"`
	ToolCount      int       `json:"toolCount"`
	Version        int       `json:"version"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// validateSlug 校验命名空间 slug。
func validateSlug(raw string) (string, error) {
	// **先在原文上校验**再归一化：若先 ToLower，`Upper` 会被洗成 `upper` 而通过，
	// 于是「不允许大写」这条规则形同虚设（实测踩到过）。
	trimmed := strings.TrimSpace(raw)
	slug := strings.ToLower(trimmed)
	if slug != trimmed {
		return "", fmt.Errorf("%w: slug 只允许小写字母、数字与下划线", ErrAssistantInvalid)
	}
	if slug == "" {
		return "", fmt.Errorf("%w: slug 必填", ErrAssistantInvalid)
	}
	if len(slug) > 32 {
		return "", fmt.Errorf("%w: slug 最长 32 字符", ErrAssistantInvalid)
	}
	if slug == assistantMCPSlugReservedSystem {
		// 保留字：否则外部工具会与 system.* 命名空间冲突。
		return "", fmt.Errorf("%w: slug 不能是保留字 %q", ErrAssistantInvalid, assistantMCPSlugReservedSystem)
	}
	// ^[a-z][a-z0-9_]*$
	if slug[0] < 'a' || slug[0] > 'z' {
		return "", fmt.Errorf("%w: slug 须以小写字母开头", ErrAssistantInvalid)
	}
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return "", fmt.Errorf("%w: slug 只允许小写字母、数字与下划线", ErrAssistantInvalid)
	}
	return slug, nil
}

// parseOrigins 把入参 origin 规则编译成 Guard 用的形式，并做校验。
func (s *AssistantMCPService) parseOrigins(in []MCPAllowedOriginInput) ([]httpclient.AllowedOrigin, string, error) {
	out := make([]httpclient.AllowedOrigin, 0, len(in))
	for _, o := range in {
		scheme := strings.ToLower(strings.TrimSpace(o.Scheme))
		if scheme != "http" && scheme != "https" {
			return nil, "", fmt.Errorf("%w: origin scheme 只能是 http 或 https", ErrAssistantInvalid)
		}
		host := strings.ToLower(strings.TrimSpace(o.Host))
		if host == "" {
			return nil, "", fmt.Errorf("%w: origin host 必填", ErrAssistantInvalid)
		}
		if o.Port < 0 || o.Port > 65535 {
			return nil, "", fmt.Errorf("%w: origin 端口非法", ErrAssistantInvalid)
		}
		parsed, err := toPrefixes(o.CIDRs)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrAssistantInvalid, err)
		}
		if len(parsed) == 0 {
			// 空 CIDR 等于「不允许任何地址」——这几乎一定是配置错误，明确拒绝。
			return nil, "", fmt.Errorf("%w: origin 必须给出至少一条 CIDR", ErrAssistantInvalid)
		}
		out = append(out, httpclient.AllowedOrigin{
			Scheme: scheme,
			Host:   host,
			Port:   o.Port,
			CIDRs:  parsed,
		})
	}
	encoded := ""
	if len(out) > 0 {
		b, err := json.Marshal(in)
		if err == nil {
			encoded = string(b)
		}
	}
	return out, encoded, nil
}

// CreateConnection 新建连接。
func (s *AssistantMCPService) CreateConnection(in MCPConnectionInput) (*MCPConnectionView, error) {
	slug, err := validateSlug(in.Slug)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 64 {
		return nil, fmt.Errorf("%w: name 必填且最长 64 字符", ErrAssistantInvalid)
	}

	origins, encodedOrigins, err := s.parseOrigins(in.AllowedOrigins)
	if err != nil {
		return nil, err
	}
	endpoint, err := s.validateEndpoint(in.Endpoint, origins)
	if err != nil {
		return nil, err
	}

	row := &model.AssistantMCPConnection{
		Slug:           slug,
		Name:           name,
		Endpoint:       endpoint,
		AuthStyle:      normalizeMCPAuthStyle(in.AuthStyle),
		AuthHeaderName: strings.TrimSpace(in.AuthHeaderName),
		AuthSource:     normalizeMCPAuthSource(in.AuthSource),
		AuthEnv:        strings.TrimSpace(in.AuthEnv),
		AllowedOrigins: encodedOrigins,
		// **连接建立后全部工具禁执行**（spec §3：默认策略）。
		// 这里 enabled 只控制连接本身；工具级 enabled 另说。
		Enabled:       in.Enabled,
		PolicyVersion: 1,
		Version:       1,
	}
	if err := s.applyMCPCredential(row, in); err != nil {
		return nil, err
	}

	if err := s.db.Create(row).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: slug 已存在", ErrAssistantMCPConflict)
		}
		return nil, err
	}
	return s.viewOfConnection(row), nil
}

// UpdateConnection 更新连接。
func (s *AssistantMCPService) UpdateConnection(id uint, in MCPConnectionInput) (*MCPConnectionView, error) {
	var row model.AssistantMCPConnection
	if err := s.db.First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: 连接不存在", ErrAssistantMCPNotFound)
		}
		return nil, err
	}
	if in.Version != 0 && in.Version != row.Version {
		return nil, fmt.Errorf("%w: 连接已被修改，请刷新后重试", ErrAssistantMCPConflict)
	}

	if strings.TrimSpace(in.Slug) != "" {
		slug, err := validateSlug(in.Slug)
		if err != nil {
			return nil, err
		}
		row.Slug = slug
	}
	if strings.TrimSpace(in.Name) != "" {
		if len(in.Name) > 64 {
			return nil, fmt.Errorf("%w: name 最长 64 字符", ErrAssistantInvalid)
		}
		row.Name = strings.TrimSpace(in.Name)
	}

	origins, encodedOrigins, err := s.parseOrigins(in.AllowedOrigins)
	if err != nil {
		return nil, err
	}
	endpoint, err := s.validateEndpoint(in.Endpoint, origins)
	if err != nil {
		return nil, err
	}
	row.Endpoint = endpoint
	row.AllowedOrigins = encodedOrigins
	row.AuthStyle = normalizeMCPAuthStyle(in.AuthStyle)
	row.AuthHeaderName = strings.TrimSpace(in.AuthHeaderName)
	row.AuthSource = normalizeMCPAuthSource(in.AuthSource)
	row.AuthEnv = strings.TrimSpace(in.AuthEnv)
	if err := s.applyMCPCredential(&row, in); err != nil {
		return nil, err
	}

	// 策略变化即使旧确认失效（spec §4.3）。
	row.PolicyVersion++
	row.Enabled = in.Enabled
	row.Version++

	if err := s.db.Save(&row).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: slug 已存在", ErrAssistantMCPConflict)
		}
		return nil, err
	}
	return s.viewOfConnection(&row), nil
}

// DeleteConnection 删除连接及其工具。
func (s *AssistantMCPService) DeleteConnection(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var row model.AssistantMCPConnection
		if err := tx.First(&row, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: 连接不存在", ErrAssistantMCPNotFound)
			}
			return err
		}
		if err := tx.Where("connection_id = ?", id).Delete(&model.AssistantMCPTool{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.AssistantMCPConnection{}, id).Error
	})
}

// ListConnections 连接列表。
func (s *AssistantMCPService) ListConnections() ([]MCPConnectionView, error) {
	var rows []model.AssistantMCPConnection
	if err := s.db.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]MCPConnectionView, 0, len(rows))
	for i := range rows {
		out = append(out, *s.viewOfConnection(&rows[i]))
	}
	return out, nil
}

// GetConnection 连接详情。
func (s *AssistantMCPService) GetConnection(id uint) (*MCPConnectionView, error) {
	var row model.AssistantMCPConnection
	if err := s.db.First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: 连接不存在", ErrAssistantMCPNotFound)
		}
		return nil, err
	}
	return s.viewOfConnection(&row), nil
}

// viewOfConnection 构造响应视图（凭据永不出现）。
func (s *AssistantMCPService) viewOfConnection(row *model.AssistantMCPConnection) *MCPConnectionView {
	var toolCount int64
	_ = s.db.Model(&model.AssistantMCPTool{}).Where("connection_id = ?", row.ID).Count(&toolCount).Error

	var origins []MCPAllowedOriginInput
	if row.AllowedOrigins != "" {
		_ = json.Unmarshal([]byte(row.AllowedOrigins), &origins)
	}
	// 「目录可能陈旧」：上次探测失败即提示（spec §4.3 冻结：发现失败不改旧目录，
	// 但界面必须标注，避免管理员以为已同步）。
	stale := row.LastProbeOK != nil && !*row.LastProbeOK

	return &MCPConnectionView{
		ID:             row.ID,
		Slug:           row.Slug,
		Name:           row.Name,
		Endpoint:       row.Endpoint,
		AuthStyle:      row.AuthStyle,
		AuthHeaderName: row.AuthHeaderName,
		AuthSource:     row.AuthSource,
		// 只回「有没有凭据」，不回值。
		HasCredential:  row.AuthEnc != "" || row.AuthEnv != "",
		AllowedOrigins: origins,
		Enabled:        row.Enabled,
		PolicyVersion:  row.PolicyVersion,
		LastProbeAt:    row.LastProbeAt,
		LastProbeOK:    row.LastProbeOK,
		DiscoveryStale: stale,
		ToolCount:      int(toolCount),
		Version:        row.Version,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

// applyMCPCredential 应用凭据三态（与同口径）。
func (s *AssistantMCPService) applyMCPCredential(row *model.AssistantMCPConnection, in MCPConnectionInput) error {
	if in.ClearCredential || row.AuthSource == model.MCPAuthSourceNone {
		row.AuthEnc = ""
		if row.AuthSource == model.MCPAuthSourceNone {
			row.AuthEnv = ""
		}
		return nil
	}
	switch row.AuthSource {
	case model.MCPAuthSourceEnv:
		if row.AuthEnv == "" {
			return fmt.Errorf("%w: auth_source=env 时必须给出环境变量名", ErrAssistantInvalid)
		}
		row.AuthEnc = ""
		return nil
	case model.MCPAuthSourceStored:
		row.AuthEnv = ""
		if in.AuthValue == "" {
			// 留空 = 保留原密文（更新语义）。
			return nil
		}
		if s.enc == nil {
			return fmt.Errorf("%w: 未配置凭据加密器", ErrAssistantEncryptorMissing)
		}
		enc, err := s.enc.Encrypt(in.AuthValue)
		if err != nil {
			return fmt.Errorf("加密凭据失败: %w", err)
		}
		row.AuthEnc = enc
		return nil
	default:
		return fmt.Errorf("%w: 未知的 auth_source", ErrAssistantInvalid)
	}
}

func normalizeMCPAuthStyle(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case model.MCPAuthStyleHeader:
		return model.MCPAuthStyleHeader
	case model.MCPAuthStyleBearer:
		return model.MCPAuthStyleBearer
	default:
		return model.MCPAuthStyleNone
	}
}

func normalizeMCPAuthSource(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case model.MCPAuthSourceEnv:
		return model.MCPAuthSourceEnv
	case model.MCPAuthSourceStored:
		return model.MCPAuthSourceStored
	default:
		return model.MCPAuthSourceNone
	}
}

// validateEndpoint 校验端点（静态 URL 准入 + 自环检测）。
func (s *AssistantMCPService) validateEndpoint(raw string, origins []httpclient.AllowedOrigin) (string, error) {
	g := s.guard(origins)
	u, err := g.ValidateBaseURL(strings.TrimSpace(raw))
	if err != nil {
		// 复用 Guard 的错误语义（ErrBlockedTarget / ErrProxyUnsupported）。
		return "", err
	}
	if err := s.checkSelfLoop(u); err != nil {
		return "", err
	}
	// **保存阶段**就拦掉明显不可达的目标（回环/元数据/私网字面量 IP）。
	//
	// 受保护出站的准入发生在拨号点，但保存时不拦会让管理员以为配置已生效，
	// 直到第一次调用才失败。字面量 IP 无需 DNS 就能判定，因此这里直接拒绝。
	// 域名不在此判定（保存期不做 DNS，避免把「暂时解析不到」当成配置错误）。
	if ip, perr := netip.ParseAddr(u.Hostname()); perr == nil {
		if err := checkLiteralAddrAllowed(ip, origins); err != nil {
			return "", err
		}
	}
	// 落库统一成 origin + path（去掉尾部斜杠），避免同一端点多写法造成重复。
	clean := u.Scheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/")
	return clean, nil
}

// checkLiteralAddrAllowed 保存期判定字面量 IP 是否可接受。
//
// 规则与受保护出站的拨号准入一致（元数据永远拒、私网需显式 CIDR），
// 只是提前到保存阶段，让配置错误立刻可见。
func checkLiteralAddrAllowed(ip netip.Addr, origins []httpclient.AllowedOrigin) error {
	for _, p := range []string{
		"169.254.169.254/32", "169.254.170.2/32", "fd00:ec2::254/128", "100.100.100.200/32",
	} {
		prefix := netip.MustParsePrefix(p)
		if prefix.Contains(ip) {
			return fmt.Errorf("%w: 拒绝访问云元数据地址 %s", httpclient.ErrBlockedTarget, ip)
		}
	}
	if ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("%w: 拒绝访问特殊地址 %s", httpclient.ErrBlockedTarget, ip)
	}
	if !ip.IsPrivate() && !ip.IsLoopback() {
		return nil // 公网可达
	}
	for _, o := range origins {
		for _, c := range o.CIDRs {
			if c.Contains(ip) {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: %s 属私网/回环且未被放行", httpclient.ErrBlockedTarget, ip)
}

// checkSelfLoop 自环检测（spec §7 第 3 条）。
//
// 为什么必须拒绝：外部工具如果是本平台自己的 API，模型就能「借外部工具回打自身」，
// 绕开对话路径上的所有授权（因为那一跳是我们主动发出的请求）。
//
// 判据：host 命中已登记的自有地址，或落在本机回环/监听地址上。
// 注意**不是**靠「像不像自己」猜，而是靠装配层显式登记 + 回环判定；
// 两者都没有时宁可放过也不误判（误判会让自托管 MCP 完全不可用），
// 但回环那一条永远成立。
func (s *AssistantMCPService) checkSelfLoop(u *url.URL) error {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	candidate := host + ":" + port

	for _, self := range s.selfAddrs {
		if strings.EqualFold(strings.TrimSpace(self), candidate) {
			return fmt.Errorf("%w: 端点指向本平台自身", ErrAssistantMCPSelfLoop)
		}
		if strings.EqualFold(strings.TrimSpace(self), host) {
			return fmt.Errorf("%w: 端点指向本平台自身", ErrAssistantMCPSelfLoop)
		}
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		// 回环地址无法区分「自己的 API」与「同机另一个服务」，但两者都不该
		// 由外部连接回打——自托管 MCP 应走局域网地址而非回环。
		return fmt.Errorf("%w: 端点为回环地址", ErrAssistantMCPSelfLoop)
	}
	return nil
}

// clientFor 构造某连接的客户端（解密凭据）。
func (s *AssistantMCPService) clientFor(row *model.AssistantMCPConnection) (*assistantmcp.Client, error) {
	origins, _, err := s.parseOrigins(decodeOrigins(row.AllowedOrigins))
	if err != nil {
		return nil, err
	}
	g := s.guard(origins)

	secret := ""
	switch row.AuthSource {
	case model.MCPAuthSourceEnv:
		secret = resolveEnvSecret(row.AuthEnv)
	case model.MCPAuthSourceStored:
		if row.AuthEnc != "" {
			if s.enc == nil {
				return nil, fmt.Errorf("%w: 未配置凭据加密器", ErrAssistantEncryptorMissing)
			}
			plain, derr := s.enc.Decrypt(row.AuthEnc)
			if derr != nil {
				return nil, fmt.Errorf("解密凭据失败: %w", derr)
			}
			secret = plain
		}
	}

	return assistantmcp.New(assistantmcp.Options{
		HTTPClient:      g.Client(),
		Endpoint:        row.Endpoint,
		AuthStyle:       row.AuthStyle,
		AuthHeaderName:  row.AuthHeaderName,
		AuthValue:       secret,
		ResponseMax:     model.AssistantMCPResponseMax,
		CallTimeout:     model.AssistantMCPCallTimeout,
		DiscoverTimeout: model.AssistantMCPDiscoverTimeout,
	})
}

// resolveEnvSecret 从环境变量读凭据（auth_source=env）。
//
// 只读该连接自己声明的变量名，**不读取任何面板 JWT / Cookie**（spec §7 第 4 条）。
// 变量名在保存时已校验为合法标识符，这里再 trim 一次避免空白。
func resolveEnvSecret(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}

// decodeOrigins 把落库的 origin JSON 解成入参形态。
func decodeOrigins(raw string) []MCPAllowedOriginInput {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []MCPAllowedOriginInput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// ProbeConnection 连通性探测（不落库工具，只记探测结果）。
func (s *AssistantMCPService) ProbeConnection(ctx context.Context, id uint) (time.Duration, error) {
	var row model.AssistantMCPConnection
	if err := s.db.First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, fmt.Errorf("%w: 连接不存在", ErrAssistantMCPNotFound)
		}
		return 0, err
	}
	c, err := s.clientFor(&row)
	if err != nil {
		return 0, err
	}
	latency, probeErr := c.Ping(ctx)

	now := time.Now()
	ok := probeErr == nil
	// 记录探测结果：失败也要记（界面据此标注「目录可能陈旧」）。
	s.db.Model(&model.AssistantMCPConnection{}).Where("id = ?", row.ID).
		Updates(map[string]any{"last_probe_at": now, "last_probe_ok": ok})

	if probeErr != nil {
		return latency, mapMCPErr(probeErr)
	}
	return latency, nil
}

// DiscoverTools 拉取并落库工具清单（spec §4.3）。
//
// 失败语义（冻结）：整次失败**不改变**现有目录与 schema_hash，旧工具维持原状态继续可用；
// 但记下探测失败，界面标注「目录可能是陈旧的」。
func (s *AssistantMCPService) DiscoverTools(ctx context.Context, id uint) ([]MCPToolView, error) {
	var row model.AssistantMCPConnection
	if err := s.db.First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: 连接不存在", ErrAssistantMCPNotFound)
		}
		return nil, err
	}
	c, err := s.clientFor(&row)
	if err != nil {
		return nil, err
	}
	tools, discoverErr := c.DiscoverTools(ctx)
	now := time.Now()
	if discoverErr != nil {
		// 记失败但**不动旧目录**。
		s.db.Model(&model.AssistantMCPConnection{}).Where("id = ?", row.ID).
			Updates(map[string]any{"last_probe_at": now, "last_probe_ok": false})
		return nil, mapMCPErr(discoverErr)
	}

	// 落库：更新既有、新增缺失、**消失的置 enabled=false 并保留记录**（审计需要）。
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var existing []model.AssistantMCPTool
		if err := tx.Where("connection_id = ?", row.ID).Find(&existing).Error; err != nil {
			return err
		}
		byRemote := make(map[string]*model.AssistantMCPTool, len(existing))
		for i := range existing {
			byRemote[existing[i].RemoteName] = &existing[i]
		}
		seen := make(map[string]struct{}, len(tools))
		for _, t := range tools {
			name := strings.TrimSpace(t.Name)
			if name == "" {
				continue
			}
			seen[name] = struct{}{}
			/*
			  必须用 `assistantExternalToolName`（可逆编码），**不能手拼点号**。

			  真实上游对 function name 的字符集是 `^[a-zA-Z0-9_-]{1,64}$`，点号不在其中：
			  带 `.` 的工具名会让整个请求 400（实测 deepseek 返回
			  `the tool name is invalid or duplicated`，一轮 run 直接 failed）。
			  编码把 `_` 转成 `_5f`、其它非法字符转 `_XX`，保证可逆且只含合法字符；
			  解码见 `decodeExternalSegment`。

			  历史：这里原先是 `"conn." + slug + "." + name`，而编码函数当时**写了但没接线**
			  （无任何调用点），于是「已修」只停留在函数层面——这条注释就是为了防止再退回去。
			*/
			namespaced := assistantExternalToolName(row.Slug, name)
			schemaHash := hashSchema(t.InputSchema)
			desc := truncateRunes(strings.TrimSpace(t.Description), assistantMCPDescriptionMax)

			if cur, ok := byRemote[name]; ok {
				updates := map[string]any{
					"namespaced_name": namespaced,
					"description":     desc,
					"input_schema":    string(t.InputSchema),
					"discovered_at":   now,
				}
				// schema 变化：更新 hash 并**使该工具所有未消费票据失效**（spec §4.3）。
				// 只失效 pending 不够——已批准未执行的票据一旦放过，用户核对的参数
				// 与 UI 展示的 schema 就对不上了。
				if cur.SchemaHash != schemaHash {
					updates["schema_hash"] = schemaHash
					if err := tx.Model(&model.AssistantConfirmation{}).
						Where("tool_name = ? AND status IN ?", cur.NamespacedName,
							[]string{model.AssistantConfirmStatusPending, model.AssistantConfirmStatusApproved}).
						Updates(map[string]any{
							"status":     model.AssistantConfirmStatusInvalidated,
							"decided_at": now,
						}).Error; err != nil {
						return err
					}
				}
				if err := tx.Model(&model.AssistantMCPTool{}).Where("id = ?", cur.ID).
					Updates(updates).Error; err != nil {
					return err
				}
				continue
			}
			// 新工具：**默认 disabled**（spec §3：连接建立后全部工具禁执行，管理员显式启用）。
			created := model.AssistantMCPTool{
				ConnectionID:   row.ID,
				RemoteName:     name,
				NamespacedName: namespaced,
				Description:    desc,
				InputSchema:    string(t.InputSchema),
				Risk:           model.MCPToolRiskUnknown,
				Enabled:        false,
				SchemaHash:     schemaHash,
				DiscoveredAt:   now,
			}
			if err := tx.Create(&created).Error; err != nil {
				return err
			}
		}
		// 消失的工具：置 enabled=false，保留记录。
		for name, cur := range byRemote {
			if _, ok := seen[name]; ok {
				continue
			}
			if err := tx.Model(&model.AssistantMCPTool{}).Where("id = ?", cur.ID).
				Updates(map[string]any{"enabled": false}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.AssistantMCPConnection{}).Where("id = ?", row.ID).
			Updates(map[string]any{"last_probe_at": now, "last_probe_ok": true}).Error
	})
	if err != nil {
		return nil, err
	}
	return s.ListTools(row.ID)
}

// MCPToolView 工具视图。
type MCPToolView struct {
	ID             uint   `json:"id"`
	ConnectionID   uint   `json:"connectionId"`
	RemoteName     string `json:"remoteName"`
	NamespacedName string `json:"namespacedName"`
	// Description 上游描述，**不可信**（界面须标注）。
	Description  string    `json:"description"`
	InputSchema  string    `json:"inputSchema"`
	Risk         string    `json:"risk"`
	Enabled      bool      `json:"enabled"`
	SchemaHash   string    `json:"schemaHash"`
	DiscoveredAt time.Time `json:"discoveredAt"`
}

// ListTools 列出某连接的工具。
func (s *AssistantMCPService) ListTools(connectionID uint) ([]MCPToolView, error) {
	var rows []model.AssistantMCPTool
	if err := s.db.Where("connection_id = ?", connectionID).
		Order("remote_name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]MCPToolView, 0, len(rows))
	for _, r := range rows {
		out = append(out, MCPToolView{
			ID: r.ID, ConnectionID: r.ConnectionID,
			RemoteName: r.RemoteName, NamespacedName: r.NamespacedName,
			Description: r.Description, InputSchema: r.InputSchema,
			Risk: r.Risk, Enabled: r.Enabled, SchemaHash: r.SchemaHash,
			DiscoveredAt: r.DiscoveredAt,
		})
	}
	return out, nil
}

// UpdateTool 启停 + 设定风险分类（spec §6）。
func (s *AssistantMCPService) UpdateTool(connectionID, toolID uint, enabled *bool, risk string) error {
	var row model.AssistantMCPTool
	if err := s.db.Where("id = ? AND connection_id = ?", toolID, connectionID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: 工具不存在", ErrAssistantMCPNotFound)
		}
		return err
	}
	updates := map[string]any{}
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	if r := strings.ToLower(strings.TrimSpace(risk)); r != "" {
		switch r {
		case model.MCPToolRiskRead, model.MCPToolRiskWrite, model.MCPToolRiskUnknown:
			updates["risk"] = r
		default:
			return fmt.Errorf("%w: risk 只能是 read / write / unknown", ErrAssistantInvalid)
		}
	}
	if len(updates) == 0 {
		return nil
	}
	// 风险分类变化也递增连接的策略版本：它使旧确认失效（spec §4.3）。
	if _, changed := updates["risk"]; changed {
		if err := s.db.Model(&model.AssistantMCPConnection{}).Where("id = ?", connectionID).
			UpdateColumn("policy_version", gorm.Expr("policy_version + 1")).Error; err != nil {
			return err
		}
	}
	return s.db.Model(&model.AssistantMCPTool{}).Where("id = ?", toolID).Updates(updates).Error
}

// hashSchema 计算 schema 指纹。
//
// **规范化后再哈希**：上游可能改变键序或空白，那不算 schema 变化。
// 若直接对原始字节哈希，上游一次无害的格式化就会让全部票据失效。
func hashSchema(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// 解析不了就退化为原文哈希（至少变化时能失效）。
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	normalized := canonicalJSON(v)
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// canonicalJSON 生成键排序的紧凑 JSON。
func canonicalJSON(v any) string {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sortStrings(keys)
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			b.WriteString(canonicalJSON(t[k]))
		}
		b.WriteByte('}')
		return b.String()
	case []any:
		var b strings.Builder
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(canonicalJSON(item))
		}
		b.WriteByte(']')
		return b.String()
	default:
		out, err := json.Marshal(v)
		if err != nil {
			return "null"
		}
		return string(out)
	}
}

// sortStrings 简单插入排序（键数量很小，避免额外依赖）。
func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// mapMCPErr 把客户端错误映射到服务层错误族（供路由层转 HTTP 码）。
func mapMCPErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, assistantmcp.ErrToolsLimit):
		return fmt.Errorf("%w: %v", ErrAssistantMCPToolsLimit, err)
	case errors.Is(err, assistantmcp.ErrTooLarge):
		return fmt.Errorf("%w: %v", ErrAssistantMCPTooLarge, err)
	case errors.Is(err, assistantmcp.ErrAuthRejected):
		return fmt.Errorf("%w: %v", ErrAssistantMCPAuthRejected, err)
	case errors.Is(err, assistantmcp.ErrUnreachable):
		return fmt.Errorf("%w: %v", ErrAssistantMCPUnreachable, err)
	case errors.Is(err, assistantmcp.ErrProtocol):
		return fmt.Errorf("%w: %v", ErrAssistantMCPProtocol, err)
	default:
		return fmt.Errorf("%w: %v", ErrAssistantMCPProtocol, err)
	}
}
