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

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// ErrImportRejected 导入被拒（Worker 守卫拒绝路径 / jar 不在候选内等业务性拒绝），
// 路由映射 422（不存在路径等按 spec 归 4xx）。
var ErrImportRejected = errors.New("导入被拒绝")

// ImportServerService 导入现有服务器（FR-302，见 ADR-069）：
// 探测（代理 Worker InspectServerDir）→ 就地接管 / 搬迁托管区 → 登记实例
// （结构化启动同 provision 路径）+ 探到的内嵌 JDK 登记进 node_jdks（managed=false）。
type ImportServerService struct {
	db       *gorm.DB
	pool     *cpgrpc.ClientPool
	instance *InstanceService
	// tasks 任务中心（FR-323）：migrate 搬迁经任务异步；nil 时回退同步（旧行为/测试）。
	tasks *TaskService
}

// NewImportServerService 创建导入服务。
func NewImportServerService(db *gorm.DB, pool *cpgrpc.ClientPool, instance *InstanceService) *ImportServerService {
	return &ImportServerService{db: db, pool: pool, instance: instance}
}

// SetTaskService 注入任务中心（FR-323，main 接线）：非 nil 后 migrate 搬迁走后台任务。
func (s *ImportServerService) SetTaskService(t *TaskService) { s.tasks = t }

// ImportInspectRequest 探测请求（POST /instances/import/inspect）。
type ImportInspectRequest struct {
	NodeID uint   `json:"nodeId" binding:"required"`
	Path   string `json:"path" binding:"required"`
}

// ImportJarCandidate 一个核心 jar 候选（对齐 workerpb.ImportJarCandidate）。
type ImportJarCandidate struct {
	Path          string `json:"path"`
	Size          int64  `json:"size"`
	MainClassHint string `json:"mainClassHint,omitempty"`
}

// ImportJdkCandidate 一个内嵌 JDK 候选（对齐 workerpb.ImportJdkCandidate）。
type ImportJdkCandidate struct {
	Path         string `json:"path"`
	Vendor       string `json:"vendor"`
	Version      string `json:"version"`
	MajorVersion int    `json:"majorVersion"`
	Arch         string `json:"arch"`
}

// ImportInspectResult 探测结果。
type ImportInspectResult struct {
	Jars         []ImportJarCandidate `json:"jars"`
	Jdks         []ImportJdkCandidate `json:"jdks"`
	ServerPort   int                  `json:"serverPort"`
	EulaAccepted bool                 `json:"eulaAccepted"`
	PropsFound   bool                 `json:"propsFound"`
}

// ImportServerRequest 导入请求（POST /instances/import）。
type ImportServerRequest struct {
	NodeID  uint   `json:"nodeId" binding:"required"`
	Path    string `json:"path" binding:"required"`
	Mode    string `json:"mode" binding:"required,oneof=in_place migrate"`
	Name    string `json:"name" binding:"required,min=1,max=128"`
	JarPath string `json:"jarPath" binding:"required"`
	// JDKID 绑定的既有节点 JDK（0=不绑定）。
	JDKID uint `json:"jdkId"`
	// RegisterJdkPaths 勾选登记的内嵌 JDK 路径（必须命中探测候选，登记为 managed=false）。
	RegisterJdkPaths []string `json:"registerJdkPaths"`
	MemoryMb         int      `json:"memoryMb"`
	// OnlineMode 预留字段（spec §3.2 请求形状）：导入不改写 server.properties，
	// 正版校验以原目录文件为准，当前不生效。
	OnlineMode *bool `json:"onlineMode"`
}

