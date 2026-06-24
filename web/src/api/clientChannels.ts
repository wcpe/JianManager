import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 客户端分发频道（FR-086）。每服一个，作为 manifest/制品端点的对外标识。 */
export interface ClientChannel {
  id: number
  /** 频道 slug（对外标识、URL 段）。 */
  channelId: string
  name: string
  description: string
  /** 当前 latest 版本指针占位（FR-088 编排）；0=未发布。 */
  currentVersion: number
  /** 频道下密钥数量（仅列表返回）。 */
  keyCount?: number
  createdAt: string
  updatedAt: string
}

/** 拉取密钥元数据（无明文）。明文仅创建/轮换时一次性返回，见 ClientKeyWithSecret。 */
export interface ClientPullKey {
  id: number
  name: string
  /** 明文前缀（如 jmck_ab12），仅供识别。 */
  keyPrefix: string
  revoked: boolean
  expiresAt: string | null
  lastUsedAt: string | null
  createdAt: string
}

/** 频道详情（含密钥元数据列表）。 */
export interface ClientChannelDetail extends ClientChannel {
  keys: ClientPullKey[]
}

/** 创建/轮换密钥的响应：在元数据之外额外带一次性明文 `key`。 */
export interface ClientKeyWithSecret extends ClientPullKey {
  /** 一次性明文密钥；仅本次响应返回，不可二次读取。 */
  key: string
}

/** 频道列表。 */
export function useClientChannels() {
  return useQuery({
    queryKey: ['client-channels'],
    queryFn: async () => {
      const { data } = await api.get<ClientChannel[]>('/client-channels')
      return data
    },
  })
}

/** 频道详情（含密钥列表）。 */
export function useClientChannel(channelId: string | null) {
  return useQuery({
    queryKey: ['client-channels', channelId],
    queryFn: async () => {
      const { data } = await api.get<ClientChannelDetail>(`/client-channels/${channelId}`)
      return data
    },
    enabled: !!channelId,
  })
}

/** 创建频道。 */
export function useCreateClientChannel() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { channelId: string; name: string; description?: string }) =>
      api.post<ClientChannel>('/client-channels', body).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['client-channels'] }),
  })
}

/** 删除频道（连同其密钥）。 */
export function useDeleteClientChannel() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (channelId: string) => api.delete(`/client-channels/${channelId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['client-channels'] }),
  })
}

/** 创建拉取密钥；返回一次性明文。 */
export function useCreateClientKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ channelId, name, expiresAt }: { channelId: string; name: string; expiresAt?: string }) =>
      api
        .post<ClientKeyWithSecret>(`/client-channels/${channelId}/keys`, { name, expiresAt })
        .then((r) => r.data),
    onSuccess: (_d, vars) => {
      qc.invalidateQueries({ queryKey: ['client-channels', vars.channelId] })
      qc.invalidateQueries({ queryKey: ['client-channels'] })
    },
  })
}

/** 轮换密钥；返回新一次性明文。 */
export function useRotateClientKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ channelId, keyId }: { channelId: string; keyId: number }) =>
      api
        .post<ClientKeyWithSecret>(`/client-channels/${channelId}/keys/${keyId}/rotate`)
        .then((r) => r.data),
    onSuccess: (_d, vars) => qc.invalidateQueries({ queryKey: ['client-channels', vars.channelId] }),
  })
}

/** 吊销密钥。 */
export function useRevokeClientKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ channelId, keyId }: { channelId: string; keyId: number }) =>
      api.delete(`/client-channels/${channelId}/keys/${keyId}`),
    onSuccess: (_d, vars) => qc.invalidateQueries({ queryKey: ['client-channels', vars.channelId] }),
  })
}

/** 内嵌客户端更新器 jar 的版本与可用性（FR-107 接入引导）。 */
export interface UpdaterJarsInfo {
  /** 内嵌更新器版本号（与 client-updater 构建同步）。 */
  version: string
  wedge: { available: boolean; size: number }
  core: { available: boolean; size: number }
}

/** 查询内嵌更新器 jar 信息（FR-107）。供接入引导展示版本 + 禁用缺失下载。 */
export function useUpdaterJarsInfo() {
  return useQuery({
    queryKey: ['client-updater-jars'],
    queryFn: async (): Promise<UpdaterJarsInfo> => {
      const { data } = await api.get('/client-dist/updater-jars')
      return data
    },
  })
}

/** 下载内嵌更新器 jar（FR-107）。component ∈ wedge | core；带鉴权流式下载并触发浏览器保存。 */
export async function downloadUpdaterJar(component: 'wedge' | 'core'): Promise<void> {
  const { data } = await api.get(`/client-dist/updater-jars/${component}`, {
    responseType: 'blob',
  })
  const filename = component === 'wedge' ? 'wedge.jar' : 'updater-core.jar'
  const url = URL.createObjectURL(data as Blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}
