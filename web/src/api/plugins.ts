import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import i18n from '@/i18n'
import api from '@/api/client'

/** 单个插件/模组（与后端 service.PluginInfo 对应）。 */
export interface PluginInfo {
  /** 展示用文件名（已剥离 .disabled 后缀，始终以 .jar 结尾）。 */
  name: string
  /** 所在目录：plugins | mods。 */
  dir: string
  /** 是否启用（true=.jar，false=.jar.disabled）。 */
  enabled: boolean
  /** 字节数。 */
  size: number
  /** 修改时间（Unix 秒）。 */
  modTime: number
}

/** 列出实例 plugins/ 与 mods/ 目录插件（含启用/禁用状态）。 */
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

/** 上传插件：先入制品库（type=plugin 去重）再部署到实例目标目录。 */
export function useUploadPlugin(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: { file: File; dir?: string }) => {
      const form = new FormData()
      form.append('file', payload.file)
      if (payload.dir) form.append('dir', payload.dir)
      const { data } = await api.post(`/instances/${instanceId}/plugins`, form, {
        headers: { 'Content-Type': 'multipart/form-data' },
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

/** 启用/禁用插件（重命名 .jar ↔ .jar.disabled，不删除文件）。 */
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
