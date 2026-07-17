package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/blobstore"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

var (
	// ErrArtifactStorageNotFound 制品存储渠道不存在。
	ErrArtifactStorageNotFound = errors.New("制品存储渠道不存在")
	// ErrArtifactStorageNameConflict 渠道名称已存在。
	ErrArtifactStorageNameConflict = errors.New("制品存储渠道名称已存在")
	// ErrArtifactStorageInvalidType 渠道类型非法（面板仅可创建 s3；local 由内置行独占）。
	ErrArtifactStorageInvalidType = errors.New("非法的制品存储渠道类型")
	// ErrArtifactStorageBuiltinImmutable 内置「本机存储」渠道不可编辑、不可删除。
	ErrArtifactStorageBuiltinImmutable = errors.New("内置本机存储渠道不可编辑或删除")
	// ErrArtifactStorageTypeImmutable 渠道类型创建后不可修改（改型=删重建）。
	ErrArtifactStorageTypeImmutable = errors.New("制品存储渠道类型不可修改")
	// ErrArtifactStorageActiveDelete 活跃渠道禁止删除（先切走活跃再删）。
	ErrArtifactStorageActiveDelete = errors.New("活跃渠道禁止删除，请先切换活跃渠道")
	// ErrArtifactStorageInUse 渠道被制品引用，禁止删除（读路径按记录自述定位，删渠道即断链）。
	ErrArtifactStorageInUse = errors.New("制品存储渠道被制品引用，无法删除")
	// ErrArtifactStorageEncryptorMissing 凭证加密器未配置（生产 autogen 失败降级态）。
	// 此时创建/编辑 s3 渠道直接拒绝——绝不落明文、不静默降级（ADR-073 决策 4）。
	ErrArtifactStorageEncryptorMissing = errors.New("凭证加密未配置，无法保存对象存储渠道")
	// ErrArtifactStorageInvalidConfig 渠道配置校验失败（endpoint/bucket 缺失、TTL 越界等）。
	ErrArtifactStorageInvalidConfig = errors.New("制品存储渠道配置非法")
)

const (
	// artifactPresignTTLDefault 预签名 URL 默认有效期秒数（10 分钟，ADR-073 决策 1）。
	artifactPresignTTLDefault = 600
	// artifactPresignTTLMin / Max 预签名 TTL 允许区间 [60, 3600]。
	artifactPresignTTLMin = 60
	artifactPresignTTLMax = 3600
	// ArtifactStorageBuiltinName 内置「本机存储」渠道名（EnsureBuiltin 幂等 seed）。
	ArtifactStorageBuiltinName = "本机存储"
	// artifactStorageProbeTimeout 连通性测试的写探测超时。
	artifactStorageProbeTimeout = 8 * time.Second
)

// ArtifactStorageChannelService 制品存储渠道服务（FR-347，见 ADR-073，修订 ADR-011 存储节）。
//
// 渠道是 client-file 制品写路径的落点路由配置：全表恰一条活跃（事务保证），切活跃仅影响
// 新上传；存量 Asset 按自身 StorageBackend + StorageChannelID 自述读取。凭证面板直填、
// 复用 FR-192 KeyEncryptor（AES-256-GCM）可逆加密落库，与备份域 ${ENV_VAR} 形态分道。
type ArtifactStorageChannelService struct {
	db   *gorm.DB
	root *dataroot.Root
	// enc 凭证可逆加密器（复用 FR-192 基建）；nil=未配置，s3 渠道创建/编辑被拒（快失败）。
	enc *KeyEncryptor
}

// NewArtifactStorageChannelService 创建制品存储渠道服务。root 提供本地渠道数据根。
func NewArtifactStorageChannelService(db *gorm.DB, root *dataroot.Root) *ArtifactStorageChannelService {
	return &ArtifactStorageChannelService{db: db, root: root}
}

