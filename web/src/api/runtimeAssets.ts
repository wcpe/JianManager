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

/** 运行时与制品全局页一次性聚合载荷（FR-082）。 */
export interface RuntimeAssetsOverview {
  jdks: JDKMatrixItem[]
  jdkSummary: JDKSummary
  assets: AssetTypeGroup[]
  assetSummary: AssetSummary
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
      }
    },
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
