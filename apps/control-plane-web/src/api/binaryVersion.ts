import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'
import { INSTANCE_QUERY_GC_TIME_MS } from '@/api/instances'
import { apiErrorMessage } from '@/lib/api-error'

/**
 * 实例二进制/Beacon 版本（FR-468）。版本真源是制品库 Asset：
 * 升级 = 换绑定 + 换文件；回滚 = 一级对称操作（Current ↔ Previous 交换）。
 */
export interface BinaryUpgradeCandidate {
  assetId: number
  filename: string
  version: string
  sha256: string
  size: number
}

export interface BinaryVersionView {
  instanceId: number
  /** 是否登记了版本绑定（非 binary/beacon 实例或搭建早于 FR-468 时为 false）。 */
  bound: boolean
  /** 当前生效制品 ID；0=无制品库版本（url/node_file 来源）。 */
  currentAssetId: number
  currentVersion: string
  currentFilename: string
  currentSha256: string
  /** 是否存在可回滚的上一版本。 */
  hasRollback: boolean
  previousAssetId: number
  previousVersion: string
  previousFilename: string
  /** 版本漂移（人工换过文件 / 摘要不一致 / 绑定记录与制品库不符）。 */
  driftDetected: boolean
  driftReason?: string
  /** 工作目录里实际二进制文件的 SHA-256（M-3：后端经 Worker 现算，未校验时为空）。 */
  diskSha256?: string
  /**
   * 本次查询是否真的做了磁盘内容比对（M-3）。
   * 与 driftDetected 分开表达：未校验 ≠ 没有漂移，前端不得把「未校验」渲染成「已核对」。
   */
  diskSha256Checked: boolean
  /**
   * 「读过盘但没能算出摘要」的原因（N-4：文件过大 / 不可读）。
   * 必须与 driftDetected 分开渲染——「本次未比对」不是「版本漂移」。
   */
  diskCheckSkippedReason?: string
  /** 无制品库版本：升级入口应给明确提示。 */
  noLibraryVersion: boolean
  note?: string
  candidates: BinaryUpgradeCandidate[]
}

export interface BinaryVersionChangeResult {
  taskId: string
  instanceId: number
  fromAssetId: number
  fromVersion: string
  toAssetId: number
  toVersion: string
  toFilename: string
  startCommand: string
  commandChanged: boolean
}

export function useBinaryVersion(instanceId: number, enabled = true) {
  return useQuery({
    queryKey: ['instance-binary-version', instanceId],
    queryFn: () => api.get<BinaryVersionView>(`/instances/${instanceId}/binary-version`).then((r) => r.data),
    enabled: enabled && instanceId > 0,
    gcTime: INSTANCE_QUERY_GC_TIME_MS,
  })
}

/**
 * 版本变更后统一失效的缓存。
 *
 * **`['tasks']` 是必需的**：升级/回滚都返回 taskId（kind=`binary_upgrade`），而任务列表
 * 轮询是条件式的——无在途任务时不轮询。不失效就会让刚提交的变更在任务中心「消失」，
 * 运维看不到进度与失败原因（与仓库其他异步任务 mutation 同口径）。
 */
function invalidateAfterBinaryChange(qc: ReturnType<typeof useQueryClient>, instanceId: number) {
  void qc.invalidateQueries({ queryKey: ['instance-binary-version', instanceId] })
  void qc.invalidateQueries({ queryKey: ['instances', instanceId] })
  void qc.invalidateQueries({ queryKey: ['tasks'] })
}

/** 受控升级到指定制品版本（实例须已停止）。 */
export function useBinaryUpgrade(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (assetId: number) =>
      api.post<BinaryVersionChangeResult>(`/instances/${instanceId}/binary-upgrade`, { assetId }).then((r) => r.data),
    onSuccess: () => {
      invalidateAfterBinaryChange(qc, instanceId)
      toast.success('升级任务已提交')
    },
    onError: (err: unknown) => toast.error(apiErrorMessage(err, '升级失败')),
  })
}

/** 回滚到上一版本（语义对称：回滚本身也可再回滚）。 */
export function useBinaryRollback(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api.post<BinaryVersionChangeResult>(`/instances/${instanceId}/binary-rollback`).then((r) => r.data),
    onSuccess: () => {
      invalidateAfterBinaryChange(qc, instanceId)
      toast.success('回滚任务已提交')
    },
    onError: (err: unknown) => toast.error(apiErrorMessage(err, '回滚失败')),
  })
}
