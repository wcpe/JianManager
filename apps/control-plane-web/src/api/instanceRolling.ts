import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'
import type { InstanceBatchAction, InstanceBatchFilter } from '@/api/instances'
import {
  isRollingActive,
  type RollingControlAction,
  type RollingOp,
} from '@jianmanager/ui/lib/instance-rolling'

// 编排契约（状态 / 失败明细 / 会话 / 控制动作 / 推进判定）归包，双侧共用（ADR-097）。
// 本地绑定来自上方 import，此处仅对外再导出，避免两处各写一份。
export type {
  RollingControlAction,
  RollingError,
  RollingOp,
  RollingState,
} from '@jianmanager/ui/lib/instance-rolling'
export { isRollingActive } from '@jianmanager/ui/lib/instance-rolling'

/**
 * 实例滚动/分批/灰度编排（FR-457）。
 *
 * 相比一次性扇出的 `POST /instances/batch`（FR-058），编排会话支持批大小、批间隔、
 * 失败即停、按比例灰度，并把进度持久化（游标/计数/状态）——暂停/继续/取消在 CP 重启后仍可恢复。
 * 契约见 `docs/API.md`（`/instances/rolling`）。
 */

// `RollingState` / `RollingError` / `RollingOp` 的定义已随受控视图归包（见上方 re-export）。

/** 创建并启动编排会话的请求。 */
export interface RollingCreateRequest {
  action: InstanceBatchAction
  /** 目标：ids 或 filter 二选一。 */
  ids?: number[]
  filter?: InstanceBatchFilter
  /** action=command 时下发的命令。 */
  command?: string
  /** 每批台数；0=不分批（退化为一并发快路径）。 */
  batchSize?: number
  /** 批间隔（秒）。 */
  batchIntervalSec?: number
  /** 失败即停。 */
  failFast?: boolean
  /** 灰度比例（0<ratio<1 时按稳定序抽样，首批即灰度）。 */
  ratio?: number
}

/** 创建并启动滚动编排。 */
export function useCreateRollingOp() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: RollingCreateRequest) => {
      const { data } = await api.post<RollingOp>('/instances/rolling', payload)
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instances'] }),
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '创建滚动编排失败')
    },
  })
}

/** 查询编排会话进度；非终态时自动轮询。 */
export function useRollingOp(opId: number | null, enabled = true) {
  return useQuery({
    queryKey: ['instance-rolling', opId],
    queryFn: async () => {
      const { data } = await api.get<RollingOp>(`/instances/rolling/${opId}`)
      return data
    },
    enabled: enabled && !!opId,
    refetchInterval: (query) => (isRollingActive(query.state.data?.state) ? 1500 : false),
  })
}

/** 暂停/继续/取消编排会话。 */
export function useRollingControl() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ opId, action }: { opId: number; action: RollingControlAction }) => {
      const { data } = await api.post<RollingOp>(`/instances/rolling/${opId}/${action}`)
      return data
    },
    onSuccess: (op) => {
      qc.setQueryData(['instance-rolling', op.id], op)
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '编排控制失败')
    },
  })
}
