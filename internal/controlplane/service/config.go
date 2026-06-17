package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
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
	ID        uint      `json:"id"`
	FilePath  string    `json:"filePath"`
	Message   string    `json:"message"`
	AuthorID  uint      `json:"authorId"`
	CreatedAt time.Time `json:"createdAt"`
}

type ConfigReadResult struct {
	Path       string           `json:"path"`
	Format     string           `json:"format"`
	Content    string           `json:"content"`
	Fields     []map[string]any `json:"fields"`
	SchemaJSON string           `json:"schemaJson"`
	Validation map[string]any   `json:"validation"`
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
	return &ConfigReadResult{Path: resp.Path, Format: resp.Format, Content: resp.Content, Fields: fields, SchemaJSON: resp.SchemaJson, Validation: validation}, nil
}

func (s *ConfigService) Write(instanceID uint, filePath, content, message string, authorID uint) (uint, map[string]any, error) {
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
	ver := model.InstanceConfigVersion{InstanceID: instanceID, FilePath: filePath, ContentHash: hex.EncodeToString(h[:]), Content: content, Message: message, AuthorID: authorID}
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
		out = append(out, ConfigVersion{ID: r.ID, FilePath: r.FilePath, Message: r.Message, AuthorID: r.AuthorID, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

func (s *ConfigService) Rollback(instanceID uint, filePath string, versionID uint, message string, authorID uint) (uint, error) {
	var ver model.InstanceConfigVersion
	if err := s.db.Where("id = ? AND instance_id = ? AND file_path = ?", versionID, instanceID, filePath).First(&ver).Error; err != nil {
		return 0, err
	}
	id, _, err := s.Write(instanceID, filePath, ver.Content, message, authorID)
	return id, err
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

func validationMap(v *workerpb.ConfigValidationResult) map[string]any {
	if v == nil {
		return map[string]any{"valid": true, "issues": []any{}}
	}
	return map[string]any{"valid": v.Valid, "issues": v.Issues}
}

func isConfigFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".properties" || ext == ".yml" || ext == ".yaml" || ext == ".toml" || ext == ".json" || ext == ".txt" || ext == ".conf"
}
