package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

const probeDownloadTokenTTL = 10 * time.Minute

var ErrProbeDownloadTokenInvalid = errors.New("ServerProbe 下载 token 无效")

// ProbeDownloadTokenScope 绑定一次 Worker 拉取的版本、节点和失效时间。
type ProbeDownloadTokenScope struct {
	VersionID uint   `json:"versionId"`
	NodeUUID  string `json:"nodeUuid"`
	ExpiresAt int64  `json:"exp"`
}

// IssueProbeDownloadToken 为指定 Worker 部署版本签发短期下载 token。
func (s *ArtifactVersionService) IssueProbeDownloadToken(scope ProbeDownloadTokenScope) (string, error) {
	if err := normalizeProbeDownloadScope(&scope); err != nil {
		return "", err
	}
	scope.ExpiresAt = time.Now().Add(probeDownloadTokenTTL).Unix()
	key, err := s.probeDownloadSigningKey()
	if err != nil {
		return "", err
	}
	return signScopedToken(key, scope), nil
}

// ValidateProbeDownloadToken 校验 token 签名、有效期和目标 scope。
func (s *ArtifactVersionService) ValidateProbeDownloadToken(token string, expected ProbeDownloadTokenScope) (*ProbeDownloadTokenScope, error) {
	key, err := s.probeDownloadSigningKey()
	if err != nil {
		return nil, ErrProbeDownloadTokenInvalid
	}
	var scope ProbeDownloadTokenScope
	if !verifyScopedToken(key, token, &scope) {
		return nil, ErrProbeDownloadTokenInvalid
	}
	if normalizeProbeDownloadScope(&scope) != nil {
		return nil, ErrProbeDownloadTokenInvalid
	}
	if scope.ExpiresAt <= time.Now().Unix() ||
		(expected.VersionID != 0 && scope.VersionID != expected.VersionID) ||
		(expected.NodeUUID != "" && scope.NodeUUID != expected.NodeUUID) {
		return nil, ErrProbeDownloadTokenInvalid
	}
	return &scope, nil
}

// signScopedToken 把任意 scope 结构签为 `<base64url(payload)>.<base64url(HMAC)>`。
// probe（FR-010）与二进制分发（FR-441）共用同一实现与同一密钥，仅 scope 结构不同——
// 分发 token 的编解码只有一条真源。
func signScopedToken(key []byte, scope any) string {
	payload, err := json.Marshal(scope)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyScopedToken 校验签名并反序列化 scope 到 out；签名不符/格式非法返回 false。
func verifyScopedToken(key []byte, token string, out any) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return false
	}
	return json.Unmarshal(payload, out) == nil
}

// BuildProbeDownloadURL 拼出供 Worker 拉取的 CP 本地 URL。
func (s *ArtifactVersionService) BuildProbeDownloadURL(baseURL string, versionID uint, token string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || versionID == 0 || strings.TrimSpace(token) == "" {
		return "", errors.New("CP ServerProbe 下载基址或参数无效")
	}
	return fmt.Sprintf("%s/probe-artifacts/%d/download?token=%s", baseURL, versionID, url.QueryEscape(token)), nil
}

// OpenCachedProbeVersion 打开已入本地 CAS 的 ServerProbe jar，供下载端点流式返回。
func (s *ArtifactVersionService) OpenCachedProbeVersion(versionID uint) (*os.File, *model.ArtifactVersion, error) {
	version, err := s.cachedVersion(versionID)
	if err != nil {
		return nil, nil, err
	}
	if version.Asset.StorageBackend != model.AssetBackendLocal {
		return nil, nil, fmt.Errorf("%w: ServerProbe 当前不支持外置存储", ErrArtifactVersionNotCached)
	}
	path := s.assets.AbsPath(version.Asset)
	if path == "" {
		return nil, nil, ErrArtifactVersionNotCached
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrArtifactVersionNotCached
		}
		return nil, nil, fmt.Errorf("打开 ServerProbe 制品失败: %w", err)
	}
	return file, version, nil
}

func normalizeProbeDownloadScope(scope *ProbeDownloadTokenScope) error {
	if scope.VersionID == 0 || strings.TrimSpace(scope.NodeUUID) == "" || strings.ContainsAny(scope.NodeUUID, "/\\") {
		return ErrProbeDownloadTokenInvalid
	}
	return nil
}

func (s *ArtifactVersionService) probeDownloadSigningKey() ([]byte, error) {
	s.probeTokenMu.Lock()
	defer s.probeTokenMu.Unlock()
	if len(s.probeTokenKey) > 0 {
		return s.probeTokenKey, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成 ServerProbe 下载 token 密钥失败: %w", err)
	}
	s.probeTokenKey = key
	return key, nil
}

func probeDownloadFilename(version *model.ArtifactVersion) string {
	if version == nil || version.Asset == nil {
		return "ServerProbe.jar"
	}
	return filepath.Base(version.Asset.Filename)
}
