package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
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
	// storageChannels 制品存储渠道服务（FR-347，见 ADR-073）：注入后 s3 制品的读取
	// （预览/代理下载/补丁物化）与 302 预签名经渠道 BlobStore；不注入 = 纯 local（既有测试零改动）。
	storageChannels *ArtifactStorageChannelService
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

// SetStorageChannels 注入制品存储渠道服务（FR-347，见 ADR-073）：s3 制品的读路径与预签名
// 经渠道 BlobStore 路由。由 main 装配；不调用则所有制品按 local 语义读取（历史行为）。
func (s *ClientVersionService) SetStorageChannels(ch *ArtifactStorageChannelService) {
	s.storageChannels = ch
}

// PresignArtifactURL 为 s3 制品现算预签名下载 URL（302 分发，FR-347，见 ADR-073 决策 1）。
// TTL 取制品所属渠道配置。渠道服务未注入/渠道缺失/凭证解密失败均报错（调用方回 503）。
func (s *ClientVersionService) PresignArtifactURL(asset *model.Asset) (string, error) {
	if s.storageChannels == nil {
		return "", fmt.Errorf("制品存储渠道服务未装配")
	}
	return s.storageChannels.PresignForAsset(asset)
}

// OpenArtifactContent 按记录自述路由打开制品内容流（FR-347）：local → os.Open CAS 文件
// （缺失 ErrAssetNotFound，与历史降级口径一致）；s3 → 渠道 BlobStore.Open 拉取对象。
// 供管理面代理下载/文本预览/补丁物化复用；玩家消费端点的 s3 分发走 302 预签名不经此。
func (s *ClientVersionService) OpenArtifactContent(asset *model.Asset, absPath string) (io.ReadCloser, error) {
	if asset.StorageBackend == model.AssetBackendS3 {
		if s.storageChannels == nil {
			return nil, fmt.Errorf("制品存储渠道服务未装配，无法读取 s3 制品")
		}
		store, err := s.storageChannels.StoreForAsset(asset)
		if err != nil {
			return nil, err
		}
		return store.Open(context.Background(), asset.RelPath)
	}
	if absPath == "" {
		return nil, ErrAssetNotFound
	}
	f, err := os.Open(absPath)
	if err != nil {
		return nil, ErrAssetNotFound
	}
	return f, nil
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
	meta, err := json.Marshal(map[string]string{"codec": codec})
	if err != nil {
		return nil, fmt.Errorf("序列化客户端文件元数据失败: %w", err)
	}
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
	files, err := s.withPatchArtifacts(channelID, p.Files)
	if err != nil {
		return nil, err
	}

	filesJSON, err := json.Marshal(files)
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

const (
	manifestPatchCodec              = "zstd-patch"
	patchInMemoryFallbackMaxBytes   = 64 << 20
	manifestContentTempFileTemplate = "client-patch-*.content"
	manifestPatchTempFileTemplate   = "client-patch-*.zst"
)

func (s *ClientVersionService) withPatchArtifacts(channelID string, files []ManifestFile) ([]ManifestFile, error) {
	out := append([]ManifestFile(nil), files...)
	ch, err := s.getChannel(channelID)
	if err != nil {
		return nil, err
	}
	if ch.CurrentVersion <= 0 {
		return out, nil
	}
	prev, err := s.findVersion(channelID, ch.CurrentVersion)
	if errors.Is(err, ErrVersionNotFound) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	prevFiles, _, _, _, err := decodeVersionSnapshot(prev)
	if err != nil {
		return nil, err
	}
	prevByKey := make(map[string]ManifestFile, len(prevFiles))
	for _, f := range prevFiles {
		prevByKey[manifestFilePatchKey(f)] = f
	}
	for i := range out {
		if out[i].Patch != nil || out[i].Sync != "strict" || out[i].Artifact.SHA256 == "" {
			continue
		}
		prevFile, ok := prevByKey[manifestFilePatchKey(out[i])]
		if !ok || prevFile.SHA256 == "" || prevFile.SHA256 == out[i].SHA256 || prevFile.Artifact.SHA256 == "" {
			continue
		}
		patch, ok, err := s.buildPatchArtifact(prevFile, out[i])
		if err != nil {
			return nil, err
		}
		if ok {
			out[i].Patch = patch
		}
	}
	return out, nil
}

func manifestFilePatchKey(f ManifestFile) string {
	return f.Path + "\x00" + f.Platform
}

func (s *ClientVersionService) buildPatchArtifact(oldFile, newFile ManifestFile) (*ManifestPatch, bool, error) {
	oldPath, cleanupOld, ok, err := s.manifestFileContentPath(oldFile)
	if err != nil || !ok {
		return nil, ok, err
	}
	defer cleanupOld()
	newPath, cleanupNew, ok, err := s.manifestFileContentPath(newFile)
	if err != nil || !ok {
		return nil, ok, err
	}
	defer cleanupNew()
	patchPath, cleanupPatch, ok, err := s.buildPatchFile(oldPath, newPath, oldFile.Size, newFile.Size)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	defer cleanupPatch()
	asset, err := s.ingestPatchArtifactFromPath(newFile, oldFile.SHA256, newFile.SHA256, patchPath)
	if err != nil {
		return nil, false, err
	}
	return &ManifestPatch{
		OldSHA256: oldFile.SHA256,
		NewSHA256: newFile.SHA256,
		Artifact:  ManifestArtifact{SHA256: asset.SHA256, Size: asset.Size, Codec: manifestPatchCodec},
	}, true, nil
}

func (s *ClientVersionService) buildPatchFile(oldPath, newPath string, oldSize, newSize int64) (string, func(), bool, error) {
	if zstdPath, err := exec.LookPath("zstd"); err == nil {
		return s.buildPatchFileWithZstdCommand(zstdPath, oldPath, newPath)
	}
	if oldSize > patchInMemoryFallbackMaxBytes || newSize > patchInMemoryFallbackMaxBytes {
		return "", nil, false, nil
	}
	oldContent, err := os.ReadFile(oldPath)
	if err != nil {
		return "", nil, false, nil
	}
	newContent, err := os.ReadFile(newPath)
	if err != nil {
		return "", nil, false, nil
	}
	patchBytes, err := encodeZstdPatch(oldContent, newContent)
	if err != nil {
		return "", nil, false, err
	}
	patchPath, cleanup, err := s.writePatchTempFile(patchBytes)
	if err != nil {
		return "", nil, false, err
	}
	return patchPath, cleanup, true, nil
}

func (s *ClientVersionService) buildPatchFileWithZstdCommand(zstdPath, oldPath, newPath string) (string, func(), bool, error) {
	patchPath, cleanup, err := s.createPatchTempFile()
	if err != nil {
		return "", nil, false, err
	}
	cmd := exec.Command(zstdPath, "--patch-from="+oldPath, "-q", "-f", "-o", patchPath, newPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		cleanup()
		return "", nil, false, fmt.Errorf("执行 zstd patch-from 失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return patchPath, cleanup, true, nil
}

func encodeZstdPatch(oldContent, newContent []byte) ([]byte, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderDictRaw(0, oldContent))
	if err != nil {
		return nil, fmt.Errorf("创建 zstd patch 编码器失败: %w", err)
	}
	patchBytes := enc.EncodeAll(newContent, nil)
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("关闭 zstd patch 编码器失败: %w", err)
	}
	return patchBytes, nil
}

func (s *ClientVersionService) ingestPatchArtifactFromPath(f ManifestFile, oldSHA, newSHA, patchPath string) (*model.Asset, error) {
	meta, err := json.Marshal(map[string]string{
		"codec":     manifestPatchCodec,
		"oldSha256": oldSHA,
		"newSha256": newSHA,
	})
	if err != nil {
		return nil, fmt.Errorf("序列化客户端补丁元数据失败: %w", err)
	}
	return s.assets.IngestFromPath(patchPath, IngestParams{
		Type:     model.AssetTypeClientFile,
		Filename: patchFilename(f),
		Metadata: string(meta),
	})
}

func patchFilename(f ManifestFile) string {
	name := strings.ReplaceAll(f.Path, "/", "-")
	if name == "" {
		name = f.SHA256
	}
	return name + ".patch.zst"
}

func (s *ClientVersionService) writePatchTempFile(patch []byte) (string, func(), error) {
	patchPath, cleanup, err := s.createPatchTempFile()
	if err != nil {
		return "", nil, err
	}
	if err := os.WriteFile(patchPath, patch, 0o644); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("写入 zstd patch 临时文件失败: %w", err)
	}
	return patchPath, cleanup, nil
}

