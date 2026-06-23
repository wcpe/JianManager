package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var (
	// ErrNoLatestVersion 频道尚未发布任何版本（无 latest）。
	ErrNoLatestVersion = errors.New("频道尚未发布版本")
	// ErrInvalidVersionFiles 发布的文件清单非法（缺字段/非法 sync/platform/路径逃逸/制品缺失）。
	ErrInvalidVersionFiles = errors.New("版本文件清单非法")
	// ErrVersionNotFound 指定版本在频道内不存在（历史详情/回滚源，FR-088）。
	ErrVersionNotFound = errors.New("版本不存在")
)

// ClientVersionService 客户端分发版本发布与 manifest 组装（FR-087，见 ADR-022、contract §2/§3）。
//
// 职责：
//   - PublishFile：把客户端文件制品入 FR-045 制品库（type=client-file，内容寻址 + 去重）；
//   - PublishVersion：以一组文件 + managedDirs + 自更新段组成版本，version 单调递增、切 latest 指针；
//   - BuildManifest：组装并 Ed25519 签名频道 latest 的 manifest；
//   - OpenArtifact：按 sha256 取制品（供 Range 分发）。
//
// 复用 ClientChannelService.VerifyKey（FR-086）做端点鉴权（在 router 层）。
type ClientVersionService struct {
	db      *gorm.DB
	assets  *AssetService
	channel *ClientChannelService
	signer  *ManifestSigner
}

// NewClientVersionService 创建版本服务。signer 为 nil 时 BuildManifest 报 ErrSignKeyNotConfigured。
func NewClientVersionService(db *gorm.DB, assets *AssetService, channel *ClientChannelService, signer *ManifestSigner) *ClientVersionService {
	return &ClientVersionService{db: db, assets: assets, channel: channel, signer: signer}
}

// PublishFileParams 上传客户端文件制品参数。
type PublishFileParams struct {
	// Filename 原始文件名（决定 CAS 扩展名/下载名），可空。
	Filename string
	// Codec 制品压缩算法（"zstd" | "none"），信息性元数据；落库 Metadata。
	Codec string
	// ExpectedSHA256 期望的**制品（压缩后）** sha256；非空则比对，不符拒收。
	ExpectedSHA256 string
}

// ClientFileResult 制品入库结果（供发布版本时引用）。
type ClientFileResult struct {
	// SHA256 制品自身 sha256 = 下载寻址 key = manifest files[].artifact.sha256。
	SHA256 string `json:"sha256"`
	// MD5 制品自身 md5（入库即算）。codec=none 时即解压后原始内容 md5，供发布向导填 file.md5（FR-088）。
	MD5 string `json:"md5"`
	// Size 制品字节数。
	Size int64 `json:"size"`
	// Codec 压缩算法。
	Codec string `json:"codec"`
}

// PublishFile 把客户端文件制品入制品库（type=client-file，按制品自身 sha256 内容寻址去重）。
// 返回的 SHA256 即 manifest files[].artifact.sha256；客户端按此值 GET /client-artifacts/{sha256}。
func (s *ClientVersionService) PublishFile(r io.Reader, p PublishFileParams) (*ClientFileResult, error) {
	codec := p.Codec
	if codec == "" {
		codec = "none"
	}
	meta, _ := json.Marshal(map[string]string{"codec": codec})
	asset, err := s.assets.Ingest(r, IngestParams{
		Type:           model.AssetTypeClientFile,
		Filename:       p.Filename,
		Metadata:       string(meta),
		ExpectedSHA256: p.ExpectedSHA256,
	})
	if err != nil {
		return nil, err
	}
	return &ClientFileResult{SHA256: asset.SHA256, MD5: asset.MD5, Size: asset.Size, Codec: codec}, nil
}

// PublishVersionParams 发布版本参数。
type PublishVersionParams struct {
	// Files 文件清单（必）。每项的 Artifact.SHA256 须已存在于 client-file 制品库。
	Files []ManifestFile
	// ManagedDirs 托管目录（可空，但建议提供；空则无减量）。
	ManagedDirs []string
	// Agent 楔子 + updater-core 自更新段（可空）。
	Agent *ManifestAgent
	// Note 发布备注（信息性）。
	Note string
	// CreatedBy 发布者用户 ID（审计辅助）。
	CreatedBy uint
}

