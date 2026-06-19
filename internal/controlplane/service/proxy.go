package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// proxyMotd 是平台生成代理配置时写入的默认 motd。
const proxyMotd = "JianManager Proxy"

// ProxyService 搭建代理实例并把 proxy↔backend 注册关系落到代理实际配置（FR-035）。
// 同时实现 RegistrationSyncer：注册关系变更后重写代理 servers/priorities/forced-host 与下发 Velocity secret。
type ProxyService struct {
	db       *gorm.DB
	pool     *cpgrpc.ClientPool
	instance *InstanceService
	core     *CoreService
	reg      *RegistrationService
}

// NewProxyService 创建代理服务。
func NewProxyService(db *gorm.DB, pool *cpgrpc.ClientPool, instance *InstanceService, core *CoreService, reg *RegistrationService) *ProxyService {
	return &ProxyService{db: db, pool: pool, instance: instance, core: core, reg: reg}
}

// ProvisionProxyRequest 向导式创建代理实例请求。
type ProvisionProxyRequest struct {
	NodeID    uint     `json:"nodeId" binding:"required"`
	Name      string   `json:"name" binding:"required,min=1,max=128"`
	ProxyType string   `json:"proxyType" binding:"required"` // velocity|waterfall|bungeecord
	Version   string   `json:"version"`                      // velocity/waterfall 的版本；bungeecord 忽略
	Build     int      `json:"build"`
	JDKID     uint     `json:"jdkId"`
	MemoryMb  int      `json:"memoryMb"`
	JvmArgs   []string `json:"jvmArgs"`
	GroupID   uint     `json:"groupId"`
	// OnlineMode 代理是否向 Mojang 校验正版（缺省 true=正版网络；离线模式群组服传 false）。
	OnlineMode *bool `json:"onlineMode"`
	// BackendRegistrations 可选：创建时即注册的后端。
	BackendRegistrations []CreateRegistrationRequest `json:"backendRegistrations"`
}

// ProvisionProxyResult 代理搭建结果。
type ProvisionProxyResult struct {
	Instance         *model.Instance    `json:"instance"`
	ForwardingSecret string             `json:"forwardingSecret,omitempty"` // 仅 Velocity，返回一次供用户留存
	Registrations    []RegistrationView `json:"registrations"`
	Warnings         []string           `json:"warnings,omitempty"`
}

// ProvisionProxy 端到端搭建一个代理实例（role=proxy），返回创建的实例与初始注册结果。
func (p *ProxyService) ProvisionProxy(ctx context.Context, req ProvisionProxyRequest) (*ProvisionProxyResult, error) {
	if !IsProxyCore(req.ProxyType) {
		return nil, fmt.Errorf("不支持的代理类型: %s", req.ProxyType)
	}
	core, err := p.core.ResolveBuild(ctx, req.ProxyType, req.Version, req.Build)
	if err != nil {
		return nil, err
	}

	ports, err := allocPortsForNode(p.db, req.NodeID)
	if err != nil {
		return nil, err
	}

	secret := ""
	if IsVelocityCore(req.ProxyType) {
		secret = genForwardingSecret()
	}

	// 代理用结构化启动：java -jar core.jar（OmitNogui，代理不接受 nogui）。
	specJSON, err := json.Marshal(LaunchSpec{MemoryMb: req.MemoryMb, JvmArgs: req.JvmArgs, CoreJar: provisionCoreJar, OmitNogui: true})
	if err != nil {
		return nil, err
	}

	inst, err := p.instance.Create(CreateInstanceRequest{
		NodeID:      req.NodeID,
		Name:        req.Name,
		Type:        model.InstanceTypeMinecraftJava,
		Role:        model.InstanceRoleProxy,
		ProcessType: model.ProcessTypeDaemon,
		JDKID:       req.JDKID,
		LaunchSpec:  string(specJSON),
		ServerPort:  ports.ServerPort, // 代理监听端口
		AutoRestart: true,
		GroupID:     req.GroupID,
	})
	if err != nil {
		return nil, err
	}

	// online-mode 选择持久化（缺省 true=正版），供 SyncProxy 重新生成配置时保留。
	onlineMode := boolOr(req.OnlineMode, true)
	if err := p.db.Model(inst).Update("proxy_online_mode", onlineMode).Error; err != nil {
		return inst2result(inst, secret), fmt.Errorf("保存 online-mode 失败: %w", err)
	}
	inst.ProxyOnlineMode = onlineMode

	if secret != "" {
		if err := p.db.Model(inst).Update("forwarding_secret", secret).Error; err != nil {
			return inst2result(inst, secret), fmt.Errorf("保存 forwarding secret 失败: %w", err)
		}
		inst.ForwardingSecret = secret
	}

	result := inst2result(inst, secret)

	// 下载核心 + 写 forwarding.secret（Velocity）。代理 servers 配置由随后的 sync 写入。
	if err := p.provisionProxyOnWorker(ctx, inst, core); err != nil {
		return result, fmt.Errorf("代理搭建失败: %w", err)
	}

	// 初始注册（每条 Create 触发一次 sync；最终 sync 收集 warnings）。
	for _, r := range req.BackendRegistrations {
		view, rerr := p.reg.Create(inst.ID, r)
		if view != nil {
			result.Registrations = append(result.Registrations, *view)
		}
		if rerr != nil {
			result.Warnings = append(result.Warnings, rerr.Error())
		}
	}

	warnings, serr := p.syncProxyDetailed(inst.ID)
	result.Warnings = append(result.Warnings, warnings...)
	if serr != nil {
		result.Warnings = append(result.Warnings, serr.Error())
	}
	return result, nil
}

