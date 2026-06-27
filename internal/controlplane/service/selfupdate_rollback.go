package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/selfupdate"
	"github.com/wcpe/JianManager/internal/version"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// RollbackControlPlane 回滚 CP 自身到升级前备份（FR-182，见 ADR-039）。
// 流程：校验备份 sha → 换回备份二进制 → 异步延迟重启（先让 HTTP 202 返回）。
// 无备份返回 ErrNoBackup。返回 (fromVersion, toVersion=备份版本, error)。
func (s *SelfUpdateService) RollbackControlPlane(_ context.Context) (string, string, error) {
	from := version.Version

	if s.cpRollbackFn != nil {
		to, err := s.cpRollbackFn()
		if err != nil {
			return from, "", err
		}
		s.scheduleCPRestart()
		return from, to, nil
	}

	meta, err := selfupdate.Rollback(selfupdate.ComponentControlPlane, s.root)
	if err != nil {
		// ErrNoBackup（无备份）与备份损坏等错误透传给上层映射。
		return from, "", err
	}
	s.scheduleCPRestart()
	return from, meta.Version, nil
}

// scheduleCPRestart 异步延迟重启 CP（与 UpgradeControlPlane 同款：先让响应回到前端再重启）。
func (s *SelfUpdateService) scheduleCPRestart() {
	go func() {
		time.Sleep(time.Second)
		if s.restartCPFn != nil {
			s.restartCPFn()
			return
		}
		if err := selfupdate.Restart(); err == nil {
			os.Exit(0)
		}
	}()
}

// RollbackNode 经 gRPC 令目标节点回滚到其升级前备份（FR-182，见 ADR-039）。
// Worker 走本地备份（不下载，不经更新源），CP 仅下发 action=rollback。
// 节点离线返回 ErrNodeOffline；节点无备份映射 ErrNoBackup。返回 (fromVersion, toVersion, error)。
func (s *SelfUpdateService) RollbackNode(ctx context.Context, nodeID uint) (string, string, error) {
	if s.nodeRollbackFn != nil {
		from, to, err := s.nodeRollbackFn(nodeID)
		return from, to, mapNodeRollbackErr(err)
	}

	var node model.Node
	if err := s.db.First(&node, nodeID).Error; err != nil {
		return "", "", fmt.Errorf("节点不存在: %w", err)
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return "", "", ErrNodeOffline
	}

	upctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := client.Worker.UpgradeWorker(upctx, &workerpb.UpgradeWorkerRequest{Action: workerRollbackActionValue})
	if err != nil {
		return "", "", fmt.Errorf("节点回滚 RPC 失败: %w", err)
	}
	if !resp.Success {
		if strings.HasPrefix(strings.TrimSpace(resp.Error), workerNoBackupMarker) {
			return resp.FromVersion, "", ErrNoBackup
		}
		return resp.FromVersion, "", fmt.Errorf("节点回滚失败: %s", resp.Error)
	}
	return resp.FromVersion, "", nil
}

// workerRollbackActionValue 是下发给 Worker 的 action 取值（与 worker 端 workerRollbackAction 一致）。
const workerRollbackActionValue = "rollback"

// mapNodeRollbackErr 把节点回滚桩/真实现的「无备份」错误归一为 ErrNoBackup（供 router 映射友好码）。
func mapNodeRollbackErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(err.Error()), workerNoBackupMarker) {
		return ErrNoBackup
	}
	return err
}
