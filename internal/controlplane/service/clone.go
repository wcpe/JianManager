package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// 克隆相关错误（FR-036）。
var (
	ErrSourceNotBackend = errors.New("源实例不是 backend 子服")
	ErrSourceRunning    = errors.New("源实例正在运行，请先停止再复制")
)

// defaultCloneExcludes 是复制工作目录时默认排除的运行态文件（FR-036）。
var defaultCloneExcludes = []string{
	"session.lock",
	"*.pid",
	"logs",
	"crash-reports",
	"cache",
	"usercache.json",
	"libraries/.cache",
}

// CloneService 一键复制 backend 子服为独立新实例（FR-036）。
type CloneService struct {
	db       *gorm.DB
	pool     *cpgrpc.ClientPool
	instance *InstanceService
	reg      *RegistrationService
}

// NewCloneService 创建克隆服务。
func NewCloneService(db *gorm.DB, pool *cpgrpc.ClientPool, instance *InstanceService, reg *RegistrationService) *CloneService {
	return &CloneService{db: db, pool: pool, instance: instance, reg: reg}
}

// CloneInstanceRequest 复制请求。
type CloneInstanceRequest struct {
	Name               string `json:"name" binding:"required,min=1,max=128"`
	Motd               string `json:"motd"`
	LevelName          string `json:"levelName"`
	RegisterToProxyIDs []uint `json:"registerToProxyIds"`
	DryRun             bool   `json:"dryRun"`
}

// CloneAllocation 复制为新实例分配的资源。
type CloneAllocation struct {
	WorkDir    string `json:"workDir"`
	ServerPort int    `json:"serverPort"`
	RCONPort   int    `json:"rconPort"`
	QueryPort  int    `json:"queryPort"`
}

// CloneResult 复制结果（dryRun 时 Instance 为空）。
type CloneResult struct {
	Instance      *model.Instance    `json:"instance,omitempty"`
	Allocated     CloneAllocation    `json:"allocated"`
	Excluded      []string           `json:"excluded"`
	Registrations []RegistrationView `json:"registrations,omitempty"`
	Warnings      []string           `json:"warnings,omitempty"`
	DryRun        bool               `json:"dryRun"`
}

// Clone 复制源 backend 子服。dryRun=true 仅预检（分配预览 + 冲突告警）不落盘。
func (s *CloneService) Clone(ctx context.Context, srcID uint, req CloneInstanceRequest) (*CloneResult, error) {
	var src model.Instance
	if err := s.db.First(&src, srcID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, fmt.Errorf("查询源实例失败: %w", err)
	}
	if src.Role != model.InstanceRoleBackend || src.Type != model.InstanceTypeMinecraftJava {
		return nil, ErrSourceNotBackend
	}
	if src.Status != model.InstanceStatusStopped && src.Status != model.InstanceStatusCrashed {
		return nil, ErrSourceRunning
	}

	ports, err := allocPortsForNode(s.db, src.NodeID)
	if err != nil {
		return nil, err
	}

	var warnings []string
	// 名称冲突预检：仅告警，不阻断（实例名非唯一约束）。
	var nameDup int64
	s.db.Model(&model.Instance{}).Where("name = ?", req.Name).Count(&nameDup)
	if nameDup > 0 {
		warnings = append(warnings, fmt.Sprintf("已存在同名实例「%s」，复制仍会创建独立新实例", req.Name))
	}

	result := &CloneResult{
		Allocated: CloneAllocation{ServerPort: ports.ServerPort, RCONPort: ports.RCONPort, QueryPort: ports.QueryPort},
		Excluded:  defaultCloneExcludes,
		Warnings:  warnings,
		DryRun:    req.DryRun,
	}
	if req.DryRun {
		// 预览工作目录（实际由 Create 分配，shortid 不同；此处仅示意）。
		result.Allocated.WorkDir = allocWorkDirRel(req.Name)
		return result, nil
	}

	// 复制源的环境变量与所属组。
	var envVars map[string]string
	if strings.TrimSpace(src.EnvVars) != "" {
		_ = json.Unmarshal([]byte(src.EnvVars), &envVars)
	}
	var gi model.GroupInstance
	groupID := uint(0)
	if err := s.db.Where("instance_id = ?", src.ID).First(&gi).Error; err == nil {
		groupID = gi.GroupID
	}

	// 创建独立新实例（系统分配新目录；同款结构化启动/JDK；新端口与新 rcon 密码）。
	dst, err := s.instance.Create(CreateInstanceRequest{
		NodeID:           src.NodeID,
		Name:             req.Name,
		Type:             src.Type,
		Role:             model.InstanceRoleBackend,
		ProcessType:      src.ProcessType,
		StartCommand:     src.StartCommand,
		JDKID:            src.JDKID,
		JavaMajorVersion: src.JavaMajorVersion,
		LaunchSpec:       src.LaunchSpec,
		EnvVars:          envVars,
		AutoRestart:      src.AutoRestart,
		GroupID:          groupID,
		ServerPort:       ports.ServerPort,
		RCONPort:         ports.RCONPort,
		QueryPort:        ports.QueryPort,
		RCONPassword:     randRCONPassword(),
	})
	if err != nil {
		return result, err
	}
	result.Instance = dst
	result.Allocated.WorkDir = dst.WorkDir

	// 复制工作目录（排除运行态文件）。
	if err := s.cloneWorkDirOnWorker(ctx, &src, dst); err != nil {
		return result, fmt.Errorf("复制工作目录失败: %w", err)
	}

	// 配置修正：新端口/rcon 密码/可选 motd、level-name；保留 forwarding secret（随目录复制）。
	if w := s.fixupConfig(ctx, dst, req); w != "" {
		result.Warnings = append(result.Warnings, w)
	}

	// 可选注册进所选代理（触发 FR-035 写代理配置 + 下发 secret）。
	for _, proxyID := range req.RegisterToProxyIDs {
		view, rerr := s.reg.Create(proxyID, CreateRegistrationRequest{BackendID: dst.ID})
		if view != nil {
			result.Registrations = append(result.Registrations, *view)
		}
		if rerr != nil {
			result.Warnings = append(result.Warnings, rerr.Error())
		}
	}
	return result, nil
}

