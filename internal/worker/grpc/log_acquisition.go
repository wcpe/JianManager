package grpc

import (
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type InstanceLogCollector interface {
	RegisterInstance(reg ingest.InstanceRegistration) error
	AppendInstanceOutput(uuid, stream string, data []byte) error
}

// InstanceLogSourceOverrides 提供实例日志路径的配置化覆盖（FR-502 §3.4）。
//
// 实现方按实例的日志命名空间（`inst:<id>`）查 `log_sources` 配置；
// 命中时以配置的 path 覆盖规则默认路径。返回空串表示无覆盖。
//
// 用函数类型而非直接依赖 config 包：grpc 层无需感知 Worker 配置结构。
type InstanceLogSourceOverrides func(targetID string) (relativePath string, ok bool)

// SetInstanceLogCollector is installed before accepting CP instance RPCs.
func (s *Server) SetInstanceLogCollector(collector InstanceLogCollector) { s.instanceLogs = collector }

// SetInstanceLogSourceOverrides 注入实例日志路径覆盖查询（FR-502）。可选；nil 时不做覆盖。
func (s *Server) SetInstanceLogSourceOverrides(fn InstanceLogSourceOverrides) {
	s.logSourceOverrides = fn
}

func (s *Server) registerInstanceLogs(req *workerpb.CreateInstanceRequest, workDir string) error {
	if s.instanceLogs == nil || req.GetLogTargetId() == "" {
		return nil
	}
	if existing, ok := s.manager.GetInstance(req.InstanceUuid); ok {
		workDir = existing.WorkDir
	}
	// FR-502：规则名与相对路径覆盖由 CP 推导下发；空值分别回退 bukkit 规则与规则默认路径。
	relativePath := req.GetLogRelativePath()
	// 路径覆盖优先级（高 → 低）：
	//   1. CP 心跳下发的实例级覆盖（`log.acq.path_overrides`，即 policy.PathOverrides）
	//      —— 它在登记时**无条件压过**本函数算出的 RelativePath（见 ingest/instances.go）
	//   2. log_sources 配置命中（运维在节点侧的显式配置）
	//   3. proto log_relative_path（CP 侧下发的实例级覆盖）
	//   4. 规则默认（由 RuleKey 决定）
	// 本函数的落点只到第 2 层：后两层由 ingest 层按「RelativePath 非空则优先」实现，
	// 此处只负责把节点侧配置命中提到 proto 字段之前。第 1 层不在这里——它由心跳策略
	// 在登记时生效，故节点侧 worker.yml 的显式 path 会被面板覆盖静默压过。
	if s.logSourceOverrides != nil {
		if override, ok := s.logSourceOverrides(req.GetLogTargetId()); ok {
			relativePath = override
		}
	}
	return s.instanceLogs.RegisterInstance(ingest.InstanceRegistration{
		UUID:         req.GetInstanceUuid(),
		TargetID:     req.GetLogTargetId(),
		Generation:   req.GetLogSourceGeneration(),
		Mode:         req.GetLogAcquireMode(),
		WorkDir:      workDir,
		RuleKey:      req.GetLogRuleKey(),
		RelativePath: relativePath,
	})
}
