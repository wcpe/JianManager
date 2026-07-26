package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/platform/onetimetoken"
)

// FR-397 流式传输票据：MCP 只承载小文本，大文件经短时单用途票据走既有数据面。
const (
	// agentTransferTicketTTL 票据有效期；足够发起一次传输，过期即须重新申请。
	agentTransferTicketTTL = 5 * time.Minute
	// agentTransferTicketDomain 域分离标识，保证票据密钥与其它签名用途互不通用。
	agentTransferTicketDomain = "jianmanager/agent-transfer/ticket/v1"
	agentTransferTicketVersion = 1

	// AgentTransferDirectionUpload 上传方向（Agent → 实例工作目录）。
	AgentTransferDirectionUpload = "upload"
	// AgentTransferDirectionDownload 下载方向（实例工作目录 → Agent）。
	AgentTransferDirectionDownload = "download"
)

// ErrAgentTransferTicketInvalid 票据不可用：过期、已消费、签名错、Token 吊销、归属变化等
// 全部归一为本错误，不向调用方泄露内部状态。
var ErrAgentTransferTicketInvalid = errors.New("票据无效或已失效")

// DeriveAgentTransferTicketSecret 从服务端主密钥域分离派生传输票据专用密钥。
func DeriveAgentTransferTicketSecret(master []byte) []byte {
	if len(master) == 0 {
		return nil
	}
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte(agentTransferTicketDomain))
	return mac.Sum(nil)
}

// agentTransferClaims 票据正文：授权上下文全部在此，端点不接受任何外部路径/实例参数。
type agentTransferClaims struct {
	Version           int    `json:"version"`
	TokenID           uint   `json:"tokenId"`
	InstanceID        uint   `json:"instanceId"`
	Direction         string `json:"direction"`
	Path              string `json:"path"`
	ExpiresAtUnixNano int64  `json:"expiresAtUnixNano"`
	// Nonce 使同参数的连续签发得到不同票据，令一次性消费按票据而非按参数生效。
	Nonce int64 `json:"nonce"`
}

// AgentTransferClaims 是消费成功后返回给数据面端点的可信上下文。
type AgentTransferClaims struct {
	TokenID    uint
	TokenName  string
	InstanceID uint
	Direction  string
	Path       string
}

// AgentTransferTicketService 签发与消费流式传输票据（HMAC 签名 + 一次性 + 实时重验）。
type AgentTransferTicketService struct {
	secret []byte
	store  *onetimetoken.Store
	agent  *AgentTokenService
	now    func() time.Time
}

// NewAgentTransferTicketService 创建票据服务；secret 由装配层域分离派生后注入。
// now 为空时取 time.Now（测试可注入假时钟）。
func NewAgentTransferTicketService(secret []byte, agent *AgentTokenService, now func() time.Time) (*AgentTransferTicketService, error) {
	if len(secret) == 0 {
		return nil, fmt.Errorf("传输票据签名密钥不能为空")
	}
	if agent == nil {
		return nil, fmt.Errorf("传输票据需要 Agent Token 服务用于消费时重验")
	}
	if now == nil {
		now = time.Now
	}
	return &AgentTransferTicketService{
		secret: append([]byte(nil), secret...),
		store:  onetimetoken.NewStore(),
		agent:  agent,
		now:    now,
	}, nil
}

// Issue 为 principal 在其授权实例上签发单用途票据。
// 实例目标经 AuthorizeInstanceAction 授权（scope 外与不存在均收敛为 ErrAgentForbidden）。
func (s *AgentTransferTicketService) Issue(p *AgentPrincipal, instanceID uint, direction, path string) (string, time.Time, error) {
	if err := validateTransferDirection(direction); err != nil {
		return "", time.Time{}, err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", time.Time{}, fmt.Errorf("path 不能为空")
	}
	if err := validatePath(path); err != nil {
		return "", time.Time{}, err
	}
	if _, _, err := s.agent.AuthorizeInstanceAction(p, AgentActionFileIssueTransferTicket, instanceID); err != nil {
		return "", time.Time{}, err
	}

	now := s.now()
	expiresAt := now.UTC().Add(agentTransferTicketTTL)
	claims := agentTransferClaims{
		Version:           agentTransferTicketVersion,
		TokenID:           p.TokenID,
		InstanceID:        instanceID,
		Direction:         direction,
		Path:              path,
		ExpiresAtUnixNano: expiresAt.UnixNano(),
		Nonce:             now.UnixNano(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("序列化传输票据失败: %w", err)
	}
	ticket := base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(s.sign(payload))
	return ticket, expiresAt, nil
}

// Consume 校验并一次性消费票据：签名 → 过期 → 单用途 → Token 有效 → 实例归属重验。
// 任一环节不符均返回 ErrAgentTransferTicketInvalid。
func (s *AgentTransferTicketService) Consume(ticket string) (*AgentTransferClaims, error) {
	claims, err := s.parseAndAuthenticate(ticket)
	if err != nil {
		return nil, err
	}
	expiresAt := time.Unix(0, claims.ExpiresAtUnixNano)
	if !s.store.Consume(ticket, expiresAt) {
		// 已消费或已过期：一次性存储已覆盖两种形态。
		return nil, ErrAgentTransferTicketInvalid
	}
	if !s.now().Before(expiresAt) {
		return nil, ErrAgentTransferTicketInvalid
	}
	principal, err := s.agent.PrincipalByID(claims.TokenID)
	if err != nil {
		// Token 已吊销/过期/删除。
		return nil, ErrAgentTransferTicketInvalid
	}
	if _, _, err := s.agent.AuthorizeInstanceAction(principal, AgentActionFileIssueTransferTicket, claims.InstanceID); err != nil {
		// 实例归属或能力已变化。
		return nil, ErrAgentTransferTicketInvalid
	}
	return &AgentTransferClaims{
		TokenID:    principal.TokenID,
		TokenName:  principal.Name,
		InstanceID: claims.InstanceID,
		Direction:  claims.Direction,
		Path:       claims.Path,
	}, nil
}

func (s *AgentTransferTicketService) parseAndAuthenticate(ticket string) (*agentTransferClaims, error) {
	parts := strings.Split(ticket, ".")
	if len(parts) != 2 {
		return nil, ErrAgentTransferTicketInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrAgentTransferTicketInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, s.sign(payload)) {
		return nil, ErrAgentTransferTicketInvalid
	}
	var claims agentTransferClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, ErrAgentTransferTicketInvalid
	}
	if claims.Version != agentTransferTicketVersion || claims.TokenID == 0 || claims.InstanceID == 0 {
		return nil, ErrAgentTransferTicketInvalid
	}
	if validateTransferDirection(claims.Direction) != nil || validatePath(claims.Path) != nil {
		return nil, ErrAgentTransferTicketInvalid
	}
	return &claims, nil
}

func (s *AgentTransferTicketService) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

// validateTransferDirection 票据方向只有上传/下载两种，绑定后不可互换使用。
func validateTransferDirection(direction string) error {
	if direction != AgentTransferDirectionUpload && direction != AgentTransferDirectionDownload {
		return fmt.Errorf("direction 仅支持 %s 或 %s", AgentTransferDirectionUpload, AgentTransferDirectionDownload)
	}
	return nil
}