func (s *ClientVersionService) createPatchTempFile() (string, func(), error) {
	tmp, err := os.CreateTemp(s.patchTempDir(), manifestPatchTempFileTemplate)
	if err != nil {
		return "", nil, fmt.Errorf("创建 zstd patch 临时文件失败: %w", err)
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, fmt.Errorf("关闭 zstd patch 临时文件失败: %w", err)
	}
	return path, func() { _ = os.Remove(path) }, nil
}

func (s *ClientVersionService) manifestFileContentPath(f ManifestFile) (string, func(), bool, error) {
	asset, absPath, err := s.OpenArtifact(f.Artifact.SHA256)
	if errors.Is(err, ErrAssetNotFound) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	if asset.Type != model.AssetTypeClientFile {
		return "", nil, false, nil
	}
	codec := strings.ToLower(strings.TrimSpace(f.Artifact.Codec))
	if codec == "" || codec == "none" {
		if f.Size > 0 && asset.Size != f.Size {
			return "", nil, false, nil
		}
		if f.SHA256 != "" && !strings.EqualFold(asset.SHA256, f.SHA256) {
			return "", nil, false, nil
		}
		// 本地 codec=none 快路径：直用 CAS 物理路径（原样保留）；
		// s3 制品本地无文件，落到下方物化管道（BlobStore.Open → 临时文件，FR-347）。
		if asset.StorageBackend != model.AssetBackendS3 {
			return absPath, func() {}, true, nil
		}
	}
	return s.materializeManifestFileContent(asset, absPath, f)
}

