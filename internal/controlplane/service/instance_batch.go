package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// 实例批量操作（FR-058）。镜像 FR-038 Bot 批量：按 id/filter 选目标，CP 侧信号量分片有界并发，
// 经 gRPC 复用既有 per-instance RPC 扇出到各自所属 Worker，返回成功/失败/skipped 计数。
//
// 与单实例 InstanceService.Start/Stop/... 的差异：单实例路径先做 DB 状态转换再异步 goroutine 委托，
// 无法同步观测 Worker 结果。批量需要精确计数，故此处采用同步委托（与 bot_scale.go 一致），
// 委托成功后再回写终态，失败回写 CRASHED，与单实例语义对齐。

const (
	// maxInstanceBatchTargets 单次批量操作目标数上限，避免单请求过载。
	maxInstanceBatchTargets = 5000
	// instanceBatchConcurrency 批量委托的有界并发度。
	instanceBatchConcurrency = 16
	// maxInstanceBatchErrors 批量结果回传的失败明细上限。
	maxInstanceBatchErrors = 100
)

// InstanceBatchService 实例批量操作服务。
// 复用 InstanceService 的 gRPC 客户端池与 DB，但批量委托走同步路径以精确计数。
type InstanceBatchService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool
}

// NewInstanceBatchService 创建实例批量操作服务。
func NewInstanceBatchService(db *gorm.DB, pool *cpgrpc.ClientPool) *InstanceBatchService {
	return &InstanceBatchService{db: db, pool: pool}
}

// InstanceBatchAction 批量操作动作。
type InstanceBatchAction string

const (
	InstanceBatchCommand InstanceBatchAction = "command"
	InstanceBatchStart   InstanceBatchAction = "start"
	InstanceBatchStop    InstanceBatchAction = "stop"
	InstanceBatchRestart InstanceBatchAction = "restart"
	InstanceBatchKill    InstanceBatchAction = "kill"
)

// ValidInstanceBatchAction 校验批量动作是否在允许枚举内。
func ValidInstanceBatchAction(a InstanceBatchAction) bool {
	switch a {
	case InstanceBatchCommand, InstanceBatchStart, InstanceBatchStop, InstanceBatchRestart, InstanceBatchKill:
		return true
	}
	return false
}

// InstanceBatchFilter 批量目标筛选条件。
type InstanceBatchFilter struct {
	NodeID *uint
	Status *model.InstanceStatus
	Role   *model.InstanceRole
}

// InstanceBatchFilterIn 批量请求体中的筛选 DTO（JSON 可绑定），经 ToFilter 转为内部筛选条件。
type InstanceBatchFilterIn struct {
	NodeID *uint   `json:"nodeId"`
	Status *string `json:"status"`
	Role   *string `json:"role"`
}

// ToFilter 将请求 DTO 转为内部筛选条件。
func (in InstanceBatchFilterIn) ToFilter() InstanceBatchFilter {
	f := InstanceBatchFilter{NodeID: in.NodeID}
	if in.Status != nil {
		s := model.InstanceStatus(*in.Status)
		f.Status = &s
	}
	if in.Role != nil {
		r := model.InstanceRole(*in.Role)
		f.Role = &r
	}
	return f
}

// InstanceBatchRequest 批量操作请求。目标由 IDs 或 Filter 二选一指定。
type InstanceBatchRequest struct {
	Action  InstanceBatchAction
	IDs     []uint
	Filter  *InstanceBatchFilter
	Command string // action=command 时下发的命令
}

// InstanceBatchError 批量操作单条失败明细。
type InstanceBatchError struct {
	InstanceID uint   `json:"instanceId"`
	Error      string `json:"error"`
}

// InstanceBatchResult 批量操作结果计数。
type InstanceBatchResult struct {
	Action    string               `json:"action"`
	Requested int                  `json:"requested"`
	Succeeded int                  `json:"succeeded"`
	Failed    int                  `json:"failed"`
	Skipped   int                  `json:"skipped"`
	Errors    []InstanceBatchError `json:"errors"`
}

// applyInstanceBatchFilter 将筛选条件作用到查询。
// scopeIDs 非 nil 时附加可访问实例集合谓词（跨组隔离下沉为 SQL，镜像 bot_scale.go）。
func applyInstanceBatchFilter(q *gorm.DB, f InstanceBatchFilter, scopeIDs []uint, scope bool) *gorm.DB {
	if scope {
		if len(scopeIDs) == 0 {
			// 无任何可见实例：强制空结果
			return q.Where("1 = 0")
		}
		q = q.Where("instances.id IN ?", scopeIDs)
	}
	if f.NodeID != nil {
		q = q.Where("instances.node_id = ?", *f.NodeID)
	}
	if f.Status != nil {
		q = q.Where("instances.status = ?", *f.Status)
	}
	if f.Role != nil {
		q = q.Where("instances.role = ?", *f.Role)
	}
	return q
}

// resolveBatchTargets 解析批量目标实例（预加载节点），并按可访问实例集合收敛。
// 返回 (目标实例列表, skipped 数量)。skipped 为请求 IDs 中不存在或越权被剔除的数量（存在性隐藏）。
func (s *InstanceBatchService) resolveBatchTargets(req InstanceBatchRequest, scopeIDs []uint, scope bool) ([]model.Instance, int, error) {
	var instances []model.Instance

	if len(req.IDs) > 0 {
		q := applyInstanceBatchFilter(s.db.Model(&model.Instance{}).Preload("Node"), InstanceBatchFilter{}, scopeIDs, scope)
		if err := q.Where("instances.id IN ?", req.IDs).Find(&instances).Error; err != nil {
			return nil, 0, fmt.Errorf("查询批量目标失败: %w", err)
		}
		// 请求了 N 个 id，鉴权/存在性过滤后剩 len(instances)，差额即 skipped。
		// 去重：同一 id 在请求里重复出现时，多余的重复也算作未命中的 skipped。
		skipped := len(req.IDs) - len(instances)
		if skipped < 0 {
			skipped = 0
		}
		return instances, skipped, nil
	}

	// filter 模式
	f := InstanceBatchFilter{}
	if req.Filter != nil {
		f = *req.Filter
	}
	q := applyInstanceBatchFilter(s.db.Model(&model.Instance{}).Preload("Node"), f, scopeIDs, scope)
	if err := q.Limit(maxInstanceBatchTargets + 1).Find(&instances).Error; err != nil {
		return nil, 0, fmt.Errorf("查询批量目标失败: %w", err)
	}
	return instances, 0, nil
}