// SetKeyEncryptor 注入凭证可逆加密器（与拉取密钥共用主密钥，密钥运维口径一致，ADR-073 决策 4）。
func (s *ArtifactStorageChannelService) SetKeyEncryptor(enc *KeyEncryptor) {
	s.enc = enc
}

// EnsureBuiltin 幂等 seed 内置「本机存储」渠道（Builtin+local，不可删不可编辑）。
// 若全表无活跃行则将内置行置活跃——local 恒可用的语义由 DB 实体承载（ADR-073 决策 2）。
func (s *ArtifactStorageChannelService) EnsureBuiltin() error {
	var builtin model.ArtifactStorageChannel
	err := s.db.Where("builtin = ?", true).First(&builtin).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var activeCount int64
		if cerr := s.db.Model(&model.ArtifactStorageChannel{}).Where("active = ?", true).Count(&activeCount).Error; cerr != nil {
			return fmt.Errorf("查询活跃渠道失败: %w", cerr)
		}
		builtin = model.ArtifactStorageChannel{
			Name:              ArtifactStorageBuiltinName,
			Type:              model.ArtifactStorageLocal,
			PresignTTLSeconds: artifactPresignTTLDefault,
			Active:            activeCount == 0,
			Builtin:           true,
		}
		if cerr := s.db.Create(&builtin).Error; cerr != nil {
			return fmt.Errorf("创建内置本机存储渠道失败: %w", cerr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("查询内置渠道失败: %w", err)
	}
	// 内置行已存在：兜底修复「全表无活跃」态（防御，正常流程不会出现）。
	var activeCount int64
	if cerr := s.db.Model(&model.ArtifactStorageChannel{}).Where("active = ?", true).Count(&activeCount).Error; cerr != nil {
		return fmt.Errorf("查询活跃渠道失败: %w", cerr)
	}
	if activeCount == 0 {
		if uerr := s.db.Model(&model.ArtifactStorageChannel{}).Where("id = ?", builtin.ID).Update("active", true).Error; uerr != nil {
			return fmt.Errorf("恢复内置渠道活跃失败: %w", uerr)
		}
	}
	return nil
}

// List 全量渠道（Builtin 恒排最前），填充 HasAccessKey/HasSecretKey（永不回凭证明文/密文）。
func (s *ArtifactStorageChannelService) List() ([]model.ArtifactStorageChannel, error) {
	var out []model.ArtifactStorageChannel
	if err := s.db.Order("builtin desc, id asc").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("查询制品存储渠道失败: %w", err)
	}
	for i := range out {
		fillArtifactStorageFlags(&out[i])
	}
	return out, nil
}

// GetByID 按 ID 获取渠道（含 Has* 填充）。
func (s *ArtifactStorageChannelService) GetByID(id uint) (*model.ArtifactStorageChannel, error) {
	var ch model.ArtifactStorageChannel
	if err := s.db.First(&ch, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrArtifactStorageNotFound
		}
		return nil, fmt.Errorf("查询制品存储渠道失败: %w", err)
	}
	fillArtifactStorageFlags(&ch)
	return &ch, nil
}

// SaveArtifactStorageParams 创建/编辑渠道参数（面板表单）。
// AccessKey/SecretKey 为明文（编辑时空=保留原密文，脱敏编辑语义）；UseSSL/PresignTTLSeconds
// 为指针以区分「未填（取默认）」与「显式 false/值」。
type SaveArtifactStorageParams struct {
	Name              string
	Type              string
	Endpoint          string
	Bucket            string
	Region            string
	Prefix            string
	AccessKey         string
	SecretKey         string
	UseSSL            *bool
	PresignTTLSeconds *int
}

