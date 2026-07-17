package service

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var (
	// ErrUploadPrecheckInvalid 秒传预查参数非法（空列表 / sha256 非 64 hex / size 为负）。
	ErrUploadPrecheckInvalid = errors.New("秒传预查参数非法")
	// ErrBatchInvalid 聚合上传参数非法（meta 空 / sha256 非法 / size 为负）。
	ErrBatchInvalid = errors.New("聚合上传参数非法")
	// ErrBatchLimitExceeded 秒传预查/聚合上传超出单次上限（防滥用护栏）。
	ErrBatchLimitExceeded = errors.New("超出单次上限")
)

// 上传增效协议上限（FR-346，spec §3）。
const (
	// precheckMaxEntries 单次预查最多查询的 hash 数：单条 IN 查询与响应体量护栏。
	precheckMaxEntries = 500
	// batchMaxFiles 单批聚合上传最多文件数：单请求 DB/审计开销可控。
	batchMaxFiles = 200
	// batchFileMaxBytes 聚合上传单文件上限 = FR-251 defaultChunkSize（8 MiB）：
	// 聚合阈值与分块协议界线对齐——超过者走分块，不重蹈单请求过大短板。
	batchFileMaxBytes = defaultChunkSize
	// batchMaxTotalBytes 单批总字节上限（32 MiB）：单请求体量护栏，低于常见反代上限。
	batchMaxTotalBytes int64 = 32 << 20
)

// ClientUploadEfficiencyService 客户端分发上传增效服务（FR-346，增强 FR-250/251）。
//
// 秒传预查：前端上传前算好各文件**原始内容** sha256，批量查 CAS——发布链路恒 codec=none，
// AssetService.Ingest 对上传字节原样散列，故 assets(type='client-file').sha256 即原始内容
// hash，预查直接以之命中（spec §2 hash 对齐语义）。命中者跳过字节上传，直接得到与真上传
// 同构的 ClientFileResult。历史 codec≠none 制品的 sha256 为压缩态字节 hash，原始内容 hash
// 查询天然不命中 → 恒走真上传，无错误引用风险。
//
// 小文件聚合：一个 multipart 请求携带 ≤200 个 ≤8 MiB 小文件（总 ≤32 MiB），逐个经
// ClientVersionService.PublishFile（Ingest 按声明 sha256 强校验 + 去重）落同一 CAS，
// 每项返回与单次/分块上传逐字段一致。加性协议，不推翻既有决策（无新 ADR）。
type ClientUploadEfficiencyService struct {
	db       *gorm.DB
	versions *ClientVersionService
}

// NewClientUploadEfficiencyService 创建上传增效服务。versions 复用其 PublishFile 落 CAS。
func NewClientUploadEfficiencyService(db *gorm.DB, versions *ClientVersionService) *ClientUploadEfficiencyService {
	return &ClientUploadEfficiencyService{db: db, versions: versions}
}

// PrecheckEntry 秒传预查单项：前端算好的原始内容 sha256 + 文件字节数。
type PrecheckEntry struct {
	// SHA256 文件原始内容 sha256（64 hex，大小写不敏感）。
	SHA256 string `json:"sha256"`
	// Size 文件字节数（≥0；命中要求与制品 size 精确相等，防前端 hash 算错文件）。
	Size int64 `json:"size"`
}

// PrecheckResult 秒传预查单项结果（与请求顺序对齐）。
type PrecheckResult struct {
	// SHA256 归一小写后的查询键（回显）。
	SHA256 string `json:"sha256"`
	// Hit 是否命中制品库（命中者免字节上传）。
	Hit bool `json:"hit"`
	// Result 命中时的制品元数据，与真上传返回的 ClientFileResult 同构；未命中省略。
	Result *ClientFileResult `json:"result,omitempty"`
}

