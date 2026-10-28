package grpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/wcpe/JianManager/proto/workerpb"
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
	if err := validateNonRootPath(inst.WorkDir, path); err != nil {
		return nil, err
	}
	format, supported := detectConfigFormat(req.Path)
	if !supported {
		return nil, fmt.Errorf("仅允许操作配置文件")
	}
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
//
// FR-530 起完整走「越界检查 → 写前快照 → 原子写入 → 读回校验」链路：
// 验收要求「写入中 kill -9 后原文件完好」与「篡改磁盘内容返回 WRITE_VERIFY_FAILED」，
// 因此不能再用 os.WriteFile 直接覆盖。
func (s *Server) WriteConfig(ctx context.Context, req *workerpb.WriteConfigRequest) (*workerpb.WriteConfigResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}
	// 路径越界检查（绝对路径 / `..` 穿越 / 符号链接逃逸）。
	path, err := resolveSafePath(inst.WorkDir, req.Path)
	if err != nil {
		return &workerpb.WriteConfigResponse{
			Success:   false,
			Error:     err.Error(),
			ErrorCode: ErrCodePathEscape,
		}, nil
	}
	format, supported := detectConfigFormat(req.Path)
	if !supported {
		return &workerpb.WriteConfigResponse{
			Success:   false,
			Error:     "仅允许操作配置文件",
			ErrorCode: ErrCodeValidationFailed,
		}, nil
	}
	validation := validateConfigText(format, req.Content)
	if !validation.Valid {
		return &workerpb.WriteConfigResponse{
			Success:    false,
			Error:      "配置格式校验失败",
			ErrorCode:  ErrCodeValidationFailed,
			Validation: validation,
		}, nil
	}
	finalContent := req.Content
	// YAML AST round-trip：保留注释与键顺序。
	if format == "yaml" {
		if normalized, err := roundTripYAML(req.Content); err == nil {
			finalContent = normalized
		}
	}
	payload := []byte(finalContent)

	// 同实例同路径串行化：避免两个写并发 rename 互相覆盖、快照被冲掉。
	unlock := globalPathWriteLocks.lock(writeLockKey(inst.UUID, path))
	defer unlock()

	// 写前快照（仅原文件存在且内容不同）。
	backupRel, backupCreated, err := writeSnapshot(inst.WorkDir, path, payload, defaultBackupRetention)
	if err != nil {
		return &workerpb.WriteConfigResponse{
			Success:    false,
			Error:      fmt.Sprintf("写前快照失败: %v", err),
			ErrorCode:  ErrCodeBackupFailed,
			Validation: validation,
		}, nil
	}

	// 原子写入：临时文件 + fsync + rename + fsync 目录。
	if err := atomicWriteFile(path, payload); err != nil {
		return &workerpb.WriteConfigResponse{
			Success:       false,
			Error:         fmt.Sprintf("写入配置失败: %v", err),
			ErrorCode:     ErrCodeWriteFailed,
			Validation:    validation,
			BackupCreated: backupCreated,
			BackupPath:    backupRel,
		}, nil
	}

	// 读回校验：重开文件读全量、算 sha256。
	actualHash, err := hashFile(path)
	if err != nil {
		return &workerpb.WriteConfigResponse{
			Success:       false,
			Error:         fmt.Sprintf("读回校验失败: %v", err),
			ErrorCode:     ErrCodeWriteVerifyFailed,
			Validation:    validation,
			BackupCreated: backupCreated,
			BackupPath:    backupRel,
		}, nil
	}
	// expected_hash 为空表示调用方不做校验（兼容旧 CP），此时仅回填 actual_hash。
	if want := strings.TrimSpace(req.ExpectedHash); want != "" {
		// 大小写不敏感比较：CP 侧可能传大写 hex。
		if !strings.EqualFold(want, actualHash) {
			return &workerpb.WriteConfigResponse{
				Success:       false,
				Error:         fmt.Sprintf("读回校验失败：写入内容与磁盘不一致（期望 %s，实际 %s），可能是磁盘故障或有并发写", want, actualHash),
				ErrorCode:     ErrCodeWriteVerifyFailed,
				Validation:    validation,
				ActualHash:    actualHash,
				BackupCreated: backupCreated,
				BackupPath:    backupRel,
			}, nil
		}
	}
	return &workerpb.WriteConfigResponse{
		Success:       true,
		Validation:    validation,
		ActualHash:    actualHash,
		BackupCreated: backupCreated,
		BackupPath:    backupRel,
	}, nil
}

// roundTripYAML 通过 yaml.v3 重新序列化以保留注释、锚点引用、键顺序等。
// 解析失败时返回原内容，调用方继续走原文路径。
func roundTripYAML(content string) (string, error) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(content), &node); err != nil {
		return content, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return content, err
	}
	_ = enc.Close()
	return buf.String(), nil
}

func (s *Server) ValidateConfig(ctx context.Context, req *workerpb.ValidateConfigRequest) (*workerpb.ValidateConfigResponse, error) {
	format, _ := detectConfigFormat(req.Path)
	return &workerpb.ValidateConfigResponse{Validation: validateConfigText(format, req.Content)}, nil
}

func detectConfigFormat(path string) (string, bool) {
	// Velocity 的 forwarding.secret 是无扩展名的密钥文件，属代理搭建期必需配置；
	// 这里仅放行该确切文件名，不放开任意无扩展名路径（权限面最小化）。
	if strings.EqualFold(filepath.Base(path), "forwarding.secret") {
		return "txt", true
	}
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
