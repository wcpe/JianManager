package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/pmezard/go-difflib/difflib"
	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/internal/controlplane/service/schema"
	"github.com/wxys233/JianManager/proto/workerpb"
	"gorm.io/gorm"
)

type ConfigFileInfo struct {
	Path      string `json:"path"`
	Format    string `json:"format"`
	Size      int64  `json:"size"`
	UpdatedAt int64  `json:"updatedAt"`
	Supported bool   `json:"supported"`
}

type ConfigVersion struct {
	ID                  uint      `json:"id"`
	FilePath            string    `json:"filePath"`
	Message             string    `json:"message"`
	AuthorID            uint      `json:"authorId"`
	RollbackOfVersionID *uint     `json:"rollbackOfVersionId,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
}

type ConfigReadResult struct {
	Path       string           `json:"path"`
	Format     string           `json:"format"`
	Content    string           `json:"content"`
	Fields     []map[string]any `json:"fields"`
	SchemaJSON string           `json:"schemaJson"`
	Model      string           `json:"model"`
	Validation map[string]any   `json:"validation"`
}

type ConfigDiff struct {
	FromVersionID uint   `json:"fromVersionId"`
	ToVersionID   uint   `json:"toVersionId"`
	UnifiedDiff   string `json:"unifiedDiff"`
	FromContent   string `json:"fromContent"`
	ToContent     string `json:"toContent"`
}

type ConfigService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool
}

func NewConfigService(db *gorm.DB, pool *cpgrpc.ClientPool) *ConfigService {
	return &ConfigService{db: db, pool: pool}
}

func (s *ConfigService) List(instanceID uint, path string) ([]ConfigFileInfo, error) {
	inst, client, err := s.client(instanceID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.Worker.ListConfigFiles(ctx, &workerpb.ListConfigFilesRequest{InstanceUuid: inst.UUID, Path: path})
	if err != nil {
		return nil, fmt.Errorf("列出配置文件失败: %w", err)
	}
	out := make([]ConfigFileInfo, 0, len(resp.Files))
	for _, f := range resp.Files {
		out = append(out, ConfigFileInfo{Path: f.Path, Format: f.Format, Size: f.Size, UpdatedAt: f.UpdatedAt, Supported: f.Supported})
	}
	return out, nil
}

func (s *ConfigService) Read(instanceID uint, filePath string) (*ConfigReadResult, error) {
	inst, client, err := s.client(instanceID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.Worker.ReadConfig(ctx, &workerpb.ReadConfigRequest{InstanceUuid: inst.UUID, Path: filePath})
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	fields := make([]map[string]any, 0, len(resp.Fields))
	for _, f := range resp.Fields {
		fields = append(fields, map[string]any{"key": f.Key, "value": f.Value, "type": f.Type, "description": f.Description, "line": f.Line})
	}
	validation := map[string]any{"valid": true, "issues": []any{}}
	if resp.Validation != nil {
		validation = map[string]any{"valid": resp.Validation.Valid, "issues": resp.Validation.Issues}
	}
	model := ""
	if resp.Model != "" {
		model = resp.Model
	} else if m := schema.MatchPath(filePath); m != nil {
		model = m.Name
	}
	return &ConfigReadResult{Path: resp.Path, Format: resp.Format, Content: resp.Content, Fields: fields, SchemaJSON: resp.SchemaJson, Model: model, Validation: validation}, nil
}

// Write 保存新版本；可选回滚源 versionID。
func (s *ConfigService) Write(instanceID uint, filePath, content, message string, authorID uint, rollbackOfVersionID *uint) (uint, map[string]any, error) {
	inst, client, err := s.client(instanceID)
	if err != nil {
		return 0, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.Worker.WriteConfig(ctx, &workerpb.WriteConfigRequest{InstanceUuid: inst.UUID, Path: filePath, Content: content})
	if err != nil {
		return 0, nil, fmt.Errorf("写入配置失败: %w", err)
	}
	if !resp.Success {
		return 0, validationMap(resp.Validation), errors.New(resp.Error)
	}
	h := sha256.Sum256([]byte(content))
	ver := model.InstanceConfigVersion{
		InstanceID:          instanceID,
		FilePath:            filePath,
		ContentHash:         hex.EncodeToString(h[:]),
		Content:             content,
		Message:             message,
		AuthorID:            authorID,
		RollbackOfVersionID: rollbackOfVersionID,
	}
	if err := s.db.Create(&ver).Error; err != nil {
		return 0, nil, fmt.Errorf("保存版本失败: %w", err)
	}
	return ver.ID, validationMap(resp.Validation), nil
}

func (s *ConfigService) Versions(instanceID uint, filePath string) ([]ConfigVersion, error) {
	var rows []model.InstanceConfigVersion
	if err := s.db.Where("instance_id = ? AND file_path = ?", instanceID, filePath).
		Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ConfigVersion, 0, len(rows))
	for _, r := range rows {
		out = append(out, ConfigVersion{
			ID:                  r.ID,
			FilePath:            r.FilePath,
			Message:             r.Message,
			AuthorID:            r.AuthorID,
			RollbackOfVersionID: r.RollbackOfVersionID,
			CreatedAt:           r.CreatedAt,
		})
	}
	return out, nil
}

// Diff 返回 fromID 与 toID 之间的 unified diff。
// toID=0 表示与当前文件内容比较。
func (s *ConfigService) Diff(instanceID uint, filePath string, fromID, toID uint) (*ConfigDiff, error) {
	var fromVer, toVer model.InstanceConfigVersion
	if err := s.db.Where("id = ? AND instance_id = ? AND file_path = ?", fromID, instanceID, filePath).First(&fromVer).Error; err != nil {
		return nil, fmt.Errorf("源版本 #%d 不存在: %w", fromID, err)
	}

	var toContent string
	if toID == 0 {
		inst, client, err := s.client(instanceID)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := client.Worker.ReadConfig(ctx, &workerpb.ReadConfigRequest{InstanceUuid: inst.UUID, Path: filePath})
		if err != nil {
			return nil, fmt.Errorf("读取当前配置失败: %w", err)
		}
		toContent = resp.Content
	} else {
		if err := s.db.Where("id = ? AND instance_id = ? AND file_path = ?", toID, instanceID, filePath).First(&toVer).Error; err != nil {
			return nil, fmt.Errorf("目标版本 #%d 不存在: %w", toID, err)
		}
		toContent = toVer.Content
	}

	unified, err := unifiedDiff(filePath, fromVer.Content, toContent, fmt.Sprintf("v%d", fromID), fmt.Sprintf("v%d", toID))
	if err != nil {
		return nil, err
	}
	return &ConfigDiff{
		FromVersionID: fromID,
		ToVersionID:   toID,
		UnifiedDiff:   unified,
		FromContent:   fromVer.Content,
		ToContent:     toContent,
	}, nil
}

func (s *ConfigService) Rollback(instanceID uint, filePath string, versionID uint, message string, authorID uint) (uint, error) {
	var ver model.InstanceConfigVersion
	if err := s.db.Where("id = ? AND instance_id = ? AND file_path = ?", versionID, instanceID, filePath).First(&ver).Error; err != nil {
		return 0, err
	}
	src := ver.ID
	id, _, err := s.Write(instanceID, filePath, ver.Content, message, authorID, &src)
	return id, err
}

// CheckCrossFile 对当前文件做跨实例一致性校验（聚合当前实例 + 同节点其它实例）。
func (s *ConfigService) CheckCrossFile(instanceID uint, filePath, content string) ([]map[string]any, error) {
	inst, _, err := s.client(instanceID)
	if err != nil {
		return nil, err
	}
	// 当前实例：解析传入 content
	current := parseToSchema(inst, filePath, content)
	// 同节点其它实例的最新版本内容
	var siblings []model.Instance
	if err := s.db.Where("node_id = ? AND id <> ?", inst.NodeID, instanceID).Find(&siblings).Error; err != nil {
		return nil, err
	}
	cfgs := []schema.ParsedConfig{current}
	for _, sib := range siblings {
		var latest model.InstanceConfigVersion
		if err := s.db.Where("instance_id = ? AND file_path = ?", sib.ID, filePath).Order("id DESC").First(&latest).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		cfgs = append(cfgs, parseToSchema(&sib, filePath, latest.Content))
	}
	issues := schema.CheckAll(cfgs)
	out := make([]map[string]any, 0, len(issues))
	for _, it := range issues {
		out = append(out, map[string]any{"level": it.Level, "message": it.Message, "key": it.Key})
	}
	return out, nil
}

func (s *ConfigService) client(instanceID uint) (*model.Instance, *cpgrpc.Client, error) {
	var inst model.Instance
	if err := s.db.Preload("Node").First(&inst, instanceID).Error; err != nil {
		return nil, nil, err
	}
	if inst.WorkDir == "" {
		return nil, nil, fmt.Errorf("实例未设置工作目录")
	}
	client, ok := s.pool.Get(inst.Node.UUID)
	if !ok {
		return nil, nil, ErrNodeNotConnected
	}
	return &inst, client, nil
}

// parseToSchema 把（实例, 路径, 内容）解析为 schema.ParsedConfig，便于复用校验。
func parseToSchema(inst *model.Instance, filePath, content string) schema.ParsedConfig {
	ext := strings.ToLower(filepath.Ext(filePath))
	format := "txt"
	switch ext {
	case ".properties":
		format = "properties"
	case ".yml", ".yaml":
		format = "yaml"
	case ".toml":
		format = "toml"
	case ".json":
		format = "json"
	}
	fields := schema.BuildFields(format, content)
	if m := schema.MatchPath(filePath); m != nil {
		fields = schema.ApplyTypes(fields, m)
	}
	return schema.ParsedConfig{Path: fmt.Sprintf("instance=%d:%s", inst.ID, filePath), Fields: fields}
}

func validationMap(v *workerpb.ConfigValidationResult) map[string]any {
	if v == nil {
		return map[string]any{"valid": true, "issues": []any{}}
	}
	return map[string]any{"valid": v.Valid, "issues": v.Issues}
}

// unifiedDiff 用 difflib 生成 unified diff 输出。
func unifiedDiff(label, a, b, ctxA, ctxB string) (string, error) {
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(a),
		B:        difflib.SplitLines(b),
		FromFile: ctxA + " " + label,
		ToFile:   ctxB + " " + label,
		Context:  3,
	})
	if err != nil {
		return "", fmt.Errorf("生成 diff 失败: %w", err)
	}
	return diff, nil
}

// schemaToJSON 序列化为 JSON 字符串，便于前端消费。
func schemaToJSON(m *schema.ModelSchema) string {
	if m == nil {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// 防止 schema 导入被 Go 编译器标记未用（BuildFields/MatchPath/ApplyTypes 已在 parseToSchema 中引用）。
var _ = schemaToJSON

func isConfigFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".properties" || ext == ".yml" || ext == ".yaml" || ext == ".toml" || ext == ".json" || ext == ".txt" || ext == ".conf"
}
