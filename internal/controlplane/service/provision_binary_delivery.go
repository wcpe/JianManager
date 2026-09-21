package service

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 二进制资产分发通道（FR-441，spec §3.2 的 kind=asset）。
//
// 复用既有制品库分发通道：与 ServerProbe 的 `/probe-artifacts/:id/download` 同用
// ArtifactVersionService 的 HMAC 签名密钥（probeDownloadSigningKey），仅 scope 结构不同。
// 不新增第二套密码学体系，也不新增匿名文件服务面——下载端点与 probe 端点同注册方式、
// 同 token 形态（base64url(scope-JSON).base64url(HMAC)），无 token 一律 403。
//
// 复用同一密钥为何仍安全：token 的 scope 结构在签名校验后按类型严格反序列化，
// 二进制 scope（assetId）与 probe scope（versionId）字段不重叠——把 probe token 用到
// 二进制端点时 assetId 为 0 即被拒，反之亦然。跨用途误用在字段校验层就失败。

// ErrBinaryDownloadTokenInvalid 表示二进制分发 token 无效、过期或 scope 不匹配。
var ErrBinaryDownloadTokenInvalid = errors.New("二进制分发 token 无效")

// binaryDownloadTokenTTL 与 ServerProbe 分发同口径（10 分钟）：签发到 Worker 开始拉取的
// 窗口是秒级，长期有效只会扩大泄露面。
const binaryDownloadTokenTTL = 10 * time.Minute

// BinaryDownloadTokenScope 绑定一次 Worker 拉取的资产、目标节点与失效时间。
// 与 ProbeDownloadTokenScope 字段不重叠（assetId vs versionId），故共用同一签名密钥时
// 跨用途误用会在 scope 校验层就被拒（assetId 为 0）。
type BinaryDownloadTokenScope struct {
	AssetID   uint   `json:"assetId"`
	NodeUUID  string `json:"nodeUuid,omitempty"`
	Filename  string `json:"filename,omitempty"`
	ExpiresAt int64  `json:"exp"`
}

// IssueBinaryDownloadToken 为指定资产签发短期分发 token（FR-441 kind=asset）。
//
// 复用 ArtifactVersionService 的签名密钥与签名/编解码实现，仅换 scope——这样
// 「制品库的分发通道」在实现层也只有一条，不会出现两处签名逻辑各自演进。
func (s *ArtifactVersionService) IssueBinaryDownloadToken(scope BinaryDownloadTokenScope) (string, error) {
	if err := normalizeBinaryDownloadScope(&scope); err != nil {
		return "", err
	}
	scope.ExpiresAt = time.Now().Add(binaryDownloadTokenTTL).Unix()
	key, err := s.probeDownloadSigningKey()
	if err != nil {
		return "", err
	}
	return signScopedToken(key, scope), nil
}

// ValidateBinaryDownloadToken 校验 token 签名、有效期与目标资产（下载端点调用）。
func (s *ArtifactVersionService) ValidateBinaryDownloadToken(token string, expected BinaryDownloadTokenScope) (*BinaryDownloadTokenScope, error) {
	key, err := s.probeDownloadSigningKey()
	if err != nil {
		return nil, ErrBinaryDownloadTokenInvalid
	}
	var scope BinaryDownloadTokenScope
	if !verifyScopedToken(key, token, &scope) {
		return nil, ErrBinaryDownloadTokenInvalid
	}
	if normalizeBinaryDownloadScope(&scope) != nil {
		return nil, ErrBinaryDownloadTokenInvalid
	}
	if scope.ExpiresAt <= time.Now().Unix() ||
		(expected.AssetID != 0 && scope.AssetID != expected.AssetID) ||
		(expected.NodeUUID != "" && scope.NodeUUID != expected.NodeUUID) {
		return nil, ErrBinaryDownloadTokenInvalid
	}
	return &scope, nil
}

// BuildBinaryDownloadURL 拼出供 Worker 拉取的 CP 本地 URL。
func (s *ArtifactVersionService) BuildBinaryDownloadURL(baseURL string, assetID uint, token string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || assetID == 0 || strings.TrimSpace(token) == "" {
		return "", errors.New("CP 二进制下载基址或参数无效")
	}
	return fmt.Sprintf("%s/binary-assets/%d/download?token=%s", baseURL, assetID, url.QueryEscape(token)), nil
}

