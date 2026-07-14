package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
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
	// ErrStorageTypeImmutable 存储后端类型不可修改（改型=删重建，FR-338）。
	ErrStorageTypeImmutable = errors.New("存储后端类型不可修改")
	// ErrStorageNameConflict 存储后端名称已存在（FR-338）。
	ErrStorageNameConflict = errors.New("存储后端名称已存在")
	// ErrCredentialEnvMissing 凭证引用的环境变量未设置。
	ErrCredentialEnvMissing = errors.New("凭证环境变量未设置")
	// ErrCredentialNotEnvRef 凭证未以 ${ENV_VAR} 形式引用（禁止硬编码明文）。
	ErrCredentialNotEnvRef = errors.New("凭证必须以 ${ENV_VAR} 形式引用环境变量")
)

const backupStorageProbeTimeout = 2 * time.Second

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

// Create 创建远程存储后端。校验类型合法且凭证字段为 ${ENV_VAR} 引用（非空时）；
// 名称冲突预检回 422 语义错误（FR-338 对称收口，替代裸撞 DB 唯一索引的 500）。
func (s *BackupStorageService) Create(st *model.BackupStorage) (*model.BackupStorage, error) {
	if !model.ValidBackupStorageType(st.Type) {
		return nil, ErrInvalidStorageType
	}
	if err := validateCredentialRefs(st); err != nil {
		return nil, err
	}
	if err := s.checkNameConflict(st.Name, 0); err != nil {
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

// Update 全量更新存储后端配置（FR-338）。类型不可改（改型=删重建）；校验与 Create 同源
// （仅静态校验，不在保存时强制探活——连通性由「测试连接」显式做）；成功后清空 lastTest*
// （配置已变，旧测试结论失效）。被备份引用的后端允许编辑（换密钥/endpoint 为合法运维场景，不加引用锁）。
func (s *BackupStorageService) Update(id uint, st model.BackupStorage) (*model.BackupStorage, error) {
	cur, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	if st.Type != cur.Type {
		return nil, fmt.Errorf("%w: %s → %s", ErrStorageTypeImmutable, cur.Type, st.Type)
	}
	if !model.ValidBackupStorageType(st.Type) {
		return nil, ErrInvalidStorageType
	}
	if err := validateCredentialRefs(&st); err != nil {
		return nil, err
	}
	if err := s.checkNameConflict(st.Name, id); err != nil {
		return nil, err
	}
	if st.Region == "" && st.Type == model.BackupStorageS3 {
		st.Region = "us-east-1"
	}
	// Updates(map) 显式写零值（false / 空串 / NULL），避开 GORM struct 更新的零值跳过。
	if err := s.db.Model(&model.BackupStorage{}).Where("id = ?", id).Updates(map[string]any{
		"name":              st.Name,
		"endpoint":          st.Endpoint,
		"bucket":            st.Bucket,
		"region":            st.Region,
		"prefix":            st.Prefix,
		"access_key_env":    st.AccessKeyEnv,
		"secret_key_env":    st.SecretKeyEnv,
		"use_ssl":           st.UseSSL,
		"last_test_at":      nil,
		"last_test_ok":      false,
		"last_test_message": "",
	}).Error; err != nil {
		return nil, fmt.Errorf("更新存储后端失败: %w", err)
	}
	return s.GetByID(id)
}

// checkNameConflict 名称冲突预检（FR-338）：与其他后端同名回 ErrStorageNameConflict；
// excludeID 排除自身（Create 传 0）。并发窗口由 DB uniqueIndex 兜底。
func (s *BackupStorageService) checkNameConflict(name string, excludeID uint) error {
	var count int64
	if err := s.db.Model(&model.BackupStorage{}).
		Where("name = ? AND id <> ?", name, excludeID).
		Count(&count).Error; err != nil {
		return fmt.Errorf("名称冲突预检失败: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("%w: %q", ErrStorageNameConflict, name)
	}
	return nil
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

type BackupStorageTestResult struct {
	Ok        bool   `json:"ok"`
	Message   string `json:"message"`
	LatencyMs int64  `json:"latencyMs"`
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

// TestCandidate 校验一个未保存的存储后端配置；不写入数据库。
func (s *BackupStorageService) TestCandidate(st model.BackupStorage) BackupStorageTestResult {
	start := time.Now()
	result := s.validateCandidate(st)
	result.LatencyMs = time.Since(start).Milliseconds()
	return result
}

func (s *BackupStorageService) validateCandidate(st model.BackupStorage) BackupStorageTestResult {
	if !model.ValidBackupStorageType(st.Type) {
		return BackupStorageTestResult{Ok: false, Message: ErrInvalidStorageType.Error()}
	}
	if err := validateCredentialRefs(&st); err != nil {
		return BackupStorageTestResult{Ok: false, Message: err.Error()}
	}
	ak, err := resolveEnvRef(st.AccessKeyEnv)
	if err != nil {
		return BackupStorageTestResult{Ok: false, Message: err.Error()}
	}
	sk, err := resolveEnvRef(st.SecretKeyEnv)
	if err != nil {
		return BackupStorageTestResult{Ok: false, Message: err.Error()}
	}
	if err := probeBackupStorage(st, ak, sk); err != nil {
		return BackupStorageTestResult{Ok: false, Message: err.Error()}
	}
	return BackupStorageTestResult{Ok: true, Message: "连接正常"}
}

// TestSaved 测试已保存的存储后端，并持久化最近一次测试结果。
func (s *BackupStorageService) TestSaved(id uint) (BackupStorageTestResult, error) {
	st, err := s.GetByID(id)
	if err != nil {
		return BackupStorageTestResult{}, err
	}
	result := s.TestCandidate(*st)
	now := time.Now().UTC()
	if err := s.db.Model(&model.BackupStorage{}).Where("id = ?", id).Updates(map[string]any{
		"last_test_at":      &now,
		"last_test_ok":      result.Ok,
		"last_test_message": result.Message,
	}).Error; err != nil {
		return BackupStorageTestResult{}, err
	}
	return result, nil
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

func probeBackupStorage(st model.BackupStorage, accessKey, secretKey string) error {
	ctx, cancel := context.WithTimeout(context.Background(), backupStorageProbeTimeout)
	defer cancel()
	switch st.Type {
	case model.BackupStorageS3:
		return probeS3Storage(ctx, st, accessKey, secretKey)
	case model.BackupStorageWebDAV:
		return probeWebDAVStorage(ctx, st, accessKey, secretKey)
	case model.BackupStorageSFTP:
		return probeSFTPStorage(ctx, st, accessKey, secretKey)
	default:
		return ErrInvalidStorageType
	}
}

func probeS3Storage(ctx context.Context, st model.BackupStorage, accessKey, secretKey string) error {
	if strings.TrimSpace(st.Bucket) == "" {
		return fmt.Errorf("S3 缺少 bucket")
	}
	u, err := backupStorageProbeURL(st.Endpoint, st.UseSSL)
	if err != nil {
		return fmt.Errorf("S3 endpoint 无效: %w", err)
	}
	appendProbePath(u, st.Bucket)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return fmt.Errorf("S3 探测失败: %w", err)
	}
	if accessKey != "" || secretKey != "" {
		region := st.Region
		if region == "" {
			region = "us-east-1"
		}
		signS3Probe(req, region, accessKey, secretKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("S3 连接失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("S3 bucket 不可访问: HTTP %d", resp.StatusCode)
	}
	return nil
}

func probeWebDAVStorage(ctx context.Context, st model.BackupStorage, user, pass string) error {
	u, err := backupStorageProbeURL(st.Endpoint, true)
	if err != nil {
		return fmt.Errorf("WebDAV endpoint 无效: %w", err)
	}
	if strings.TrimSpace(st.Prefix) != "" {
		appendProbePath(u, st.Prefix)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, u.String(), nil)
	if err != nil {
		return fmt.Errorf("WebDAV 探测失败: %w", err)
	}
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("WebDAV 连接失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("WebDAV 不可访问: HTTP %d", resp.StatusCode)
	}
	return nil
}

func probeSFTPStorage(ctx context.Context, st model.BackupStorage, user, pass string) error {
	if strings.TrimSpace(user) == "" {
		return fmt.Errorf("SFTP 缺少用户名")
	}
	addr, err := sftpProbeAddr(st.Endpoint)
	if err != nil {
		return err
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         backupStorageProbeTimeout,
	}
	conn, err := (&net.Dialer{Timeout: backupStorageProbeTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("SFTP 连接失败: %w", err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("SFTP 握手失败: %w", err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	_ = client.Close()
	return nil
}

func backupStorageProbeURL(endpoint string, useSSL bool) (*url.URL, error) {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return nil, fmt.Errorf("缺少 endpoint")
	}
	if !strings.Contains(ep, "://") {
		scheme := "https"
		if !useSSL {
			scheme = "http"
		}
		ep = scheme + "://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, fmt.Errorf("缺少 host")
	}
	return u, nil
}

func appendProbePath(u *url.URL, part string) {
	prefix := strings.TrimRight(u.Path, "/")
	next := strings.TrimLeft(strings.TrimSpace(part), "/")
	if next == "" {
		u.Path = prefix
		return
	}
	u.Path = prefix + "/" + next
}

func sftpProbeAddr(endpoint string) (string, error) {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return "", fmt.Errorf("SFTP 缺少 endpoint")
	}
	if _, _, err := net.SplitHostPort(ep); err == nil {
		return ep, nil
	}
	return net.JoinHostPort(ep, "22"), nil
}

const s3ProbeEmptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func signS3Probe(req *http.Request, region, accessKey, secretKey string) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", s3ProbeEmptyPayloadHash)

	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		req.URL.Host, s3ProbeEmptyPayloadHash, amzDate)
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		canonicalS3ProbeQuery(req.URL.Query()),
		canonicalHeaders,
		signedHeaders,
		s3ProbeEmptyPayloadHash,
	}, "\n")
	scope := strings.Join([]string{dateStamp, region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		s3ProbeSHA256Hex([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(s3ProbeHMACSHA256(deriveS3ProbeKey(secretKey, dateStamp, region), []byte(stringToSign)))
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey,
		scope,
		signedHeaders,
		signature,
	))
}

func canonicalS3ProbeQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	first := true
	for _, k := range keys {
		values := append([]string(nil), q[k]...)
		sort.Strings(values)
		for _, v := range values {
			if !first {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(k))
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(v))
			first = false
		}
	}
	return b.String()
}

func deriveS3ProbeKey(secretKey, dateStamp, region string) []byte {
	kDate := s3ProbeHMACSHA256([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := s3ProbeHMACSHA256(kDate, []byte(region))
	kService := s3ProbeHMACSHA256(kRegion, []byte("s3"))
	return s3ProbeHMACSHA256(kService, []byte("aws4_request"))
}

func s3ProbeHMACSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(data)
	return h.Sum(nil)
}

func s3ProbeSHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
