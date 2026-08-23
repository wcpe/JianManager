package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// BotLoadTemplateView 是模板 API 响应 DTO。
type BotLoadTemplateView struct {
	ID              uint              `json:"id"`
	UUID            string            `json:"uuid"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	CommandSchedule json.RawMessage   `json:"commandSchedule"`
	LoadProfile     BotLoadProfile    `json:"loadProfile"`
	Thresholds      BotLoadThresholds `json:"thresholds"`
	Tags            []string          `json:"tags"`
	CreatedBy       uint              `json:"createdBy"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
}

// BotLoadTemplateInput 创建/更新模板请求。
type BotLoadTemplateInput struct {
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	CommandSchedule json.RawMessage `json:"commandSchedule"`
	LoadProfile     json.RawMessage `json:"loadProfile"`
	Thresholds      json.RawMessage `json:"thresholds"`
	Tags            []string        `json:"tags"`
}

// BotLoadTemplateListQuery 列表查询参数。
type BotLoadTemplateListQuery struct {
	Page     int
	PageSize int
	Q        string
	Tag      string
	OwnerID  *uint // 仅平台管理员可用
}

// BotLoadTemplateListResult 分页结果。
type BotLoadTemplateListResult struct {
	Items    []BotLoadTemplateView `json:"items"`
	Total    int64                 `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"pageSize"`
}

// BotLoadTemplateService 管理命令压测模板。
type BotLoadTemplateService struct {
	db *gorm.DB
}

// NewBotLoadTemplateService 创建模板服务。
func NewBotLoadTemplateService(db *gorm.DB) *BotLoadTemplateService {
	return &BotLoadTemplateService{db: db}
}

// Create 创建模板；createdBy 固定为当前用户。
func (s *BotLoadTemplateService) Create(userID uint, input BotLoadTemplateInput) (*BotLoadTemplateView, error) {
	name, scheduleJSON, profileJSON, thresholdsJSON, tagsJSON, nameKey, err := s.normalizeInput(input)
	if err != nil {
		return nil, err
	}
	tpl := &model.BotLoadTemplate{
		CreatedBy:       userID,
		ActiveNameKey:   &nameKey,
		Name:            name,
		Description:     strings.TrimSpace(input.Description),
		CommandSchedule: scheduleJSON,
		LoadProfile:     profileJSON,
		Thresholds:      thresholdsJSON,
		Tags:            tagsJSON,
	}
	if err := s.db.Create(tpl).Error; err != nil {
		if isUniqueConflict(err) {
			return nil, ErrBotLoadTemplateNameConflict
		}
		return nil, fmt.Errorf("创建 Bot 负载模板失败: %w", err)
	}
	return s.viewFromModel(tpl)
}

// Update 全量替换可编辑字段；非管理员仅可更新自己的模板。
func (s *BotLoadTemplateService) Update(id, userID uint, isAdmin bool, input BotLoadTemplateInput) (*BotLoadTemplateView, error) {
	tpl, err := s.getOwned(id, userID, isAdmin)
	if err != nil {
		return nil, err
	}
	name, scheduleJSON, profileJSON, thresholdsJSON, tagsJSON, nameKey, err := s.normalizeInput(input)
	if err != nil {
		return nil, err
	}
	tpl.Name = name
	tpl.ActiveNameKey = &nameKey
	tpl.Description = strings.TrimSpace(input.Description)
	tpl.CommandSchedule = scheduleJSON
	tpl.LoadProfile = profileJSON
	tpl.Thresholds = thresholdsJSON
	tpl.Tags = tagsJSON
	if err := s.db.Save(tpl).Error; err != nil {
		if isUniqueConflict(err) {
			return nil, ErrBotLoadTemplateNameConflict
		}
		return nil, fmt.Errorf("更新 Bot 负载模板失败: %w", err)
	}
	return s.viewFromModel(tpl)
}

