import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'
import { INSTANCE_QUERY_GC_TIME_MS } from '@/api/instances'
import { apiErrorMessage } from '@/lib/api-error'

/**
 * 实例整机快照（FR-466）：一次快照 = 一个时间点语义的整机可回滚点。
 * kind/state 取值与后端 model.InstanceSnapshot 对齐。
 */
export interface InstanceSnapshot {
  id: number
  uuid: string
  instanceId: number
  name: string
  /** manual=手动；pre_rollback=回滚前自动创建（不可关闭）；scheduled=定时（预留）。 */
  kind: 'manual' | 'pre_rollback' | 'scheduled'
  /** pending/running/completed/failed/rolled_back；仅 completed 与 rolled_back 可回滚。 */
  state: string
  /** 底层全量备份 ID（复用 FR-013/056 归档通道）。 */
  rootBackupId: number
  /** 快照时刻的启动二进制名与摘要（FR-468 协同：数据 + 版本可核对）。 */
  binaryName: string
  binarySha256: string
  configHash: string
  /** 启动命令摘要（前端展示「当时用的什么命令」）。 */
  configSummary: string
  triggeredBy: number
  /** 由哪次回滚触发（仅 pre_rollback 有值），形成「回滚 → 退回」可视链。 */
  triggeredByRollbackId: number
  sizeMb: number
  failureReason: string
  /** 创建时的一致性提示（如运行态创建可能世界文件不一致）。 */
  note: string
  /** 底层备份的可用性（ok=可回滚；missing=底链已缺失）。 */
  rootBackupState?: 'ok' | 'missing' | ''
  /**
   * 状态显示为可回滚、但**实际不可回滚**时的原因（B-1）：
   * 典型是底层备份已被删除/被保留策略清理，有值即表示不能点回滚。
   */
  notRollableReason?: string
  createdAt: string
  updatedAt: string
}

/** 回滚结果（FR-466）：保留回滚前快照 ID 与二进制差异提示。 */
export interface SnapshotRollbackResult {
  taskId: string
  snapshotId: number
  /** 回滚前自动创建的 pre_rollback 快照 ID —— 可据此退回回滚前状态。 */
  preRollbackSnapshotId: number
  instanceId: number
  /** 本次是否因运行态先做了停服编排。 */
  stopped: boolean
  /** 目标快照二进制与当前不一致（仅提示，不越权替换可执行文件）。 */
  binaryMismatch: boolean
  binaryNote?: string
  finalStatus: string
}

/**
 * 快照是否**真的**可作为回滚目标。
 *
 * 除状态外还必须满足底链可用（B-1）：底层备份被删除/被保留策略清理后，
 * 快照状态仍是 completed，但回滚必然失败——只看 state 会把它显示成可回滚。
 */
export function isSnapshotRollable(snap: InstanceSnapshot): boolean {
  const stateOk = snap.state === 'completed' || snap.state === 'rolled_back'
  return stateOk && !snap.notRollableReason
}

export function useInstanceSnapshots(instanceId: number, enabled = true) {
  return useQuery({
    queryKey: ['instance-snapshots', instanceId],
    queryFn: () => api.get<InstanceSnapshot[]>(`/instances/${instanceId}/snapshots`).then((r) => r.data),
    enabled: enabled && instanceId > 0,
    gcTime: INSTANCE_QUERY_GC_TIME_MS,
  })
}

/**
 * 快照相关 mutation 成功后统一失效的缓存。
 *
 * **为什么必须失效 `['tasks']`**：快照创建/回滚都是异步任务（后端返回 taskId，kind 为
 * `snapshot_create` / `snapshot_rollback`），而任务列表的轮询是**条件式**的
 * （`tasks.ts` 的 tasksRefetchInterval：无在途任务即 false 不轮询）。若不在提交后失效，
 * 「当前没有别的在途任务」时刚提交的任务不会出现在任务中心，直到用户手动刷新——
 * 运维因此看不到进度与失败原因。这是仓库对「提交异步任务」的统一约定。
 *
 * **为什么是 `['instances', instanceId]`**：实例详情的真实 queryKey 是复数
 * （`instances.ts` 的 instanceQueryOptions）。回滚会停服并把实例置为 STOPPED，
 * 失效单数 `['instance']` 是**死失效**（全仓无 query 使用该 key），实例状态显示不会刷新。
 */
function invalidateAfterSnapshotTask(qc: ReturnType<typeof useQueryClient>, instanceId: number) {
  void qc.invalidateQueries({ queryKey: ['instance-snapshots', instanceId] })
  void qc.invalidateQueries({ queryKey: ['instances', instanceId] })
  void qc.invalidateQueries({ queryKey: ['tasks'] })
}

/** 创建整机快照（异步任务：后端打包工作目录 + 采集二进制指纹）。 */
export function useCreateSnapshot(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (name?: string) =>
      api.post<InstanceSnapshot>(`/instances/${instanceId}/snapshots`, { name: name ?? '' }).then((r) => r.data),
    onSuccess: () => {
      invalidateAfterSnapshotTask(qc, instanceId)
      toast.success('快照创建任务已提交')
    },
    onError: (err: unknown) => toast.error(apiErrorMessage(err, '创建快照失败')),
  })
}

/**
 * 一键回滚到指定快照（后端强制先建回滚前快照；运行中实例会先停服）。
 *
 * 破坏性操作：后端要求服务端确认要素（m-1），此处固定回填 confirmSnapshotId——
 * 它来自用户二次确认弹窗选中的那条快照，语义上就是「我确认回滚这一条」。
 * 弹窗另要求用户逐字输入快照名（confirmText），与后端 `confirmName` 同语义。
 */
export function useRollbackSnapshot(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (snapshotId: number) =>
      api
        .post<SnapshotRollbackResult>(`/snapshots/${snapshotId}/rollback`, { confirmSnapshotId: snapshotId })
        .then((r) => r.data),
    onSuccess: (res) => {
      invalidateAfterSnapshotTask(qc, instanceId)
      if (res.binaryMismatch) {
        toast.warning(res.binaryNote ?? '数据已回滚，二进制未动')
      } else {
        toast.success('回滚任务已提交')
      }
    },
    onError: (err: unknown) => toast.error(apiErrorMessage(err, '回滚失败')),
  })
}

/** 删除快照（含底层备份）。 */
export function useDeleteSnapshot(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (snapshotId: number) => api.delete(`/snapshots/${snapshotId}`).then((r) => r.data),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['instance-snapshots', instanceId] })
      toast.success('快照已删除')
    },
    onError: (err: unknown) => toast.error(apiErrorMessage(err, '删除快照失败')),
  })
}
