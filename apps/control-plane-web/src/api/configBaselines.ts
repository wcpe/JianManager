import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'

/**
 * 配置基线（模板）下发、漂移检测、一键收敛（FR-458）。
 *
 * 基线键为 `(scopeKey, filePath)`，`scopeKey` 限定「哪些实例应共享该基线」
 * （`group:<id>` 含子树 / `network:<id>` / `tag:<tag>` / `instance:<id>` / `all`）。
 * 契约见 `docs/API.md`（`/config-baselines`）。
 */

/** 配置基线（模板）。 */
export interface ConfigBaseline {
  id: number
  scopeKey: string
  filePath: string
  content: string
  /** sha256(content)，与 InstanceConfigVersion.ContentHash 同口径。 */
  contentHash: string
  message: string
  authorId: number
  createdAt: string
  updatedAt: string
}

/** 单实例漂移比对结果。 */
export interface DriftItem {
  instanceId: number
  instanceName: string
  drift: boolean
  currentHash: string
  baselineHash: string
  hasVersion: boolean
  error?: string
}

/** 漂移检测结果（含漂移计数）。 */
export interface DriftResult {
  items: DriftItem[]
  drifted: number
}

/** 单实例收敛结果。 */
export interface ConvergeInstanceResult {
  instanceId: number
  success: boolean
  versionId?: number
  error?: string
}

/** 收敛汇总（含收敛后复核的残余漂移）。 */
export interface ConvergeResult {
  baselineId: number
  targeted: number
  succeeded: number
  failed: number
  results: ConvergeInstanceResult[]
  residualDrift: DriftItem[]
}

/** 创建/更新基线的入参。 */
export interface BaselineInput {
  scopeKey: string
  filePath: string
  content: string
  message?: string
}

/** 列出全部基线。 */
export function useConfigBaselines() {
  return useQuery({
    queryKey: ['config-baselines'],
    queryFn: async () => {
      const { data } = await api.get<{ baselines: ConfigBaseline[] }>('/config-baselines')
      return data.baselines
    },
  })
}

/** 创建或更新基线（同 scopeKey+filePath 覆盖）。 */
export function useUpsertBaseline() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (input: BaselineInput) => {
      const { data } = await api.post<ConfigBaseline>('/config-baselines', input)
      return data
    },
    onSuccess: () => {
      toast.success('基线已保存')
      qc.invalidateQueries({ queryKey: ['config-baselines'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '保存基线失败')
    },
  })
}

/** 删除基线。 */
export function useDeleteBaseline() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      await api.delete(`/config-baselines/${id}`)
    },
    onSuccess: () => {
      toast.success('基线已删除')
      qc.invalidateQueries({ queryKey: ['config-baselines'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '删除基线失败')
    },
  })
}

/** 漂移检测（只读、零副作用）。 */
export function useBaselineDrift(baselineId: number | null, enabled = true) {
  return useQuery({
    queryKey: ['config-baseline-drift', baselineId],
    queryFn: async () => {
      const { data } = await api.get<DriftResult>(`/config-baselines/${baselineId}/drift`)
      return data
    },
    enabled: enabled && !!baselineId,
  })
}

/** 一键收敛：把基线推送到所有漂移实例并复核残余漂移。 */
export function useConvergeBaseline() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({
      baselineId,
      batchSize = 0,
      failFast = false,
    }: { baselineId: number; batchSize?: number; failFast?: boolean }) => {
      const { data } = await api.post<ConvergeResult>(`/config-baselines/${baselineId}/converge`, {
        batchSize,
        failFast,
      })
      return data
    },
    onSuccess: (_res, vars) => {
      qc.invalidateQueries({ queryKey: ['config-baseline-drift', vars.baselineId] })
      qc.invalidateQueries({ queryKey: ['configs'] })
    },
  })
}
