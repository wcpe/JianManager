package grpc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// DisposeOrphanRuntime 处置无主运行时（FR-326）：CP 反向对账确认后下发。
//
// 语义对齐 FR-310/325 运行态清理，但**不删工作目录**、不触碰 CP 业务记录：
//  1. 若注册表中有该实例且进程在跑 → Kill 进程树；
//  2. 移除 Worker 注册表条目；
//  3. ReapDaemonForDelete：强杀遗留 wrapper/Java 进程树并清 PID/sock。
// 幂等：实例未注册且无 PID 文件时仍 success（已干净）。
func (s *Server) DisposeOrphanRuntime(ctx context.Context, req *workerpb.DisposeOrphanRuntimeRequest) (*workerpb.DisposeOrphanRuntimeResponse, error) {
	if req == nil || req.InstanceUuid == "" {
		return &workerpb.DisposeOrphanRuntimeResponse{Success: false, Error: "instance_uuid 不能为空"}, nil
	}
	uuid := req.InstanceUuid
	slog.Warn("处置无主运行时：开始清理本地运行态", "instanceId", uuid)

	if inst, ok := s.manager.GetInstance(uuid); ok {
		switch inst.State {
		case process.StateRunning, process.StateStarting, process.StateStopping:
			if err := s.manager.Kill(uuid); err != nil {
				slog.Warn("处置无主运行时：Kill 失败（继续 reap/Remove）",
					"instanceId", uuid, "error", err)
			}
		}
		if err := s.manager.Remove(uuid); err != nil {
			return &workerpb.DisposeOrphanRuntimeResponse{
				Success: false,
				Error:   fmt.Sprintf("移除实例注册失败: %v", err),
			}, nil
		}
	}

	// 未在注册表或 Kill 后仍可能遗留 daemon PID/sock（FR-310 同根）。
	s.manager.ReapDaemonForDelete(uuid)

	slog.Info("处置无主运行时：本地运行态已清理", "instanceId", uuid)
	return &workerpb.DisposeOrphanRuntimeResponse{Success: true}, nil
}
