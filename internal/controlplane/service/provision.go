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
// 使结构化启动命令与具体 Paper/SpongeVanilla 构建文件名解耦。
const provisionCoreJar = "server.jar"

// ProvisionService 一键搭建 MC 子服（FR-034/046）：解析核心 → 分配端口/目录 →
// 结构化启动 → 下载/安装核心 + 写基础配置。串起 FR-032/033/034/044/045/046 的运行时底座。
type ProvisionService struct {
	db       *gorm.DB
	pool     *cpgrpc.ClientPool
	instance *InstanceService
	core     *CoreService
	// bridge 用于建服时签发实例级插件桥 token 并写入探针 config（FR-065，见 ADR-016）。
	// 为 nil 时探针 config 不含 bridge 段（探针只跑 /metrics，不连反向 WS）。
	bridge *PluginBridgeService
	// tasks 任务中心（FR-319）：搭建的下载/写配置阶段经任务异步执行与展示；
	// nil 时（测试/未接线）回退同步执行（旧行为）。
	tasks *TaskService
}

// SetTaskService 注入任务中心（FR-319，main 接线）：非 nil 后搭建走异步任务模式。
func (p *ProvisionService) SetTaskService(t *TaskService) { p.tasks = t }

// NewProvisionService 创建一键搭建服务。
// bridge 可为 nil（未启用插件桥时）；非 nil 时建服自动为实例签发插件桥 token 并下发探针。
func NewProvisionService(db *gorm.DB, pool *cpgrpc.ClientPool, instance *InstanceService, core *CoreService, bridge *PluginBridgeService) *ProvisionService {
	return &ProvisionService{db: db, pool: pool, instance: instance, core: core, bridge: bridge}
}

// ProvisionServerRequest 一键搭建后端子服请求。
type ProvisionServerRequest struct {
	NodeID    uint     `json:"nodeId" binding:"required"`
	Name      string   `json:"name" binding:"required,min=1,max=128"`
	CoreType  string   `json:"coreType" binding:"required"` // paper / spongevanilla / spongeforge
	MCVersion string   `json:"mcVersion" binding:"required"`
	Build     int      `json:"build"` // 0 = 最新构建
	JDKID     uint     `json:"jdkId"`
	MemoryMb  int      `json:"memoryMb"`
	JvmArgs   []string `json:"jvmArgs"`
	GroupID   uint     `json:"groupId"`
	// OnlineMode 子服是否向 Mojang 校验正版（缺省 false=代理就绪/离线；独立正版服可传 true）。
	OnlineMode *bool `json:"onlineMode"`
}

// ProvisionBukkitRequest 保留旧 /instances/provision/bukkit 的请求体兼容。
type ProvisionBukkitRequest = ProvisionServerRequest

// ProvisionServer 端到端搭建一个后端子服，返回创建的实例（STOPPED，可一键启动）。
// coreType 可选 Paper/SpongeVanilla/SpongeForge；代理核心必须走代理搭建入口。
func (p *ProvisionService) ProvisionServer(ctx context.Context, req ProvisionServerRequest) (*model.Instance, error) {
	if IsProxyCore(req.CoreType) {
		return nil, fmt.Errorf("代理核心请使用代理搭建入口: %s", req.CoreType)
	}
	core, err := p.core.ResolveBuild(ctx, req.CoreType, req.MCVersion, req.Build)
	if err != nil {
		return nil, err
	}

	// 创建实例：系统分配工作目录 + 结构化启动 + 绑定 JDK + 端口；daemon 启动不杀服（ADR-003）。
	// Create 内部会派生 java 启动命令并把实例注册到 Worker（创建工作目录）。
	inst, err := p.createProvisionInstance(req, core)
	if err != nil {
		return nil, err
	}

	if err := p.provisionOnWorker(ctx, inst, core, boolOr(req.OnlineMode, false), nil); err != nil {
		// 实例已落库（STOPPED），返回实例与错误，便于用户重试或删除。
		return inst, fmt.Errorf("子服搭建失败: %w", err)
	}
	return inst, nil
}

