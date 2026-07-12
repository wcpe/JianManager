import type { AxiosProgressEvent } from 'axios'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import i18n from '@/i18n'
import api from '@/api/client'

/** 单个插件/模组（与后端 service.PluginInfo 对应）。 */
export interface PluginInfo {
  /** 展示用文件名（已剥离 .disabled 后缀，按目录以 .jar 或 .zip 结尾）。 */
  name: string
  /** 所在目录：plugins | mods | resourcepacks | datapacks。 */
  dir: string
  /** 是否启用（true=原文件名，false=原文件名追加 .disabled）。 */
  enabled: boolean
  /** 字节数。 */
  size: number
  /** 修改时间（Unix 秒）。 */
  modTime: number
  /** 可选版本号：由后端解析 plugin.yml / mod metadata / pack.mcmeta 后返回。 */
  version?: string
  /** 可选作者：后端解析元信息后返回。 */
  author?: string
  /** 可选依赖摘要：后端解析元信息后返回。 */
  dependencies?: string[]
}

export interface PluginBatchDeployRequest {
  /** 从制品库选择的 type=plugin 资产 ID。 */
  assetIds: number[]
  /** 目标实例与筛选条件；ids 与 filter 二选一。 */
  target: {
    ids?: number[]
    filter?: {
      nodeId?: number
      status?: string
      role?: string
      q?: string
    }
  }
  destination?: 'plugins' | 'mods'
  overwrite?: boolean
}

export interface PluginBatchDeployInstanceResult {
  id: number | string
  name: string
  skipped: boolean
  reason?: string
  error?: string
}

export interface PluginBatchDeployItem {
  instanceId: number
  assetId: number
  ok: boolean
  error?: string
}

type PluginBatchDeployRawResult = Partial<PluginBatchDeployInstanceResult & PluginBatchDeployItem>

export interface PluginBatchDeployResult {
  /** 计数口径为实例。 */
  total: number
  success: number
  failed: number
  skipped: number
  results: PluginBatchDeployInstanceResult[]
  requestedInstances: number
  requestedAssets: number
  succeeded: number
  items?: PluginBatchDeployItem[]
}

/** 单服插件上传请求。onProgress 由 axios 上传进度回调驱动，供页面展示进度条。 */
export interface UploadPluginPayload {
  file: File
  dir?: string
  overwrite?: boolean
  onProgress?: (loaded: number, total: number) => void
}

function normalizePluginBatchDeployResultItem(item: PluginBatchDeployRawResult, index: number): PluginBatchDeployInstanceResult {
  if (item.name || item.id !== undefined) {
    return {
      id: item.id ?? `legacy-${index}`,
      name: item.name ?? `目标 #${item.id ?? index + 1}`,
      skipped: Boolean(item.skipped),
      reason: item.reason,
      error: item.error,
    }
  }
  const instanceId = item.instanceId ?? 0
  const assetId = item.assetId ?? 0
  return {
    id: `${instanceId}-${assetId}-${index}`,
    name: `实例 #${instanceId} / 资产 #${assetId}`,
    skipped: false,
    error: item.ok === false ? item.error || '部署失败' : item.error,
  }
}

function normalizePluginBatchDeployResult(data: Partial<Omit<PluginBatchDeployResult, 'results'> & { results: PluginBatchDeployRawResult[] }>): PluginBatchDeployResult {
  const success = data.success ?? data.succeeded ?? 0
  const failed = data.failed ?? 0
  const skipped = data.skipped ?? 0
  const total = data.total ?? data.requestedInstances ?? success + failed + skipped
  const result: PluginBatchDeployResult = {
    total,
    success,
    failed,
    skipped,
    results: (data.results ?? []).map(normalizePluginBatchDeployResultItem),
    requestedInstances: data.requestedInstances ?? total,
    requestedAssets: data.requestedAssets ?? 0,
    succeeded: data.succeeded ?? success,
  }
  if (data.items) result.items = data.items
  return result
}

/** 列出实例 plugins/mods/resourcepacks/datapacks 目录制品（含启用/禁用状态）。 */
export function usePlugins(instanceId: number) {
  return useQuery({
    queryKey: ['plugins', instanceId],
    queryFn: async () => {
      const { data } = await api.get<PluginInfo[]>(`/instances/${instanceId}/plugins`)
      return data
    },
    enabled: !!instanceId,
  })
}

/** 上传受控制品：jar 入制品库后部署，zip 直接部署到实例目标目录。 */
export function useUploadPlugin(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: UploadPluginPayload) => {
      const form = new FormData()
      form.append('file', payload.file, payload.file.name)
      if (payload.dir) form.append('dir', payload.dir)
      if (payload.overwrite) form.append('overwrite', 'true')
      const { data } = await api.post(`/instances/${instanceId}/plugins`, form, {
        headers: { 'Content-Type': 'multipart/form-data' },
        onUploadProgress: (evt: AxiosProgressEvent) => {
          payload.onProgress?.(evt.loaded, evt.total ?? payload.file.size)
        },
      })
      return data
    },
    onSuccess: () => {
      toast.success(i18n.t('plugins.uploaded'))
      qc.invalidateQueries({ queryKey: ['plugins', instanceId] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || i18n.t('plugins.uploadFailed'))
    },
  })
}

/** 从制品库选择插件资产，批量部署到多个实例的 plugins/ 目录。 */
export function useBatchDeployPlugins() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: PluginBatchDeployRequest) => {
      const { data } = await api.post<PluginBatchDeployResult>('/plugins/batch-deploy', payload)
      return normalizePluginBatchDeployResult(data)
    },
    onSuccess: (data) => {
      toast.success(i18n.t('plugins.batchDeploy.successToast', { success: data.success, skipped: data.skipped, failed: data.failed }))
      qc.invalidateQueries({ queryKey: ['plugins'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || i18n.t('plugins.batchDeploy.failedToast'))
    },
  })
}

/** 删除插件（前端二次确认后调用）。 */
export function useDeletePlugin(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: { name: string; dir: string }) => {
      await api.delete(`/instances/${instanceId}/plugins/${encodeURIComponent(payload.name)}`, {
        params: { dir: payload.dir },
      })
    },
    onSuccess: () => {
      toast.success(i18n.t('plugins.deleted'))
      qc.invalidateQueries({ queryKey: ['plugins', instanceId] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || i18n.t('plugins.deleteFailed'))
    },
  })
}

/** 启用/禁用受控制品（原文件名 ↔ 原文件名.disabled，不删除文件）。 */
export function useTogglePlugin(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: { name: string; dir: string }) => {
      const { data } = await api.post<{ enabled: boolean }>(
        `/instances/${instanceId}/plugins/${encodeURIComponent(payload.name)}/toggle`,
        null,
        { params: { dir: payload.dir } },
      )
      return data
    },
    onSuccess: (data) => {
      toast.success(data.enabled ? i18n.t('plugins.enabled') : i18n.t('plugins.disabled'))
      qc.invalidateQueries({ queryKey: ['plugins', instanceId] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || i18n.t('plugins.toggleFailed'))
    },
  })
}

/** 从制品库批量部署插件到多个实例（FR-053）。 */
export function usePluginBatchDeploy() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: PluginBatchDeployRequest) => {
      const { data } = await api.post<PluginBatchDeployResult>('/plugins/batch-deploy', payload)
      return normalizePluginBatchDeployResult(data)
    },
    onSuccess: () => {
      toast.success(i18n.t('plugins.batchDeployDone'))
      qc.invalidateQueries({ queryKey: ['plugins'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || i18n.t('plugins.batchDeployFailed'))
    },
  })
}
