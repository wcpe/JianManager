package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-396 创建/搭建/导入/克隆/重建/配置更新与任务查询。
func init() {
	registerToolSpecs(
		toolSpec{
			Def: ToolDef{
				Name:        "instance_create",
				Description: "在 scope 内节点上创建实例（须 instance.provision）；通用自带 jar/docker 场景",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"nodeId":       map[string]any{"type": "number", "description": "目标节点 ID"},
						"name":         map[string]any{"type": "string", "description": "实例名称"},
						"type":         map[string]any{"type": "string", "description": "实例类型（如 minecraft_java）"},
						"processType":  map[string]any{"type": "string", "description": "进程类型：direct/daemon/docker"},
						"startCommand": map[string]any{"type": "string", "description": "启动命令（非 docker 必填）"},
						"jdkId":        map[string]any{"type": "number", "description": "可选：绑定 JDK ID"},
						"image":        map[string]any{"type": "string", "description": "可选：docker 镜像"},
						"autoStart":    map[string]any{"type": "boolean"},
						"autoRestart":  map[string]any{"type": "boolean"},
						"serverPort":   map[string]any{"type": "number"},
					},
					"required": []string{"nodeId", "name", "type", "processType"},
				},
			},
			Action: service.AgentActionInstanceCreate,
			Exec:   execInstanceCreate,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_provision_server",
				Description: "一键搭建后端子服（异步，返回 taskId；须 instance.provision）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"nodeId":    map[string]any{"type": "number", "description": "目标节点 ID"},
						"name":      map[string]any{"type": "string"},
						"coreType":  map[string]any{"type": "string", "description": "paper / spongevanilla / spongeforge"},
						"mcVersion": map[string]any{"type": "string"},
						"build":     map[string]any{"type": "number", "description": "0=最新构建"},
						"jdkId":     map[string]any{"type": "number"},
						"memoryMb":  map[string]any{"type": "number"},
					},
					"required": []string{"nodeId", "name", "coreType", "mcVersion"},
				},
			},
			Action: service.AgentActionInstanceProvisionServer,
			Exec:   execInstanceProvisionServer,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_import_inspect",
				Description: "导入前探测节点上的服务器目录（须 instance.provision）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"nodeId": map[string]any{"type": "number"},
						"path":   map[string]any{"type": "string", "description": "节点上的绝对路径"},
					},
					"required": []string{"nodeId", "path"},
				},
			},
			Action: service.AgentActionInstanceImportInspect,
			Exec:   execInstanceImportInspect,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_import",
				Description: "导入现成服务器目录为受管实例（须 instance.provision）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"nodeId":  map[string]any{"type": "number"},
						"path":    map[string]any{"type": "string"},
						"mode":    map[string]any{"type": "string", "description": "in_place 或 migrate"},
						"name":    map[string]any{"type": "string"},
						"jarPath": map[string]any{"type": "string", "description": "相对 path 的 jar 路径"},
						"jdkId":   map[string]any{"type": "number"},
						"memoryMb": map[string]any{"type": "number"},
					},
					"required": []string{"nodeId", "path", "mode", "name", "jarPath"},
				},
			},
			Action: service.AgentActionInstanceImport,
			Exec:   execInstanceImport,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_clone",
				Description: "克隆源 backend 实例（须 instance.provision；源实例在 scope 内；支持 dryRun）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":      map[string]any{"type": "number", "description": "源实例 ID"},
						"name":    map[string]any{"type": "string"},
						"mode":    map[string]any{"type": "string", "description": "quick 或 advanced（默认 advanced）"},
						"dryRun":  map[string]any{"type": "boolean"},
						"include": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"exclude": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
					"required": []string{"id", "name"},
				},
			},
			Action: service.AgentActionInstanceClone,
			Exec:   execInstanceClone,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_rebuild",
				Description: "重建损毁实例（复用原搭建参数，异步返回 taskId；须 instance.provision）",
				InputSchema: idSchema("实例 ID"),
			},
			Action: service.AgentActionInstanceRebuild,
			Exec:   execInstanceRebuild,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "instance_update_config",
				Description: "更新实例结构化配置字段（名称/启动命令/自启/JDK/资源限额等；须 instance.configure）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":           map[string]any{"type": "number"},
						"name":         map[string]any{"type": "string"},
						"startCommand": map[string]any{"type": "string"},
						"autoStart":    map[string]any{"type": "boolean"},
						"autoRestart":  map[string]any{"type": "boolean"},
						"jdkId":        map[string]any{"type": "number"},
						"cpuLimit":     map[string]any{"type": "number"},
						"memLimitMb":   map[string]any{"type": "number"},
						"diskLimitMb":  map[string]any{"type": "number"},
					},
					"required": []string{"id"},
				},
			},
			Action: service.AgentActionInstanceUpdateConfig,
			Exec:   execInstanceUpdateConfig,
		},
		toolSpec{
			Def: ToolDef{
				Name:        "task_get",
				Description: "查询本 Token 可访问目标关联的异步任务进度（须 instance.read）",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"taskId": map[string]any{"type": "string", "description": "任务 ID"},
					},
					"required": []string{"taskId"},
				},
			},
			Action: service.AgentActionTaskGet,
			Exec:   execTaskGet,
		},
	)
}

