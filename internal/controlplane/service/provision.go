package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	cpembed "github.com/wcpe/JianManager/internal/controlplane/embed"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// provisionCoreJar 是一键搭建时核心 jar 在工作目录内的固定文件名，
// 使结构化启动命令与具体 Paper 构建文件名解耦。
const provisionCoreJar = "server.jar"

// ProvisionService 一键搭建 MC 子服（FR-034）：解析核心 → 分配端口/目录 →
// 结构化启动 → 下载核心 + 写基础配置。串起 FR-032/033/034/044/045 的运行时底座。
type ProvisionService struct {
	db       *gorm.DB
	pool     *cpgrpc.ClientPool
	instance *InstanceService
	core     *CoreService
	// bridge 用于建服时签发实例级插件桥 token 并写入探针 config（FR-065，见 ADR-016）。
	// 为 nil 时探针 config 不含 bridge 段（探针只跑 /metrics，不连反向 WS）。
	bridge *PluginBridgeService
}

// NewProvisionService 创建一键搭建服务。
// bridge 可为 nil（未启用插件桥时）；非 nil 时建服自动为实例签发插件桥 token 并下发探针。
func NewProvisionService(db *gorm.DB, pool *cpgrpc.ClientPool, instance *InstanceService, core *CoreService, bridge *PluginBridgeService) *ProvisionService {
	return &ProvisionService{db: db, pool: pool, instance: instance, core: core, bridge: bridge}
}

// ProvisionBukkitRequest 一键搭建 Paper 后端子服请求。
type ProvisionBukkitRequest struct {
	NodeID    uint     `json:"nodeId" binding:"required"`
	Name      string   `json:"name" binding:"required,min=1,max=128"`
	CoreType  string   `json:"coreType" binding:"required"` // 目前支持 paper
	MCVersion string   `json:"mcVersion" binding:"required"`
	Build     int      `json:"build"` // 0 = 最新构建
	JDKID     uint     `json:"jdkId"`
	MemoryMb  int      `json:"memoryMb"`
	JvmArgs   []string `json:"jvmArgs"`
	GroupID   uint     `json:"groupId"`
	// OnlineMode 子服是否向 Mojang 校验正版（缺省 false=代理就绪/离线；独立正版服可传 true）。
	OnlineMode *bool `json:"onlineMode"`
}

// ProvisionBukkit 端到端搭建一个 Paper 后端子服，返回创建的实例（STOPPED，可一键启动）。
func (p *ProvisionService) ProvisionBukkit(ctx context.Context, req ProvisionBukkitRequest) (*model.Instance, error) {
	core, err := p.core.ResolveBuild(ctx, req.CoreType, req.MCVersion, req.Build)
	if err != nil {
		return nil, err
	}

	ports, err := allocPortsForNode(p.db, req.NodeID)
	if err != nil {
		return nil, err
	}

	specJSON, err := json.Marshal(LaunchSpec{MemoryMb: req.MemoryMb, JvmArgs: req.JvmArgs, CoreJar: provisionCoreJar})
	if err != nil {
		return nil, err
	}

	// 创建实例：系统分配工作目录 + 结构化启动 + 绑定 JDK + 端口；daemon 启动不杀服（ADR-003）。
	// Create 内部会派生 java 启动命令并把实例注册到 Worker（创建工作目录）。
	inst, err := p.instance.Create(CreateInstanceRequest{
		NodeID:       req.NodeID,
		Name:         req.Name,
		Type:         model.InstanceTypeMinecraftJava,
		Role:         model.InstanceRoleBackend,
		ProcessType:  model.ProcessTypeDaemon,
		JDKID:        req.JDKID,
		LaunchSpec:   string(specJSON),
		ServerPort:   ports.ServerPort,
		QueryPort:    ports.QueryPort,
		ProbePort:    ports.ProbePort,
		AutoRestart:  true,
		GroupID:      req.GroupID,
	})
	if err != nil {
		return nil, err
	}

	if err := p.provisionOnWorker(ctx, inst, core, boolOr(req.OnlineMode, false)); err != nil {
		// 实例已落库（STOPPED），返回实例与错误，便于用户重试或删除。
		return inst, fmt.Errorf("子服搭建失败: %w", err)
	}
	return inst, nil
}

// NodePorts 返回某节点的端口占用与分配范围（FR-032：系统分配端口的可视化）。
func (p *ProvisionService) NodePorts(nodeID uint) (*NodePortsResult, error) {
	var node model.Node
	if err := p.db.First(&node, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("查找节点失败: %w", err)
	}
	usage, err := NodePortUsage(p.db, nodeID)
	if err != nil {
		return nil, err
	}
	return &NodePortsResult{NodeID: nodeID, Ranges: DefaultPortRanges(), Occupied: usage}, nil
}

