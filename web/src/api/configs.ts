import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'

export interface ConfigFileInfo {
  path: string
  format: string
  size: number
  updatedAt: number
  supported: boolean
}

export interface ConfigField {
  key: string
  value: string
  type: string
  description?: string
  line?: number
}

export interface ValidationIssue {
  level: string
  message: string
  path?: string
  line?: number
  key?: string
}

export interface ConfigValidationResult {
  valid: boolean
  issues: ValidationIssue[]
}

export interface ConfigReadResult {
  path: string
  format: string
  content: string
  fields: ConfigField[]
  schemaJson: string
  validation: ConfigValidationResult
}

export interface ConfigVersion {
  id: number
  filePath: string
  message: string
  authorId: number
  createdAt: string
  rollbackOfVersionId?: number
}

export interface ConfigDiff {
  fromVersionId: number
  toVersionId: number
  unifiedDiff: string
  fromContent: string
  toContent: string
}

/** 列出实例可管理配置文件。 */
export function useConfigFiles(instanceId: number, path = '') {
  return useQuery({
    queryKey: ['configs', instanceId, path],
    queryFn: async () => {
      const { data } = await api.get<ConfigFileInfo[]>(`/instances/${instanceId}/configs`, { params: { path } })
      return data
    },
    enabled: !!instanceId,
  })
}

/** 读取单文件配置：原文 + 字段 + schema + 校验结果。 */
export function useConfigRead(instanceId: number, filePath: string | null) {
  return useQuery({
    queryKey: ['configs', instanceId, 'read', filePath],
    queryFn: async () => {
      const { data } = await api.get<ConfigReadResult>(`/instances/${instanceId}/configs/read`, {
        params: { path: filePath },
      })
      return data
    },
    enabled: !!instanceId && !!filePath,
  })
}

/** 写入配置文件，返回新版本号。 */
export function useWriteConfig(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: { path: string; content: string; message?: string }) => {
      const { data } = await api.post<{ versionId: number; validation: ConfigValidationResult }>(
        `/instances/${instanceId}/configs/write`,
        payload,
      )
      return data
    },
    onSuccess: (_, vars) => {
      toast.success(`已保存 ${vars.path}`)
      qc.invalidateQueries({ queryKey: ['configs', instanceId] })
    },
    onError: (err: Error & { response?: { data?: { message?: string; validation?: ConfigValidationResult } } }) => {
      const msg = err.response?.data?.validation?.issues?.[0]?.message ?? err.response?.data?.message
      toast.error(msg || err.message || '保存失败')
    },
  })
}

/** 查询历史版本列表（按 ID 倒序）。 */
export function useConfigVersions(instanceId: number, filePath: string | null) {
  return useQuery({
    queryKey: ['configs', instanceId, 'versions', filePath],
    queryFn: async () => {
      const { data } = await api.get<ConfigVersion[]>(
        `/instances/${instanceId}/configs/${encodeURIComponent(filePath || '')}/versions`,
      )
      return data
    },
    enabled: !!instanceId && !!filePath,
  })
}

/** 回滚到指定版本（写入新版本并记录 RollbackOfVersionId）。 */
export function useRollbackConfig(instanceId: number, filePath: string | null) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: { versionId: number; message?: string }) => {
      const { data } = await api.post<{ versionId: number }>(
        `/instances/${instanceId}/configs/${encodeURIComponent(filePath || '')}/rollback`,
        payload,
      )
      return data
    },
    onSuccess: (_, vars) => {
      toast.success(`已回滚到版本 #${vars.versionId}`)
      qc.invalidateQueries({ queryKey: ['configs', instanceId] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '回滚失败')
    },
  })
}

/** 查询两版本之间的 diff。 */
export function useConfigDiff(instanceId: number, filePath: string | null, fromId?: number, toId?: number) {
  return useQuery({
    queryKey: ['configs', instanceId, 'diff', filePath, fromId, toId],
    queryFn: async () => {
      const { data } = await api.get<ConfigDiff>(
        `/instances/${instanceId}/configs/${encodeURIComponent(filePath || '')}/versions/${fromId}/diff`,
        { params: { to: toId } },
      )
      return data
    },
    enabled: !!instanceId && !!filePath && !!fromId && !!toId,
  })
}