// Precheck 批量秒传预查：按 (type='client-file', sha256) 查 CAS，命中且 size 相等者返回
// 与真上传同构的结果并 bump last_used_at（与 Ingest 去重路径一致）。结果与入参顺序对齐。
//
// 命中结果的 Codec 恒 "none"：发布链路恒 codec=none（spec §2），不回读既有资产 metadata。
// 参数非法返回 ErrUploadPrecheckInvalid；超上限返回 ErrBatchLimitExceeded。
func (s *ClientUploadEfficiencyService) Precheck(entries []PrecheckEntry) ([]PrecheckResult, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: files 为空", ErrUploadPrecheckInvalid)
	}
	if len(entries) > precheckMaxEntries {
		return nil, fmt.Errorf("%w: 单次预查最多 %d 项", ErrBatchLimitExceeded, precheckMaxEntries)
	}
	norm := make([]string, len(entries))
	uniq := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for i, e := range entries {
		h, ok := normalizeSHA256Hex(e.SHA256)
		if !ok {
			return nil, fmt.Errorf("%w: 第 %d 项 sha256 非法", ErrUploadPrecheckInvalid, i)
		}
		if e.Size < 0 {
			return nil, fmt.Errorf("%w: 第 %d 项 size 为负", ErrUploadPrecheckInvalid, i)
		}
		norm[i] = h
		if _, dup := seen[h]; !dup {
			seen[h] = struct{}{}
			uniq = append(uniq, h)
		}
	}

	var assets []model.Asset
	if err := s.db.Where("type = ? AND sha256 IN ?", model.AssetTypeClientFile, uniq).Find(&assets).Error; err != nil {
		return nil, fmt.Errorf("查询制品失败: %w", err)
	}
	bySha := make(map[string]*model.Asset, len(assets))
	for i := range assets {
		bySha[assets[i].SHA256] = &assets[i]
	}

	results := make([]PrecheckResult, len(entries))
	hitIDs := make([]uint, 0, len(entries))
	hitSeen := make(map[uint]struct{}, len(entries))
	for i := range entries {
		a := bySha[norm[i]]
		if a != nil && a.Size == entries[i].Size {
			results[i] = PrecheckResult{
				SHA256: norm[i],
				Hit:    true,
				Result: &ClientFileResult{SHA256: a.SHA256, MD5: a.MD5, Size: a.Size, Codec: "none"},
			}
			if _, dup := hitSeen[a.ID]; !dup {
				hitSeen[a.ID] = struct{}{}
				hitIDs = append(hitIDs, a.ID)
			}
			continue
		}
		results[i] = PrecheckResult{SHA256: norm[i], Hit: false}
	}
	// 命中即将被发布引用：bump last_used_at（一条 UPDATE），与真上传去重路径语义一致。
	if len(hitIDs) > 0 {
		now := time.Now()
		if err := s.db.Model(&model.Asset{}).Where("id IN ?", hitIDs).Update("last_used_at", &now).Error; err != nil {
			return nil, fmt.Errorf("更新制品使用时间失败: %w", err)
		}
	}
	return results, nil
}

// BatchFileMeta 聚合上传单文件声明（multipart 首 part `meta` JSON 数组元素，与 files part 同序）。
type BatchFileMeta struct {
	// Filename 原始文件名（决定 CAS 扩展名/下载名），可空。
	Filename string `json:"filename"`
	// Size 声明字节数（0..batchFileMaxBytes；实际字节以声明 sha256 强校验兜底）。
	Size int64 `json:"size"`
	// SHA256 声明的原始内容 sha256（必填 64 hex）：聚合路径前端必已算 hash，经 Ingest 强校验。
	SHA256 string `json:"sha256"`
}

// ValidateBatchMetas 校验聚合上传声明：数量/单文件/总字节上限 + sha256 格式。
// 在读取任何文件字节前调用（护栏前置）。非法返回 ErrBatchInvalid / ErrBatchLimitExceeded。
func (s *ClientUploadEfficiencyService) ValidateBatchMetas(metas []BatchFileMeta) error {
	if len(metas) == 0 {
		return fmt.Errorf("%w: meta 为空", ErrBatchInvalid)
	}
	if len(metas) > batchMaxFiles {
		return fmt.Errorf("%w: 单批最多 %d 个文件", ErrBatchLimitExceeded, batchMaxFiles)
	}
	var total int64
	for i, m := range metas {
		if _, ok := normalizeSHA256Hex(m.SHA256); !ok {
			return fmt.Errorf("%w: 第 %d 项 sha256 非法", ErrBatchInvalid, i)
		}
		if m.Size < 0 {
			return fmt.Errorf("%w: 第 %d 项 size 为负", ErrBatchInvalid, i)
		}
		if m.Size > batchFileMaxBytes {
			return fmt.Errorf("%w: 第 %d 项 %d 字节超过单文件上限 %d", ErrBatchLimitExceeded, i, m.Size, batchFileMaxBytes)
		}
		total += m.Size
	}
	if total > batchMaxTotalBytes {
		return fmt.Errorf("%w: 总字节 %d 超过单批上限 %d", ErrBatchLimitExceeded, total, batchMaxTotalBytes)
	}
	return nil
}

// IngestBatchFile 聚合上传单文件入库：LimitReader(size+1) 防超量写盘 → 复用 PublishFile
//（codec=none + 声明 sha256 强校验 + CAS 去重）。字节与声明不符（含长度不符）返回
// ErrChecksumMismatch——长度不同必然 hash 不同，统一归为校验和不符。
func (s *ClientUploadEfficiencyService) IngestBatchFile(meta BatchFileMeta, r io.Reader) (*ClientFileResult, error) {
	return s.versions.PublishFile(io.LimitReader(r, meta.Size+1), PublishFileParams{
		Filename:       meta.Filename,
		Codec:          "none",
		ExpectedSHA256: meta.SHA256,
	})
}

// normalizeSHA256Hex 归一 sha256 为小写并校验 64 位 hex；非法返回 ok=false。
func normalizeSHA256Hex(s string) (string, bool) {
	h := strings.ToLower(strings.TrimSpace(s))
	if len(h) != 64 {
		return "", false
	}
	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	return h, true
}
