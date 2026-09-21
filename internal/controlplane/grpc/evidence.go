package grpc

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// evidenceReconcileGraceBeats 是「DB 运行态但心跳清单缺失、进程侧证据又不可得」时的宽限拍数：
// 连续 N 拍才落 STOPPED，避免证据拉取瞬时失败/通道抖动导致误判（FR-455③）。
const evidenceReconcileGraceBeats = 3

// evidenceProbeTimeout 单次证据拉取超时。
const evidenceProbeTimeout = 8 * time.Second

var (
	// errEvidenceWorkerOffline Worker 未连接，无法拉取证据。
	errEvidenceWorkerOffline = errors.New("节点未连接，无法拉取进程侧证据")
	// errEvidenceUnsupported 老 Worker 不支持证据 RPC（需升级）。
	errEvidenceUnsupported = errors.New("Worker 不支持 ProbeInstanceEvidence（需升级）")
)

// EvidenceProbeClient 抽象 CP→Worker 的进程侧证据拉取（FR-455③），便于单测注入假客户端。
type EvidenceProbeClient interface {
	// ProbeInstanceEvidence 返回一批实例的进程侧证据：uuid→是否仍在运行。
	// 缺项表示 Worker 未给出该实例证据（视为未知）。返回 err 时整批视为不可得。
	ProbeInstanceEvidence(ctx context.Context, nodeUUID string, instanceUUIDs []string) (map[string]bool, error)
}

// poolEvidenceClient 经 ClientPool 调 Worker.ProbeInstanceEvidence。
type poolEvidenceClient struct {
	pool *ClientPool
}

// NewEvidenceProbeFromPool 由连接池构造证据拉取客户端；pool 为 nil 时返回 nil。
func NewEvidenceProbeFromPool(pool *ClientPool) EvidenceProbeClient {
	if pool == nil {
		return nil
	}
	return &poolEvidenceClient{pool: pool}
}

func (c *poolEvidenceClient) ProbeInstanceEvidence(ctx context.Context, nodeUUID string, instanceUUIDs []string) (map[string]bool, error) {
	client, ok := c.pool.Get(nodeUUID)
	if !ok || client == nil || client.Worker == nil {
		return nil, errEvidenceWorkerOffline
	}
	callCtx, cancel := context.WithTimeout(ctx, evidenceProbeTimeout)
	defer cancel()
	resp, err := client.Worker.ProbeInstanceEvidence(callCtx, &workerpb.ProbeInstanceEvidenceRequest{InstanceUuids: instanceUUIDs})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return nil, errEvidenceUnsupported
		}
		return nil, err
	}
	out := make(map[string]bool, len(resp.Evidence))
	for _, e := range resp.Evidence {
		if e == nil || e.InstanceUuid == "" {
			continue
		}
		out[e.InstanceUuid] = e.Running
	}
	return out, nil
}

// SetEvidenceProbe 注入进程侧证据拉取客户端（FR-455③）。不注入则 syncInstanceStates 退化为
// 旧行为（清单缺失即落 STOPPED），保持向后兼容与现有测试语义。
func (h *ControlPlaneHandler) SetEvidenceProbe(c EvidenceProbeClient) {
	h.evidence = c
}

// bumpReconcileGrace 递增某实例的连续宽限拍数并返回新值。
func (h *ControlPlaneHandler) bumpReconcileGrace(uuid string) int {
	h.reconcileMu.Lock()
	defer h.reconcileMu.Unlock()
	if h.reconcileGrace == nil {
		h.reconcileGrace = make(map[string]int)
	}
	h.reconcileGrace[uuid]++
	return h.reconcileGrace[uuid]
}

// clearReconcileGrace 清除某实例的宽限计数（证据一致或已落终态时）。
func (h *ControlPlaneHandler) clearReconcileGrace(uuid string) {
	h.reconcileMu.Lock()
	defer h.reconcileMu.Unlock()
	delete(h.reconcileGrace, uuid)
}