// Inspect 代理 Worker 探测某目录（守卫在 Worker 侧强制执行）。
func (s *ImportServerService) Inspect(ctx context.Context, nodeID uint, path string) (*ImportInspectResult, error) {
	client, err := s.workerClient(nodeID)
	if err != nil {
		return nil, err
	}
	inspectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := client.Worker.InspectServerDir(inspectCtx, &workerpb.InspectServerDirRequest{Path: path})
	if err != nil {
		return nil, fmt.Errorf("Worker InspectServerDir RPC 失败: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("%w: %s", ErrImportRejected, resp.Error)
	}
	return mapInspectResult(resp), nil
}

// Import 导入一个现成服务器目录为受管实例。流程（spec §3.2）：
//  1. 服务端重新探测（守卫强制 + 端口/JDK 候选取服务端真源，不信任前端复述）；
//  2. migrate 调 ImportServerDir 搬迁（同盘 rename / 跨盘拷贝校验清源）；in_place 工作目录=原路径；
//  3. 建实例（Type=minecraft_java / ProcessType=daemon / 结构化启动同 provision，端口沿用探测值不改文件）；
//  4. registerJdkPaths 逐个登记 node_jdks（managed=false，外部登记语义）。
func (s *ImportServerService) Import(ctx context.Context, req ImportServerRequest) (*model.Instance, string, error) {
	client, err := s.workerClient(req.NodeID)
	if err != nil {
		return nil, "", err
	}

	inspectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	resp, err := client.Worker.InspectServerDir(inspectCtx, &workerpb.InspectServerDirRequest{Path: req.Path})
	cancel()
	if err != nil {
		return nil, "", fmt.Errorf("Worker InspectServerDir RPC 失败: %w", err)
	}
	if !resp.Success {
		return nil, "", fmt.Errorf("%w: %s", ErrImportRejected, resp.Error)
	}
	inspect := mapInspectResult(resp)

	// 所选核心 jar 必须在探测候选内：防任意相对路径注入结构化启动命令。
	if !jarInCandidates(inspect.Jars, req.JarPath) {
		return nil, "", fmt.Errorf("%w: 所选 jar 不在探测候选内: %s", ErrImportRejected, req.JarPath)
	}

	spec := LaunchSpec{MemoryMb: req.MemoryMb, CoreJar: req.JarPath}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, "", err
	}
	inPlace := req.Mode == "in_place"

	// ── 就地接管：O(1) 无拷贝，同步建实例即完成（工作目录=原目录绝对路径，托管区外例外）。
	if inPlace {
		inst, err := s.instance.Create(CreateInstanceRequest{
			NodeID: req.NodeID, Name: req.Name, Type: model.InstanceTypeMinecraftJava,
			Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon, JDKID: req.JDKID,
			LaunchSpec: string(specJSON), ServerPort: inspect.ServerPort, AutoRestart: true,
			importWorkDir: strings.TrimSpace(req.Path), importInPlace: true,
		})
		if err != nil {
			return nil, "", err
		}
		s.registerJdkPaths(req.NodeID, inspect.Jdks, req.RegisterJdkPaths)
		return inst, "", nil
	}

	// ── 搬迁（migrate）：先按目标托管区相对路径建实例，再异步搬迁（FR-323）——大目录跨盘拷贝
	// 可数十分钟，同步会阻塞 HTTP 请求。有任务中心即秒回 {instance, taskId}，搬迁在 CP 后台推进；
	// 无任务中心（测试）回退同步搬迁（旧行为）。
	rel := allocWorkDirRel(req.Name) // var/servers/<slug>-<shortid>
	slug := rel[strings.LastIndex(rel, "/")+1:]

	if s.tasks == nil {
		if merr := s.migrateOnWorker(ctx, client, req.Path, slug, nil); merr != nil {
			return nil, "", merr
		}
		inst, err := s.createImportedInstance(req, specJSON, inspect, rel)
		if err != nil {
			return nil, "", err
		}
		s.registerJdkPaths(req.NodeID, inspect.Jdks, req.RegisterJdkPaths)
		return inst, "", nil
	}

	inst, err := s.createImportedInstance(req, specJSON, inspect, rel)
	if err != nil {
		return nil, "", err
	}
	_ = s.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status_reason", "导入中：正在搬迁目录（完成前请勿启动）").Error
	nodeID, path := req.NodeID, req.Path
	jdks, regJdks := inspect.Jdks, req.RegisterJdkPaths
	taskID := s.tasks.RunAsync(RunSpec{
		NodeID: nodeID, InstanceID: inst.ID, Kind: model.TaskKindImport,
		Title: fmt.Sprintf("导入服务器 %s", inst.Name),
	}, func(ctx context.Context, stage func(int, string)) (string, error) {
		cli, cerr := s.workerClient(nodeID)
		if cerr != nil {
			_ = s.db.Model(&model.Instance{}).Where("id = ?", inst.ID).Update("status_reason", "导入未完成："+cerr.Error()).Error
			return "", cerr
		}
		if merr := s.migrateOnWorker(ctx, cli, path, slug, stage); merr != nil {
			_ = s.db.Model(&model.Instance{}).Where("id = ?", inst.ID).Update("status_reason", "导入未完成："+merr.Error()).Error
			return "", merr
		}
		s.registerJdkPaths(nodeID, jdks, regJdks)
		_ = s.db.Model(&model.Instance{}).Where("id = ?", inst.ID).Update("status_reason", "").Error
		return "", nil
	})
	return inst, taskID, nil
}

