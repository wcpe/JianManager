package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
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
	// ErrNoCoreVersion 无任何已归档的 updater-core 版本（coreEndpoint 无可返回）。
	ErrNoCoreVersion = errors.New("无已归档的 updater-core 版本")
	// ErrCoreVersionNotFound 指定的 core 版本（sha256）不存在于归档制品库。
	ErrCoreVersionNotFound = errors.New("updater-core 版本不存在")
)

// EmbeddedCore 描述 CP 内嵌的默认 updater-core（FR-193，见 ADR-045 改写）。
// 由装配处（main.go）从 embed.UpdaterCoreJar() 计算一次后注入：sha256/size + 单调整数版本。
// 内嵌 core 随 CP 走、运营不管理；CP 自更新（FR-081）后内嵌 jar 即新版、本结构随之刷新。
type EmbeddedCore struct {
	// Version updater-core 整数版本号 = manifest agent.core.version（对客户端单调只升不降，FR-091）。
	Version int
	// SHA256 内嵌 core jar 自身 sha256 = manifest agent.core.platforms[os].artifact.sha256 = 制品下载寻址 key。
	SHA256 string
	// Size 内嵌 core jar 字节数。
	Size int64
	// Codec 制品压缩算法；内嵌 jar 为原始 jar，恒 "none"。
	Codec string
}

// NewEmbeddedCoreFromJar 由内嵌 updater-core jar 字节 + 版本字符串构造 EmbeddedCore（FR-193，见 ADR-045 改写）。
//   - jar 为 nil/空（未经 make embed-client-updater 注入）：返回 nil（调用方据此省略 agent.core，优雅降级）；
//   - versionStr 非整数：回退版本号 1（保证 agent.core.version 为合法单调整数，不因配置失误产出坏 manifest）。
//
// sha256/size 由 jar 字节现算（= 制品内容寻址 key，与经制品端点下发的字节一致）。
func NewEmbeddedCoreFromJar(jar []byte, versionStr string) *EmbeddedCore {
	if len(jar) == 0 {
		return nil
	}
	version, err := strconv.Atoi(strings.TrimSpace(versionStr))
	if err != nil || version <= 0 {
		version = 1
	}
	sum := sha256.Sum256(jar)
	return &EmbeddedCore{
		Version: version,
		SHA256:  hex.EncodeToString(sum[:]),
		Size:    int64(len(jar)),
		Codec:   "none",
	}
}

// ClientVersionService 客户端分发版本发布与 manifest 组装（FR-087 / FR-256 简化后）。
//
// 职责：
//   - PublishFile：把客户端文件制品入 FR-045 制品库（type=client-file，内容寻址 + 去重）；
//   - PublishVersion：以一组文件 + managedDirs + 自更新段组成版本，version 单调递增、切 latest 指针；
//   - BuildManifest：组装频道 latest 的 manifest（FR-256 起不再签名）；
//   - OpenArtifact：按 sha256 取制品（供 Range 分发）。
//
// 复用 ClientChannelService.VerifyKey（FR-086）做端点鉴权（在 router 层）。
type ClientVersionService struct {
	db      *gorm.DB
	assets  *AssetService
	channel *ClientChannelService
	// embeddedCore CP 内嵌的默认 updater-core（FR-193，见 ADR-045 改写）。非 nil 时 BuildManifest
	// 用它自动产出 agent.core（取代运营手填/pin）；nil（无内嵌 jar）时省略 agent.core（不破 FR-087/088）。
	embeddedCore *EmbeddedCore
}

// NewClientVersionService 创建版本服务。
func NewClientVersionService(db *gorm.DB, assets *AssetService, channel *ClientChannelService) *ClientVersionService {
	return &ClientVersionService{db: db, assets: assets, channel: channel}
}

