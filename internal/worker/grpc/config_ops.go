package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/wxys233/JianManager/proto/workerpb"
	"gopkg.in/yaml.v3"
)

// ListConfigFiles 列出实例工作目录下可管理的配置文件。
func (s *Server) ListConfigFiles(ctx context.Context, req *workerpb.ListConfigFilesRequest) (*workerpb.ListConfigFilesResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}
	dir := filepath.Join(inst.WorkDir, req.Path)
	if err := validatePath(inst.WorkDir, dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取目录失败: %w", err)
	}
	files := make([]*workerpb.ConfigFileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		format, ok := detectConfigFormat(name)
		if !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(req.Path, name))
		files = append(files, &workerpb.ConfigFileInfo{Path: rel, Format: format, Size: info.Size(), UpdatedAt: info.ModTime().Unix(), Supported: true})
	}
	return &workerpb.ListConfigFilesResponse{Files: files}, nil
}

// ReadConfig 读取配置原文并输出 MVP 解析结果。
func (s *Server) ReadConfig(ctx context.Context, req *workerpb.ReadConfigRequest) (*workerpb.ReadConfigResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}
	path := filepath.Join(inst.WorkDir, req.Path)
	if err := validatePath(inst.WorkDir, path); err != nil {
		return nil, err
	}
	format, _ := detectConfigFormat(req.Path)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	text := string(content)
	return &workerpb.ReadConfigResponse{
		Path:       req.Path,
		Format:     format,
		Content:    text,
		Fields:     parseConfigFields(format, text),
		SchemaJson: `{"known":false}`,
		Validation: validateConfigText(format, text),
		Model:      modelNameForPath(req.Path),
	}, nil
}

// modelNameForPath 返回已知 schema 名称；未知返回空。
func modelNameForPath(p string) string {
	base := strings.ToLower(filepath.Base(p))
	switch base {
	case "server.properties":
		return "server.properties"
	case "spigot.yml":
		return "spigot.yml"
	case "bukkit.yml":
		return "bukkit.yml"
	case "paper-global.yml", "paper.yml":
		return "paper-global.yml"
	case "velocity.toml":
		return "velocity.toml"
	case "config.yml":
		if strings.Contains(filepath.ToSlash(strings.ToLower(p)), "velocity/") {
			return "velocity.toml"
		}
		return "config.yml"
	}
	return ""
}

// WriteConfig 写入配置原文；properties 以行级 round-trip 为 MVP，其他格式仅校验后原文保存。
func (s *Server) WriteConfig(ctx context.Context, req *workerpb.WriteConfigRequest) (*workerpb.WriteConfigResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}
	path := filepath.Join(inst.WorkDir, req.Path)
	if err := validatePath(inst.WorkDir, path); err != nil {
		return nil, err
	}
	format, _ := detectConfigFormat(req.Path)
	validation := validateConfigText(format, req.Content)
	if !validation.Valid {
		return &workerpb.WriteConfigResponse{Success: false, Error: "配置格式校验失败", Validation: validation}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return &workerpb.WriteConfigResponse{Success: false, Error: fmt.Sprintf("创建目录失败: %v", err), Validation: validation}, nil
	}
	if err := os.WriteFile(path, []byte(req.Content), 0644); err != nil {
		return &workerpb.WriteConfigResponse{Success: false, Error: fmt.Sprintf("写入配置失败: %v", err), Validation: validation}, nil
	}
	return &workerpb.WriteConfigResponse{Success: true, Validation: validation}, nil
}

func (s *Server) ValidateConfig(ctx context.Context, req *workerpb.ValidateConfigRequest) (*workerpb.ValidateConfigResponse, error) {
	format, _ := detectConfigFormat(req.Path)
	return &workerpb.ValidateConfigResponse{Validation: validateConfigText(format, req.Content)}, nil
}

func detectConfigFormat(path string) (string, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".properties":
		return "properties", true
	case ".yml", ".yaml":
		return "yaml", true
	case ".toml":
		return "toml", true
	case ".json":
		return "json", true
	case ".txt", ".conf":
		return "txt", true
	default:
		return "txt", false
	}
}

func parseConfigFields(format, content string) []*workerpb.ConfigField {
	if format != "properties" {
		return []*workerpb.ConfigField{}
	}
	fields := []*workerpb.ConfigField{}
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			continue
		}
		sep := strings.IndexAny(line, "=:")
		if sep < 0 {
			continue
		}
		key := strings.TrimSpace(line[:sep])
		value := strings.TrimSpace(line[sep+1:])
		fields = append(fields, &workerpb.ConfigField{Key: key, Value: value, Type: inferScalarType(value), Line: int32(i + 1)})
	}
	return fields
}

func validateConfigText(format, content string) *workerpb.ConfigValidationResult {
	result := &workerpb.ConfigValidationResult{Valid: true}
	var err error
	switch format {
	case "json":
		var v any
		err = json.Unmarshal([]byte(content), &v)
	case "yaml":
		var v any
		err = yaml.Unmarshal([]byte(content), &v)
	case "toml":
		var v any
		err = toml.Unmarshal([]byte(content), &v)
	}
	if err != nil {
		result.Valid = false
		result.Issues = append(result.Issues, &workerpb.ValidationIssue{Level: "error", Message: err.Error()})
	}
	return result
}

func inferScalarType(value string) string {
	lower := strings.ToLower(value)
	if lower == "true" || lower == "false" {
		return "bool"
	}
	var n int
	if _, err := fmt.Sscanf(value, "%d", &n); err == nil {
		return "int"
	}
	return "string"
}