// createImportedInstance 建导入实例（结构化启动同 provision，ADR-008；backend 角色可注册代理/可克隆）。
func (s *ImportServerService) createImportedInstance(req ImportServerRequest, specJSON []byte, inspect *ImportInspectResult, relWorkDir string) (*model.Instance, error) {
	return s.instance.Create(CreateInstanceRequest{
		NodeID: req.NodeID, Name: req.Name, Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon, JDKID: req.JDKID,
		LaunchSpec: string(specJSON), ServerPort: inspect.ServerPort, AutoRestart: true,
		importWorkDir: relWorkDir, importInPlace: false,
	})
}

// migrateOnWorker 委托 Worker 搬迁原目录到托管区 slug（stage 非 nil 上报阶段，FR-323）。
func (s *ImportServerService) migrateOnWorker(ctx context.Context, client *cpgrpc.Client, path, slug string, stage func(int, string)) error {
	if stage != nil {
		stage(20, "搬迁目录到托管区…")
	}
	moveCtx, cancelMove := context.WithTimeout(ctx, 30*time.Minute)
	defer cancelMove()
	mv, err := client.Worker.ImportServerDir(moveCtx, &workerpb.ImportServerDirRequest{
		Path: path, Mode: "migrate", TargetSlug: slug,
	})
	if err != nil {
		return fmt.Errorf("Worker ImportServerDir RPC 失败: %w", err)
	}
	if !mv.Success {
		return fmt.Errorf("%w: %s", ErrImportRejected, mv.Error)
	}
	return nil
}

// registerJdkPaths 把勾选的内嵌 JDK 登记进 node_jdks（managed=false：外部登记语义，
// 平台永不删其文件）。仅接受命中探测候选的路径（元数据取服务端探测真源）；
// 已登记（同节点同路径）跳过；单条失败仅告警不阻断导入（实例已建成）。
func (s *ImportServerService) registerJdkPaths(nodeID uint, candidates []ImportJdkCandidate, paths []string) {
	for _, p := range paths {
		var cand *ImportJdkCandidate
		for i := range candidates {
			if candidates[i].Path == p {
				cand = &candidates[i]
				break
			}
		}
		if cand == nil {
			slog.Warn("导入：登记 JDK 路径不在探测候选内，跳过", "nodeId", nodeID, "path", p)
			continue
		}
		var existing model.NodeJDK
		err := s.db.Where("node_id = ? AND path = ?", nodeID, cand.Path).First(&existing).Error
		if err == nil {
			continue // 已登记，不重复
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			slog.Warn("导入：查询既有 JDK 登记失败，跳过", "nodeId", nodeID, "path", p, "error", err)
			continue
		}
		if err := s.db.Create(&model.NodeJDK{
			NodeID:       nodeID,
			Vendor:       cand.Vendor,
			MajorVersion: cand.MajorVersion,
			Version:      cand.Version,
			Arch:         cand.Arch,
			Path:         cand.Path,
			Managed:      false,
		}).Error; err != nil {
			slog.Warn("导入：登记内嵌 JDK 失败（不阻断导入）", "nodeId", nodeID, "path", p, "error", err)
		}
	}
}

func (s *ImportServerService) workerClient(nodeID uint) (*cpgrpc.Client, error) {
	var node model.Node
	if err := s.db.First(&node, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("查找节点失败: %w", err)
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeOffline
	}
	return client, nil
}

func jarInCandidates(jars []ImportJarCandidate, path string) bool {
	for _, j := range jars {
		if j.Path == path {
			return true
		}
	}
	return false
}

func mapInspectResult(resp *workerpb.InspectServerDirResponse) *ImportInspectResult {
	out := &ImportInspectResult{
		Jars:         make([]ImportJarCandidate, 0, len(resp.Jars)),
		Jdks:         make([]ImportJdkCandidate, 0, len(resp.Jdks)),
		ServerPort:   int(resp.ServerPort),
		EulaAccepted: resp.EulaAccepted,
		PropsFound:   resp.PropsFound,
	}
	for _, j := range resp.Jars {
		out.Jars = append(out.Jars, ImportJarCandidate{Path: j.Path, Size: j.Size, MainClassHint: j.MainClassHint})
	}
	for _, j := range resp.Jdks {
		out.Jdks = append(out.Jdks, ImportJdkCandidate{
			Path: j.Path, Vendor: j.Vendor, Version: j.Version, MajorVersion: int(j.MajorVersion), Arch: j.Arch,
		})
	}
	return out
}