func (s *ClientVersionService) materializeManifestFileContent(asset *model.Asset, absPath string, f ManifestFile) (string, func(), bool, error) {
	// 源按记录后端路由（FR-347）：local os.Open；s3 渠道 BlobStore.Open。
	// 打开失败按「制品不可用于 patch」降级（返回 ok=false 不报错），与历史口径一致。
	src, err := s.OpenArtifactContent(asset, absPath)
	if err != nil {
		return "", nil, false, nil
	}
	defer src.Close()

	reader, closeReader, ok, err := decodedManifestArtifactReader(src, f.Artifact.Codec)
	if err != nil || !ok {
		return "", nil, ok, err
	}
	defer closeReader()

	tmp, err := os.CreateTemp(s.patchTempDir(), manifestContentTempFileTemplate)
	if err != nil {
		return "", nil, false, fmt.Errorf("创建 manifest 内容临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	sha := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(tmp, sha), reader)
	closeErr := tmp.Close()
	if copyErr != nil {
		cleanup()
		return "", nil, false, fmt.Errorf("解码 manifest 内容失败: %w", copyErr)
	}
	if closeErr != nil {
		cleanup()
		return "", nil, false, fmt.Errorf("关闭 manifest 内容临时文件失败: %w", closeErr)
	}
	if f.Size > 0 && size != f.Size {
		cleanup()
		return "", nil, false, nil
	}
	if f.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(sha.Sum(nil)), f.SHA256) {
		cleanup()
		return "", nil, false, nil
	}
	return tmpPath, cleanup, true, nil
}

func decodedManifestArtifactReader(raw io.Reader, codec string) (io.Reader, func(), bool, error) {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "", "none":
		return raw, func() {}, true, nil
	case "zstd":
		dec, err := zstd.NewReader(raw)
		if err != nil {
			return nil, nil, false, err
		}
		return dec, dec.Close, true, nil
	default:
		return nil, nil, false, nil
	}
}