// provisionOnWorker 在 Worker 上下载核心 jar 并写入 eula.txt / server.properties。
func (p *ProvisionService) provisionOnWorker(ctx context.Context, inst *model.Instance, core *CoreInfo, onlineMode bool) error {
	var node model.Node
	if err := p.db.First(&node, inst.NodeID).Error; err != nil {
		return fmt.Errorf("查找节点失败: %w", err)
	}
	client, ok := p.pool.Get(node.UUID)
	if !ok {
		return fmt.Errorf("节点 %s 未连接", node.UUID)
	}

	dlCtx, cancel := context.WithTimeout(ctx, 16*time.Minute)
	defer cancel()
	dl, err := client.Worker.DownloadCore(dlCtx, &workerpb.DownloadCoreRequest{
		InstanceUuid: inst.UUID,
		DestFilename: provisionCoreJar,
		DownloadUrl:  core.DownloadURL,
		Sha256:       core.SHA256,
	})
	if err != nil {
		return fmt.Errorf("下载核心失败: %w", err)
	}
	if !dl.Success {
		return fmt.Errorf("下载核心失败: %s", dl.Error)
	}

	cfgCtx, cancel2 := context.WithTimeout(ctx, 15*time.Second)
	defer cancel2()
	configs := []struct{ path, content string }{
		{"eula.txt", "eula=true\n"},
		{"server.properties", buildServerProperties(inst.ServerPort, inst.QueryPort, onlineMode)},
	}
	for _, c := range configs {
		resp, werr := client.Worker.WriteConfig(cfgCtx, &workerpb.WriteConfigRequest{
			InstanceUuid: inst.UUID,
			Path:         c.path,
			Content:      c.content,
		})
		if werr != nil {
			return fmt.Errorf("写入 %s 失败: %w", c.path, werr)
		}
		if !resp.Success {
			return fmt.Errorf("写入 %s 失败: %s", c.path, resp.Error)
		}
	}

	// 部署 ServerProbe 监控探针（FR-010）：CP 内嵌探针 jar 时下发 jar + 开启 /metrics 的 config.yml；
	// 未内嵌（未跑 make embed-probe）则跳过。探针为辅助监控，部署失败仅告警、不阻断建服。
	if jar := cpembed.ServerProbeJar(); len(jar) > 0 {
		probeCtx, cancel3 := context.WithTimeout(ctx, 30*time.Second)
		defer cancel3()
		dp, derr := client.Worker.DeployServerProbe(probeCtx, &workerpb.DeployServerProbeRequest{
			InstanceUuid: inst.UUID,
			Jar:          jar,
			ConfigYaml:   buildServerProbeConfig(inst.ProbePort, p.bridgeConfigBlock(inst.UUID, node.WSPort)),
		})
		switch {
		case derr != nil:
			slog.Warn("部署 ServerProbe 探针失败（不阻断建服）", "instance", inst.UUID, "err", derr)
		case !dp.Success:
			slog.Warn("部署 ServerProbe 探针失败（不阻断建服）", "instance", inst.UUID, "err", dp.Error)
		}
	}
	return nil
}

// buildServerProbeConfig 生成实例的 ServerProbe config.yml：仅本机开启 /metrics 端点于分配的
// probe 端口，token 留空依赖本机 IP 白名单（探针与 Worker 同机，Worker 抓 localhost）。
// bridgeBlock 为插件桥配置段（FR-065，见 ADR-016）；为空时 config 不含 bridge 段（探针只跑 /metrics）。
func buildServerProbeConfig(probePort int, bridgeBlock string) string {
	var b strings.Builder
	b.WriteString("# 本文件由 JianManager 建服时自动生成：开启 ServerProbe /metrics 供 Worker 抓取（FR-010）。\n")
	b.WriteString("# 仅本机回环可访问；如需远程 Prometheus 抓取请改 host 并配置 token/allowed-ips。\n")
	b.WriteString("metrics:\n")
	b.WriteString("  enabled: true\n")
	b.WriteString("  host: \"127.0.0.1\"\n")
	fmt.Fprintf(&b, "  port: %d\n", probePort)
	b.WriteString("  token: \"\"\n")
	b.WriteString("  allowed-ips:\n")
	b.WriteString("    - \"127.0.0.1\"\n")
	if bridgeBlock != "" {
		b.WriteString("\n")
		b.WriteString(bridgeBlock)
	}
	return b.String()
}

// bridgeConfigBlock 为实例签发插件桥 token 并生成探针 config.yml 的 bridge 段（FR-065）。
// bridge 服务未注入或签发失败时返回空串（探针不连反向 WS，监控 /metrics 不受影响、建服不阻断）。
func (p *ProvisionService) bridgeConfigBlock(instanceUUID string, wsPort int) string {
	if p.bridge == nil {
		return ""
	}
	token, err := p.bridge.IssueToken(instanceUUID)
	if err != nil {
		slog.Warn("签发插件桥 token 失败（探针将不连反向 WS，不阻断建服）", "instance", instanceUUID, "err", err)
		return ""
	}
	return p.bridge.BuildBridgeConfigBlock(pluginBridgeWSURL(wsPort), instanceUUID, token)
}

// buildServerProperties 生成基础 server.properties：分配的 server-port、按 onlineMode 设正版校验
//（代理转发场景传 false）与 query。RCON 已退役（FR-067，见 ADR-016）：治理改走 ServerProbe 探针，
// 不再开启 rcon，去除额外暴露面。
func buildServerProperties(serverPort, queryPort int, onlineMode bool) string {
	var b strings.Builder
	b.WriteString("# 由 JianManager 一键开服生成（FR-034）\n")
	fmt.Fprintf(&b, "server-port=%d\n", serverPort)
	fmt.Fprintf(&b, "online-mode=%t\n", onlineMode)
	b.WriteString("enable-query=true\n")
	fmt.Fprintf(&b, "query.port=%d\n", queryPort)
	return b.String()
}