// Create 创建 s3 渠道。仅允许 type=s3（local 由内置行独占，杜绝多条 local 语义歧义）；
// 校验名称唯一（预检 + DB 唯一索引兜底）、endpoint/bucket 非空、TTL 区间；凭证加密落库。
func (s *ArtifactStorageChannelService) Create(p SaveArtifactStorageParams) (*model.ArtifactStorageChannel, error) {
	if p.Type != string(model.ArtifactStorageS3) {
		return nil, fmt.Errorf("%w: %q（面板仅可创建 s3 渠道）", ErrArtifactStorageInvalidType, p.Type)
	}
	if s.enc == nil {
		return nil, ErrArtifactStorageEncryptorMissing
	}
	ttl, err := validateArtifactStorageParams(&p)
	if err != nil {
		return nil, err
	}
	if err := s.checkNameConflict(p.Name, 0); err != nil {
		return nil, err
	}
	akEnc, skEnc, err := s.encryptCredentials(p.AccessKey, p.SecretKey)
	if err != nil {
		return nil, err
	}
	ch := &model.ArtifactStorageChannel{
		Name:              strings.TrimSpace(p.Name),
		Type:              model.ArtifactStorageS3,
		Endpoint:          strings.TrimSpace(p.Endpoint),
		Bucket:            strings.TrimSpace(p.Bucket),
		Region:            artifactStorageRegion(p.Region),
		Prefix:            strings.Trim(strings.TrimSpace(p.Prefix), "/"),
		AccessKeyEnc:      akEnc,
		SecretKeyEnc:      skEnc,
		UseSSL:            p.UseSSL != nil && *p.UseSSL,
		PresignTTLSeconds: ttl,
	}
	if err := s.db.Create(ch).Error; err != nil {
		return nil, fmt.Errorf("创建制品存储渠道失败: %w", err)
	}
	fillArtifactStorageFlags(ch)
	return ch, nil
}

