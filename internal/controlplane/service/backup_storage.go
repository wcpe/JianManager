package service

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

var (
	// ErrStorageNotFound 存储后端不存在。
	ErrStorageNotFound = errors.New("存储后端不存在")
	// ErrInvalidStorageType 非法的存储后端类型。
	ErrInvalidStorageType = errors.New("非法的存储后端类型")
	// ErrStorageInUse 存储后端被备份引用，禁止删除。
	ErrStorageInUse = errors.New("存储后端被备份引用，无法删除")
	// ErrCredentialEnvMissing 凭证引用的环境变量未设置。
	ErrCredentialEnvMissing = errors.New("凭证环境变量未设置")
	// ErrCredentialNotEnvRef 凭证未以 ${ENV_VAR} 形式引用（禁止硬编码明文）。
	ErrCredentialNotEnvRef = errors.New("凭证必须以 ${ENV_VAR} 形式引用环境变量")
)

// envRefPattern 匹配整串恰为一个 ${VAR} 引用（VAR 为字母/数字/下划线）。
var envRefPattern = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// BackupStorageService 备份远程存储后端服务（FR-057）。
// 负责后端 CRUD 与「把存储配置 + 从 ${ENV_VAR} 解析的明文凭证」组装为下发 Worker 的传输参数。
type BackupStorageService struct {
	db *gorm.DB
}

// NewBackupStorageService 创建备份存储服务。
func NewBackupStorageService(db *gorm.DB) *BackupStorageService {
	return &BackupStorageService{db: db}
}

// Create 创建远程存储后端。校验类型合法且凭证字段为 ${ENV_VAR} 引用（非空时）。
func (s *BackupStorageService) Create(st *model.BackupStorage) (*model.BackupStorage, error) {
	if !model.ValidBackupStorageType(st.Type) {
		return nil, ErrInvalidStorageType
	}
	if err := validateCredentialRefs(st); err != nil {
		return nil, err
	}
	if st.Region == "" && st.Type == model.BackupStorageS3 {
		st.Region = "us-east-1"
	}
	if err := s.db.Create(st).Error; err != nil {
		return nil, fmt.Errorf("创建存储后端失败: %w", err)
	}
	return st, nil
}

// List 列出所有远程存储后端。
func (s *BackupStorageService) List() ([]model.BackupStorage, error) {
	var out []model.BackupStorage
	if err := s.db.Order("id desc").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("查询存储后端失败: %w", err)
	}
	return out, nil
}

// GetByID 按 ID 获取存储后端。
func (s *BackupStorageService) GetByID(id uint) (*model.BackupStorage, error) {
	var st model.BackupStorage
	if err := s.db.First(&st, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrStorageNotFound
		}
		return nil, fmt.Errorf("查询存储后端失败: %w", err)
	}
	return &st, nil
}

// Delete 删除存储后端；被备份引用时拒绝（保护远程恢复链路）。
func (s *BackupStorageService) Delete(id uint) error {
	if _, err := s.GetByID(id); err != nil {
		return err
	}
	var refs int64
	if err := s.db.Model(&model.Backup{}).Where("storage_id = ?", id).Count(&refs).Error; err != nil {
		return err
	}
	if refs > 0 {
		return fmt.Errorf("%w: 当前被 %d 个备份引用", ErrStorageInUse, refs)
	}
	return s.db.Delete(&model.BackupStorage{}, id).Error
}

// ResolveSpec 把存储后端 ID 解析为下发 Worker 的传输参数，凭证从 ${ENV_VAR} 取明文。
// 供 BackupService 在创建/恢复时调用。后端不存在或环境变量缺失则报错。
func (s *BackupStorageService) ResolveSpec(id uint) (*workerpb.StorageBackendSpec, error) {
	st, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	ak, err := resolveEnvRef(st.AccessKeyEnv)
	if err != nil {
		return nil, fmt.Errorf("解析 access key: %w", err)
	}
	sk, err := resolveEnvRef(st.SecretKeyEnv)
	if err != nil {
		return nil, fmt.Errorf("解析 secret key: %w", err)
	}
	return &workerpb.StorageBackendSpec{
		Type:      string(st.Type),
		Endpoint:  st.Endpoint,
		Bucket:    st.Bucket,
		Region:    st.Region,
		Prefix:    st.Prefix,
		AccessKey: ak,
		SecretKey: sk,
		UseSsl:    st.UseSSL,
	}, nil
}

// validateCredentialRefs 校验凭证字段为空或为 ${ENV_VAR} 引用，拒绝明文硬编码（config-files.md）。
func validateCredentialRefs(st *model.BackupStorage) error {
	for _, ref := range []string{st.AccessKeyEnv, st.SecretKeyEnv} {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if !envRefPattern.MatchString(ref) {
			return fmt.Errorf("%w: %q", ErrCredentialNotEnvRef, ref)
		}
	}
	return nil
}

// resolveEnvRef 解析 ${ENV_VAR} 引用为环境变量值。空引用返回空串（如匿名 WebDAV）。
// 非 ${...} 形式或变量未设置则报错，杜绝明文与静默空凭证。
func resolveEnvRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	m := envRefPattern.FindStringSubmatch(ref)
	if m == nil {
		return "", fmt.Errorf("%w: %q", ErrCredentialNotEnvRef, ref)
	}
	val, ok := os.LookupEnv(m[1])
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrCredentialEnvMissing, m[1])
	}
	return val, nil
}
