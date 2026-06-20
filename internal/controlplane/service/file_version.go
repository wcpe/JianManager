package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// FileVersionConfig 控制通用文件版本（FR-051）的保留与快照策略。
type FileVersionConfig struct {
	// MaxPerFile 单文件保留的版本上限；超出时删除最旧的版本。<=0 表示不限制。
	MaxPerFile int
	// MaxSizeBytes 触发快照的单文件大小上限；超过则跳过快照（避免大文件撑爆 DB）。<=0 表示不限制。
	MaxSizeBytes int64
}

// DefaultFileVersionConfig 返回安全的默认保留策略。
func DefaultFileVersionConfig() FileVersionConfig {
	return FileVersionConfig{MaxPerFile: 20, MaxSizeBytes: 5 * 1024 * 1024}
}

// FileVersionService 通用文件版本服务（FR-051，Control Plane 侧）。
//
// 设计上复用 FR-031 配置版本的「改前快照 + 列表 + diff + 回滚」机制，
// 但作用于实例工作目录下的任意文件；文件本体归 Worker，版本事实源在 CP DB。
// 写入前先经 gRPC 读旧内容落库为快照，再由 FileService 执行真正写入。
type FileVersionService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool
	cfg  FileVersionConfig
}

// NewFileVersionService 创建通用文件版本服务。
func NewFileVersionService(db *gorm.DB, pool *cpgrpc.ClientPool, cfg FileVersionConfig) *FileVersionService {
	if cfg.MaxPerFile == 0 && cfg.MaxSizeBytes == 0 {
		cfg = DefaultFileVersionConfig()
	}
	return &FileVersionService{db: db, pool: pool, cfg: cfg}
}