// cloneWorkDirOnWorker 确保源/目标在 Worker 注册后，调用 CloneWorkDir 本机复制。
func (s *CloneService) cloneWorkDirOnWorker(ctx context.Context, src, dst *model.Instance) error {
	if err := s.instance.EnsureRegistered(src); err != nil {
		return fmt.Errorf("源实例注册到 Worker 失败: %w", err)
	}
	if err := s.instance.EnsureRegistered(dst); err != nil {
		return fmt.Errorf("目标实例注册到 Worker 失败: %w", err)
	}

	var node model.Node
	if err := s.db.First(&node, src.NodeID).Error; err != nil {
		return fmt.Errorf("查找节点失败: %w", err)
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return fmt.Errorf("节点 %s 未连接", node.UUID)
	}

	cloneCtx, cancel := context.WithTimeout(ctx, 16*time.Minute)
	defer cancel()
	resp, err := client.Worker.CloneWorkDir(cloneCtx, &workerpb.CloneWorkDirRequest{
		SrcInstanceUuid: src.UUID,
		DstInstanceUuid: dst.UUID,
		Exclude:         defaultCloneExcludes,
	})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// fixupConfig 修正目标 server.properties（端口/rcon 密码/motd/level-name）。返回告警（空表示成功）。
func (s *CloneService) fixupConfig(ctx context.Context, dst *model.Instance, req CloneInstanceRequest) string {
	var node model.Node
	if err := s.db.First(&node, dst.NodeID).Error; err != nil {
		return fmt.Sprintf("查找节点失败: %v", err)
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return "节点未连接，server.properties 未修正"
	}

	rCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	existing := ""
	if resp, err := client.Worker.ReadConfig(rCtx, &workerpb.ReadConfigRequest{InstanceUuid: dst.UUID, Path: "server.properties"}); err == nil && resp != nil {
		existing = resp.Content
	}

	kv := map[string]string{
		"server-port":  fmt.Sprintf("%d", dst.ServerPort),
		"query.port":   fmt.Sprintf("%d", dst.QueryPort),
		"rcon.port":    fmt.Sprintf("%d", dst.RCONPort),
		"rcon.password": dst.RCONPassword,
	}
	if strings.TrimSpace(req.Motd) != "" {
		kv["motd"] = req.Motd
	}
	if strings.TrimSpace(req.LevelName) != "" {
		kv["level-name"] = req.LevelName
	}

	var content string
	if strings.TrimSpace(existing) == "" {
		// 源无 server.properties：生成基础档（代理就绪 online-mode=false）再叠加 motd/level-name。
		content = patchProperties(buildServerProperties(dst.ServerPort, dst.RCONPort, dst.QueryPort, dst.RCONPassword, false), kv)
	} else {
		content = patchProperties(existing, kv)
	}

	wCtx, cancel2 := context.WithTimeout(ctx, 10*time.Second)
	defer cancel2()
	resp, err := client.Worker.WriteConfig(wCtx, &workerpb.WriteConfigRequest{InstanceUuid: dst.UUID, Path: "server.properties", Content: content})
	if err != nil {
		return fmt.Sprintf("写 server.properties 失败: %v", err)
	}
	if resp != nil && !resp.Success {
		return fmt.Sprintf("写 server.properties 失败: %s", resp.Error)
	}
	return ""
}

// patchProperties 在 .properties 文本中就地修改给定键的值，保留其它行与注释；缺失键追加到末尾（按键名排序，稳定输出）。
func patchProperties(content string, kv map[string]string) string {
	lines := strings.Split(content, "\n")
	seen := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if v, ok := kv[key]; ok {
			lines[i] = key + "=" + v
			seen[key] = true
		}
	}
	missing := make([]string, 0)
	for k := range kv {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		lines = append(lines, k+"="+kv[k])
	}
	return strings.Join(lines, "\n")
}