// Update 编辑渠道。Builtin 拒；type 不可改；accessKey/secretKey 空=保留原密文、非空=重加密覆盖；
// 成功后清 LastTest*（配置已变，旧测试结论失效，沿 FR-338 备份域先例）。
func (s *ArtifactStorageChannelService) Update(id uint, p SaveArtifactStorageParams) (*model.ArtifactStorageChannel, error) {
	cur, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	if cur.Builtin {
		return nil, ErrArtifactStorageBuiltinImmutable
	}
	if p.Type != "" && p.Type != string(cur.Type) {
		return nil, fmt.Errorf("%w: %s → %s", ErrArtifactStorageTypeImmutable, cur.Type, p.Type)
	}
	if s.enc == nil {
		return nil, ErrArtifactStorageEncryptorMissing
	}
	ttl, err := validateArtifactStorageParams(&p)
	if err != nil {
		return nil, err
	}
	if err := s.checkNameConflict(p.Name, id); err != nil {
		return nil, err
	}
	updates := map[string]any{
		"name":                strings.TrimSpace(p.Name),
		"endpoint":            strings.TrimSpace(p.Endpoint),
		"bucket":              strings.TrimSpace(p.Bucket),
		"region":              artifactStorageRegion(p.Region),
		"prefix":              strings.Trim(strings.TrimSpace(p.Prefix), "/"),
		"use_ssl":             p.UseSSL != nil && *p.UseSSL,
		"presign_ttl_seconds": ttl,
		// 配置已变，旧连通性结论失效。
		"last_test_at":      nil,
		"last_test_ok":      false,
		"last_test_message": "",
	}
	// 凭证脱敏编辑语义：空=保留原密文，非空=重加密覆盖。
	if strings.TrimSpace(p.AccessKey) != "" {
		enc, eerr := s.enc.Encrypt(p.AccessKey)
		if eerr != nil {
			return nil, fmt.Errorf("加密 access key 失败: %w", eerr)
		}
		updates["access_key_enc"] = enc
	}
	if strings.TrimSpace(p.SecretKey) != "" {
		enc, eerr := s.enc.Encrypt(p.SecretKey)
		if eerr != nil {
			return nil, fmt.Errorf("加密 secret key 失败: %w", eerr)
		}
		updates["secret_key_enc"] = enc
	}
	if err := s.db.Model(&model.ArtifactStorageChannel{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("更新制品存储渠道失败: %w", err)
	}
	return s.GetByID(id)
}

// Delete 删除渠道（硬删除）。Builtin 拒；活跃拒（先切走）；被制品引用拒（附引用数）——
// 读路径按 Asset.StorageChannelID 自述定位，删被引用渠道即断链（ADR-073 决策 2）。
func (s *ArtifactStorageChannelService) Delete(id uint) error {
	ch, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if ch.Builtin {
		return ErrArtifactStorageBuiltinImmutable
	}
	if ch.Active {
		return ErrArtifactStorageActiveDelete
	}
	var refs int64
	if err := s.db.Model(&model.Asset{}).Where("storage_channel_id = ?", id).Count(&refs).Error; err != nil {
		return fmt.Errorf("检查渠道引用失败: %w", err)
	}
	if refs > 0 {
		return fmt.Errorf("%w: 当前被 %d 个制品引用", ErrArtifactStorageInUse, refs)
	}
	return s.db.Delete(&model.ArtifactStorageChannel{}, id).Error
}

// SetActive 设活跃渠道：事务内先清后设，全表恰一条活跃。切活跃只影响新上传，
// 存量制品按各自记录自述读取，不受影响。
func (s *ArtifactStorageChannelService) SetActive(id uint) (*model.ArtifactStorageChannel, error) {
	if _, err := s.GetByID(id); err != nil {
		return nil, err
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&model.ArtifactStorageChannel{}).Where("active = ?", true).Update("active", false).Error; e != nil {
			return fmt.Errorf("清除旧活跃渠道失败: %w", e)
		}
		if e := tx.Model(&model.ArtifactStorageChannel{}).Where("id = ?", id).Update("active", true).Error; e != nil {
			return fmt.Errorf("设置活跃渠道失败: %w", e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetByID(id)
}

// Active 当前活跃渠道；无活跃行时回退内置 local 行（防御，EnsureBuiltin 正常已兜底）。
func (s *ArtifactStorageChannelService) Active() (*model.ArtifactStorageChannel, error) {
	var ch model.ArtifactStorageChannel
	err := s.db.Where("active = ?", true).First(&ch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if berr := s.db.Where("builtin = ?", true).First(&ch).Error; berr != nil {
			return nil, fmt.Errorf("无活跃渠道且内置渠道缺失: %w", berr)
		}
		fillArtifactStorageFlags(&ch)
		return &ch, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询活跃渠道失败: %w", err)
	}
	fillArtifactStorageFlags(&ch)
	return &ch, nil
}

// StoreFor 把渠道解析为 BlobStore 实例：local→数据根适配器；s3→解密凭证→S3 适配器。
func (s *ArtifactStorageChannelService) StoreFor(ch *model.ArtifactStorageChannel) (blobstore.Store, error) {
	switch ch.Type {
	case model.ArtifactStorageLocal:
		return blobstore.NewLocal(s.root)
	case model.ArtifactStorageS3:
		ak, sk, err := s.decryptCredentials(ch)
		if err != nil {
			return nil, err
		}
		return blobstore.NewS3(blobstore.S3Config{
			Endpoint:  ch.Endpoint,
			Bucket:    ch.Bucket,
			Region:    ch.Region,
			Prefix:    ch.Prefix,
			AccessKey: ak,
			SecretKey: sk,
			UseSSL:    ch.UseSSL,
		})
	default:
		return nil, fmt.Errorf("%w: %q", ErrArtifactStorageInvalidType, ch.Type)
	}
}

// StoreForAsset 按制品记录自述（StorageBackend + StorageChannelID）解析读路径 BlobStore。
// local 记录（含历史 StorageChannelID=0）恒走数据根；被引用渠道不可能已删（删除守卫兜底）。
func (s *ArtifactStorageChannelService) StoreForAsset(a *model.Asset) (blobstore.Store, error) {
	if a.StorageBackend != model.AssetBackendS3 {
		return blobstore.NewLocal(s.root)
	}
	if a.StorageChannelID == 0 {
		return nil, fmt.Errorf("s3 制品缺少渠道引用（asset %d）", a.ID)
	}
	ch, err := s.GetByID(a.StorageChannelID)
	if err != nil {
		return nil, err
	}
	return s.StoreFor(ch)
}

// PresignForAsset 为 s3 制品现算预签名下载 URL，TTL 取渠道配置（302 分发，ADR-073 决策 1）。
func (s *ArtifactStorageChannelService) PresignForAsset(a *model.Asset) (string, error) {
	if a.StorageBackend != model.AssetBackendS3 || a.StorageChannelID == 0 {
		return "", fmt.Errorf("制品非 s3 存储，无需预签名（asset %d）", a.ID)
	}
	ch, err := s.GetByID(a.StorageChannelID)
	if err != nil {
		return "", err
	}
	store, err := s.StoreFor(ch)
	if err != nil {
		return "", err
	}
	return store.Presign(a.RelPath, time.Duration(clampPresignTTL(ch.PresignTTLSeconds))*time.Second)
}

// ArtifactStorageTestResult 渠道连通性测试结果（形态沿备份域 StorageTestResult 先例）。
type ArtifactStorageTestResult struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
	ErrorCode string `json:"errorCode,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
}

// TestCandidate 真连探测一个（可能未保存的）渠道配置，不写库。
// s3 = 写探测对象（PutFile 8 字节 → Stat → Delete，探测键挂渠道 prefix 下）——验证的正是
// 写路径所需权限；local = 数据根可写探测。id>0 且凭证留空时用存库密文解密后探测（脱敏编辑态）。
func (s *ArtifactStorageChannelService) TestCandidate(p SaveArtifactStorageParams, id uint) ArtifactStorageTestResult {
	start := time.Now()
	result := s.probeCandidate(p, id)
	result.LatencyMs = time.Since(start).Milliseconds()
	return result
}

func (s *ArtifactStorageChannelService) probeCandidate(p SaveArtifactStorageParams, id uint) ArtifactStorageTestResult {
	switch p.Type {
	case string(model.ArtifactStorageLocal):
		return s.probeLocal()
	case string(model.ArtifactStorageS3):
	default:
		return ArtifactStorageTestResult{Message: ErrArtifactStorageInvalidType.Error(), ErrorCode: "INVALID_CONFIG"}
	}
	if _, err := validateArtifactStorageParams(&p); err != nil {
		return ArtifactStorageTestResult{Message: err.Error(), ErrorCode: "INVALID_CONFIG"}
	}
	ak, sk := p.AccessKey, p.SecretKey
	// 编辑态凭证留空：用存库密文解密后探测（不要求用户重填 SK）。
	if id > 0 && (strings.TrimSpace(ak) == "" || strings.TrimSpace(sk) == "") {
		saved, err := s.GetByID(id)
		if err != nil {
			return ArtifactStorageTestResult{Message: err.Error(), ErrorCode: "NOT_FOUND"}
		}
		savedAK, savedSK, derr := s.decryptCredentials(saved)
		if derr != nil {
			return ArtifactStorageTestResult{Message: derr.Error(), ErrorCode: "DECRYPT_FAILED"}
		}
		if strings.TrimSpace(ak) == "" {
			ak = savedAK
		}
		if strings.TrimSpace(sk) == "" {
			sk = savedSK
		}
	}
	store, err := blobstore.NewS3(blobstore.S3Config{
		Endpoint:  p.Endpoint,
		Bucket:    p.Bucket,
		Region:    artifactStorageRegion(p.Region),
		Prefix:    strings.Trim(strings.TrimSpace(p.Prefix), "/"),
		AccessKey: ak,
		SecretKey: sk,
		UseSSL:    p.UseSSL != nil && *p.UseSSL,
	})
	if err != nil {
		return ArtifactStorageTestResult{Message: err.Error(), ErrorCode: "INVALID_CONFIG"}
	}
	if err := probeArtifactStore(store); err != nil {
		return ArtifactStorageTestResult{Message: err.Error(), ErrorCode: "PROBE_FAILED"}
	}
	return ArtifactStorageTestResult{OK: true, Message: "连接正常"}
}

// probeLocal 本地渠道探测：数据根制品目录可写即通过。
func (s *ArtifactStorageChannelService) probeLocal() ArtifactStorageTestResult {
	if s.root == nil {
		return ArtifactStorageTestResult{Message: "数据根未配置", ErrorCode: "PROBE_FAILED"}
	}
	dir := s.root.ArtifactsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ArtifactStorageTestResult{Message: fmt.Sprintf("制品目录不可写: %v", err), ErrorCode: "PROBE_FAILED"}
	}
	f, err := os.CreateTemp(dir, ".jm-probe-*")
	if err != nil {
		return ArtifactStorageTestResult{Message: fmt.Sprintf("制品目录不可写: %v", err), ErrorCode: "PROBE_FAILED"}
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return ArtifactStorageTestResult{OK: true, Message: "本机存储可用"}
}

// probeArtifactStore s3 写探测：PutFile 8 字节 → Stat → Delete。探测键挂渠道 prefix 下的
// probe/ 子空间，不与 CAS 键冲突；验证的正是写路径（PUT）与元数据（HEAD）权限。
func probeArtifactStore(store blobstore.Store) error {
	ctx, cancel := context.WithTimeout(context.Background(), artifactStorageProbeTimeout)
	defer cancel()
	tmp, err := os.CreateTemp("", "jm-probe-*")
	if err != nil {
		return fmt.Errorf("创建探测临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	payload := []byte("jm-probe")
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入探测临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭探测临时文件失败: %w", err)
	}
	key := fmt.Sprintf("probe/jm-probe-%d", time.Now().UnixNano())
	if err := store.PutFile(ctx, key, tmpPath, int64(len(payload))); err != nil {
		return err
	}
	// 探测对象尽力清理：Stat 失败也尝试删除，不留垃圾。
	defer func() { _ = store.Delete(ctx, key) }()
	if _, err := store.Stat(ctx, key); err != nil {
		return err
	}
	return nil
}

// TestSaved 测试已保存渠道并持久化 LastTest*（沿备份域 TestSaved 先例）。
func (s *ArtifactStorageChannelService) TestSaved(id uint) (ArtifactStorageTestResult, error) {
	ch, err := s.GetByID(id)
	if err != nil {
		return ArtifactStorageTestResult{}, err
	}
	result := s.TestCandidate(SaveArtifactStorageParams{
		Name:     ch.Name,
		Type:     string(ch.Type),
		Endpoint: ch.Endpoint,
		Bucket:   ch.Bucket,
		Region:   ch.Region,
		Prefix:   ch.Prefix,
		UseSSL:   &ch.UseSSL,
	}, id)
	now := time.Now().UTC()
	if uerr := s.db.Model(&model.ArtifactStorageChannel{}).Where("id = ?", id).Updates(map[string]any{
		"last_test_at":      &now,
		"last_test_ok":      result.OK,
		"last_test_message": result.Message,
	}).Error; uerr != nil {
		return ArtifactStorageTestResult{}, uerr
	}
	return result, nil
}

// checkNameConflict 名称冲突预检；excludeID 排除自身（Create 传 0）。并发窗口由 DB 唯一索引兜底。
func (s *ArtifactStorageChannelService) checkNameConflict(name string, excludeID uint) error {
	var count int64
	if err := s.db.Model(&model.ArtifactStorageChannel{}).
		Where("name = ? AND id <> ?", strings.TrimSpace(name), excludeID).
		Count(&count).Error; err != nil {
		return fmt.Errorf("名称冲突预检失败: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("%w: %q", ErrArtifactStorageNameConflict, name)
	}
	return nil
}

// encryptCredentials 把明文凭证加密为落库密文（enc 已由调用方判非 nil）。
func (s *ArtifactStorageChannelService) encryptCredentials(ak, sk string) (akEnc, skEnc string, err error) {
	if strings.TrimSpace(ak) != "" {
		if akEnc, err = s.enc.Encrypt(ak); err != nil {
			return "", "", fmt.Errorf("加密 access key 失败: %w", err)
		}
	}
	if strings.TrimSpace(sk) != "" {
		if skEnc, err = s.enc.Encrypt(sk); err != nil {
			return "", "", fmt.Errorf("加密 secret key 失败: %w", err)
		}
	}
	return akEnc, skEnc, nil
}

// decryptCredentials 从渠道密文还原明文凭证。加密器未配置/密钥不符时报错（快失败，
// 运维恢复密钥文件即愈，见 spec §3.2 与 ADR-073 后果）。
func (s *ArtifactStorageChannelService) decryptCredentials(ch *model.ArtifactStorageChannel) (ak, sk string, err error) {
	if ch.AccessKeyEnc != "" {
		if ak, err = s.enc.Decrypt(ch.AccessKeyEnc); err != nil {
			return "", "", fmt.Errorf("解密渠道 access key 失败: %w", err)
		}
	}
	if ch.SecretKeyEnc != "" {
		if sk, err = s.enc.Decrypt(ch.SecretKeyEnc); err != nil {
			return "", "", fmt.Errorf("解密渠道 secret key 失败: %w", err)
		}
	}
	return ak, sk, nil
}

// validateArtifactStorageParams 校验 s3 渠道参数：name/endpoint/bucket 非空、TTL 在 [60,3600]
// （未填取默认 600）。返回落库 TTL 值。
func validateArtifactStorageParams(p *SaveArtifactStorageParams) (int, error) {
	if strings.TrimSpace(p.Name) == "" {
		return 0, fmt.Errorf("%w: 名称不能为空", ErrArtifactStorageInvalidConfig)
	}
	if strings.TrimSpace(p.Endpoint) == "" {
		return 0, fmt.Errorf("%w: endpoint 不能为空", ErrArtifactStorageInvalidConfig)
	}
	if strings.TrimSpace(p.Bucket) == "" {
		return 0, fmt.Errorf("%w: bucket 不能为空", ErrArtifactStorageInvalidConfig)
	}
	ttl := artifactPresignTTLDefault
	if p.PresignTTLSeconds != nil {
		ttl = *p.PresignTTLSeconds
		if ttl < artifactPresignTTLMin || ttl > artifactPresignTTLMax {
			return 0, fmt.Errorf("%w: 预签名有效期须在 %d~%d 秒之间", ErrArtifactStorageInvalidConfig, artifactPresignTTLMin, artifactPresignTTLMax)
		}
	}
	return ttl, nil
}

// artifactStorageRegion region 缺省 us-east-1（SigV4 必需）。
func artifactStorageRegion(region string) string {
	region = strings.TrimSpace(region)
	if region == "" {
		return "us-east-1"
	}
	return region
}

// clampPresignTTL 读路径 TTL 兜底钳制（存量脏数据防御；写路径已校验区间）。
func clampPresignTTL(ttl int) int {
	if ttl <= 0 {
		return artifactPresignTTLDefault
	}
	if ttl < artifactPresignTTLMin {
		return artifactPresignTTLMin
	}
	if ttl > artifactPresignTTLMax {
		return artifactPresignTTLMax
	}
	return ttl
}

// fillArtifactStorageFlags 填充列表标示位（永不回凭证明文/密文）。
func fillArtifactStorageFlags(ch *model.ArtifactStorageChannel) {
	ch.HasAccessKey = ch.AccessKeyEnc != ""
	ch.HasSecretKey = ch.SecretKeyEnc != ""
}