// Delete 软删模板，同时将 active_name_key 置 null 以允许名称复用。
func (s *BotLoadTemplateService) Delete(id, userID uint, isAdmin bool) error {
	tpl, err := s.getOwned(id, userID, isAdmin)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(tpl).Update("active_name_key", nil).Error; err != nil {
			return fmt.Errorf("清除模板名称键失败: %w", err)
		}
		if err := tx.Delete(tpl).Error; err != nil {
			return fmt.Errorf("删除 Bot 负载模板失败: %w", err)
		}
		return nil
	})
}

// Get 获取单个模板。
func (s *BotLoadTemplateService) Get(id, userID uint, isAdmin bool) (*BotLoadTemplateView, error) {
	tpl, err := s.getOwned(id, userID, isAdmin)
	if err != nil {
		return nil, err
	}
	return s.viewFromModel(tpl)
}

// List 分页搜索模板。
func (s *BotLoadTemplateService) List(userID uint, isAdmin bool, q BotLoadTemplateListQuery) (*BotLoadTemplateListResult, error) {
	page, pageSize := normalizeBotLoadPage(q.Page, q.PageSize)
	db := s.db.Model(&model.BotLoadTemplate{})
	if !isAdmin {
		db = db.Where("created_by = ?", userID)
	} else if q.OwnerID != nil {
		db = db.Where("created_by = ?", *q.OwnerID)
	}
	if term := strings.TrimSpace(q.Q); term != "" {
		like := "%" + term + "%"
		db = db.Where("name LIKE ? OR description LIKE ?", like, like)
	}
	if tag := strings.TrimSpace(q.Tag); tag != "" {
		// tags 为 JSON 数组字符串，用 LIKE 做简单包含匹配
		db = db.Where("tags LIKE ?", "%\""+tag+"\"%")
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("统计模板失败: %w", err)
	}
	var rows []model.BotLoadTemplate
	if err := db.Order("updated_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询模板失败: %w", err)
	}
	items := make([]BotLoadTemplateView, 0, len(rows))
	for i := range rows {
		view, err := s.viewFromModel(&rows[i])
		if err != nil {
			return nil, err
		}
		items = append(items, *view)
	}
	return &BotLoadTemplateListResult{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

// CreateRunFromTemplate 从模板深拷贝快照创建 schemaVersion=2 运行。
func (s *BotLoadTemplateService) CreateRunFromTemplate(
	templateID, userID uint, isAdmin bool,
	instanceID uint, name, namePrefix string, config json.RawMessage,
	scheduleOverride, profileOverride, thresholdsOverride json.RawMessage,
) (*model.BotStressSession, error) {
	tpl, err := s.getOwned(templateID, userID, isAdmin)
	if err != nil {
		return nil, err
	}
	scheduleRaw := json.RawMessage(tpl.CommandSchedule)
	if len(scheduleOverride) > 0 && string(scheduleOverride) != "null" {
		scheduleRaw = scheduleOverride
	}
	profileRaw := json.RawMessage(tpl.LoadProfile)
	if len(profileOverride) > 0 && string(profileOverride) != "null" {
		profileRaw = profileOverride
	}
	thresholdsRaw := json.RawMessage(tpl.Thresholds)
	if len(thresholdsOverride) > 0 && string(thresholdsOverride) != "null" {
		thresholdsRaw = thresholdsOverride
	}

	scheduleJSON, err := normalizeCommandScheduleJSON(scheduleRaw)
	if err != nil {
		return nil, err
	}
	profile, err := NormalizeAndValidateLoadProfile(profileRaw)
	if err != nil {
		return nil, err
	}
	thresholds, err := NormalizeAndValidateThresholds(thresholdsRaw)
	if err != nil {
		return nil, err
	}
	configNorm, err := normalizeV2OfflineConfig(config)
	if err != nil {
		return nil, err
	}
	profileJSON, err := EncodeJSON(profile)
	if err != nil {
		return nil, err
	}
	thresholdsJSON, err := EncodeJSON(thresholds)
	if err != nil {
		return nil, err
	}

	name = strings.TrimSpace(name)
	namePrefix = strings.TrimSpace(namePrefix)
	if name == "" || namePrefix == "" || instanceID == 0 {
		return nil, ErrBotStressSessionInvalid
	}

	runState := model.BotLoadRunPending
	verdict := model.BotLoadVerdictPending
	stage := 0
	maxStable := 0
	failSummary, err := EncodeJSON(EmptyFailureSummary())
	if err != nil {
		return nil, fmt.Errorf("序列化 Bot 负载失败摘要失败: %w", err)
	}
	tplID := tpl.ID
	sess := &model.BotStressSession{
		InstanceID:          instanceID,
		Name:                name,
		NamePrefix:          namePrefix,
		BotCount:            ProfileMaxTargetBots(profile),
		Behavior:            "command-orchestration-v1",
		Config:              configNorm,
		CommandScheduleSnap: scheduleJSON,
		SchemaVersion:       2,
		TemplateID:          &tplID,
		LoadProfile:         profileJSON,
		Thresholds:          thresholdsJSON,
		RunState:            &runState,
		CurrentStage:        &stage,
		Verdict:             &verdict,
		MaxStableBots:       &maxStable,
		FailureSummary:      failSummary,
		Status:              model.BotStressSessionPending,
	}
	if err := s.db.Create(sess).Error; err != nil {
		return nil, fmt.Errorf("从模板创建运行失败: %w", err)
	}
	return sess, nil
}

func (s *BotLoadTemplateService) normalizeInput(input BotLoadTemplateInput) (name, scheduleJSON, profileJSON, thresholdsJSON, tagsJSON, nameKey string, err error) {
	name = strings.TrimSpace(input.Name)
	if name == "" || len(name) > 128 {
		return "", "", "", "", "", "", fmt.Errorf("%w: 名称长度 1..128", ErrBotStressSessionInvalid)
	}
	nameKey = ActiveNameKey(name)
	scheduleJSON, err = normalizeCommandScheduleJSON(input.CommandSchedule)
	if err != nil {
		return "", "", "", "", "", "", err
	}
	profile, err := NormalizeAndValidateLoadProfile(input.LoadProfile)
	if err != nil {
		return "", "", "", "", "", "", err
	}
	thresholds, err := NormalizeAndValidateThresholds(input.Thresholds)
	if err != nil {
		return "", "", "", "", "", "", err
	}
	profileJSON, err = EncodeJSON(profile)
	if err != nil {
		return "", "", "", "", "", "", err
	}
	thresholdsJSON, err = EncodeJSON(thresholds)
	if err != nil {
		return "", "", "", "", "", "", err
	}
	tags := input.Tags
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, err = EncodeJSON(tags)
	if err != nil {
		return "", "", "", "", "", "", err
	}
	return name, scheduleJSON, profileJSON, thresholdsJSON, tagsJSON, nameKey, nil
}

func (s *BotLoadTemplateService) getOwned(id, userID uint, isAdmin bool) (*model.BotLoadTemplate, error) {
	var tpl model.BotLoadTemplate
	q := s.db.Where("id = ?", id)
	if !isAdmin {
		q = q.Where("created_by = ?", userID)
	}
	if err := q.First(&tpl).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotLoadTemplateNotFound
		}
		return nil, fmt.Errorf("查询模板失败: %w", err)
	}
	return &tpl, nil
}

