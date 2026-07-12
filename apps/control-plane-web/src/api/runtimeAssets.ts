import type { AxiosProgressEvent } from 'axios'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 资产类型（与后端 model.AssetType 对齐，FR-045）。 */
export type AssetType = 'core' | 'plugin' | 'image' | 'video' | 'archive' | 'blob' | 'client-file'

/** 引用某 JDK 的实例（引用关系下钻 / 删除占用方提示，FR-082）。 */
export interface JDKRefInstance {
  id: number
  uuid: string
  name: string
  status: string
  /** direct=按具体 JDK 绑定；major=按 Java 大版本解析到本 JDK。 */
  binding: 'direct' | 'major'
}

/** 跨节点 JDK 矩阵的一项 = 一个节点上的一个 JDK + 其引用实例。 */
export interface JDKMatrixItem {
  id: number
  nodeId: number
  nodeName: string
  nodeOnline: boolean
  vendor: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
  instances: JDKRefInstance[]
  refCount: number
}

/** JDK 区汇总统计。 */
export interface JDKSummary {
  nodeCount: number
  jdkCount: number
  referencedJdk: number
  instanceRefs: number
}

/** 制品库资产（与后端 model.Asset 对齐，FR-045）。 */
export interface AssetInfo {
  id: number
  type: AssetType
  name: string
  version: string
  filename: string
  sha256: string
  md5: string
  size: number
  contentType: string
  sourceUrl: string
  metadata: string
  storageState: 'hot' | 'archived' | 'external'
  storageBackend: string
  refCount: number
  relPath: string
  createdAt: string
  lastUsedAt: string | null
}

/** 制品按类型分组（每组含占用/去重/冷热统计）。 */
export interface AssetTypeGroup {
  type: AssetType
  items: AssetInfo[]
  count: number
  totalSize: number
  referencedCount: number
  hotCount: number
  archivedCount: number
  externalCount: number
}

/** 制品区汇总统计。 */
export interface AssetSummary {
  assetCount: number
  totalSize: number
  referencedCount: number
  hotCount: number
  archivedCount: number
  externalCount: number
}

/**
 * 跨节点多运行时矩阵项（FR-301 加性扩展）：type 区分 jdk / nodejs / python（预留）。
 * type=jdk 行 name=厂商且携带引用实例；其它类型当前无引用消费者，instances 恒空。
 */
export interface RuntimeMatrixEntry {
  id: number
  nodeId: number
  nodeName: string
  nodeOnline: boolean
  type: string
  name: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
  instances: JDKRefInstance[]
  refCount: number
}

/** 一个节点的库存同步状态（FR-301）。syncedAt=null 表示从未同步。 */
export interface RuntimeNodeSync {
  nodeId: number
  nodeName: string
  online: boolean
  syncedAt: string | null
}

/** 运行时与制品全局页一次性聚合载荷（FR-082；FR-301 加性扩展 runtimes/runtimeSyncs/syncedAt）。 */
export interface RuntimeAssetsOverview {
  jdks: JDKMatrixItem[]
  jdkSummary: JDKSummary
  assets: AssetTypeGroup[]
  assetSummary: AssetSummary
  runtimes: RuntimeMatrixEntry[]
  runtimeSyncs: RuntimeNodeSync[]
  /** 整体上次同步时间 = 各节点最大值；null=全部未同步。 */
  syncedAt: string | null
}

/** 拉取运行时与制品全局聚合（FR-082）。 */
export function useRuntimeAssetsOverview() {
  return useQuery({
    queryKey: ['runtime-assets-overview'],
    queryFn: async () => {
      const { data } = await api.get<RuntimeAssetsOverview>('/runtime-assets/overview')
      // 后端空切片序列化为 null（Go nil slice → JSON null），前端按数组 .length/.map 会崩白屏——统一归一为 []。
      return {
        ...data,
        jdks: (data.jdks ?? []).map((j) => ({ ...j, instances: j.instances ?? [] })),
        assets: (data.assets ?? []).map((g) => ({ ...g, items: g.items ?? [] })),
        runtimes: (data.runtimes ?? []).map((r) => ({ ...r, instances: r.instances ?? [] })),
        runtimeSyncs: data.runtimeSyncs ?? [],
        syncedAt: data.syncedAt ?? null,
      }
    },
  })
}

/** 单节点强制同步结果（FR-301）。失败节点 syncedAt 保留旧值（显旧数据语义）。 */
export interface RuntimeRefreshResult {
  nodeId: number
  nodeName: string
  ok: boolean
  error?: string
  syncedAt: string | null
}

/** POST /runtime-assets/refresh 载荷（FR-301）。 */
export interface RuntimeRefreshOutcome {
  results: RuntimeRefreshResult[]
  syncedAt: string | null
}

/**
 * 强制全节点库存同步（FR-301 手动刷新）。失败容忍：响应逐节点回报 ok/error，
 * 调用方据此提示失败节点（数据显旧）。成功后失效全局聚合缓存令 syncedAt/矩阵即刻更新。
 */
export function useRefreshRuntimeAssets() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      const { data } = await api.post<RuntimeRefreshOutcome>('/runtime-assets/refresh')
      return { ...data, results: data.results ?? [] }
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['runtime-assets-overview'] }),
  })
}

/**
 * 删除某节点上的一个 JDK（复用 FR-033 引用保护：被实例占用返回 409 + 占用方）。
 * 成功后失效全局聚合缓存。
 */
export function useDeleteRuntimeJDK() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ nodeId, jdkId }: { nodeId: number; jdkId: number }) =>
      api.delete(`/nodes/${nodeId}/jdks/${jdkId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['runtime-assets-overview'] }),
  })
}

/**
 * 删除一个制品（复用 FR-045 引用保护：refCount>0 返回 409）。
 * 成功后失效全局聚合缓存。
 */
export function useDeleteAsset() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (assetId: number) => api.delete(`/assets/${assetId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['runtime-assets-overview'] }),
  })
}

/** 导入制品请求（FR-155 下载进度补齐）。onProgress 由 axios 上传进度回调驱动，供弹窗展示进度条。 */
export interface ImportAssetPayload {
  type: AssetType
  file: File
  name?: string
  version?: string
  /** 进度回调：已上传字节 / 总字节。 */
  onProgress?: (loaded: number, total: number) => void
}

/**
 * 导入一个制品到制品库（FR-155：制品导入下载进度）。
 * 走既有 multipart 入库路由（POST /assets，服务层 AssetService.Ingest CAS 去重），
 * 用 axios onUploadProgress 上报字节进度（与 useUploadPlugin 同一进度机制，不新造轮子）。
 * 成功后失效全局聚合缓存，令新制品即刻出现在列表。
 */
export function useImportAsset() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: ImportAssetPayload) => {
      const form = new FormData()
      form.append('type', payload.type)
      form.append('file', payload.file, payload.file.name)
      if (payload.name) form.append('name', payload.name)
      if (payload.version) form.append('version', payload.version)
      const { data } = await api.post<AssetInfo>('/assets', form, {
        headers: { 'Content-Type': 'multipart/form-data' },
        onUploadProgress: (evt: AxiosProgressEvent) => {
          payload.onProgress?.(evt.loaded, evt.total ?? payload.file.size)
        },
      })
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['runtime-assets-overview'] }),
  })
}