func (s *ClientVersionService) patchTempDir() string {
	if s.assets != nil && s.assets.root != nil {
		dir := s.assets.root.CacheDir()
		if err := os.MkdirAll(dir, 0o755); err == nil {
			return dir
		}
	}
	return os.TempDir()
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
	// 失效制品（FR-349）：外置对象已缺失，预览给明确错误（410）而非拉对象撞 404。
	if asset.StorageState == model.AssetStorageLost {
		return nil, ErrArtifactLost
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

	// 读取按记录后端路由（FR-347）：local 保持 os.Open 降级口径，s3 经渠道 BlobStore 限量读。
	rc, oerr := s.OpenArtifactContent(asset, absPath)
	if oerr != nil {
		return nil, oerr
	}
	defer rc.Close()
	data, rerr := io.ReadAll(io.LimitReader(rc, ArtifactTextPreviewMaxBytes))
	if rerr != nil {
		return nil, fmt.Errorf("读取制品失败: %w", rerr)
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
		if f.Patch != nil {
			if err := validateManifestPatch(f); err != nil {
				return fmt.Errorf("%w: %q %v", ErrInvalidVersionFiles, f.Path, err)
			}
		}
	}
	return nil
}

func validateManifestPatch(f ManifestFile) error {
	if f.Patch.OldSHA256 == "" {
		return errors.New("缺 patch.oldSha256")
	}
	if f.Patch.NewSHA256 == "" {
		return errors.New("缺 patch.newSha256")
	}
	if f.Patch.NewSHA256 != f.SHA256 {
		return errors.New("patch.newSha256 必须等于文件 sha256")
	}
	if f.Patch.Artifact.SHA256 == "" {
		return errors.New("缺 patch.artifact.sha256")
	}
	if f.Patch.Artifact.Codec != manifestPatchCodec {
		return errors.New("patch.artifact.codec 必须为 zstd-patch")
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
// 其中 version 是频道级递增分发版本，不等于归档版本；选回旧 sha 回滚时仍必须递增，确保楔子下载。

// CoreEndpointInfo coreEndpoint 端点返回的版本信息（spec §2.5.3 冻结格式）。
// downloadUrl 由 router 层拼接（需请求上下文推断公网基址），service 层不产出。
type CoreEndpointInfo struct {
	// Version 频道级 core 分发版本号（楔子只据此比较是否下载）。
	Version int `json:"version"`
	// SHA256 core jar 制品 sha256 = 下载寻址 key。
	SHA256 string `json:"sha256"`
	// Size core jar 字节数。
	Size int64 `json:"size"`
}

// CoreVersionSummary 归档版本列表项（管理面，JWT 平台管理员）。
type CoreVersionSummary struct {
	// Version 数字归档版本号（从 Asset.Version 解析），保留给 wedge 分发兜底与旧前端兼容。
	Version int `json:"version"`
	// CoreVersion jar 内声明的语义版本（如 0.1.0-SNAPSHOT），缺失为空。
	CoreVersion string `json:"coreVersion,omitempty"`
	// DisplayVersion 前端优先展示的完整构建版本（version+commit，dirty 时带 .dirty）。
	DisplayVersion string `json:"displayVersion,omitempty"`
	// GitCommit jar 构建时的 12 位短提交 hash，缺失为空。
	GitCommit string `json:"gitCommit,omitempty"`
	// Dirty jar 构建时是否存在未提交的已跟踪文件变更。
	Dirty bool `json:"dirty"`
	// BuildTime jar 构建时间（RFC3339），缺失为空。
	BuildTime string `json:"buildTime,omitempty"`
	// SHA256 制品 sha256（切换选定版本的寻址键）。
	SHA256 string `json:"sha256"`
	// Size 字节数。
	Size int64 `json:"size"`
	// CreatedAt 归档时间。
	CreatedAt time.Time `json:"createdAt"`
	// Selected 是否为该频道当前选定版本（运营据此高亮当前选择）。
	Selected bool `json:"selected"`
}

type updaterCoreBuildMetadata struct {
	Codec       string `json:"codec,omitempty"`
	Source      string `json:"source,omitempty"`
	CoreVersion string `json:"coreVersion,omitempty"`
	GitCommit   string `json:"gitCommit,omitempty"`
	Dirty       bool   `json:"dirty"`
	BuildTime   string `json:"buildTime,omitempty"`
}

// ArchiveCoreJar 把 updater-core jar 入库归档为 client-updater-core 类型（FR-259）。
// 内容寻址去重：同 sha256 复用、不同 sha256 各自归档（不覆盖旧版）。version 写入 Asset.Version。
func (s *ClientVersionService) ArchiveCoreJar(r io.Reader, version string) (*model.Asset, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("读取 updater-core jar 失败: %w", err)
	}
	buildMeta := readUpdaterCoreBuildMetadata(data)
	buildMeta.Codec = "none"
	buildMeta.Source = "embedded-updater-core"
	metadataJSON := encodeUpdaterCoreBuildMetadata(buildMeta)

	sha := sha256.Sum256(data)
	shaHex := hex.EncodeToString(sha[:])
	var existing model.Asset
	err = s.db.Where("type = ? AND sha256 = ?", model.AssetTypeClientUpdaterCore, shaHex).First(&existing).Error
	if err == nil {
		if shouldPatchUpdaterCoreMetadata(existing.Metadata, buildMeta) {
			if e := s.db.Model(&model.Asset{}).Where("id = ?", existing.ID).Update("metadata", metadataJSON).Error; e == nil {
				existing.Metadata = metadataJSON
			}
		}
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("查询 updater-core 归档失败: %w", err)
	}

	nextVersion, err := s.normalizeCoreArchiveVersion(version)
	if err != nil {
		return nil, err
	}
	return s.assets.Ingest(bytes.NewReader(data), IngestParams{
		Type:     model.AssetTypeClientUpdaterCore,
		Name:     "updater-core",
		Version:  strconv.Itoa(nextVersion),
		Filename: "updater-core.jar",
		Metadata: metadataJSON,
	})
}

func readUpdaterCoreBuildMetadata(data []byte) updaterCoreBuildMetadata {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return updaterCoreBuildMetadata{}
	}
	var manifest map[string]string
	for _, f := range zr.File {
		switch strings.ToUpper(f.Name) {
		case "META-INF/JM-UPDATER-CORE.PROPERTIES":
			props := readZipTextFile(f)
			if len(props) > 0 {
				m := parseSimpleProperties(props)
				return updaterCoreBuildMetadata{
					CoreVersion: strings.TrimSpace(m["version"]),
					GitCommit:   strings.TrimSpace(m["gitCommit"]),
					Dirty:       strings.EqualFold(strings.TrimSpace(m["dirty"]), "true"),
					BuildTime:   strings.TrimSpace(m["buildTime"]),
				}
			}
		case "META-INF/MANIFEST.MF":
			manifest = parseManifestAttributes(readZipTextFile(f))
		}
	}
	if len(manifest) == 0 {
		return updaterCoreBuildMetadata{}
	}
	return updaterCoreBuildMetadata{
		CoreVersion: strings.TrimSpace(manifest["JM-Updater-Core-Version"]),
		GitCommit:   strings.TrimSpace(manifest["JM-Git-Commit"]),
		Dirty:       strings.EqualFold(strings.TrimSpace(manifest["JM-Git-Dirty"]), "true"),
		BuildTime:   strings.TrimSpace(manifest["JM-Build-Time"]),
	}
}

func readZipTextFile(f *zip.File) string {
	rc, err := f.Open()
	if err != nil {
		return ""
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 64*1024))
	if err != nil {
		return ""
	}
	return string(data)
}