func (s *BotLoadTemplateService) viewFromModel(tpl *model.BotLoadTemplate) (*BotLoadTemplateView, error) {
	var profile BotLoadProfile
	if err := json.Unmarshal([]byte(tpl.LoadProfile), &profile); err != nil {
		return nil, fmt.Errorf("解析模板 loadProfile 失败: %w", err)
	}
	var thresholds BotLoadThresholds
	if err := json.Unmarshal([]byte(tpl.Thresholds), &thresholds); err != nil {
		return nil, fmt.Errorf("解析模板 thresholds 失败: %w", err)
	}
	var tags []string
	if tpl.Tags != "" {
		if err := json.Unmarshal([]byte(tpl.Tags), &tags); err != nil {
			return nil, fmt.Errorf("解析模板 tags 失败: %w", err)
		}
	}
	if tags == nil {
		tags = []string{}
	}
	return &BotLoadTemplateView{
		ID: tpl.ID, UUID: tpl.UUID, Name: tpl.Name, Description: tpl.Description,
		CommandSchedule: json.RawMessage(tpl.CommandSchedule),
		LoadProfile:     profile, Thresholds: thresholds, Tags: tags,
		CreatedBy: tpl.CreatedBy, CreatedAt: tpl.CreatedAt, UpdatedAt: tpl.UpdatedAt,
	}, nil
}

