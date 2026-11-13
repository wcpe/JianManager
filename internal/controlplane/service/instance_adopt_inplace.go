package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 就地导入（in_place）实例的外部进程收养。
//
// ## 为什么它就是“导入后应该发生的事”
// 就地导入的语义是「这个目录里已经有一份跑着的服，我现在要纳管它」。
// 但导入完成时平台记的是 STOPPED，而目录里的进程正在服务——面板上写着停止、
// 玩家却能进来，这是一眼就能看出的分裂。既有的 AdoptForeignRuntime 能对齐这个状态，
// 但它**先停后启**：对 tmux 里跑着的服，那等于一次重启，而且新拉起的进程会因端口冲突
// 反复崩溃（真实机实测：6 台服全部“崩溃×5→熔断”，而原服务器未受任何影响）。
//
// 所以就地导入需要的是一条“不重启”的收养：只把现有进程登记成运行态。
// 本文件负责 CP 侧衔接——转调 Worker 的 AdoptInPlaceRuntime，并把结果同步回 CP 状态。

// adoptInPlaceTimeout 是收养 RPC 的截止时间。
//
// 比 adoptRuntimeTimeout（60s，包含“停进程 + 重启 + 等就绪”）短得多：
// 这条路径不发信号、不启动进程，只是复核归属 + 置状态，正常应在百毫秒内完成。
// 给 20s 是留给“归属复核要读 /proc 与目录信息”的抖动余量。
const adoptInPlaceTimeout = 20 * time.Second

// ErrAdoptInPlaceNoRuntime 表示目录下没有可收养的活进程。
//
// 调用方（导入流程）应据此判定为“导入成 STOPPED 的正常结果”而不是错误——
// 目录本来就是停着的，没有任何东西需要收养。
var ErrAdoptInPlaceNoRuntime = errors.New("工作目录下没有可收养的活进程")

// AdoptInPlaceRuntime 收养就地导入实例的外部运行进程（不重启）。
//
// 与 AdoptForeignRuntime 共用同一把单实例操作锁：两都是“改运行态”的操作，
// 不能与启停/删除并发穿叉。
//
// 成功后直接置 RUNNING 并清漂移标记（与 AdoptForeignRuntime 同口径：
// 接管是“把平台外的运行态收编回平台”，属逃生式对齐，不经状态机；
// 下一拍心跳会以 Worker 上报再次正向对齐）。
//
// @return 被收养的 PID；目录下无活进程时返回 ErrAdoptInPlaceNoRuntime。
func (s *InstanceService) AdoptInPlaceRuntime(id uint) (int, error) {
	releaseOperation := s.acquireInstanceOperation(id)
	defer releaseOperation()

	instance, _, client, err := s.managedProcessClient(id)
	if err != nil {
		return 0, err
	}

	// 先把实例注册到 Worker 再收养：
	//
	// 就地导入走的是「CP 直接写库 + 工作目录=原路径」，**不经过 Worker 的实例登记**（见 import_server.go
	// 的 in_place 分支——它只调 instance.Create 写库）。于是 Worker 的进程表里根本没有这个实例，
	// 收养时会回“实例不存在”（真实机上一开始就是这个报错）。CreateInstance 是幂等的（已存在则刷新规格），
	// 注册一次既补齐了缺口，也保证后续 Start/Stop/终端 的路径都能找到它。
	if err := s.registerOnWorkerLocked(instance); err != nil {
		reason := "就地收养前注册实例到节点失败: " + err.Error()
		s.updateStatusReasonOnly(instance.ID, reason)
		return 0, errors.New(reason)
	}

	ctx, cancel := context.WithTimeout(context.Background(), adoptInPlaceTimeout)
	defer cancel()

	resp, err := client.Worker.AdoptInPlaceRuntime(ctx, &workerpb.InstanceActionRequest{
		InstanceUuid: instance.UUID,
	})
	if err != nil {
		reason := "收养就地进程失败: " + err.Error()
		s.updateStatusReasonOnly(instance.ID, reason)
		return 0, errors.New(reason)
	}
	if resp != nil && !resp.Success {
		// Worker 的“没有可收养进程”是一种**正常结果**而非故障（目录本来就没在跑）。
		// 用 sentinel 包一层，让导入流程能区分“没东西可收养”与“收养失败”。
		if isNoRuntimeMessage(resp.Error) {
			return 0, ErrAdoptInPlaceNoRuntime
		}
		reason := "收养就地进程失败: " + resp.Error
		s.updateStatusReasonOnly(instance.ID, reason)
		return 0, errors.New(reason)
	}

	if err := s.db.Model(&model.Instance{}).Where("id = ?", id).
		Updates(map[string]any{
			"status":                model.InstanceStatusRunning,
			"status_reason":         "",
			"runtime_drift_pid":     int64(0),
			"runtime_drift_cmdline": "",
			"runtime_drift_at":      nil,
		}).Error; err != nil {
		return 0, fmt.Errorf("收养成功但回写状态失败: %w", err)
	}

	slog.Info("已收养就地导入实例的外部运行进程（未重启）", "instanceId", instance.UUID, "name", instance.Name)
	return 0, nil
}

// isNoRuntimeMessage 判定 Worker 返回的是“没有可收养进程”。
//
// 按文案匹配而非错误码：proto 里 InstanceActionResponse 只有 error 字符串，
// 没有结构化码位。匹配目标与 Worker 侧 ErrAdoptNoRuntime 的文案保持一致。
//
// 两种形态都要认：
//   - 裸哨兵：“工作目录下没有可收养的活进程”；
//   - 包装形态：“…（目录下只有 pid=N 的 "-bash"，不像游戏服运行时）”——
//     Worker 用 %w 包了哨兵，但 gRPC 只传 Error() 文本，前缀所以保留。
func isNoRuntimeMessage(msg string) bool {
	if msg == "" {
		return false
	}
	return strings.Contains(msg, "没有可收养的活进程") ||
		strings.Contains(msg, "no adoptable runtime") ||
		strings.Contains(msg, "不像游戏服运行时")
}