// ProvisionServerAsync 一键搭建的异步入口（FR-319）：同步段只做核心解析 + 建实例 + 登记任务
// （立即返回，前端不再被慢下载拖到超时），下载/写配置/探针在 CP 后台 goroutine 执行——
// 用独立 context（不挂请求），前端断开不再连锁取消下载（真机事故：Paper CDN ~200KB/s、
// 47MB 需 4 分钟，同步链路被前端超时腰斩后留空壳实例且错误进黑洞）。
// 失败：任务终态含错误链 + 实例 statusReason 标注「搭建未完成」+ slog 落平台日志。
func (p *ProvisionService) ProvisionServerAsync(ctx context.Context, req ProvisionServerRequest, createdBy uint) (*model.Instance, string, error) {
	if p.tasks == nil {
		inst, err := p.ProvisionServer(ctx, req)
		return inst, "", err
	}
	if IsProxyCore(req.CoreType) {
		return nil, "", fmt.Errorf("代理核心请使用代理搭建入口: %s", req.CoreType)
	}
	core, err := p.core.ResolveBuild(ctx, req.CoreType, req.MCVersion, req.Build)
	if err != nil {
		slog.Error("一键搭建失败：解析核心构建", "name", req.Name, "coreType", req.CoreType, "mcVersion", req.MCVersion, "error", err)
		return nil, "", err
	}
	inst, err := p.createProvisionInstance(req, core)
	if err != nil {
		slog.Error("一键搭建失败：创建实例", "name", req.Name, "error", err)
		return nil, "", err
	}

	// 全程「搭建中」标注：实例卡片/详情可见状态，配合启动闸阻止过早启动（真机复现：
	// 异步化后实例秒回 STOPPED 可点启动，但核心还在下载→点启动得 corrupt/缺 jar）。
	_ = p.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status_reason", "搭建中：正在下载核心（完成前请勿启动）").Error
	onlineMode := boolOr(req.OnlineMode, false)
	title := fmt.Sprintf("一键搭建 %s（%s %s）", inst.Name, req.CoreType, req.MCVersion)
	// 迁 FR-323 共享底座：CreateTask→后台 goroutine→阶段进度→终态，statusReason 副作用在 work 内自负。
	// InstanceID 关联使启动闸拦截「核心还在下载就点启动」（FR-319 二轮）。
	taskID := p.tasks.RunAsync(RunSpec{
		NodeID: req.NodeID, InstanceID: inst.ID, Kind: model.TaskKindProvision, Title: title, CreatedBy: createdBy,
	}, func(ctx context.Context, stage func(int, string)) (string, error) {
		if err := p.provisionOnWorker(ctx, inst, core, onlineMode, stage); err != nil {
			// 空壳标注：statusReason 让实例卡片/详情可见「为什么这个实例不可用」，启动前一目了然。
			_ = p.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
				Update("status_reason", "搭建未完成："+err.Error()).Error
			return "", err
		}
		_ = p.db.Model(&model.Instance{}).Where("id = ?", inst.ID).Update("status_reason", "").Error
		slog.Info("一键搭建完成", "instance", inst.Name, "instanceId", inst.ID)
		return "", nil
	})
	if taskID == "" {
		return inst, "", fmt.Errorf("登记搭建任务失败")
	}
	return inst, taskID, nil
}

