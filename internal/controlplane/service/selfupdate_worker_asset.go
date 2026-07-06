package service

import (
	"context"
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

	"github.com/wcpe/JianManager/internal/platform/selfupdate"
	"github.com/wcpe/JianManager/internal/version"
)

var (
	// ErrWorkerAssetNotCached 表示请求的 Worker 资产尚未缓存。
	ErrWorkerAssetNotCached = errors.New("Worker 二进制资产未缓存")
	// ErrWorkerAssetTokenInvalid 表示 Worker 资产下载 token 无效、过期或 scope 不匹配。
	ErrWorkerAssetTokenInvalid = errors.New("Worker 二进制下载 token 无效")
)

const (
	// WorkerAssetPurposeUpgrade 表示 token 用于节点升级。
	WorkerAssetPurposeUpgrade = "upgrade"
	// WorkerAssetPurposeInstall 表示 token 用于节点安装。
	WorkerAssetPurposeInstall = "install"

	workerAssetTokenTTL = 10 * time.Minute
)

// WorkerAssetCacheEntry 描述 CP 本地 Worker 二进制缓存条目（FR-190）。
type WorkerAssetCacheEntry struct {
	Version   string    `json:"version"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
	Cached    bool      `json:"cached"`
	SHA256    string    `json:"sha256,omitempty"`
	Size      int64     `json:"size,omitempty"`
	SourceURL string    `json:"sourceUrl,omitempty"`
	CachedAt  time.Time `json:"cachedAt,omitempty"`
	LastError string    `json:"lastError,omitempty"`
	Path      string    `json:"-"`
}

// WorkerAssetTokenScope 是 Worker 资产短 token 绑定的 scope。
type WorkerAssetTokenScope struct {
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Purpose   string `json:"purpose,omitempty"`
	NodeUUID  string `json:"nodeUuid,omitempty"`
	ExpiresAt int64  `json:"exp,omitempty"`
}

// EnsureWorkerAsset 确保与当前 CP 版本一致的目标平台 Worker 二进制已缓存。
func (s *SelfUpdateService) EnsureWorkerAsset(ctx context.Context, goos, goarch string) (*WorkerAssetCacheEntry, error) {
	if err := validateWorkerAssetPart(goos); err != nil {
		return nil, err
	}
	if err := validateWorkerAssetPart(goarch); err != nil {
		return nil, err
	}
	feed, art, err := s.resolveArtifact(ctx, ComponentWorker, goos, goarch, version.Version)
	if err != nil {
		return nil, err
	}
	return s.ensureWorkerAssetFromArtifact(ctx, feed.Version, goos, goarch, art)
}

// WorkerAssetStatus 返回目标平台当前 CP 版本 Worker 资产缓存状态。
func (s *SelfUpdateService) WorkerAssetStatus(goos, goarch string) (*WorkerAssetCacheEntry, error) {
	if err := validateWorkerAssetPart(goos); err != nil {
		return nil, err
	}
	if err := validateWorkerAssetPart(goarch); err != nil {
		return nil, err
	}
	entry, err := s.readWorkerAssetMetadata(version.Version, goos, goarch)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &WorkerAssetCacheEntry{Version: version.Version, OS: goos, Arch: goarch, Cached: false}, nil
		}
		return nil, err
	}
	if err := s.validateWorkerAssetEntry(entry); err != nil {
		entry.Cached = false
		entry.LastError = err.Error()
		return entry, nil
	}
	entry.Cached = true
	return entry, nil
}

// ListWorkerAssets 列出 CP 本地已缓存的 Worker 二进制资产。
func (s *SelfUpdateService) ListWorkerAssets() ([]WorkerAssetCacheEntry, error) {
	pattern := filepath.Join(s.workerAssetRoot(), "*", "*", "metadata.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("扫描 Worker 资产缓存失败: %w", err)
	}
	items := make([]WorkerAssetCacheEntry, 0, len(files))
	for _, file := range files {
		entry, err := readWorkerAssetMetadataFile(file)
		if err != nil {
			items = append(items, WorkerAssetCacheEntry{Cached: false, LastError: err.Error()})
			continue
		}
		entry.Path = filepath.Join(filepath.Dir(file), workerAssetBinaryName(entry.OS))
		if err := s.validateWorkerAssetEntry(entry); err != nil {
			entry.Cached = false
			entry.LastError = err.Error()
		} else {
			entry.Cached = true
		}
		items = append(items, *entry)
	}
	return items, nil
}

// OpenWorkerAsset 打开已缓存的 Worker 资产，并在返回前重新校验 sha256。
func (s *SelfUpdateService) OpenWorkerAsset(assetVersion, goos, goarch string) (*os.File, *WorkerAssetCacheEntry, error) {
	entry, err := s.readWorkerAssetMetadata(assetVersion, goos, goarch)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrWorkerAssetNotCached
		}
		return nil, nil, err
	}
	if err := s.validateWorkerAssetEntry(entry); err != nil {
		return nil, nil, err
	}
	f, err := os.Open(entry.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrWorkerAssetNotCached
		}
		return nil, nil, err
	}
	entry.Cached = true
	return f, entry, nil
}

// IssueWorkerAssetToken 签发绑定 Worker 资产 scope 的短期下载 token。
func (s *SelfUpdateService) IssueWorkerAssetToken(scope WorkerAssetTokenScope) (string, error) {
	if err := normalizeWorkerAssetScope(&scope); err != nil {
		return "", err
	}
	scope.ExpiresAt = time.Now().Add(workerAssetTokenTTL).Unix()
	payload, err := json.Marshal(scope)
	if err != nil {
		return "", err
	}
	key, err := s.workerAssetSigningKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// ValidateWorkerAssetToken 校验 token 签名、有效期与非空 expected scope。
func (s *SelfUpdateService) ValidateWorkerAssetToken(token string, expected WorkerAssetTokenScope) (*WorkerAssetTokenScope, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, ErrWorkerAssetTokenInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrWorkerAssetTokenInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrWorkerAssetTokenInvalid
	}
	key, err := s.workerAssetSigningKey()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, ErrWorkerAssetTokenInvalid
	}
	var scope WorkerAssetTokenScope
	if err := json.Unmarshal(payload, &scope); err != nil {
		return nil, ErrWorkerAssetTokenInvalid
	}
	if err := normalizeWorkerAssetScope(&scope); err != nil {
		return nil, ErrWorkerAssetTokenInvalid
	}
	if scope.ExpiresAt <= time.Now().Unix() || !workerAssetScopeMatches(scope, expected) {
		return nil, ErrWorkerAssetTokenInvalid
	}
	return &scope, nil
}

// BuildWorkerAssetDownloadURL 拼 CP-local Worker 资产下载 URL。
func (s *SelfUpdateService) BuildWorkerAssetDownloadURL(baseURL, assetVersion, goos, goarch, token string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return "", errors.New("CP Worker 资产下载基址为空")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("CP Worker 资产下载基址无效: %s", baseURL)
	}
	return fmt.Sprintf("%s/worker-assets/%s/%s/%s/worker?token=%s",
		baseURL,
		url.PathEscape(assetVersion),
		url.PathEscape(goos),
		url.PathEscape(goarch),
		url.QueryEscape(token),
	), nil
}

func (s *SelfUpdateService) ensureWorkerAssetFromArtifact(ctx context.Context, assetVersion, goos, goarch string, art *FeedArtifact) (*WorkerAssetCacheEntry, error) {
	entry, err := s.readWorkerAssetMetadata(assetVersion, goos, goarch)
	if err == nil && strings.EqualFold(entry.SHA256, strings.TrimSpace(art.SHA256)) {
		if err := s.validateWorkerAssetEntry(entry); err == nil {
			entry.Cached = true
			return entry, nil
		}
	}

	dir := s.workerAssetDir(assetVersion, goos, goarch)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 Worker 资产缓存目录失败: %w", err)
	}
	binPath := filepath.Join(dir, workerAssetBinaryName(goos))
	_ = os.Remove(binPath)
	if err := selfupdate.DownloadWith(ctx, s.outboundClient(), art.URL, art.SHA256, binPath, s.cfg.AllowInsecure); err != nil {
		return nil, err
	}
	st, err := os.Stat(binPath)
	if err != nil {
		return nil, err
	}
	entry = &WorkerAssetCacheEntry{
		Version:   assetVersion,
		OS:        goos,
		Arch:      goarch,
		Cached:    true,
		SHA256:    strings.ToLower(strings.TrimSpace(art.SHA256)),
		Size:      st.Size(),
		SourceURL: art.URL,
		CachedAt:  time.Now().UTC(),
		Path:      binPath,
	}
	if err := writeWorkerAssetMetadata(filepath.Join(dir, "metadata.json"), entry); err != nil {
		return nil, err
	}
	return entry, nil
}

func (s *SelfUpdateService) workerAssetRoot() string {
	base := filepath.Join(os.TempDir(), "jianmanager-worker-assets")
	if s.root != nil {
		base = s.root.CacheDir()
	}
	return filepath.Join(base, "worker-assets")
}

func (s *SelfUpdateService) workerAssetDir(assetVersion, goos, goarch string) string {
	return filepath.Join(s.workerAssetRoot(), assetVersion, goos+"-"+goarch)
}

func (s *SelfUpdateService) readWorkerAssetMetadata(assetVersion, goos, goarch string) (*WorkerAssetCacheEntry, error) {
	if err := validateWorkerAssetPart(assetVersion); err != nil {
		return nil, err
	}
	if err := validateWorkerAssetPart(goos); err != nil {
		return nil, err
	}
	if err := validateWorkerAssetPart(goarch); err != nil {
		return nil, err
	}
	entry, err := readWorkerAssetMetadataFile(filepath.Join(s.workerAssetDir(assetVersion, goos, goarch), "metadata.json"))
	if err != nil {
		return nil, err
	}
	entry.Path = filepath.Join(s.workerAssetDir(assetVersion, goos, goarch), workerAssetBinaryName(goos))
	return entry, nil
}

func (s *SelfUpdateService) validateWorkerAssetEntry(entry *WorkerAssetCacheEntry) error {
	if entry == nil || entry.Path == "" {
		return ErrWorkerAssetNotCached
	}
	got, err := selfupdate.FileSHA256(entry.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrWorkerAssetNotCached
		}
		return err
	}
	if !strings.EqualFold(got, strings.TrimSpace(entry.SHA256)) {
		return fmt.Errorf("%w: 期望 %s 实得 %s", selfupdate.ErrChecksumMismatch, entry.SHA256, got)
	}
	if st, err := os.Stat(entry.Path); err == nil {
		entry.Size = st.Size()
	}
	return nil
}

func (s *SelfUpdateService) workerAssetSigningKey() ([]byte, error) {
	s.workerAssetTokenMu.Lock()
	defer s.workerAssetTokenMu.Unlock()
	if len(s.workerAssetTokenKey) > 0 {
		return s.workerAssetTokenKey, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成 Worker 资产 token 密钥失败: %w", err)
	}
	s.workerAssetTokenKey = key
	return key, nil
}

func readWorkerAssetMetadataFile(path string) (*WorkerAssetCacheEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entry WorkerAssetCacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, err
	}
	return &entry, nil
}

func writeWorkerAssetMetadata(path string, entry *WorkerAssetCacheEntry) error {
	raw, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmp, path)
}

func workerAssetBinaryName(goos string) string {
	if strings.EqualFold(goos, "windows") {
		return "worker.exe"
	}
	return "worker"
}

func validateWorkerAssetPart(v string) error {
	v = strings.TrimSpace(v)
	if v == "" || strings.Contains(v, "..") || strings.ContainsAny(v, `/\`) {
		return fmt.Errorf("Worker 资产路径参数无效: %q", v)
	}
	return nil
}

func normalizeWorkerAssetScope(scope *WorkerAssetTokenScope) error {
	if err := validateWorkerAssetPart(scope.Version); err != nil {
		return err
	}
	if err := validateWorkerAssetPart(scope.OS); err != nil {
		return err
	}
	if err := validateWorkerAssetPart(scope.Arch); err != nil {
		return err
	}
	if scope.Purpose == "" {
		return errors.New("Worker 资产 token purpose 不能为空")
	}
	if scope.Purpose != WorkerAssetPurposeUpgrade && scope.Purpose != WorkerAssetPurposeInstall {
		return fmt.Errorf("Worker 资产 token purpose 无效: %s", scope.Purpose)
	}
	if scope.Purpose == WorkerAssetPurposeUpgrade && strings.TrimSpace(scope.NodeUUID) == "" {
		return errors.New("Worker 升级 token 必须绑定节点 UUID")
	}
	return nil
}

func workerAssetScopeMatches(got, want WorkerAssetTokenScope) bool {
	if want.Version != "" && got.Version != want.Version {
		return false
	}
	if want.OS != "" && got.OS != want.OS && !workerAssetInstallWildcard(got.Purpose, got.OS) {
		return false
	}
	if want.Arch != "" && got.Arch != want.Arch && !workerAssetInstallWildcard(got.Purpose, got.Arch) {
		return false
	}
	if want.Purpose != "" && got.Purpose != want.Purpose {
		return false
	}
	if want.NodeUUID != "" && got.NodeUUID != want.NodeUUID {
		return false
	}
	return true
}

func workerAssetInstallWildcard(purpose, value string) bool {
	return purpose == WorkerAssetPurposeInstall && value == "*"
}