// SetEmbeddedCore 注入 CP 内嵌的默认 updater-core 信息（FR-193，见 ADR-045 改写）。
// 注入后 BuildManifest 的 agent.core 由内嵌 core 自动驱动（version + 三平台同制品）；不注入则省略 agent.core。
// 经 setter 注入以保持构造签名稳定（既有装配/测试零改动）。
func (s *ClientVersionService) SetEmbeddedCore(ec *EmbeddedCore) {
	s.embeddedCore = ec
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
	// ManagedDirs 托管目录（可空，但建议提供；空则无减量）。FR-255：可含 "*" 表 clean-all。
	ManagedDirs []string
	// CleanExclude 运营自定义排除（FR-255）：命中前缀的路径永不删。空则省略。
	CleanExclude []string
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
	// FR-255：校验 cleanExclude 路径合法性。
	if err := validateCleanExclude(p.CleanExclude); err != nil {
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
	var cleanExcludeJSON string
	if len(p.CleanExclude) > 0 {
		raw, merr := json.Marshal(p.CleanExclude)
		if merr != nil {
			return nil, fmt.Errorf("序列化清理排除失败: %w", merr)
		}
		cleanExcludeJSON = string(raw)
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
			ChannelID:        channelID,
			Version:          next,
			FilesJSON:        string(filesJSON),
			ManagedDirsJSON:  string(managedJSON),
			CleanExcludeJSON: cleanExcludeJSON,
			AgentJSON:        agentJSON,
			Note:             p.Note,
			CreatedBy:        p.CreatedBy,
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

// BuildManifest 组装频道 latest 的 manifest（contract §2）。FR-256 起不再签名。
// 频道不存在返回 ErrChannelNotFound；无 latest（CurrentVersion=0 或缺记录）返回 ErrNoLatestVersion。
func (s *ClientVersionService) BuildManifest(channelID string) (*SignedManifest, error) {
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
	// agent.core 由 CP 内嵌默认 updater-core 自动驱动（FR-193，见 ADR-045 改写）：覆盖快照中的手填透传值。
	// 无内嵌 jar（embeddedCore 未注入）时省略 agent.core，沿用快照（兼容 FR-087/088）。
	s.applyEmbeddedCore(manifest)
	return manifest, nil
}

// embeddedCorePlatforms manifest agent.core.platforms 须填的平台键集合（contract §2、ADR-021）。
// ADR-021「一份 jar 三平台通用」——内嵌一份 core jar，fan-out 填这三键（同制品）。
// 客户端 Platform.tag() 取 windows/macos/linux 之一；other 平台无键、不自更新（沿用 FR-091）。
var embeddedCorePlatforms = []string{"windows", "macos", "linux"}

// applyEmbeddedCore 用 CP 内嵌的默认 updater-core 自动产出 manifest 的 agent.core（FR-193，见 ADR-045 改写）。
// embeddedCore 未注入（无内嵌 jar）时不动 agent.core——省略而非置空（保留快照、不破 FR-087/088）。
// agent.wedge 不受影响（楔子冻结、信息性，随快照透传）。一份内嵌 jar fan-out 填三平台键（ADR-021）。
func (s *ClientVersionService) applyEmbeddedCore(manifest *SignedManifest) {
	if s.embeddedCore == nil {
		return
	}
	platforms := make(map[string]ManifestAgentArtifact, len(embeddedCorePlatforms))
	for _, os := range embeddedCorePlatforms {
		platforms[os] = ManifestAgentArtifact{
			SHA256: s.embeddedCore.SHA256,
			Size:   s.embeddedCore.Size,
			Codec:  s.embeddedCore.Codec,
		}
	}
	if manifest.Agent == nil {
		manifest.Agent = &ManifestAgent{}
	}
	manifest.Agent.Core = &ManifestCore{Version: s.embeddedCore.Version, Platforms: platforms}
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
	Version      int            `json:"version"`
	Note         string         `json:"note"`
	CreatedBy    uint           `json:"createdBy"`
	CreatedAt    time.Time      `json:"createdAt"`
	IsLatest     bool           `json:"isLatest"`
	ManagedDirs  []string       `json:"managedDirs"`
	CleanExclude []string       `json:"cleanExclude,omitempty"`
	Files        []ManifestFile `json:"files"`
	Agent        *ManifestAgent `json:"agent,omitempty"`
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
	files, managedDirs, cleanExclude, agent, err := decodeVersionSnapshot(ver)
	if err != nil {
		return nil, err
	}
	return &VersionDetail{
		Version:      ver.Version,
		Note:         ver.Note,
		CreatedBy:    ver.CreatedBy,
		CreatedAt:    ver.CreatedAt,
		IsLatest:     ver.Version == ch.CurrentVersion,
		ManagedDirs:  managedDirs,
		CleanExclude: cleanExclude,
		Files:        files,
		Agent:        agent,
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
	files, managedDirs, cleanExclude, agent, err := decodeVersionSnapshot(src)
	if err != nil {
		return nil, err
	}
	if note == "" {
		note = fmt.Sprintf("回滚至 v%d", sourceVersion)
	}
	return s.PublishVersion(channelID, PublishVersionParams{
		Files:        files,
		ManagedDirs:  managedDirs,
		CleanExclude: cleanExclude,
		Agent:        agent,
		Note:         note,
		CreatedBy:    createdBy,
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

// decodeVersionSnapshot 把版本快照的 JSON 字段还原为 files/managedDirs/cleanExclude/agent。
// files 永不为 nil（空清单为 []）；managedDirs 同理；cleanExclude 可为空切片（未声明）；agent 可为 nil。
func decodeVersionSnapshot(ver *model.ClientVersion) ([]ManifestFile, []string, []string, *ManifestAgent, error) {
	var files []ManifestFile
	if err := json.Unmarshal([]byte(ver.FilesJSON), &files); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("解析文件清单失败: %w", err)
	}
	if files == nil {
		files = []ManifestFile{}
	}
	managedDirs := []string{}
	if ver.ManagedDirsJSON != "" {
		if err := json.Unmarshal([]byte(ver.ManagedDirsJSON), &managedDirs); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("解析托管目录失败: %w", err)
		}
	}
	// FR-255：cleanExclude（老版本无此列，空串=未声明）。
	cleanExclude := []string{}
	if ver.CleanExcludeJSON != "" {
		if err := json.Unmarshal([]byte(ver.CleanExcludeJSON), &cleanExclude); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("解析清理排除失败: %w", err)
		}
	}
	var agent *ManifestAgent
	if ver.AgentJSON != "" {
		agent = &ManifestAgent{}
		if err := json.Unmarshal([]byte(ver.AgentJSON), agent); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("解析自更新段失败: %w", err)
		}
	}
	return files, managedDirs, cleanExclude, agent, nil
}

// artifactDownloadableTypes 可经 /client-artifacts/:sha256 公网端点分发的制品类型集合。
// FR-259 起 core jar 归档为 client-updater-core 类型，也经此端点下发（楔子自动拉 core）。
var artifactDownloadableTypes = []model.AssetType{
	model.AssetTypeClientFile,
	model.AssetTypeClientUpdaterCore,
}

// OpenArtifact 按制品 sha256 打开可分发制品（client-file 或 client-updater-core），供端点 Range 分发。
// 返回资产元数据与其物理文件绝对路径；不存在返回 ErrAssetNotFound。
// FR-259 起扩展支持 client-updater-core 类型（楔子经 coreEndpoint 拉取 core jar）。
func (s *ClientVersionService) OpenArtifact(sha256 string) (*model.Asset, string, error) {
	var asset model.Asset
	err := s.db.Where("type IN ? AND sha256 = ?", artifactDownloadableTypes, sha256).First(&asset).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", ErrAssetNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("查询制品失败: %w", err)
	}
	return &asset, s.assets.AbsPath(&asset), nil
}

// ArtifactTextPreviewMaxBytes 文本预览的制品上限：超过此字节数只回降级标记、不读全量（FR-214）。
// 与前端 clientDistSource 的 PREVIEW_MAX_BYTES（1 MiB）对齐，避免大制品撑爆内存/响应。
const ArtifactTextPreviewMaxBytes = 1 << 20 // 1 MiB

// ArtifactTextPreview 客户端分发制品的文本预览结果（FR-214，管理面 JWT 读取）。
// 降级由 Kind 显式表达，调用方/前端据此渲染，不再自行猜测。
type ArtifactTextPreview struct {
	// Kind 预览类别：text（可文本预览）| binary（含 NUL/压缩，仅下载）| too-large（超上限，仅下载）。
	Kind string `json:"kind"`
	// Content UTF-8 文本（仅 Kind=text）。
	Content string `json:"content,omitempty"`
	// Size 制品字节数（降级态展示用）。
	Size int64 `json:"size"`
	// Codec 制品压缩算法（none|zstd 等，信息性）。
	Codec string `json:"codec"`
}

// ReadArtifactText 读取 client-file 制品内容用于**管理面**文本预览（FR-214）。
//
// 为什么需要本方法：玩家消费端点 GET /client-artifacts/:sha256 走拉取密钥（X-Client-Key）鉴权，
// 与运营浏览器 JWT 入口物理隔离（ADR-022/023）——管理台浏览器无拉取密钥、不能复用该端点取内容预览。
// 故补一个 JWT 平台管理员可用的**只读**文本读取路径，仅服务发布页/版本详情的内容预览。
//
// 降级口径（适配器/前端只消费结果，关注点分离）：
//   - 超过 {@link ArtifactTextPreviewMaxBytes} → Kind=too-large（不读全量）；
//   - 内容含 NUL 字节 → Kind=binary；
//   - 制品已压缩（codec != none/空，本期发布恒 none）→ Kind=binary（不在管理面解压，避免引入解压依赖）；
//   - 其余 → Kind=text + UTF-8 内容。
//
// 制品不存在返回 ErrAssetNotFound。
func (s *ClientVersionService) ReadArtifactText(sha256 string) (*ArtifactTextPreview, error) {
	asset, absPath, err := s.OpenArtifact(sha256)
	if err != nil {
		return nil, err
	}
	codec := artifactCodec(asset.Metadata)
	out := &ArtifactTextPreview{Size: asset.Size, Codec: codec}

	// 超大：不读全量，直接降级。
	if asset.Size > ArtifactTextPreviewMaxBytes {
		out.Kind = "too-large"
		return out, nil
	}
	// 压缩制品不在管理面解压（本期发布恒 codec=none；zstd 等仅可下载）。
	if codec != "" && codec != "none" {
		out.Kind = "binary"
		return out, nil
	}

	data, rerr := readFileCapped(absPath, ArtifactTextPreviewMaxBytes)
	if rerr != nil {
		return nil, rerr
	}
	if bytesContainNUL(data) {
		out.Kind = "binary"
		return out, nil
	}
	out.Kind = "text"
	out.Content = string(data)
	return out, nil
}

// artifactCodec 从制品 Metadata JSON 取 codec（PublishFile 写入 {"codec": "..."}）；缺失/解析失败回退 "none"。
func artifactCodec(metadata string) string {
	if metadata == "" {
		return "none"
	}
	var m struct {
		Codec string `json:"codec"`
	}
	if err := json.Unmarshal([]byte(metadata), &m); err != nil || m.Codec == "" {
		return "none"
	}
	return m.Codec
}

// bytesContainNUL 报告字节切片是否含 NUL（与前端/ArchiveViewer 二进制判定范式一致的启发式）。
func bytesContainNUL(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// readFileCapped 读取文件至多 limit 字节（超出部分丢弃；调用方已先据 size 判定 too-large）。
// 用 io.LimitReader 防御性兜底，避免极端情况下读入超大文件。
func readFileCapped(absPath string, limit int64) ([]byte, error) {
	if absPath == "" {
		return nil, ErrAssetNotFound
	}
	f, err := os.Open(absPath)
	if err != nil {
		return nil, ErrAssetNotFound
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, fmt.Errorf("读取制品失败: %w", err)
	}
	return data, nil
}

// assembleManifest 把频道 + 版本快照还原为 SignedManifest（未签名）。
func assembleManifest(ch *model.ClientChannel, ver *model.ClientVersion) (*SignedManifest, error) {
	files, managedDirs, cleanExclude, agent, err := decodeVersionSnapshot(ver)
	if err != nil {
		return nil, err
	}
	return &SignedManifest{
		SchemaVersion: manifestSchemaVersion,
		Channel:       ch.ChannelID,
		Version:       ver.Version,
		IssuedAt:      ver.CreatedAt.UTC().Format(time.RFC3339),
		ManagedDirs:   managedDirs,
		CleanExclude:  cleanExclude,
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

// validateCleanExclude 校验运营自定义排除路径合法性（FR-255）：
// 非"*"（非哨兵）、POSIX 风格、不绝对、无 .. 段、不含反斜杠/驱动器。
func validateCleanExclude(excludes []string) error {
	for _, ex := range excludes {
		if ex == "*" {
			return fmt.Errorf("%w: cleanExclude 不得为 \"*\"（与 managedDirs 哨兵冲突）", ErrInvalidVersionFiles)
		}
		if !safeManifestPath(ex) {
			return fmt.Errorf("%w: 非法 cleanExclude 路径 %q", ErrInvalidVersionFiles, ex)
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

// ---- updater-core 版本归档 + 频道选定（FR-259，见 updater-arch-simplification spec §D）----
//
// core jar 不再随 CP 内嵌单一版本"覆盖"分发，而是每次构建入库归档（type=client-updater-core，
// 内容寻址去重——不同版本 sha256 不同即天然多版本不覆盖）。频道选定版本（SelectedCoreSHA256）
// 指向某归档制品；coreEndpoint 端点据此返回 {version, sha256, downloadUrl, size} 供楔子拉取。
// 运营经面板版本选择器一键切换选定版本实现回滚（客户端下次启动查端点即用旧版）。

// CoreEndpointInfo coreEndpoint 端点返回的版本信息（spec §2.5.3 冻结格式）。
// downloadUrl 由 router 层拼接（需请求上下文推断公网基址），service 层不产出。
type CoreEndpointInfo struct {
	// Version updater-core 整数版本号（楔子据此比较升降级）。
	Version int `json:"version"`
	// SHA256 core jar 制品 sha256 = 下载寻址 key。
	SHA256 string `json:"sha256"`
	// Size core jar 字节数。
	Size int64 `json:"size"`
}

// CoreVersionSummary 归档版本列表项（管理面，JWT 平台管理员）。
type CoreVersionSummary struct {
	// Version 整数版本号（从 Asset.Version 解析）。
	Version int `json:"version"`
	// SHA256 制品 sha256（切换选定版本的寻址键）。
	SHA256 string `json:"sha256"`
	// Size 字节数。
	Size int64 `json:"size"`
	// CreatedAt 归档时间。
	CreatedAt time.Time `json:"createdAt"`
	// Selected 是否为该频道当前选定版本（运营据此高亮当前选择）。
	Selected bool `json:"selected"`
}

// ArchiveCoreJar 把 updater-core jar 入库归档为 client-updater-core 类型（FR-259）。
// 内容寻址去重：同 sha256 复用、不同 sha256 各自归档（不覆盖旧版）。version 写入 Asset.Version。
func (s *ClientVersionService) ArchiveCoreJar(r io.Reader, version string) (*model.Asset, error) {
	return s.assets.Ingest(r, IngestParams{
		Type:     model.AssetTypeClientUpdaterCore,
		Name:     "updater-core",
		Version:  version,
		Filename: "updater-core.jar",
		Metadata: `{"codec":"none","source":"embedded-updater-core"}`,
	})
}

// GetCoreEndpointInfo 查频道选定版本的 core jar 信息（FR-259 coreEndpoint 端点数据源）。
// SelectedCoreSHA256 非空 → 查该版本；空 → 回退最新归档版本（按 id DESC）。
// 频道不存在返回 ErrChannelNotFound；无任何归档返回 ErrNoCoreVersion。
func (s *ClientVersionService) GetCoreEndpointInfo(channelID string) (*CoreEndpointInfo, error) {
	if _, err := s.getChannel(channelID); err != nil {
		return nil, err
	}
	// 查频道选定 sha256；空则取最新归档。
	var ch model.ClientChannel
	if err := s.db.Select("selected_core_sha256").Where("channel_id = ?", channelID).First(&ch).Error; err != nil {
		return nil, fmt.Errorf("查询频道选定 core 版本失败: %w", err)
	}

	var asset model.Asset
	if ch.SelectedCoreSHA256 != "" {
		// 选定版本须存在于 client-updater-core 归档。
		err := s.db.Where("type = ? AND sha256 = ?", model.AssetTypeClientUpdaterCore, ch.SelectedCoreSHA256).First(&asset).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 选定 sha256 指向已失效的归档（制品被删等）→ 回退最新，避免客户端拿不到 core。
			return s.latestCoreArchive()
		}
		if err != nil {
			return nil, fmt.Errorf("查询选定 core 版本失败: %w", err)
		}
	} else {
		return s.latestCoreArchive()
	}
	return assetToCoreEndpointInfo(&asset), nil
}

// latestCoreArchive 取最新归档的 client-updater-core 制品（按 id DESC = 最近入库）。
// 无任何归档返回 ErrNoCoreVersion。
func (s *ClientVersionService) latestCoreArchive() (*CoreEndpointInfo, error) {
	var asset model.Asset
	err := s.db.Where("type = ?", model.AssetTypeClientUpdaterCore).Order("id DESC").First(&asset).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNoCoreVersion
	}
	if err != nil {
		return nil, fmt.Errorf("查询最新 core 归档失败: %w", err)
	}
	return assetToCoreEndpointInfo(&asset), nil
}

// assetToCoreEndpointInfo 把 Asset 转为 CoreEndpointInfo（Asset.Version 字符串转 int，非整数回退 0）。
func assetToCoreEndpointInfo(a *model.Asset) *CoreEndpointInfo {
	return &CoreEndpointInfo{
		Version: parseCoreVersionInt(a.Version),
		SHA256:  a.SHA256,
		Size:    a.Size,
	}
}

// parseCoreVersionInt 把 Asset.Version 字符串解析为整数；非法/空回退 0（楔子据此判无版本声明）。
func parseCoreVersionInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// ListCoreVersions 列出所有归档的 updater-core 版本（按 id DESC = 最近入库在前）。
// channelID 非空时标记该频道当前选定版本（Selected=true），供面板高亮当前选择。
func (s *ClientVersionService) ListCoreVersions(channelID ...string) ([]CoreVersionSummary, error) {
	var assets []model.Asset
	if err := s.db.Where("type = ?", model.AssetTypeClientUpdaterCore).Order("id DESC").Find(&assets).Error; err != nil {
		return nil, fmt.Errorf("查询 core 归档列表失败: %w", err)
	}
	// 查频道选定 sha256（可选，用于标记 Selected）。
	var selectedSHA string
	if len(channelID) > 0 && channelID[0] != "" {
		var ch model.ClientChannel
		if err := s.db.Select("selected_core_sha256").Where("channel_id = ?", channelID[0]).First(&ch).Error; err == nil {
			selectedSHA = ch.SelectedCoreSHA256
		}
		// 频道不存在时 selectedSHA 留空（不标记任何版本），由 router 层先行校验频道存在。
	}
	out := make([]CoreVersionSummary, 0, len(assets))
	for i := range assets {
		a := &assets[i]
		out = append(out, CoreVersionSummary{
			Version:   parseCoreVersionInt(a.Version),
			SHA256:    a.SHA256,
			Size:      a.Size,
			CreatedAt: a.CreatedAt,
			Selected:  selectedSHA != "" && selectedSHA == a.SHA256,
		})
	}
	return out, nil
}

// SelectCoreVersion 切换频道选定的 core 版本（FR-259 回滚操作）。
// 校验 sha256 存在于 client-updater-core 归档后更新 ClientChannel.SelectedCoreSHA256。
// 频道不存在返回 ErrChannelNotFound；sha256 不存在返回 ErrCoreVersionNotFound。
func (s *ClientVersionService) SelectCoreVersion(channelID, sha256 string) error {
	if _, err := s.getChannel(channelID); err != nil {
		return err
	}
	// 校验该 sha256 存在于归档制品库。
	var count int64
	if err := s.db.Model(&model.Asset{}).
		Where("type = ? AND sha256 = ?", model.AssetTypeClientUpdaterCore, sha256).
		Count(&count).Error; err != nil {
		return fmt.Errorf("校验 core 版本失败: %w", err)
	}
	if count == 0 {
		return ErrCoreVersionNotFound
	}
	if err := s.db.Model(&model.ClientChannel{}).Where("channel_id = ?", channelID).
		Update("selected_core_sha256", sha256).Error; err != nil {
		return fmt.Errorf("更新选定 core 版本失败: %w", err)
	}
	return nil
}