func parseSimpleProperties(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		idx := strings.IndexAny(line, "=:")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if key != "" {
			out[key] = value
		}
	}
	return out
}

func parseManifestAttributes(raw string) map[string]string {
	out := map[string]string{}
	lastKey := ""
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, " ") && lastKey != "" {
			out[lastKey] += strings.TrimPrefix(line, " ")
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		lastKey = strings.TrimSpace(line[:idx])
		out[lastKey] = strings.TrimSpace(line[idx+1:])
	}
	return out
}

func encodeUpdaterCoreBuildMetadata(meta updaterCoreBuildMetadata) string {
	if meta.Codec == "" {
		meta.Codec = "none"
	}
	if meta.Source == "" {
		meta.Source = "embedded-updater-core"
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return `{"codec":"none","source":"embedded-updater-core"}`
	}
	return string(raw)
}

func shouldPatchUpdaterCoreMetadata(existing string, next updaterCoreBuildMetadata) bool {
	if next.CoreVersion == "" && next.GitCommit == "" && next.BuildTime == "" && !next.Dirty {
		return false
	}
	current := decodeUpdaterCoreBuildMetadata(existing)
	return current.CoreVersion == "" && current.GitCommit == "" && current.BuildTime == "" && !current.Dirty
}

func decodeUpdaterCoreBuildMetadata(raw string) updaterCoreBuildMetadata {
	var meta updaterCoreBuildMetadata
	if raw == "" {
		return meta
	}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		slog.Warn("解析更新核心构建元数据失败，已忽略损坏数据", "error", err)
	}
	return meta
}

func updaterCoreDisplayVersion(meta updaterCoreBuildMetadata) string {
	version := strings.TrimSpace(meta.CoreVersion)
	commit := strings.TrimSpace(meta.GitCommit)
	if version == "" {
		return ""
	}
	if commit == "" || strings.EqualFold(commit, "unknown") {
		if meta.Dirty {
			return version + "+dirty"
		}
		return version
	}
	display := version + "+" + commit
	if meta.Dirty {
		display += ".dirty"
	}
	return display
}

func (s *ClientVersionService) normalizeCoreArchiveVersion(version string) (int, error) {
	maxVersion, err := s.maxCoreArchiveVersion(s.db)
	if err != nil {
		return 0, err
	}
	input := parseCoreVersionInt(version)
	if input > maxVersion {
		return input, nil
	}
	return maxVersion + 1, nil
}