// PublishVersion 发布一个版本：校验文件清单 → 写 ClientVersion 快照（version=当前 latest+1，单调递增）
// → 在同一事务内把频道 CurrentVersion 指向新版本（切 latest 指针）。
// 频道不存在返回 ErrChannelNotFound；清单非法返回 ErrInvalidVersionFiles。
func (s *ClientVersionService) PublishVersion(channelID string, p PublishVersionParams) (*model.ClientVersion, error) {
	if err := validateManifestFiles(p.Files); err != nil {
		return nil, err
	}

	filesJSON, err := json.Marshal(p.Files)
	if err != nil {
		return nil, fmt.Errorf("序列化文件清单失败: %w", err)
	}
	managed := p.ManagedDirs
	if managed == nil {
		managed = []string{}
	}
	managedJSON, err := json.Marshal(managed)
	if err != nil {
		return nil, fmt.Errorf("序列化托管目录失败: %w", err)
	}
	var agentJSON string
	if p.Agent != nil {
		raw, merr := json.Marshal(p.Agent)
		if merr != nil {
			return nil, fmt.Errorf("序列化自更新段失败: %w", merr)
		}
		agentJSON = string(raw)
	}

	var version model.ClientVersion
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var ch model.ClientChannel
		if e := tx.Where("channel_id = ?", channelID).First(&ch).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return ErrChannelNotFound
			}
			return fmt.Errorf("查询频道失败: %w", e)
		}

		// version 单调递增：取频道当前已发布的最大版本 +1（防并发拿同号靠唯一索引兜底）。
		var maxVer struct{ Max int }
		if e := tx.Model(&model.ClientVersion{}).
			Select("COALESCE(MAX(version),0) AS max").
			Where("channel_id = ?", channelID).Scan(&maxVer).Error; e != nil {
			return fmt.Errorf("查询版本号失败: %w", e)
		}
		next := maxVer.Max + 1

		version = model.ClientVersion{
			ChannelID:       channelID,
			Version:         next,
			FilesJSON:       string(filesJSON),
			ManagedDirsJSON: string(managedJSON),
			AgentJSON:       agentJSON,
			Note:            p.Note,
			CreatedBy:       p.CreatedBy,
		}
		if e := tx.Create(&version).Error; e != nil {
			return fmt.Errorf("写入版本失败: %w", e)
		}
		// 切 latest 指针。
		if e := tx.Model(&model.ClientChannel{}).Where("channel_id = ?", channelID).
			Update("current_version", next).Error; e != nil {
			return fmt.Errorf("更新 latest 指针失败: %w", e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &version, nil
}

// BuildManifest 组装并签名频道 latest 的 manifest（contract §2/§3）。
// 频道不存在返回 ErrChannelNotFound；无 latest（CurrentVersion=0 或缺记录）返回 ErrNoLatestVersion；
// 未配置签名私钥返回 ErrSignKeyNotConfigured。
func (s *ClientVersionService) BuildManifest(channelID string) (*SignedManifest, error) {
	if s.signer == nil {
		return nil, ErrSignKeyNotConfigured
	}
	ch, err := s.getChannel(channelID)
	if err != nil {
		return nil, err
	}
	if ch.CurrentVersion <= 0 {
		return nil, ErrNoLatestVersion
	}

	ver, err := s.findVersion(channelID, ch.CurrentVersion)
	if err != nil {
		// latest 指针指向不存在的版本属数据不一致，对玩家侧等价于「无 latest」。
		if errors.Is(err, ErrVersionNotFound) {
			return nil, ErrNoLatestVersion
		}
		return nil, err
	}

	manifest, err := assembleManifest(ch, ver)
	if err != nil {
		return nil, err
	}
	if err := s.signer.Sign(manifest); err != nil {
		return nil, fmt.Errorf("签名 manifest 失败: %w", err)
	}
	return manifest, nil
}

// LatestVersion 返回频道当前 latest 版本号（0=未发布）。
func (s *ClientVersionService) LatestVersion(channelID string) (int, error) {
	ch, err := s.getChannel(channelID)
	if err != nil {
		return 0, err
	}
	return ch.CurrentVersion, nil
}

// VersionSummary 版本历史列表项（FR-088，仅管理面；不向玩家暴露）。
type VersionSummary struct {
	// Version 单调递增版本号。
	Version int `json:"version"`
	// Note 发布/回滚备注。
	Note string `json:"note"`
	// FileCount 该版本文件数（来自快照清单）。
	FileCount int `json:"fileCount"`
	// CreatedBy 发布者用户 ID（0=未知）。
	CreatedBy uint `json:"createdBy"`
	// CreatedAt 发布时间。
	CreatedAt time.Time `json:"createdAt"`
	// IsLatest 是否为频道当前 latest 指针所指版本。
	IsLatest bool `json:"isLatest"`
}

// VersionDetail 版本详情（FR-088）：元数据 + 解析后文件清单/托管目录/自更新段。
type VersionDetail struct {
	Version     int            `json:"version"`
	Note        string         `json:"note"`
	CreatedBy   uint           `json:"createdBy"`
	CreatedAt   time.Time      `json:"createdAt"`
	IsLatest    bool           `json:"isLatest"`
	ManagedDirs []string       `json:"managedDirs"`
	Files       []ManifestFile `json:"files"`
	Agent       *ManifestAgent `json:"agent,omitempty"`
}

// ListVersions 列出频道版本历史（版本号 DESC，含 isLatest 标记）。
// 历史**仅供管理面**（运营回滚/审计）；玩家侧只认 latest（contract §2），不经此端点。
// 频道不存在返回 ErrChannelNotFound。
func (s *ClientVersionService) ListVersions(channelID string) ([]VersionSummary, error) {
	ch, err := s.getChannel(channelID)
	if err != nil {
		return nil, err
	}
	var versions []model.ClientVersion
	if err := s.db.Where("channel_id = ?", channelID).Order("version DESC").Find(&versions).Error; err != nil {
		return nil, fmt.Errorf("查询版本历史失败: %w", err)
	}
	out := make([]VersionSummary, 0, len(versions))
	for i := range versions {
		v := &versions[i]
		out = append(out, VersionSummary{
			Version:   v.Version,
			Note:      v.Note,
			FileCount: countSnapshotFiles(v.FilesJSON),
			CreatedBy: v.CreatedBy,
			CreatedAt: v.CreatedAt,
			IsLatest:  v.Version == ch.CurrentVersion,
		})
	}
	return out, nil
}

// GetVersionDetail 取频道某版本的完整快照详情（文件清单 + 托管目录 + 自更新段）。
// 频道不存在返回 ErrChannelNotFound；版本不存在返回 ErrVersionNotFound。
func (s *ClientVersionService) GetVersionDetail(channelID string, version int) (*VersionDetail, error) {
	ch, err := s.getChannel(channelID)
	if err != nil {
		return nil, err
	}
	ver, err := s.findVersion(channelID, version)
	if err != nil {
		return nil, err
	}
	files, managedDirs, agent, err := decodeVersionSnapshot(ver)
	if err != nil {
		return nil, err
	}
	return &VersionDetail{
		Version:     ver.Version,
		Note:        ver.Note,
		CreatedBy:   ver.CreatedBy,
		CreatedAt:   ver.CreatedAt,
		IsLatest:    ver.Version == ch.CurrentVersion,
		ManagedDirs: managedDirs,
		Files:       files,
		Agent:       agent,
	}, nil
}

// Rollback 运营回滚：取历史版本 sourceVersion 的内容，**以更高版本号重发为新 latest**（ADR-022 §3、contract §3）。
// 不下发更低版本号——保持 version 单调，客户端按防降级正常前进、不被拒。复用 PublishVersion 完成校验/单调递增/切指针。
// 频道不存在返回 ErrChannelNotFound；源版本不存在返回 ErrVersionNotFound。
func (s *ClientVersionService) Rollback(channelID string, sourceVersion int, createdBy uint, note string) (*model.ClientVersion, error) {
	if _, err := s.getChannel(channelID); err != nil {
		return nil, err
	}
	src, err := s.findVersion(channelID, sourceVersion)
	if err != nil {
		return nil, err
	}
	files, managedDirs, agent, err := decodeVersionSnapshot(src)
	if err != nil {
		return nil, err
	}
	if note == "" {
		note = fmt.Sprintf("回滚至 v%d", sourceVersion)
	}
	return s.PublishVersion(channelID, PublishVersionParams{
		Files:       files,
		ManagedDirs: managedDirs,
		Agent:       agent,
		Note:        note,
		CreatedBy:   createdBy,
	})
}

// getChannel 按 channelId 查频道；不存在返回 ErrChannelNotFound。
func (s *ClientVersionService) getChannel(channelID string) (*model.ClientChannel, error) {
	var ch model.ClientChannel
	err := s.db.Where("channel_id = ?", channelID).First(&ch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrChannelNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询频道失败: %w", err)
	}
	return &ch, nil
}

// findVersion 查频道内指定版本号的快照；不存在返回 ErrVersionNotFound。
func (s *ClientVersionService) findVersion(channelID string, version int) (*model.ClientVersion, error) {
	var ver model.ClientVersion
	err := s.db.Where("channel_id = ? AND version = ?", channelID, version).First(&ver).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVersionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询版本失败: %w", err)
	}
	return &ver, nil
}

