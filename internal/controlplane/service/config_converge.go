package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 配置基线（模板）下发、漂移检测、一键收敛（FR-458）。
//
// 复用既有 ConfigService.Write（版本落库 + Worker 侧 WriteConfig 校验），不另建写入通道；
// 漂移检测只读零副作用（优先读最新 InstanceConfigVersion，缺失则现场 Read + 计算哈希）。

// 基线 scope 前缀：限定「哪些实例应共享该基线」。
const (
	baselineScopeGroup    = "group"
	baselineScopeNetwork  = "network"
	baselineScopeTag      = "tag"
	baselineScopeInstance = "instance"
	baselineScopeAll      = "all"
)

// BaselineScopeAllKey 是「全部实例」的 scope 键。
const BaselineScopeAllKey = "all"

// ConfigBaselineService 配置基线服务（FR-458）。
type ConfigBaselineService struct {
	db     *gorm.DB
	config *ConfigService
	// convergeWrite 收敛写入口子，默认为 nil（走 config.Write）；测试可注入以绕过 Worker。
	convergeWrite func(instanceID uint, filePath, content, message string, authorID uint) (uint, error)
}

// NewConfigBaselineService 创建配置基线服务。
func NewConfigBaselineService(db *gorm.DB, config *ConfigService) *ConfigBaselineService {
	return &ConfigBaselineService{db: db, config: config}
}

// SetConvergeWriterForTest 注入收敛写入实现（仅供测试）。
func (s *ConfigBaselineService) SetConvergeWriterForTest(fn func(instanceID uint, filePath, content, message string, authorID uint) (uint, error)) {
	s.convergeWrite = fn
}

// convergeOne 单实例收敛写入：优先测试口子，否则复用 ConfigService.Write。
func (s *ConfigBaselineService) convergeOne(instanceID uint, filePath, content, message string, authorID uint) (uint, error) {
	if s.convergeWrite != nil {
		return s.convergeWrite(instanceID, filePath, content, message, authorID)
	}
	vid, _, err := s.config.Write(instanceID, filePath, content, message, authorID, nil)
	return vid, err
}

// BaselineInput 是创建/更新基线的入参。
type BaselineInput struct {
	ScopeKey string `json:"scopeKey"`
	FilePath string `json:"filePath"`
	Content  string `json:"content"`
	Message  string `json:"message"`
}

// DriftItem 是单实例的漂移比对结果（FR-458）。
type DriftItem struct {
	InstanceID   uint   `json:"instanceId"`
	InstanceName string `json:"instanceName"`
	Drift        bool   `json:"drift"`
	CurrentHash  string `json:"currentHash"`
	BaselineHash string `json:"baselineHash"`
	HasVersion   bool   `json:"hasVersion"`
	Error        string `json:"error,omitempty"`
}

// ConvergeOptions 收敛选项。
type ConvergeOptions struct {
	BatchSize int
	FailFast  bool
}