func inst2result(inst *model.Instance, secret string) *ProvisionProxyResult {
	r := &ProvisionProxyResult{Instance: inst}
	if secret != "" {
		r.ForwardingSecret = secret
	}
	return r
}

// SyncProxy 实现 RegistrationSyncer：把注册关系落到代理实际配置并下发 secret。
// 仅在代理配置写入失败（代理离线）时返回错误；后端下发问题作为 warning 记日志，不阻断注册。
func (p *ProxyService) SyncProxy(proxyID uint) error {
	warnings, err := p.syncProxyDetailed(proxyID)
	for _, w := range warnings {
		slog.Warn("代理配置同步告警", "proxyId", proxyID, "warning", w)
	}
	return err
}

// Resync 手动重推（代理/后端离线恢复后），返回 secret 一致性与告警。
func (p *ProxyService) Resync(proxyID uint) (consistent bool, warnings []string, err error) {
	warnings, err = p.syncProxyDetailed(proxyID)
	consistent = true
	for _, w := range warnings {
		if containsSecretInconsistency(w) {
			consistent = false
		}
	}
	return consistent, warnings, err
}

// syncProxyDetailed 重写代理配置（servers/try/forced-hosts）并向各后端下发转发配置，返回告警列表。
func (p *ProxyService) syncProxyDetailed(proxyID uint) ([]string, error) {
	var proxy model.Instance
	if err := p.db.First(&proxy, proxyID).Error; err != nil {
		return nil, fmt.Errorf("代理实例不存在: %w", err)
	}
	if proxy.Role != model.InstanceRoleProxy {
		return nil, ErrNotAProxy
	}

	proxyClient, err := p.clientForInstance(&proxy)
	if err != nil {
		return nil, fmt.Errorf("代理 %s 不可达，无法写配置: %w", proxy.Name, err)
	}

	var regs []model.ServerRegistration
	p.db.Where("proxy_id = ? AND enabled = ?", proxyID, true).Order("priority asc, id asc").Find(&regs)

	var warnings []string
	entries := make([]proxyServerEntry, 0, len(regs))
	for _, r := range regs {
		var backend model.Instance
		if err := p.db.First(&backend, r.BackendID).Error; err != nil {
			warnings = append(warnings, fmt.Sprintf("注册 %s 的后端(id=%d)不存在，已跳过", r.Alias, r.BackendID))
			continue
		}
		addr, aerr := p.backendAddress(&proxy, &backend)
		if aerr != nil {
			warnings = append(warnings, aerr.Error())
			continue
		}
		entries = append(entries, proxyServerEntry{Alias: r.Alias, Address: addr, ForcedHost: r.ForcedHost, Restricted: r.Restricted})
	}

	isVelocity := proxy.ForwardingSecret != ""

	// 写代理配置（critical）。online-mode 取持久化选择，避免每次同步把它重置。
	if isVelocity {
		if err := p.writeConfig(proxyClient, proxy.UUID, "velocity.toml", buildVelocityToml(proxy.ServerPort, proxyMotd, proxy.ProxyOnlineMode, entries)); err != nil {
			return warnings, fmt.Errorf("写 velocity.toml 失败: %w", err)
		}
		// 确保 forwarding.secret 文件存在（provision 已写，这里幂等补写）。
		if err := p.writeConfig(proxyClient, proxy.UUID, "forwarding.secret", proxy.ForwardingSecret); err != nil {
			warnings = append(warnings, fmt.Sprintf("写 forwarding.secret 失败: %v", err))
		}
	} else {
		content, berr := buildBungeeConfig(proxy.ServerPort, proxyMotd, proxy.ProxyOnlineMode, entries)
		if berr != nil {
			return warnings, berr
		}
		if err := p.writeConfig(proxyClient, proxy.UUID, "config.yml", content); err != nil {
			return warnings, fmt.Errorf("写 config.yml 失败: %w", err)
		}
	}

	// 向各后端下发转发配置（best-effort）。
	for _, r := range regs {
		var backend model.Instance
		if err := p.db.First(&backend, r.BackendID).Error; err != nil {
			continue
		}
		if w := p.distributeToBackend(&proxy, &backend, isVelocity); w != "" {
			warnings = append(warnings, w)
		}
	}

	// Velocity 跨代理 secret 一致性校验。
	if isVelocity {
		seen := map[uint]bool{}
		for _, r := range regs {
			if seen[r.BackendID] {
				continue
			}
			seen[r.BackendID] = true
			if w := p.checkSecretConsistency(r.BackendID); w != "" {
				warnings = append(warnings, w)
			}
		}
	}
	return warnings, nil
}

