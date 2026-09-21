package grpc

import "sync"

// evidenceReconcileDispatcher 按节点单飞 + 异步派发「进程侧证据对账」任务（FR-456 F10）。
//
// 背景：FR-455③ 的证据对账需向 Worker 拉取进程侧证据，最长阻塞 evidenceProbeTimeout（8s）。
// 该对账原先在心跳处理内同步执行，使单节点心跳应答最长阻塞 8s，与 spec §2.4「重推不应阻塞心跳」
// 相冲突。本派发器把对账移出心跳应答关键路径：异步执行 + 同节点单飞（上一拍对账未完成则跳过本拍，
// 避免对慢节点堆积重复任务）。对账本身幂等（每拍重查缺失集合），跳过一拍仅等价于延迟一拍。
type evidenceReconcileDispatcher struct {
	mu       sync.Mutex
	inflight map[string]struct{}
}

// NewEvidenceReconcileDispatcher 创建证据对账单飞派发器。
func NewEvidenceReconcileDispatcher() *evidenceReconcileDispatcher {
	return &evidenceReconcileDispatcher{inflight: make(map[string]struct{})}
}

// Dispatch 派发该节点的一次对账任务；同节点已有在飞任务则跳过本拍。空节点/空任务忽略。
func (d *evidenceReconcileDispatcher) Dispatch(nodeUUID string, task func()) {
	if d == nil || task == nil || nodeUUID == "" {
		return
	}
	d.mu.Lock()
	if _, busy := d.inflight[nodeUUID]; busy {
		d.mu.Unlock()
		return
	}
	d.inflight[nodeUUID] = struct{}{}
	d.mu.Unlock()

	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.inflight, nodeUUID)
			d.mu.Unlock()
		}()
		task()
	}()
}