// ConvergeInstanceResult 单实例收敛结果。
type ConvergeInstanceResult struct {
	InstanceID uint   `json:"instanceId"`
	Success    bool   `json:"success"`
	VersionID  uint   `json:"versionId,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ConvergeResult 收敛汇总（含收敛后复核的残余漂移）。
type ConvergeResult struct {
	BaselineID    uint                     `json:"baselineId"`
	Targeted      int                      `json:"targeted"`
	Succeeded     int                      `json:"succeeded"`
	Failed        int                      `json:"failed"`
	Results       []ConvergeInstanceResult `json:"results"`
	ResidualDrift []DriftItem              `json:"residualDrift"`
}

// UpsertBaseline 创建或更新基线（同 scopeKey+filePath 覆盖），并计算内容哈希。
func (s *ConfigBaselineService) UpsertBaseline(in BaselineInput, authorID uint) (*model.ConfigBaseline, error) {
	scopeKey := strings.TrimSpace(in.ScopeKey)
	if scopeKey == "" {
		return nil, errors.New("scopeKey 不能为空")
	}
	filePath := strings.TrimSpace(in.FilePath)
	if filePath == "" {
		return nil, errors.New("filePath 不能为空")
	}
	if _, err := s.resolveScopeInstances(scopeKey); err != nil {
		return nil, err
	}
	hash := hashContent(in.Content)
	var existing model.ConfigBaseline
	err := s.db.Where("scope_key = ? AND file_path = ?", scopeKey, filePath).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row := &model.ConfigBaseline{
			ScopeKey: scopeKey, FilePath: filePath, Content: in.Content,
			ContentHash: hash, Message: in.Message, AuthorID: authorID,
		}
		if err := s.db.Create(row).Error; err != nil {
			return nil, fmt.Errorf("创建配置基线失败: %w", err)
		}
		return row, nil
	}
	if err != nil {
		return nil, err
	}
	existing.Content = in.Content
	existing.ContentHash = hash
	existing.Message = in.Message
	existing.AuthorID = authorID
	if err := s.db.Save(&existing).Error; err != nil {
		return nil, fmt.Errorf("更新配置基线失败: %w", err)
	}
	return &existing, nil
}

// ListBaselines 列出全部基线。
func (s *ConfigBaselineService) ListBaselines() ([]model.ConfigBaseline, error) {
	var rows []model.ConfigBaseline
	if err := s.db.Order("id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// GetBaseline 读取单条基线。
func (s *ConfigBaselineService) GetBaseline(id uint) (*model.ConfigBaseline, error) {
	var row model.ConfigBaseline
	if err := s.db.First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRollingOpNotFound
		}
		return nil, err
	}
	return &row, nil
}

// DeleteBaseline 删除基线。
func (s *ConfigBaselineService) DeleteBaseline(id uint) error {
	return s.db.Delete(&model.ConfigBaseline{}, id).Error
}

// DetectDrift 对 scope 内每台实例取该 filePath 的当前内容哈希并与基线比对（只读，零副作用）。
func (s *ConfigBaselineService) DetectDrift(baselineID uint) ([]DriftItem, error) {
	bl, err := s.GetBaseline(baselineID)
	if err != nil {
		return nil, err
	}
	insts, err := s.resolveScopeInstances(bl.ScopeKey)
	if err != nil {
		return nil, err
	}
	out := make([]DriftItem, 0, len(insts))
	for _, in := range insts {
		item := DriftItem{InstanceID: in.ID, InstanceName: in.Name, BaselineHash: bl.ContentHash}
		cur, has, herr := s.currentHash(in.ID, bl.FilePath)
		if herr != nil {
			item.Error = herr.Error()
			item.Drift = true
		} else {
			item.CurrentHash = cur
			item.HasVersion = has
			item.Drift = cur != bl.ContentHash
		}
		out = append(out, item)
	}
	return out, nil
}

// Converge 对所有漂移实例推送基线内容，收敛后再跑一次检测复核并回报残余漂移（FR-458）。
func (s *ConfigBaselineService) Converge(baselineID uint, opts ConvergeOptions, authorID uint) (*ConvergeResult, error) {
	bl, err := s.GetBaseline(baselineID)
	if err != nil {
		return nil, err
	}
	if s.config == nil && s.convergeWrite == nil {
		return nil, errors.New("配置服务未注入")
	}
	drifts, err := s.DetectDrift(baselineID)
	if err != nil {
		return nil, err
	}
	res := &ConvergeResult{BaselineID: baselineID, Results: []ConvergeInstanceResult{}}
	ids := make([]uint, 0, len(drifts))
	for _, d := range drifts {
		if d.Drift {
			ids = append(ids, d.InstanceID)
		}
	}
	res.Targeted = len(ids)

	for _, batch := range formBatches(ids, opts.BatchSize) {
		batchFailed := s.convergeBatch(bl, batch, res, authorID)
		if opts.FailFast && batchFailed {
			break
		}
	}

	residual, err := s.DetectDrift(baselineID)
	if err != nil {
		return res, err
	}
	for _, d := range residual {
		if d.Drift {
			res.ResidualDrift = append(res.ResidualDrift, d)
		}
	}
	if res.ResidualDrift == nil {
		res.ResidualDrift = []DriftItem{}
	}
	return res, nil
}

func (s *ConfigBaselineService) convergeBatch(bl *model.ConfigBaseline, batch []uint, res *ConvergeResult, authorID uint) bool {
	var (
		mu        sync.Mutex
		wg        sync.WaitGroup
		sem       = make(chan struct{}, instanceBatchConcurrency)
		batchFail bool
	)
	message := fmt.Sprintf("配置基线收敛 #%d", bl.ID)
	for _, iid := range batch {
		id := iid
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			vid, werr := s.convergeOne(id, bl.FilePath, bl.Content, message, authorID)
			mu.Lock()
			defer mu.Unlock()
			if werr != nil {
				batchFail = true
				res.Failed++
				res.Results = append(res.Results, ConvergeInstanceResult{InstanceID: id, Success: false, Error: werr.Error()})
			} else {
				res.Succeeded++
				res.Results = append(res.Results, ConvergeInstanceResult{InstanceID: id, Success: true, VersionID: vid})
			}
		}()
	}
	wg.Wait()
	return batchFail
}

// currentHash 取实例某配置文件的当前内容哈希：优先最新 InstanceConfigVersion，缺失则现场 Read + 计算。
func (s *ConfigBaselineService) currentHash(instanceID uint, filePath string) (string, bool, error) {
	var latest model.InstanceConfigVersion
	err := s.db.Where("instance_id = ? AND file_path = ?", instanceID, filePath).Order("id DESC").First(&latest).Error
	if err == nil {
		return latest.ContentHash, true, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, err
	}
	if s.config == nil {
		return "", false, errors.New("配置服务未注入")
	}
	read, rerr := s.config.Read(instanceID, filePath)
	if rerr != nil {
		return "", false, rerr
	}
	return hashContent(read.Content), false, nil
}

// resolveScopeInstances 解析 scopeKey 限定的实例集合。
// 支持 group:<id>（含子树）/ network:<id> / tag:<tag> / instance:<id> / all 或空。
func (s *ConfigBaselineService) resolveScopeInstances(scopeKey string) ([]model.Instance, error) {
	key := strings.TrimSpace(scopeKey)
	if key == "" || key == BaselineScopeAllKey {
		var insts []model.Instance
		if err := s.db.Find(&insts).Error; err != nil {
			return nil, err
		}
		return insts, nil
	}
	kind, raw, ok := strings.Cut(key, ":")
	if !ok {
		return nil, fmt.Errorf("非法 scopeKey: %s（形如 group:1 / network:2 / tag:prod / instance:3 / all）", scopeKey)
	}
	switch kind {
	case baselineScopeGroup:
		id, err := parseUintArg(raw)
		if err != nil {
			return nil, fmt.Errorf("非法分组 ID: %s", raw)
		}
		return s.instancesByGroups(s.groupSubtreeIDs(id))
	case baselineScopeNetwork:
		id, err := parseUintArg(raw)
		if err != nil {
			return nil, fmt.Errorf("非法网络 ID: %s", raw)
		}
		var members []model.NetworkMember
		if err := s.db.Where("network_id = ?", id).Find(&members).Error; err != nil {
			return nil, err
		}
		ids := make([]uint, 0, len(members))
		for _, m := range members {
			ids = append(ids, m.InstanceID)
		}
		return s.instancesByIDs(ids)
	case baselineScopeTag:
		return s.instancesByTag(strings.TrimSpace(raw))
	case baselineScopeInstance:
		id, err := parseUintArg(raw)
		if err != nil {
			return nil, fmt.Errorf("非法实例 ID: %s", raw)
		}
		return s.instancesByIDs([]uint{id})
	default:
		return nil, fmt.Errorf("不支持的 scopeKey 前缀: %s", kind)
	}
}

func (s *ConfigBaselineService) instancesByGroups(groupIDs []uint) ([]model.Instance, error) {
	if len(groupIDs) == 0 {
		return []model.Instance{}, nil
	}
	var members []model.InstanceGroupMember
	if err := s.db.Where("group_id IN ?", groupIDs).Find(&members).Error; err != nil {
		return nil, err
	}
	return s.instancesByIDs(collectInstanceIDs(members))
}

func (s *ConfigBaselineService) instancesByIDs(ids []uint) ([]model.Instance, error) {
	if len(ids) == 0 {
		return []model.Instance{}, nil
	}
	var insts []model.Instance
	if err := s.db.Where("id IN ?", ids).Order("id asc").Find(&insts).Error; err != nil {
		return nil, err
	}
	return insts, nil
}

func (s *ConfigBaselineService) instancesByTag(tag string) ([]model.Instance, error) {
	if tag == "" {
		return []model.Instance{}, nil
	}
	var insts []model.Instance
	if err := s.db.Order("id asc").Find(&insts).Error; err != nil {
		return nil, err
	}
	out := make([]model.Instance, 0, len(insts))
	for _, in := range insts {
		for _, t := range model.ParseTags(in.Tags) {
			if t == tag {
				out = append(out, in)
				break
			}
		}
	}
	return out, nil
}

// groupSubtreeIDs 返回分组及其全部后代分组 ID（自引用邻接表 BFS）。
func (s *ConfigBaselineService) groupSubtreeIDs(root uint) []uint {
	var nodes []model.InstanceGroupNode
	if err := s.db.Find(&nodes).Error; err != nil {
		return []uint{root}
	}
	children := map[uint][]uint{}
	for _, n := range nodes {
		if n.ParentID != nil {
			children[*n.ParentID] = append(children[*n.ParentID], n.ID)
		}
	}
	out := []uint{root}
	queue := []uint{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			out = append(out, c)
			queue = append(queue, c)
		}
	}
	return out
}

func collectInstanceIDs(members []model.InstanceGroupMember) []uint {
	out := make([]uint, 0, len(members))
	for _, m := range members {
		out = append(out, m.InstanceID)
	}
	return out
}

func parseUintArg(raw string) (uint, error) {
	v, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(v), nil
}

// hashContent 计算内容 sha256（与 config.go 落 InstanceConfigVersion.ContentHash 同口径）。
func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}