// countSnapshotFiles 统计版本快照文件数（解析失败计 0，仅用于列表展示，不影响信任）。
func countSnapshotFiles(filesJSON string) int {
	if filesJSON == "" {
		return 0
	}
	var files []ManifestFile
	if err := json.Unmarshal([]byte(filesJSON), &files); err != nil {
		return 0
	}
	return len(files)
}

// decodeVersionSnapshot 把版本快照的 JSON 字段还原为 files/managedDirs/agent。
// files 永不为 nil（空清单为 []）；managedDirs 同理；agent 可为 nil（未声明自更新段）。
func decodeVersionSnapshot(ver *model.ClientVersion) ([]ManifestFile, []string, *ManifestAgent, error) {
	var files []ManifestFile
	if err := json.Unmarshal([]byte(ver.FilesJSON), &files); err != nil {
		return nil, nil, nil, fmt.Errorf("解析文件清单失败: %w", err)
	}
	if files == nil {
		files = []ManifestFile{}
	}
	managedDirs := []string{}
	if ver.ManagedDirsJSON != "" {
		if err := json.Unmarshal([]byte(ver.ManagedDirsJSON), &managedDirs); err != nil {
			return nil, nil, nil, fmt.Errorf("解析托管目录失败: %w", err)
		}
	}
	var agent *ManifestAgent
	if ver.AgentJSON != "" {
		agent = &ManifestAgent{}
		if err := json.Unmarshal([]byte(ver.AgentJSON), agent); err != nil {
			return nil, nil, nil, fmt.Errorf("解析自更新段失败: %w", err)
		}
	}
	return files, managedDirs, agent, nil
}