// FileVersion 文件版本列表项（不含内容，仅元数据）。
type FileVersion struct {
	ID                  uint      `json:"id"`
	FilePath            string    `json:"filePath"`
	Size                int64     `json:"size"`
	AuthorID            uint      `json:"authorId"`
	RollbackOfVersionID *uint     `json:"rollbackOfVersionId,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
}

// FileVersionDiff 两版本（或版本 vs 当前文件）之间的差异。
// 二进制内容无法生成文本 diff，Binary=true 时 UnifiedDiff 为空。
type FileVersionDiff struct {
	FromVersionID uint   `json:"fromVersionId"`
	ToVersionID   uint   `json:"toVersionId"`
	UnifiedDiff   string `json:"unifiedDiff"`
	Binary        bool   `json:"binary"`
}

// SnapshotBeforeWrite 在写入/上传覆盖文件前，对「已存在」的目标文件做改前快照（FR-051 验收 1）。
//
// 经 gRPC 读旧内容：读不到（文件不存在/新建）则跳过，不视为错误；
// 读到则将旧内容落库为一个版本。超过 MaxSizeBytes 的文件跳过快照。
// 该方法只负责快照，真正的写入仍由调用方（FileService）执行，从而无需新增 proto。
func (s *FileVersionService) SnapshotBeforeWrite(instanceID uint, filePath string, authorID uint) error {
	inst, client, err := s.client(instanceID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.Worker.ReadFile(ctx, &workerpb.ReadFileRequest{InstanceUuid: inst.UUID, Path: filePath})
	if err != nil {
		// 文件不存在（新建写入）等读取失败不阻塞写入：无旧内容即无需快照。
		return nil
	}
	if s.cfg.MaxSizeBytes > 0 && int64(len(resp.Content)) > s.cfg.MaxSizeBytes {
		return nil
	}
	_, err = s.saveVersion(instanceID, filePath, resp.Content, authorID, nil)
	return err
}

// Rollback 回滚到指定版本：先对当前文件做改前快照，再经 gRPC 把旧内容写回（FR-051 验收 3）。
// 返回写回后新生成的版本 ID。
func (s *FileVersionService) Rollback(instanceID uint, filePath string, versionID uint, authorID uint) (uint, error) {
	var ver model.FileVersion
	if err := s.db.Where("id = ? AND instance_id = ? AND file_path = ?", versionID, instanceID, filePath).First(&ver).Error; err != nil {
		return 0, fmt.Errorf("版本 #%d 不存在: %w", versionID, err)
	}
	content, err := decodeContent(ver.Content)
	if err != nil {
		return 0, err
	}

	inst, client, err := s.client(instanceID)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 回滚前先快照当前内容，使回滚本身也可被再次回滚（与 FR-031 行为一致）。
	if cur, rerr := client.Worker.ReadFile(ctx, &workerpb.ReadFileRequest{InstanceUuid: inst.UUID, Path: filePath}); rerr == nil {
		if _, serr := s.saveVersion(instanceID, filePath, cur.Content, authorID, nil); serr != nil {
			return 0, serr
		}
	}

	resp, err := client.Worker.WriteFile(ctx, &workerpb.WriteFileRequest{InstanceUuid: inst.UUID, Path: filePath, Content: content})
	if err != nil {
		return 0, fmt.Errorf("回滚写入失败: %w", err)
	}
	if !resp.Success {
		return 0, fmt.Errorf("回滚写入失败: %s", resp.Error)
	}

	src := ver.ID
	return s.saveVersion(instanceID, filePath, content, authorID, &src)
}

// Versions 返回某文件的版本列表（按 ID 倒序，最新在前）。
func (s *FileVersionService) Versions(instanceID uint, filePath string) ([]FileVersion, error) {
	var rows []model.FileVersion
	if err := s.db.Where("instance_id = ? AND file_path = ?", instanceID, filePath).
		Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]FileVersion, 0, len(rows))
	for _, r := range rows {
		out = append(out, FileVersion{
			ID:                  r.ID,
			FilePath:            r.FilePath,
			Size:                r.Size,
			AuthorID:            r.AuthorID,
			RollbackOfVersionID: r.RollbackOfVersionID,
			CreatedAt:           r.CreatedAt,
		})
	}
	return out, nil
}

// Diff 返回 fromID 与 toID 之间的 unified diff（复用配置版本的 unifiedDiff 实现）。
// toID=0 表示与当前文件内容比较（经 gRPC 读取）。任一侧为二进制时返回 Binary=true。
func (s *FileVersionService) Diff(instanceID uint, filePath string, fromID, toID uint) (*FileVersionDiff, error) {
	var fromVer model.FileVersion
	if err := s.db.Where("id = ? AND instance_id = ? AND file_path = ?", fromID, instanceID, filePath).First(&fromVer).Error; err != nil {
		return nil, fmt.Errorf("源版本 #%d 不存在: %w", fromID, err)
	}
	fromBytes, err := decodeContent(fromVer.Content)
	if err != nil {
		return nil, err
	}

	var toBytes []byte
	if toID == 0 {
		inst, client, cerr := s.client(instanceID)
		if cerr != nil {
			return nil, cerr
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, rerr := client.Worker.ReadFile(ctx, &workerpb.ReadFileRequest{InstanceUuid: inst.UUID, Path: filePath})
		if rerr != nil {
			return nil, fmt.Errorf("读取当前文件失败: %w", rerr)
		}
		toBytes = resp.Content
	} else {
		var toVer model.FileVersion
		if err := s.db.Where("id = ? AND instance_id = ? AND file_path = ?", toID, instanceID, filePath).First(&toVer).Error; err != nil {
			return nil, fmt.Errorf("目标版本 #%d 不存在: %w", toID, err)
		}
		toBytes, err = decodeContent(toVer.Content)
		if err != nil {
			return nil, err
		}
	}

	if !utf8.Valid(fromBytes) || !utf8.Valid(toBytes) {
		return &FileVersionDiff{FromVersionID: fromID, ToVersionID: toID, Binary: true}, nil
	}
	unified, err := unifiedDiff(filePath, string(fromBytes), string(toBytes), fmt.Sprintf("v%d", fromID), fmt.Sprintf("v%d", toID))
	if err != nil {
		return nil, err
	}
	return &FileVersionDiff{FromVersionID: fromID, ToVersionID: toID, UnifiedDiff: unified}, nil
}

// saveVersion 落库一条版本并按保留策略裁剪旧版本。content 为原始字节，存库前 base64 编码。
// rollbackOfVersionID 非 nil 时标记本次写入由回滚触发。
func (s *FileVersionService) saveVersion(instanceID uint, filePath string, content []byte, authorID uint, rollbackOfVersionID *uint) (uint, error) {
	h := sha256.Sum256(content)
	ver := model.FileVersion{
		InstanceID:          instanceID,
		FilePath:            filePath,
		ContentHash:         hex.EncodeToString(h[:]),
		Content:             base64.StdEncoding.EncodeToString(content),
		Size:                int64(len(content)),
		AuthorID:            authorID,
		RollbackOfVersionID: rollbackOfVersionID,
	}
	if err := s.db.Create(&ver).Error; err != nil {
		return 0, fmt.Errorf("保存文件版本失败: %w", err)
	}
	if err := s.prune(instanceID, filePath); err != nil {
		return 0, err
	}
	return ver.ID, nil
}

// prune 按 MaxPerFile 删除单文件超出上限的最旧版本（FR-051 验收 4）。
func (s *FileVersionService) prune(instanceID uint, filePath string) error {
	if s.cfg.MaxPerFile <= 0 {
		return nil
	}
	var ids []uint
	if err := s.db.Model(&model.FileVersion{}).
		Where("instance_id = ? AND file_path = ?", instanceID, filePath).
		Order("id DESC").
		Offset(s.cfg.MaxPerFile).
		Pluck("id", &ids).Error; err != nil {
		return fmt.Errorf("查询待裁剪版本失败: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	if err := s.db.Where("id IN ?", ids).Delete(&model.FileVersion{}).Error; err != nil {
		return fmt.Errorf("裁剪旧版本失败: %w", err)
	}
	return nil
}

func (s *FileVersionService) client(instanceID uint) (*model.Instance, *cpgrpc.Client, error) {
	var inst model.Instance
	if err := s.db.Preload("Node").First(&inst, instanceID).Error; err != nil {
		return nil, nil, err
	}
	if inst.WorkDir == "" {
		return nil, nil, ErrWorkDirNotSet
	}
	client, ok := s.pool.Get(inst.Node.UUID)
	if !ok {
		return nil, nil, ErrNodeNotConnected
	}
	return &inst, client, nil
}

// decodeContent 将存库的 base64 内容还原为原始字节。
func decodeContent(stored string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return nil, fmt.Errorf("解码版本内容失败: %w", err)
	}
	return b, nil
}