// distributeToBackend 把转发配置下发到一个后端（Velocity secret / Bungee 开关）。返回告警（空表示成功）。
func (p *ProxyService) distributeToBackend(proxy, backend *model.Instance, isVelocity bool) string {
	client, err := p.clientForInstance(backend)
	if err != nil {
		return fmt.Sprintf("后端 %s 离线，转发配置未下发（恢复后请对代理 resync）", backend.Name)
	}
	if isVelocity {
		existing := p.readConfig(client, backend.UUID, "config/paper-global.yml")
		merged, merr := mergeVelocitySecretIntoPaperGlobal(existing, proxy.ForwardingSecret)
		if merr != nil {
			return fmt.Sprintf("后端 %s 合并 paper-global.yml 失败: %v", backend.Name, merr)
		}
		if err := p.writeConfig(client, backend.UUID, "config/paper-global.yml", merged); err != nil {
			return fmt.Sprintf("后端 %s 写 paper-global.yml 失败: %v", backend.Name, err)
		}
		return ""
	}
	// BungeeCord/Waterfall：spigot.yml settings.bungeecord=true + paper-global proxies.bungee-cord.online-mode=false
	spig := p.readConfig(client, backend.UUID, "spigot.yml")
	if mSpig, serr := mergeBungeeIntoSpigot(spig); serr == nil {
		_ = p.writeConfig(client, backend.UUID, "spigot.yml", mSpig)
	}
	pg := p.readConfig(client, backend.UUID, "config/paper-global.yml")
	if mPg, perr := mergeBungeeIntoPaperGlobal(pg); perr == nil {
		_ = p.writeConfig(client, backend.UUID, "config/paper-global.yml", mPg)
	}
	return ""
}

// checkSecretConsistency 校验某后端注册进的所有 Velocity 代理 secret 是否一致。
func (p *ProxyService) checkSecretConsistency(backendID uint) string {
	var regs []model.ServerRegistration
	p.db.Where("backend_id = ?", backendID).Find(&regs)
	secrets := map[string]bool{}
	for _, r := range regs {
		var proxy model.Instance
		if err := p.db.First(&proxy, r.ProxyID).Error; err != nil {
			continue
		}
		if proxy.ForwardingSecret != "" {
			secrets[proxy.ForwardingSecret] = true
		}
	}
	if len(secrets) > 1 {
		var backend model.Instance
		p.db.First(&backend, backendID)
		return fmt.Sprintf("secret 不一致：后端 %s 注册进多个 Velocity 代理且 forwarding secret 不同，modern 转发要求一致", backend.Name)
	}
	return ""
}

// provisionProxyOnWorker 在 Worker 上下载代理核心，并（Velocity）写 forwarding.secret 文件。
func (p *ProxyService) provisionProxyOnWorker(ctx context.Context, inst *model.Instance, core *CoreInfo) error {
	client, err := p.clientForInstance(inst)
	if err != nil {
		return err
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
	if inst.ForwardingSecret != "" {
		if err := p.writeConfig(client, inst.UUID, "forwarding.secret", inst.ForwardingSecret); err != nil {
			return fmt.Errorf("写 forwarding.secret 失败: %w", err)
		}
	}
	return nil
}

// backendAddress 解析后端在代理视角下的地址（host:port）。同节点用 127.0.0.1，跨节点用后端节点 host。
func (p *ProxyService) backendAddress(proxy, backend *model.Instance) (string, error) {
	if backend.ServerPort == 0 {
		return "", fmt.Errorf("后端 %s 未分配监听端口", backend.Name)
	}
	host := "127.0.0.1"
	if backend.NodeID != proxy.NodeID {
		var node model.Node
		if err := p.db.First(&node, backend.NodeID).Error; err != nil {
			return "", fmt.Errorf("查找后端 %s 的节点失败: %w", backend.Name, err)
		}
		host = node.Host
	}
	return fmt.Sprintf("%s:%d", host, backend.ServerPort), nil
}

func (p *ProxyService) clientForInstance(inst *model.Instance) (*cpgrpc.Client, error) {
	var node model.Node
	if err := p.db.First(&node, inst.NodeID).Error; err != nil {
		return nil, fmt.Errorf("查找节点失败: %w", err)
	}
	client, ok := p.pool.Get(node.UUID)
	if !ok {
		return nil, fmt.Errorf("节点 %s 未连接", node.UUID)
	}
	return client, nil
}

func (p *ProxyService) readConfig(client *cpgrpc.Client, uuid, path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.Worker.ReadConfig(ctx, &workerpb.ReadConfigRequest{InstanceUuid: uuid, Path: path})
	if err != nil || resp == nil {
		return "" // 文件不存在视为空，后续 merge 生成最小档
	}
	return resp.Content
}

func (p *ProxyService) writeConfig(client *cpgrpc.Client, uuid, path, content string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := client.Worker.WriteConfig(ctx, &workerpb.WriteConfigRequest{InstanceUuid: uuid, Path: path, Content: content})
	if err != nil {
		return err
	}
	if resp != nil && !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

func containsSecretInconsistency(w string) bool {
	return len(w) >= 6 && w[:6] == "secret"
}