// GetCoreEndpointInfo 查频道选定版本的 core jar 信息（FR-259 coreEndpoint 端点数据源）。
// SelectedCoreSHA256 非空 → 查该归档制品，并返回频道级分发版本；空 → 回退最新归档版本（按 id DESC）。
// 频道不存在返回 ErrChannelNotFound；无任何归档返回 ErrNoCoreVersion。
func (s *ClientVersionService) GetCoreEndpointInfo(channelID string) (*CoreEndpointInfo, error) {
	if _, err := s.getChannel(channelID); err != nil {
		return nil, err
	}
	// 查频道选定 sha256；空则取最新归档。
	var ch model.ClientChannel
	if err := s.db.Select("selected_core_sha256", "selected_core_version").Where("channel_id = ?", channelID).First(&ch).Error; err != nil {
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
	info := assetToCoreEndpointInfo(&asset)
	info.Version = maxInt(info.Version, ch.SelectedCoreVersion)
	return info, nil
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

func (s *ClientVersionService) maxCoreArchiveVersion(db *gorm.DB) (int, error) {
	var assets []model.Asset
	if err := db.Select("version").Where("type = ?", model.AssetTypeClientUpdaterCore).Find(&assets).Error; err != nil {
		return 0, fmt.Errorf("查询 core 最大归档版本失败: %w", err)
	}
	maxVersion := 0
	for i := range assets {
		maxVersion = maxInt(maxVersion, parseCoreVersionInt(assets[i].Version))
	}
	return maxVersion, nil
}

func selectedCoreDistributionVersion(current, assetVersion, maxArchiveVersion int) int {
	floor := current
	if assetVersion < maxArchiveVersion {
		// 回滚到旧归档时，客户端可能已经见过最新归档版本，端点版本必须抬过最新版本。
		floor = maxInt(floor, maxArchiveVersion)
	}
	if assetVersion <= floor {
		return floor + 1
	}
	return assetVersion
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// CoreVersionSummaryFromAsset 把归档资产转换为管理面摘要。selected 由调用方按频道选定状态传入。
func CoreVersionSummaryFromAsset(a *model.Asset, selected bool) CoreVersionSummary {
	meta := decodeUpdaterCoreBuildMetadata(a.Metadata)
	return CoreVersionSummary{
		Version:        parseCoreVersionInt(a.Version),
		CoreVersion:    meta.CoreVersion,
		DisplayVersion: updaterCoreDisplayVersion(meta),
		GitCommit:      meta.GitCommit,
		Dirty:          meta.Dirty,
		BuildTime:      meta.BuildTime,
		SHA256:         a.SHA256,
		Size:           a.Size,
		CreatedAt:      a.CreatedAt,
		Selected:       selected,
	}
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
		out = append(out, CoreVersionSummaryFromAsset(a, selectedSHA != "" && selectedSHA == a.SHA256))
	}
	return out, nil
}

// SelectCoreVersion 切换频道选定的 core 版本（FR-259 回滚操作）。
// 校验 sha256 存在于 client-updater-core 归档后更新选定 SHA，并维护频道级递增分发版本。
// 频道不存在返回 ErrChannelNotFound；sha256 不存在返回 ErrCoreVersionNotFound。
func (s *ClientVersionService) SelectCoreVersion(channelID, sha256 string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var ch model.ClientChannel
		if err := tx.Select("channel_id", "selected_core_sha256", "selected_core_version").
			Where("channel_id = ?", channelID).First(&ch).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrChannelNotFound
			}
			return fmt.Errorf("查询频道失败: %w", err)
		}

		// 校验该 sha256 存在于归档制品库，并读取其归档版本。
		var asset model.Asset
		if err := tx.Select("sha256", "version").
			Where("type = ? AND sha256 = ?", model.AssetTypeClientUpdaterCore, sha256).
			First(&asset).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCoreVersionNotFound
			}
			return fmt.Errorf("校验 core 版本失败: %w", err)
		}

		nextVersion := ch.SelectedCoreVersion
		if ch.SelectedCoreSHA256 != sha256 || nextVersion <= 0 {
			maxArchiveVersion, err := s.maxCoreArchiveVersion(tx)
			if err != nil {
				return err
			}
			nextVersion = selectedCoreDistributionVersion(ch.SelectedCoreVersion, parseCoreVersionInt(asset.Version), maxArchiveVersion)
		}
		if err := tx.Model(&model.ClientChannel{}).Where("channel_id = ?", channelID).
			Updates(map[string]any{
				"selected_core_sha256":  sha256,
				"selected_core_version": nextVersion,
			}).Error; err != nil {
			return fmt.Errorf("更新选定 core 版本失败: %w", err)
		}
		return nil
	})
}