func execInstanceCreate(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	_ = ctx
	nodeID, err := requireNodeID(args)
	if err != nil {
		return toolErr(err.Error())
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	if _, err := deps.Agent.AuthorizeNodeAction(p, action, nodeID); err != nil {
		return toolForbidden(err)
	}
	if deps.Instance == nil {
		return toolErr("实例服务不可用")
	}
	req := service.CreateInstanceRequest{
		NodeID:       nodeID,
		Name:         strings.TrimSpace(stringArg(args, "name")),
		Type:         model.InstanceType(strings.TrimSpace(stringArg(args, "type"))),
		ProcessType:  model.ProcessType(strings.TrimSpace(stringArg(args, "processType"))),
		StartCommand: stringArg(args, "startCommand"),
		Image:        stringArg(args, "image"),
		AutoStart:    boolArg(args, "autoStart"),
		AutoRestart:  boolArg(args, "autoRestart"),
	}
	if v, ok := args["jdkId"]; ok {
		if n, e := toUint(v); e == nil {
			req.JDKID = n
		}
	}
	if v, ok := args["serverPort"]; ok {
		if n, e := toUint(v); e == nil {
			req.ServerPort = int(n)
		}
	}
	inst, err := deps.Instance.Create(req)
	if err != nil {
		return toolErr("创建实例失败: " + err.Error())
	}
	return toolOK(inst)
}

func execInstanceProvisionServer(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	nodeID, err := requireNodeID(args)
	if err != nil {
		return toolErr(err.Error())
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	if _, err := deps.Agent.AuthorizeNodeAction(p, action, nodeID); err != nil {
		return toolForbidden(err)
	}
	if deps.Provision == nil {
		return toolErr("搭建服务不可用")
	}
	req := service.ProvisionServerRequest{
		NodeID:    nodeID,
		Name:      strings.TrimSpace(stringArg(args, "name")),
		CoreType:  strings.TrimSpace(stringArg(args, "coreType")),
		MCVersion: strings.TrimSpace(stringArg(args, "mcVersion")),
	}
	if v, ok := args["build"]; ok {
		if n, e := toUint(v); e == nil {
			req.Build = int(n)
		}
	}
	if v, ok := args["jdkId"]; ok {
		if n, e := toUint(v); e == nil {
			req.JDKID = n
		}
	}
	if v, ok := args["memoryMb"]; ok {
		if n, e := toUint(v); e == nil {
			req.MemoryMb = int(n)
		}
	}
	// Agent 非平台用户：createdBy=0。
	inst, taskID, err := deps.Provision.ProvisionServerAsync(ctx, req, 0)
	if err != nil {
		return toolErr("搭建失败: " + err.Error())
	}
	return toolOK(map[string]any{"instance": inst, "taskId": taskID})
}

func execInstanceImportInspect(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	nodeID, err := requireNodeID(args)
	if err != nil {
		return toolErr(err.Error())
	}
	path := strings.TrimSpace(stringArg(args, "path"))
	if path == "" {
		return toolErr("缺少 path")
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	if _, err := deps.Agent.AuthorizeNodeAction(p, action, nodeID); err != nil {
		return toolForbidden(err)
	}
	if deps.Import == nil {
		return toolErr("导入服务不可用")
	}
	r, err := deps.Import.Inspect(ctx, nodeID, path)
	if err != nil {
		return toolErr("探测失败: " + err.Error())
	}
	return toolOK(r)
}

func execInstanceImport(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	nodeID, err := requireNodeID(args)
	if err != nil {
		return toolErr(err.Error())
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	if _, err := deps.Agent.AuthorizeNodeAction(p, action, nodeID); err != nil {
		return toolForbidden(err)
	}
	if deps.Import == nil {
		return toolErr("导入服务不可用")
	}
	req := service.ImportServerRequest{
		NodeID:  nodeID,
		Path:    strings.TrimSpace(stringArg(args, "path")),
		Mode:    strings.TrimSpace(stringArg(args, "mode")),
		Name:    strings.TrimSpace(stringArg(args, "name")),
		JarPath: strings.TrimSpace(stringArg(args, "jarPath")),
	}
	if v, ok := args["jdkId"]; ok {
		if n, e := toUint(v); e == nil {
			req.JDKID = n
		}
	}
	if v, ok := args["memoryMb"]; ok {
		if n, e := toUint(v); e == nil {
			req.MemoryMb = int(n)
		}
	}
	inst, taskID, err := deps.Import.Import(ctx, req)
	if err != nil {
		return toolErr("导入失败: " + err.Error())
	}
	return toolOK(map[string]any{"instance": inst, "taskId": taskID})
}

func execInstanceClone(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	if _, _, err := deps.Agent.AuthorizeInstanceAction(p, action, id); err != nil {
		return toolForbidden(err)
	}
	if deps.Clone == nil {
		return toolErr("克隆服务不可用")
	}
	req := service.CloneInstanceRequest{
		Name:   strings.TrimSpace(stringArg(args, "name")),
		Mode:   strings.TrimSpace(stringArg(args, "mode")),
		DryRun: boolArg(args, "dryRun"),
	}
	if v, ok := args["include"]; ok {
		req.Include = toStringSlice(v)
	}
	if v, ok := args["exclude"]; ok {
		req.Exclude = toStringSlice(v)
	}
	r, err := deps.Clone.Clone(ctx, id, req)
	if err != nil {
		return toolErr("克隆失败: " + err.Error())
	}
	return toolOK(r)
}

func execInstanceRebuild(ctx context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	if _, _, err := deps.Agent.AuthorizeInstanceAction(p, action, id); err != nil {
		return toolForbidden(err)
	}
	if deps.Provision == nil {
		return toolErr("搭建服务不可用")
	}
	taskID, err := deps.Provision.RebuildInstance(ctx, id, 0)
	if err != nil {
		return toolErr("重建失败: " + err.Error())
	}
	return toolOK(map[string]any{"taskId": taskID})
}

func execInstanceUpdateConfig(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	id, e := requireID(args)
	if e != nil {
		return toolErr(e.Error())
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	if _, _, err := deps.Agent.AuthorizeInstanceAction(p, action, id); err != nil {
		return toolForbidden(err)
	}
	if deps.Instance == nil {
		return toolErr("实例服务不可用")
	}
	var f service.UpdateInstanceFields
	if v, ok := args["name"].(string); ok {
		f.Name = &v
	}
	if v, ok := args["startCommand"].(string); ok {
		f.StartCommand = &v
	}
	if v, ok := args["autoStart"].(bool); ok {
		f.AutoStart = &v
	}
	if v, ok := args["autoRestart"].(bool); ok {
		f.AutoRestart = &v
	}
	if v, ok := args["jdkId"]; ok {
		if n, e := toUint(v); e == nil {
			f.JDKID = &n
		}
	}
	if v, ok := args["cpuLimit"]; ok {
		if n, e := toFloat(v); e == nil {
			f.CPULimit = &n
		}
	}
	if v, ok := args["memLimitMb"]; ok {
		if n, e := toInt64(v); e == nil {
			f.MemLimitMB = &n
		}
	}
	if v, ok := args["diskLimitMb"]; ok {
		if n, e := toInt64(v); e == nil {
			f.DiskLimitMB = &n
		}
	}
	inst, err := deps.Instance.Update(id, f)
	if err != nil {
		return toolErr("更新配置失败: " + err.Error())
	}
	return toolOK(inst)
}

func execTaskGet(_ context.Context, deps ToolDeps, p *service.AgentPrincipal, action string, args map[string]any) ToolResult {
	_ = action
	taskID := strings.TrimSpace(stringArg(args, "taskId"))
	if taskID == "" {
		return toolErr("缺少 taskId")
	}
	if deps.Agent == nil {
		return toolErr("策略服务不可用")
	}
	t, logs, err := deps.Agent.GetTaskForAgent(p, taskID)
	if err != nil {
		return toolForbidden(err)
	}
	return toolOK(map[string]any{"task": t, "logs": logs})
}

func requireNodeID(args map[string]any) (uint, error) {
	v, ok := args["nodeId"]
	if !ok {
		return 0, fmt.Errorf("缺少 nodeId")
	}
	return toUint(v)
}

func boolArg(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	default:
		return 0, fmt.Errorf("期望数字")
	}
}

func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case float64:
		return int64(n), nil
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	default:
		return 0, fmt.Errorf("期望数字")
	}
}