// ActiveNameKey 计算 trim 后名称的 SHA-256 hex。
func ActiveNameKey(trimmedName string) string {
	sum := sha256.Sum256([]byte(trimmedName))
	return hex.EncodeToString(sum[:])
}

func normalizeCommandScheduleJSON(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("%w: commandSchedule 必填", ErrBotLoadScenarioInvalid)
	}
	var input CommandScheduleInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", &CommandScheduleValidationError{Path: "$", Message: "命令计划 JSON 无效"}
	}
	plan, err := NormalizeCommandSchedule(&input)
	if err != nil {
		return "", err
	}
	// 规范化快照保留原始 commands 形态（API 兼容），jitterMs 缺省写 0
	jitter := int64(0)
	if input.JitterMS != nil {
		jitter = *input.JitterMS
	} else {
		jitter = plan.JitterMS
	}
	snapshot := struct {
		Commands   []CommandScheduleInputCommand `json:"commands"`
		DurationMS int64                         `json:"durationMs"`
		JitterMS   int64                         `json:"jitterMs"`
	}{Commands: input.Commands, DurationMS: plan.DurationMS, JitterMS: jitter}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("序列化命令计划失败: %w", err)
	}
	if len(b) > 256*1024 {
		return "", &CommandScheduleValidationError{Path: "$", Message: "规范 JSON 不得超过 256KiB"}
	}
	return string(b), nil
}

// ErrBotLoadScenarioInvalid 复用场景/命令计划校验失败（映射 422）。
var ErrBotLoadScenarioInvalid = errors.New("Bot 负载场景或命令计划无效")

func normalizeV2OfflineConfig(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", ErrBotStressSessionInvalid
	}
	var cfg struct {
		Server   string `json:"server"`
		Port     int    `json:"port"`
		Auth     string `json:"auth"`
		Version  string `json:"version,omitempty"`
		Password string `json:"password"`
		// 拒绝凭据字段
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ClientToken  string `json:"clientToken"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", ErrBotStressSessionInvalid
	}
	if cfg.Password != "" || cfg.AccessToken != "" || cfg.RefreshToken != "" || cfg.ClientToken != "" {
		return "", ErrBotStressSessionInvalid
	}
	if strings.TrimSpace(cfg.Server) == "" || cfg.Port < 1 || cfg.Port > 65535 {
		return "", ErrBotStressSessionInvalid
	}
	if cfg.Auth != "offline" {
		return "", ErrBotStressSessionInvalid
	}
	out := map[string]any{"server": strings.TrimSpace(cfg.Server), "port": cfg.Port, "auth": "offline"}
	if v := strings.TrimSpace(cfg.Version); v != "" {
		out["version"] = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func normalizeBotLoadPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func isUniqueConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate") || strings.Contains(msg, "constraint failed")
}

// NewRunUUID 生成运行 UUID（测试辅助可覆盖）。
func NewRunUUID() string { return uuid.New().String() }
