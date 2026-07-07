package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
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
	db   *gorm.DB
	pool *cpgrpc.ClientPool
	root *dataroot.Root
}

// StorageStats 是备份存储容量聚合结果。
type StorageStats struct {
	BackupCount int64 `json:"backupCount"`
	UsedBytes   int64 `json:"usedBytes"`
}

// StorageTestResult 是后端连通性测试结果。
type StorageTestResult struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
	ErrorCode string `json:"errorCode,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
	NodeUUID  string `json:"nodeUuid,omitempty"`
}

// NewBackupStorageService 创建备份存储服务。
func NewBackupStorageService(db *gorm.DB, pools ...*cpgrpc.ClientPool) *BackupStorageService {
	var pool *cpgrpc.ClientPool
	if len(pools) > 0 {
		pool = pools[0]
	}
	return &BackupStorageService{db: db, pool: pool}
}

// SetClientPool 注入 Worker 连接池，供连通性测试复用。
func (s *BackupStorageService) SetClientPool(pool *cpgrpc.ClientPool) {
	s.pool = pool
}

// SetDataRoot 注入 CP 数据根，用于本地备份容量扫描。
func (s *BackupStorageService) SetDataRoot(root *dataroot.Root) {
	s.root = root
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

// ListWithStats 列出所有远程存储后端，并附带备份份数与已用空间。
func (s *BackupStorageService) ListWithStats() ([]model.BackupStorage, error) {
	out, err := s.List()
	if err != nil {
		return nil, err
	}
	if err := s.fillStats(out); err != nil {
		return nil, err
	}
	return out, nil
}

// Stats 查询单个远程存储后端的备份份数与已用空间。
func (s *BackupStorageService) Stats(id uint) (*StorageStats, error) {
	if _, err := s.GetByID(id); err != nil {
		return nil, err
	}
	stats, err := s.statsByID(id)
	if err != nil {
		return nil, err
	}
	return &stats, nil
}

// LocalStats 查询节点本地备份份数，并从 CP 数据根 var/backups 扫描本地占用。
func (s *BackupStorageService) LocalStats() (*StorageStats, error) {
	var count int64
	if err := s.db.Model(&model.Backup{}).
		Where("storage_id IS NULL AND status = ?", model.BackupStatusCompleted).
		Count(&count).Error; err != nil {
		return nil, err
	}
	usedBytes, err := s.scanLocalBackupBytes()
	if err != nil {
		return nil, err
	}
	return &StorageStats{BackupCount: count, UsedBytes: usedBytes}, nil
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

// TestConnection 通过在线 Worker 对远程存储后端做一次非破坏性读写探测。
func (s *BackupStorageService) TestConnection(id uint) (*StorageTestResult, error) {
	spec, err := s.ResolveSpec(id)
	if err != nil {
		if errors.Is(err, ErrCredentialEnvMissing) {
			return failedStorageTest("CREDENTIAL_ENV_MISSING", err.Error(), 0, ""), nil
		}
		return nil, err
	}
	client, nodeUUID := s.firstWorkerClient()
	if client == nil || client.Worker == nil {
		return failedStorageTest("NO_WORKER", "没有在线 Worker 可执行存储探测", 0, ""), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	started := time.Now()
	resp, err := client.Worker.TestStorageBackend(ctx, &workerpb.TestStorageBackendRequest{Storage: spec})
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return failedStorageTest("WORKER_RPC_FAILED", err.Error(), latency, nodeUUID), nil
	}
	if resp.GetSuccess() {
		latency = preferLatency(resp.GetLatencyMs(), latency)
		return &StorageTestResult{OK: true, Message: "连接成功", LatencyMs: latency, NodeUUID: nodeUUID}, nil
	}
	latency = preferLatency(resp.GetLatencyMs(), latency)
	code := resp.GetErrorCode()
	if code == "" {
		code = "PROBE_FAILED"
	}
	return failedStorageTest(code, resp.GetError(), latency, nodeUUID), nil
}

func (s *BackupStorageService) firstWorkerClient() (*cpgrpc.Client, string) {
	if s.pool == nil {
		return nil, ""
	}
	nodes := s.pool.ConnectedNodes()
	if len(nodes) == 0 {
		return nil, ""
	}
	sort.Strings(nodes)
	client, ok := s.pool.Get(nodes[0])
	if !ok {
		return nil, ""
	}
	return client, nodes[0]
}

func failedStorageTest(code, msg string, latency int64, nodeUUID string) *StorageTestResult {
	if msg == "" {
		msg = "连接测试失败"
	}
	return &StorageTestResult{OK: false, Message: msg, ErrorCode: code, LatencyMs: latency, NodeUUID: nodeUUID}
}

func preferLatency(workerLatency, fallback int64) int64 {
	if workerLatency > 0 {
		return workerLatency
	}
	return fallback
}

func (s *BackupStorageService) fillStats(storages []model.BackupStorage) error {
	if len(storages) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(storages))
	for i := range storages {
		ids = append(ids, storages[i].ID)
	}
	rows, err := s.statsByIDs(ids)
	if err != nil {
		return err
	}
	for i := range storages {
		stats := rows[storages[i].ID]
		storages[i].BackupCount = stats.BackupCount
		storages[i].UsedBytes = stats.UsedBytes
	}
	return nil
}

func (s *BackupStorageService) statsByID(id uint) (StorageStats, error) {
	rows, err := s.statsByIDs([]uint{id})
	if err != nil {
		return StorageStats{}, err
	}
	return rows[id], nil
}

func (s *BackupStorageService) statsByIDs(ids []uint) (map[uint]StorageStats, error) {
	type statRow struct {
		StorageID   uint
		BackupCount int64
		UsedMB      float64
	}
	var rows []statRow
	err := s.db.Model(&model.Backup{}).
		Select("storage_id, COUNT(*) AS backup_count, COALESCE(SUM(file_size_mb), 0) AS used_mb").
		Where("storage_id IN ? AND status = ?", ids, model.BackupStatusCompleted).
		Group("storage_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uint]StorageStats, len(rows))
	for _, row := range rows {
		out[row.StorageID] = StorageStats{BackupCount: row.BackupCount, UsedBytes: int64(row.UsedMB * 1024 * 1024)}
	}
	return out, nil
}

func (s *BackupStorageService) scanLocalBackupBytes() (int64, error) {
	if s.root == nil {
		return s.localBackupBytesFromDB()
	}
	base := s.root.Abs("var/backups")
	var total int64
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}

func (s *BackupStorageService) localBackupBytesFromDB() (int64, error) {
	var row struct{ UsedMB float64 }
	err := s.db.Model(&model.Backup{}).
		Select("COALESCE(SUM(file_size_mb), 0) AS used_mb").
		Where("storage_id IS NULL AND status = ?", model.BackupStatusCompleted).
		Scan(&row).Error
	if err != nil {
		return 0, err
	}
	return int64(row.UsedMB * 1024 * 1024), nil
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
