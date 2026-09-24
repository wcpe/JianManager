package grpc

import "github.com/wcpe/JianManager/proto/workerpb"

type InstanceLogCollector interface {
	RegisterInstance(uuid, targetID, generation, mode, workDir string) error
	AppendInstanceOutput(uuid, stream string, data []byte) error
}

// SetInstanceLogCollector is installed before accepting CP instance RPCs.
func (s *Server) SetInstanceLogCollector(collector InstanceLogCollector) { s.instanceLogs = collector }

func (s *Server) registerInstanceLogs(req *workerpb.CreateInstanceRequest, workDir string) error {
	if s.instanceLogs == nil || req.GetLogTargetId() == "" {
		return nil
	}
	if existing, ok := s.manager.GetInstance(req.InstanceUuid); ok {
		workDir = existing.WorkDir
	}
	return s.instanceLogs.RegisterInstance(req.InstanceUuid, req.LogTargetId, req.LogSourceGeneration, req.LogAcquireMode, workDir)
}
