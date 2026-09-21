package grpc

import (
	"context"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// maxEvidenceBatch 单次证据拉取的实例数上限（防御异常大请求；CP 仅对「DB 运行态但清单缺失」的少数实例拉取）。
const maxEvidenceBatch = 200

// ProbeInstanceEvidence 返回一批实例的进程侧证据（FR-455③）：wrapper/Java PID 是否存活、
// daemon socket 是否可达、docker 容器是否在跑。CP 据此在「心跳清单缺失」时二次确认实例是否真已停机，
// 消除通道抖动导致的「面板 STOPPED 而进程在跑」。只读、无副作用、幂等。
func (s *Server) ProbeInstanceEvidence(ctx context.Context, req *workerpb.ProbeInstanceEvidenceRequest) (*workerpb.ProbeInstanceEvidenceResponse, error) {
	uuids := req.GetInstanceUuids()
	if len(uuids) > maxEvidenceBatch {
		uuids = uuids[:maxEvidenceBatch]
	}
	evidences := s.manager.ProbeInstanceEvidence(uuids)
	resp := &workerpb.ProbeInstanceEvidenceResponse{Evidence: make([]*workerpb.InstanceEvidence, 0, len(evidences))}
	for _, e := range evidences {
		resp.Evidence = append(resp.Evidence, &workerpb.InstanceEvidence{
			InstanceUuid:     e.UUID,
			Running:          e.Running,
			RootPid:          int32(e.RootPID),
			ProcessAlive:     e.ProcessAlive,
			SocketReachable:  e.SocketReachable,
			ContainerRunning: e.ContainerRunning,
			Detail:           e.Detail,
		})
	}
	return resp, nil
}