// Batch 执行批量操作：解析目标 → 按各自节点有界并发同步委托既有 per-instance RPC → 计数。
func (s *InstanceBatchService) Batch(req InstanceBatchRequest, scopeIDs []uint, scope bool) (*InstanceBatchResult, error) {
	instances, skipped, err := s.resolveBatchTargets(req, scopeIDs, scope)
	if err != nil {
		return nil, err
	}
	if len(instances) > maxInstanceBatchTargets {
		return nil, fmt.Errorf("批量目标数 %d 超过上限 %d", len(instances), maxInstanceBatchTargets)
	}

	result := &InstanceBatchResult{
		Action:    string(req.Action),
		Requested: len(instances),
		Skipped:   skipped,
		Errors:    []InstanceBatchError{},
	}
	if len(instances) == 0 {
		return result, nil
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, instanceBatchConcurrency)
	)
	for i := range instances {
		inst := instances[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			derr := s.delegateBatchOne(req, &inst)

			mu.Lock()
			defer mu.Unlock()
			if derr != nil {
				result.Failed++
				if len(result.Errors) < maxInstanceBatchErrors {
					result.Errors = append(result.Errors, InstanceBatchError{InstanceID: inst.ID, Error: derr.Error()})
				}
			} else {
				result.Succeeded++
			}
		}()
	}
	wg.Wait()

	return result, nil
}

// delegateBatchOne 将单个实例的批量动作同步委托到其所属 Worker（复用既有 per-instance RPC）。
// 生命周期动作委托成功后回写终态，失败回写 CRASHED，与单实例 delegateToWorker 语义对齐；
// command 动作不改实例状态。
func (s *InstanceBatchService) delegateBatchOne(req InstanceBatchRequest, inst *model.Instance) error {
	if inst.Node.UUID == "" {
		return fmt.Errorf("实例 %d 缺少关联节点", inst.ID)
	}

	// command 的前置校验独立于连通性：未运行时 stdin 不存在，先于 pool 查询拒绝，
	// 给出更准确的失败原因（而非笼统的「Worker 未连接」）。
	if req.Action == InstanceBatchCommand && inst.Status != model.InstanceStatusRunning {
		return fmt.Errorf("实例未运行，无法下发命令（当前状态 %s）", inst.Status)
	}

	client, ok := s.pool.Get(inst.Node.UUID)
	if !ok {
		// 生命周期动作与单实例 delegateToWorker 对齐：节点不可达视为失败并回写 CRASHED。
		// command 不改状态。
		if req.Action != InstanceBatchCommand {
			s.updateStatus(inst.ID, model.InstanceStatusCrashed)
		}
		return fmt.Errorf("Worker %s 未连接", inst.Node.UUID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if req.Action == InstanceBatchCommand {
		resp, err := client.Worker.SendCommand(ctx, &workerpb.SendCommandRequest{
			InstanceUuid: inst.UUID,
			Command:      req.Command,
		})
		if err != nil {
			return fmt.Errorf("gRPC SendCommand 失败: %w", err)
		}
		if !resp.Success {
			return fmt.Errorf("Worker SendCommand 失败: %s", resp.Error)
		}
		return nil
	}

	actionReq := &workerpb.InstanceActionRequest{InstanceUuid: inst.UUID}
	var (
		resp   *workerpb.InstanceActionResponse
		err    error
		target model.InstanceStatus
	)
	switch req.Action {
	case InstanceBatchStart:
		resp, err = client.Worker.StartInstance(ctx, actionReq)
		target = model.InstanceStatusRunning
	case InstanceBatchStop:
		resp, err = client.Worker.StopInstance(ctx, actionReq)
		target = model.InstanceStatusStopped
	case InstanceBatchRestart:
		resp, err = client.Worker.RestartInstance(ctx, actionReq)
		target = model.InstanceStatusRunning
	case InstanceBatchKill:
		resp, err = client.Worker.KillInstance(ctx, actionReq)
		target = model.InstanceStatusStopped
	default:
		return fmt.Errorf("不支持的批量动作: %s", req.Action)
	}

	if err != nil {
		s.updateStatus(inst.ID, model.InstanceStatusCrashed)
		return fmt.Errorf("gRPC %s 失败: %w", req.Action, err)
	}
	if resp != nil && !resp.Success {
		s.updateStatus(inst.ID, model.InstanceStatusCrashed)
		return fmt.Errorf("Worker %s 失败: %s", req.Action, resp.Error)
	}

	s.updateStatus(inst.ID, target)
	return nil
}

// updateStatus 回写实例状态，失败记 warning 不阻塞批量（与既有「失败不阻塞」语义一致）。
func (s *InstanceBatchService) updateStatus(id uint, status model.InstanceStatus) {
	if err := s.db.Model(&model.Instance{}).Where("id = ?", id).Update("status", status).Error; err != nil {
		slog.Warn("批量更新实例状态失败", "instanceId", id, "status", status, "error", err)
	}
}
