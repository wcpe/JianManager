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
	//   1. log_sources 配置命中（运维在节点侧的显式配置优先）
	//   2. proto log_relative_path（CP 侧下发的实例级覆盖）
	//   3. 规则默认（由 RuleKey 决定）
	// 后两者由 ingest 层按「RelativePath 非空则优先」实现，此处只负责把配置命中提到最前。
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