// OpenArtifact 按制品 sha256 打开 client-file 制品（供端点 Range 分发）。
// 返回资产元数据与其物理文件绝对路径；不存在返回 ErrAssetNotFound。
func (s *ClientVersionService) OpenArtifact(sha256 string) (*model.Asset, string, error) {
	var asset model.Asset
	err := s.db.Where("type = ? AND sha256 = ?", model.AssetTypeClientFile, sha256).First(&asset).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", ErrAssetNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("查询制品失败: %w", err)
	}
	return &asset, s.assets.AbsPath(&asset), nil
}

// assembleManifest 把频道 + 版本快照还原为 SignedManifest（未签名）。
func assembleManifest(ch *model.ClientChannel, ver *model.ClientVersion) (*SignedManifest, error) {
	files, managedDirs, agent, err := decodeVersionSnapshot(ver)
	if err != nil {
		return nil, err
	}
	return &SignedManifest{
		SchemaVersion: manifestSchemaVersion,
		Channel:       ch.ChannelID,
		Version:       ver.Version,
		IssuedAt:      ver.CreatedAt.UTC().Format(time.RFC3339),
		ManagedDirs:   managedDirs,
		Files:         files,
		Agent:         agent,
	}, nil
}

// validateManifestFiles 校验发布文件清单：非空、路径安全（POSIX、无逃逸）、sync/platform 合法、
// sha256/制品引用齐备。校验失败返回 ErrInvalidVersionFiles（带具体原因）。
func validateManifestFiles(files []ManifestFile) error {
	if len(files) == 0 {
		return fmt.Errorf("%w: 文件清单为空", ErrInvalidVersionFiles)
	}
	seen := make(map[string]struct{}, len(files))
	for i, f := range files {
		if f.Path == "" {
			return fmt.Errorf("%w: 第 %d 项缺 path", ErrInvalidVersionFiles, i)
		}
		if !safeManifestPath(f.Path) {
			return fmt.Errorf("%w: 非法路径 %q", ErrInvalidVersionFiles, f.Path)
		}
		if _, dup := seen[f.Path]; dup {
			return fmt.Errorf("%w: 重复路径 %q", ErrInvalidVersionFiles, f.Path)
		}
		seen[f.Path] = struct{}{}
		if f.SHA256 == "" {
			return fmt.Errorf("%w: %q 缺 sha256", ErrInvalidVersionFiles, f.Path)
		}
		if !ValidSyncMode(f.Sync) {
			return fmt.Errorf("%w: %q 非法 sync=%q", ErrInvalidVersionFiles, f.Path, f.Sync)
		}
		if !ValidPlatform(f.Platform) {
			return fmt.Errorf("%w: %q 非法 platform=%q", ErrInvalidVersionFiles, f.Path, f.Platform)
		}
		// ignore 文件仅展示/审计，可不带制品；其余须带下载制品引用。
		if f.Sync != "ignore" && f.Artifact.SHA256 == "" {
			return fmt.Errorf("%w: %q 缺 artifact.sha256", ErrInvalidVersionFiles, f.Path)
		}
	}
	return nil
}

// safeManifestPath 报告 manifest 相对路径是否安全：非空、POSIX 风格、不绝对、无 `..` 段、不含反斜杠/驱动器。
func safeManifestPath(p string) bool {
	if p == "" || p[0] == '/' {
		return false
	}
	for _, r := range p {
		if r == '\\' {
			return false
		}
	}
	// Windows 盘符（c:）规避。
	if len(p) >= 2 && p[1] == ':' {
		return false
	}
	for _, seg := range splitSlash(p) {
		if seg == ".." {
			return false
		}
	}
	return true
}

// splitSlash 按 `/` 切分路径段。
func splitSlash(p string) []string {
	var segs []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			segs = append(segs, p[start:i])
			start = i + 1
		}
	}
	segs = append(segs, p[start:])
	return segs
}
