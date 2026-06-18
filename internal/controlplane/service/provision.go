package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
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
}

// NewProvisionService 创建一键搭建服务。
func NewProvisionService(db *gorm.DB, pool *cpgrpc.ClientPool, instance *InstanceService, core *CoreService) *ProvisionService {
	return &ProvisionService{db: db, pool: pool, instance: instance, core: core}
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
		ProcessType:  model.ProcessTypeDaemon,
		JDKID:        req.JDKID,
		LaunchSpec:   string(specJSON),
		ServerPort:   ports.ServerPort,
		RCONPort:     ports.RCONPort,
		QueryPort:    ports.QueryPort,
		RCONPassword: randRCONPassword(),
		AutoRestart:  true,
		GroupID:      req.GroupID,
	})
	if err != nil {
		return nil, err
	}

	if err := p.provisionOnWorker(ctx, inst, core); err != nil {
		// 实例已落库（STOPPED），返回实例与错误，便于用户重试或删除。
		return inst, fmt.Errorf("子服搭建失败: %w", err)
	}
	return inst, nil
}

// provisionOnWorker 在 Worker 上下载核心 jar 并写入 eula.txt / server.properties。
func (p *ProvisionService) provisionOnWorker(ctx context.Context, inst *model.Instance, core *CoreInfo) error {
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
		{"server.properties", buildServerProperties(inst.ServerPort, inst.RCONPort, inst.QueryPort, inst.RCONPassword)},
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
	return nil
}

// buildServerProperties 生成基础 server.properties：分配的 server-port、代理转发场景关闭
// 正版校验、开启 rcon（供指标采集）与 query。
func buildServerProperties(serverPort, rconPort, queryPort int, rconPassword string) string {
	var b strings.Builder
	b.WriteString("# 由 JianManager 一键开服生成（FR-034）\n")
	fmt.Fprintf(&b, "server-port=%d\n", serverPort)
	b.WriteString("online-mode=false\n")
	b.WriteString("enable-rcon=true\n")
	fmt.Fprintf(&b, "rcon.port=%d\n", rconPort)
	fmt.Fprintf(&b, "rcon.password=%s\n", rconPassword)
	b.WriteString("enable-query=true\n")
	fmt.Fprintf(&b, "query.port=%d\n", queryPort)
	return b.String()
}

// randRCONPassword 生成随机 rcon 密码（hex）。
func randRCONPassword() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "changeme-" + fmt.Sprint(time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