// OpenAssetForDownload 打开资产内容供分发端点流式返回，并返回资产元数据。
// 失效资产（外置对象缺失）给明确错误，避免下载端吐 0 字节文件被 Worker 当成有效二进制落地。
func (s *ArtifactVersionService) OpenAssetForDownload(assetID uint) (*model.Asset, io.ReadCloser, error) {
	if s.assets == nil {
		return nil, nil, errors.New("制品版本库未配置 CAS 服务")
	}
	asset, err := s.assets.GetByID(assetID)
	if err != nil {
		return nil, nil, err
	}
	if asset.StorageState == model.AssetStorageLost {
		return nil, nil, fmt.Errorf("%w: 资产已标记失效（外置对象缺失），请先重传同内容文件", ErrAssetNotFound)
	}
	content, err := s.assets.OpenContent(asset)
	if err != nil {
		return nil, nil, err
	}
	return asset, content, nil
}

// normalizeBinaryDownloadScope 校验 scope 必备字段；同时把非法字符挡在签名之前。
func normalizeBinaryDownloadScope(scope *BinaryDownloadTokenScope) error {
	if scope.AssetID == 0 || strings.ContainsAny(scope.NodeUUID, "/\\") {
		return ErrBinaryDownloadTokenInvalid
	}
	return nil
}

// BuildBinaryDownloadURLForRequest 解析 CP 公共基址后拼出下载地址（FR-441）。
// 请求可见基址优先，缺失时回退平台设置 platform.public_base_url——二进制下载既可能由
// 控制台发起，也可能由 MCP/脚本发起，没有请求上下文时仍需可用的绝对地址。
func (p *ProvisionService) BuildBinaryDownloadURLForRequest(assetID uint, token, requestBaseURL string) (string, error) {
	if p.artifactVersions == nil {
		return "", errors.New("制品版本库服务未装配，无法以 asset 为来源搭建二进制实例")
	}
	base, err := p.resolveBinaryBaseURL(requestBaseURL)
	if err != nil {
		return "", err
	}
	return p.artifactVersions.BuildBinaryDownloadURL(base, assetID, token)
}

// resolveBinaryBaseURL 解析 CP 公共基址：请求可见基址优先，其次平台公共基址设置。
// 两者皆无时给出可操作错误（提示配置公共基址），而非拼出相对地址让 Worker 无从解析。
func (p *ProvisionService) resolveBinaryBaseURL(requestBase string) (string, error) {
	candidates := []string{requestBase}
	if p.binaryBaseURL != nil {
		candidates = append(candidates, p.binaryBaseURL())
	}
	candidates = append(candidates, p.platformPublicBaseURL())
	for _, raw := range candidates {
		trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
		if trimmed == "" {
			continue
		}
		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			continue
		}
		return trimmed, nil
	}
	return "", errors.New("无法确定 CP 公共基址（asset 来源需 Worker 从 CP 拉取）：" +
		"请在平台设置中配置 platform.public_base_url，或改用 kind=url / node_file 来源")
}

// platformPublicBaseURL 读平台公共基址设置（FR-405：平台生成绝对链接唯一允许的基址）。
// 未配置/不合法时返回空串——由调用方决定报错还是换来源，不在此静默兜底成一个可能错的地址。
func (p *ProvisionService) platformPublicBaseURL() string {
	if p.db == nil {
		return ""
	}
	var setting model.PlatformSetting
	if err := p.db.First(&setting, "key = ?", SettingKeyPlatformPublicBaseURL).Error; err != nil {
		return ""
	}
	value := strings.TrimSpace(setting.Value)
	if validatePublicBaseURL(value) != nil {
		return ""
	}
	return value
}

// SetBinaryBaseURLProvider 注入 CP 公共基址提供者（FR-441，main 接线）。
// 未注入时回退平台设置读取；两者皆无则 asset 来源报错（不静默拼出不可用地址）。
func (p *ProvisionService) SetBinaryBaseURLProvider(provider func() string) {
	p.binaryBaseURL = provider
}