// createProvisionInstance 同步段的「分配端口 + 结构化启动 + 建实例」（与旧同步路径共用）。
func (p *ProvisionService) createProvisionInstance(req ProvisionServerRequest, core *CoreInfo) (*model.Instance, error) {
	ports, err := allocPortsForNode(p.db, req.NodeID)
	if err != nil {
		return nil, err
	}
	var node model.Node
	if err := p.db.First(&node, req.NodeID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("查找节点失败: %w", err)
	}
	spec, err := launchSpecForProvision(req, core, node.OS)
	if err != nil {
		return nil, err
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	return p.instance.Create(CreateInstanceRequest{
		NodeID:      req.NodeID,
		Name:        req.Name,
		Type:        model.InstanceTypeMinecraftJava,
		Role:        model.InstanceRoleBackend,
		ProcessType: model.ProcessTypeDaemon,
		JDKID:       req.JDKID,
		LaunchSpec:  string(specJSON),
		ServerPort:  ports.ServerPort,
		QueryPort:   ports.QueryPort,
		ProbePort:   ports.ProbePort,
		AutoRestart: true,
		GroupID:     req.GroupID,
	})
}

func launchSpecForProvision(req ProvisionServerRequest, core *CoreInfo, nodeOS string) (LaunchSpec, error) {
	spec := LaunchSpec{MemoryMb: req.MemoryMb, JvmArgs: req.JvmArgs, CoreJar: provisionCoreJar}
	if core != nil && core.Runtime != nil && core.Runtime.Distribution == "spongeforge" {
		if strings.TrimSpace(core.Runtime.LaunchJar) == "" {
			return LaunchSpec{}, fmt.Errorf("SpongeForge 缺少 Forge 启动 jar")
		}
		forgeVersion := strings.TrimSpace(core.Runtime.ForgeVersion)
		if forgeVersion == "" {
			return LaunchSpec{}, fmt.Errorf("SpongeForge 缺少 Forge 版本")
		}
		spec.CoreJar = core.Runtime.LaunchJar
		spec.JavaArgFiles = []string{"user_jvm_args.txt", forgeArgsFile(forgeVersion, nodeOS)}
	}
	return spec, nil
}

func forgeArgsFile(forgeVersion, nodeOS string) string {
	name := "unix_args.txt"
	if strings.EqualFold(strings.TrimSpace(nodeOS), "windows") {
		name = "win_args.txt"
	}
	return "libraries/net/minecraftforge/forge/" + forgeVersion + "/" + name
}

// ProvisionBukkit 端到端搭建一个 Paper 后端子服，保留旧入口兼容。
func (p *ProvisionService) ProvisionBukkit(ctx context.Context, req ProvisionBukkitRequest) (*model.Instance, error) {
	return p.ProvisionServer(ctx, req)
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

// provisionOnWorker 在 Worker 上下载/安装核心并写入 eula.txt / server.properties。
// stage 非 nil 时各阶段经任务中心上报（FR-319/323）；nil = 旧同步路径不上报。
func (p *ProvisionService) provisionOnWorker(ctx context.Context, inst *model.Instance, core *CoreInfo, onlineMode bool, stageFn func(int, string)) error {
	stage := func(progress int, text string) {
		if stageFn != nil {
			stageFn(progress, text)
		}
	}
	var node model.Node
	if err := p.db.First(&node, inst.NodeID).Error; err != nil {
		return fmt.Errorf("查找节点失败: %w", err)
	}
	client, ok := p.pool.Get(node.UUID)
	if !ok {
		return fmt.Errorf("节点 %s 未连接", node.UUID)
	}

	if isSpongeForgeCore(core) {
		stage(10, "安装 Forge/SpongeForge（可能需要数分钟）…")
		if err := p.installSpongeForge(ctx, client, inst, core); err != nil {
			return err
		}
	} else {
		stage(10, fmt.Sprintf("下载核心 %s（源站较慢时可能需要数分钟；节点已缓存则秒完成）…", core.DownloadURL))
		cacheHit, err := p.downloadSingleCore(ctx, client, inst, core)
		if err != nil {
			return err
		}
		// stage 文案区分「缓存命中/下载中」（FR-330）：命中免远程下载，任务轨迹一眼可辨。
		if cacheHit {
			stage(60, "核心缓存命中：已从节点缓存秒级复用，跳过远程下载")
		} else {
			stage(60, "核心下载完成")
		}
	}
	stage(70, "写入 eula.txt / server.properties …")

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
	stage(85, "部署监控探针 …")
	if jar := cpembed.ServerProbeJar(); len(jar) > 0 {
		probeCtx, cancel3 := context.WithTimeout(ctx, 30*time.Second)
		defer cancel3()
		dp, derr := client.Worker.DeployServerProbe(probeCtx, buildDeployServerProbeRequest(
			inst.UUID,
			jar,
			embeddedProbeLibrariesZip(inst.UUID),
			buildServerProbeConfig(inst.ProbePort, p.bridgeConfigBlock(inst.UUID, node.WSPort)),
		))
		switch {
		case derr != nil:
			slog.Warn("部署 ServerProbe 探针失败（不阻断建服）", "instance", inst.UUID, "err", derr)
		case !dp.Success:
			slog.Warn("部署 ServerProbe 探针失败（不阻断建服）", "instance", inst.UUID, "err", dp.Error)
		}
	}
	return nil
}

// downloadSingleCore 委托 Worker 下载核心，返回是否节点缓存命中（FR-330 stage 文案用）。
// 组合缓存键成分（CoreType/MCVersion/Build）随请求下发：ResolveBuild 已把 latest/未指定构建
// 解析为具体构建，无 sha256 源（Sponge Maven 等）在 Worker 侧按组合键复用缓存。
func (p *ProvisionService) downloadSingleCore(ctx context.Context, client *cpgrpc.Client, inst *model.Instance, core *CoreInfo) (bool, error) {
	dlCtx, cancel := context.WithTimeout(ctx, 16*time.Minute)
	defer cancel()
	dl, err := client.Worker.DownloadCore(dlCtx, &workerpb.DownloadCoreRequest{
		InstanceUuid: inst.UUID,
		DestFilename: provisionCoreJar,
		DownloadUrl:  core.DownloadURL,
		Sha256:       core.SHA256,
		CoreType:     core.Type,
		McVersion:    core.MCVersion,
		Build:        int32(core.Build),
	})
	if err != nil {
		return false, fmt.Errorf("下载核心失败: %w", err)
	}
	if !dl.Success {
		return false, fmt.Errorf("下载核心失败: %s", dl.Error)
	}
	return dl.CacheHit, nil
}

func (p *ProvisionService) installSpongeForge(ctx context.Context, client *cpgrpc.Client, inst *model.Instance, core *CoreInfo) error {
	rt := core.Runtime
	if rt == nil || strings.TrimSpace(rt.ForgeInstallerURL) == "" {
		return fmt.Errorf("SpongeForge 缺少 Forge installer")
	}
	installCtx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	resp, err := client.Worker.InstallForgeServer(installCtx, &workerpb.InstallForgeServerRequest{
		InstanceUuid:         inst.UUID,
		ForgeInstallerUrl:    rt.ForgeInstallerURL,
		SpongeforgeUrl:       core.DownloadURL,
		SpongeforgeSha256:    core.SHA256,
		SpongeforgeFilename:  stringOr(rt.ModFilename, "SpongeForge.jar"),
		LaunchJar:            rt.LaunchJar,
		ForgeInstallerSha256: "",
	})
	if err != nil {
		return fmt.Errorf("安装 SpongeForge 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("安装 SpongeForge 失败: %s", resp.Error)
	}
	return nil
}

func isSpongeForgeCore(core *CoreInfo) bool {
	return core != nil && (project(core.Type) == "spongeforge" || (core.Runtime != nil && core.Runtime.Distribution == "spongeforge"))
}

func buildDeployServerProbeRequest(instanceUUID string, jar, librariesZip []byte, configYaml string) *workerpb.DeployServerProbeRequest {
	return &workerpb.DeployServerProbeRequest{
		InstanceUuid: instanceUUID,
		Jar:          jar,
		ConfigYaml:   configYaml,
		LibrariesZip: librariesZip,
	}
}

func embeddedProbeLibrariesZip(instanceUUID string) []byte {
	libs := cpembed.ServerProbeLibrariesZip()
	if len(libs) == 0 {
		slog.Warn("ServerProbe 探针依赖缓存未内嵌，首启可能联网拉依赖", "instance", instanceUUID)
	}
	return libs
}

func stringOr(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
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
// （代理转发场景传 false）与 query。RCON 已退役（FR-067，见 ADR-016）：治理改走 ServerProbe 探针，
// 不再开启 rcon，去除额外暴露面。
func buildServerProperties(serverPort, queryPort int, onlineMode bool) string {
	var b strings.Builder
	b.WriteString("# 由 JianManager 一键开服生成（FR-034/046）\n")
	fmt.Fprintf(&b, "server-port=%d\n", serverPort)
	fmt.Fprintf(&b, "online-mode=%t\n", onlineMode)
	b.WriteString("enable-query=true\n")
	fmt.Fprintf(&b, "query.port=%d\n", queryPort)
	return b.String()
}
