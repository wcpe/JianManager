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

/** 单字段 schema 元数据（与后端 service/schema.FieldSchema 对应）。 */
export interface FieldSchema {
  key: string
  type: string
  default: string
  description: string
  choices?: string[]
}

/** 单配置文件 schema（与后端 service/schema.ModelSchema 对应）。 */
export interface ModelSchema {
  name: string
  description: string
  format: string
  fields: Record<string, FieldSchema>
}

/** 跨文件/跨实例一致性校验告警。 */
export interface CrossCheckIssue {
  level: string
  message: string
  key?: string
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

/** 递归发现的单个配置文件（与后端 service.DiscoveredConfig 对应，FR-071）。 */
export interface DiscoveredConfig {
  path: string
  format: string
  supported: boolean
}

export interface ConfigDiscoverResult {
  files: DiscoveredConfig[]
  truncated: boolean
}

/** 递归发现实例 server 目录下全部配置文件（FR-071，不限内置 schema）。 */
export function useConfigDiscover(instanceId: number) {
  return useQuery({
    queryKey: ['configs', instanceId, 'discover'],
    queryFn: async () => {
      const { data } = await api.get<ConfigDiscoverResult>(`/instances/${instanceId}/configs/discover`)
      return data
    },
    enabled: !!instanceId,
  })
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

/** 表单模式保存：提交字段修改，后端字段级补丁回原文（保留注释）。 */
export function useWriteConfigFields(instanceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: { path: string; fields: Record<string, string>; message?: string }) => {
      const { data } = await api.post<{ versionId: number; validation: ConfigValidationResult }>(
        `/instances/${instanceId}/configs/write-fields`,
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

/** 跨文件/跨实例一致性校验（端口唯一 / online-mode 配套 / forwarding secret 跨代理一致）。 */
export function useCrossCheck(instanceId: number) {
  return useMutation({
    mutationFn: async (payload: { path: string; content: string }) => {
      const { data } = await api.post<{ issues: CrossCheckIssue[] }>(
        `/instances/${instanceId}/configs/cross-check`,
        payload,
      )
      return data.issues
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '校验失败')
    },
  })
}

/** 查询历史版本列表（按 ID 倒序）。 */
export function useConfigVersions(instanceId: number, filePath: string | null) {
  return useQuery({
    queryKey: ['configs', instanceId, 'versions', filePath],
    queryFn: async () => {
      const { data } = await api.get<ConfigVersion[]>(
        `/instances/${instanceId}/configs/versions/${encodeURIComponent(filePath || '')}`,
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
        `/instances/${instanceId}/configs/rollback/${encodeURIComponent(filePath || '')}`,
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
        `/instances/${instanceId}/configs/diff/${encodeURIComponent(filePath || '')}`,
        { params: { from: fromId, to: toId } },
      )
      return data
    },
    enabled: !!instanceId && !!filePath && !!fromId && !!toId,
  })
}